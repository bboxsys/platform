package admin

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/bboxsys/platform/user"
	"github.com/bboxsys/store"
	"github.com/nats-io/nkeys"
)

type Store struct {
	*store.Store
	secrets *secretBox
}

type AdminStoreOptions struct {
	Location    string `json:"location"`
	Application string `json:"application"`
	Component   string `json:"component"`
	// SecretsKey is the 32-byte AES key that seals stored secrets (API token
	// values). When nil, the key is read from SecretsKeyFile, which defaults
	// to secrets.key beside the store directory and is generated on first use.
	SecretsKey     []byte `json:"-"`
	SecretsKeyFile string `json:"secrets_key_file"`
}

// Secondary index types for cross-account lookups

// UserEmailIndex maps email to account and user ID for login lookups
type UserEmailIndex struct {
	AccountID string `json:"account_id"`
	UserID    string `json:"user_id"`
}

// OAuthIndex maps OAuth provider:subject to account ID
type OAuthIndex struct {
	AccountID string `json:"account_id"`
	UserID    string `json:"user_id"`
}

// InvitationIndex maps invitation token to account ID
type InvitationIndex struct {
	AccountID string `json:"account_id"`
}

// OIDCSlugIndex maps a per-tenant OIDC login slug to its account ID.
type OIDCSlugIndex struct {
	AccountID string `json:"account_id"`
}

func NewStore(options *AdminStoreOptions) (*Store, error) {
	s, err := store.NewStore(&store.Options{
		Location:    options.Location,
		Application: options.Application,
		Component:   options.Component,
	})
	if err != nil {
		return &Store{}, err
	}
	key := options.SecretsKey
	if key == nil {
		path := options.SecretsKeyFile
		if path == "" {
			path = defaultSecretsKeyPath(options.Location)
		}
		key, err = loadOrCreateSecretsKey(path)
		if err != nil {
			_ = s.Close()
			return &Store{}, fmt.Errorf("secrets key: %w", err)
		}
	}
	box, err := newSecretBox(key)
	if err != nil {
		_ = s.Close()
		return &Store{}, err
	}
	return &Store{Store: s, secrets: box}, nil
}

// ///////////////////////////////////////////////////////////////////////////////////////
// Initialization
// ///////////////////////////////////////////////////////////////////////////////////////
func (s *Store) PutInitialized() error {
	return s.PutOneRaw("initialized", "true")
}
func (s *Store) GetInitialized() (bool, error) {
	return s.GetKeyExists("initialized")
}
func (s *Store) PutInitializerData(initializerData InitializerData) error {
	return s.PutOne("initializer_data", initializerData)
}
func (s *Store) GetInitializerData() (InitializerData, error) {
	var initializerData InitializerData
	err := s.GetOne("initializer_data", &initializerData)
	return initializerData, err
}

// ///////////////////////////////////////////////////////////////////////////////////////
// User (Account-Scoped)
// ///////////////////////////////////////////////////////////////////////////////////////
func (s *Store) NewUser(userConfig *user.UserConfig) (*user.User, error) {
	signingKeyPair, err := s.GetAccountKeyPair(userConfig.AccountID)
	if err != nil {
		return &user.User{}, err
	}
	// Email is the global login identifier and is resolved through a single
	// global index (user_email_index:{email}); the same email in two accounts
	// would clobber that index and misroute or lock out login. Enforce global
	// email uniqueness at creation time.
	if userConfig.Email != "" {
		if existing, _, lookupErr := s.GetUserByEmail(userConfig.Email); lookupErr == nil && existing != nil {
			return &user.User{}, fmt.Errorf("a user with email %s already exists", userConfig.Email)
		}
	}
	u, userPassword, err := user.NewUser(userConfig, signingKeyPair)
	if err != nil {
		return &user.User{}, err
	}
	err = s.PutUser(userConfig.AccountID, u)
	if err != nil {
		return &user.User{}, err
	}
	err = s.PutUserPassword(userConfig.AccountID, userPassword)
	if err != nil {
		return &user.User{}, err
	}
	return u, nil
}

// PutUser stores a user with account scoping and maintains the email index
func (s *Store) PutUser(accountID string, u *user.User) error {
	// Store user in account scope: {accountID}:user:{userID}
	err := s.PutScoped(accountID, "user", u.ID, u)
	if err != nil {
		return err
	}
	// Maintain email index for cross-account login lookup
	index := &UserEmailIndex{AccountID: accountID, UserID: u.ID}
	return s.PutOne(fmt.Sprintf("user_email_index:%s", u.Email), index)
}

// GetUsers returns all users for an account
func (s *Store) GetUsers(accountID string) (*[]user.User, error) {
	users := make([]user.User, 0)
	err := s.ListScoped(accountID, "user", &users)
	return &users, err
}

// GetUsersStream streams users for an account
func (s *Store) GetUsersStream(w io.Writer, wg *sync.WaitGroup, accountID string) {
	s.GetScopedStream(w, wg, accountID, &store.Query{
		Prefix: "user",
	})
}

// GetUserByEmail looks up a user by email using the global index
// Returns the user, their accountID, and any error
func (s *Store) GetUserByEmail(email string) (*user.User, string, error) {
	// Look up email index to find account
	var index UserEmailIndex
	err := s.GetOne(fmt.Sprintf("user_email_index:%s", email), &index)
	if err != nil {
		return nil, "", err
	}
	// Fetch user from account scope
	var u user.User
	err = s.GetScoped(index.AccountID, "user", index.UserID, &u)
	if err != nil {
		return nil, "", err
	}
	return &u, index.AccountID, nil
}

// GetUserByID returns a user by ID within an account
func (s *Store) GetUserByID(accountID, userID string) (*user.User, error) {
	var u user.User
	err := s.GetScoped(accountID, "user", userID, &u)
	return &u, err
}

// GetUserByPublicKey returns the identity behind a NATS public key within
// an account. It is what the role gates (RequireRole, RequireAdmin) look up
// after a bearer token validates. Users are found first; when no user has
// the key, an agent and then a service record with it is returned as a
// *user.User synthesized from its UserBase, so an agent or service whose
// JWT carries roles passes the same gates a user with those roles would.
// Agents and services have no roles unless a super admin grants them, so
// the fallback escalates nothing by itself.
func (s *Store) GetUserByPublicKey(accountID, publicKey string) (*user.User, error) {
	var u user.User
	filters := make([]store.Filter, 0)
	filters = append(filters, store.Filter{Path: "publicKey", Value: publicKey})
	err := s.GetScopedByFilter(accountID, "user", &filters, &u)
	if err == nil && u.PublicKey == publicKey {
		return &u, nil
	}
	var agent user.Agent
	if aerr := s.GetScopedByFilter(accountID, "agent", &filters, &agent); aerr == nil && agent.PublicKey == publicKey {
		return &user.User{UserBase: agent.UserBase, FirstName: agent.Hostname}, nil
	}
	var service user.Service
	if serr := s.GetScopedByFilter(accountID, "service", &filters, &service); serr == nil && service.PublicKey == publicKey {
		return &user.User{UserBase: service.UserBase, FirstName: service.Name}, nil
	}
	// No identity has the key: the user lookup's result is returned as
	// before (an empty user and no error when nothing matched).
	return &u, err
}

// GetUserByName returns a user by name within an account
func (s *Store) GetUserByName(accountID, name string) (*user.User, error) {
	var u user.User
	filters := make([]store.Filter, 0)
	filters = append(filters, store.Filter{Path: "name", Value: name})
	err := s.GetScopedByFilter(accountID, "user", &filters, &u)
	return &u, err
}

// DeleteUser deletes a user and their email index
func (s *Store) DeleteUser(accountID, userID string) error {
	// Get user first to delete email index
	var u user.User
	err := s.GetScoped(accountID, "user", userID, &u)
	if err == nil && u.Email != "" {
		// Only remove the email index if it still points to this user, so
		// deleting one account's user cannot drop another account's index
		// entry for a colliding email.
		var index UserEmailIndex
		if idxErr := s.GetOne(fmt.Sprintf("user_email_index:%s", u.Email), &index); idxErr == nil {
			if index.AccountID == accountID && index.UserID == userID {
				s.DeleteOneByKey(fmt.Sprintf("user_email_index:%s", u.Email))
			}
		}
	}
	return s.DeleteScoped(accountID, "user", userID)
}

// ///////////////////////////////////////////////////////////////////////////////////////
// Agent (Account-Scoped)
// ///////////////////////////////////////////////////////////////////////////////////////
func (s *Store) NewAgent(userConfig *user.UserConfig) (*user.Agent, error) {
	signingKeyPair, err := s.GetAccountKeyPair(userConfig.AccountID)
	if err != nil {
		return &user.Agent{}, err
	}
	agent, err := user.NewAgent(userConfig, signingKeyPair)
	if err != nil {
		return &user.Agent{}, err
	}
	err = s.PutAgent(userConfig.AccountID, agent)
	if err != nil {
		return &user.Agent{}, err
	}
	return agent, nil
}

// PutAgent stores an agent with account scoping
func (s *Store) PutAgent(accountID string, agent *user.Agent) error {
	return s.PutScoped(accountID, "agent", agent.HostID, agent)
}

// GetAgents returns all agents for an account
func (s *Store) GetAgents(accountID string) (*[]user.Agent, error) {
	agents := make([]user.Agent, 0)
	err := s.ListScoped(accountID, "agent", &agents)
	return &agents, err
}

// GetAgentsStream streams agents for an account
func (s *Store) GetAgentsStream(w io.Writer, wg *sync.WaitGroup, accountID string) {
	s.GetScopedStream(w, wg, accountID, &store.Query{
		Prefix: "agent",
	})
}

// GetAgentByHostID returns an agent by host ID within an account
func (s *Store) GetAgentByHostID(accountID, hostID string) (*user.Agent, error) {
	var agent user.Agent
	err := s.GetScoped(accountID, "agent", hostID, &agent)
	if err != nil {
		return &user.Agent{}, err
	}
	if agent.HostID == "" {
		return &user.Agent{}, errors.New("agent does not exist")
	}
	return &agent, nil
}

// DeleteAgent deletes an agent from an account
func (s *Store) DeleteAgent(accountID, hostID string) error {
	return s.DeleteScoped(accountID, "agent", hostID)
}

// ///////////////////////////////////////////////////////////////////////////////////////
// Service (Account-Scoped)
// ///////////////////////////////////////////////////////////////////////////////////////
func (s *Store) NewService(userConfig *user.UserConfig) (*user.Service, error) {
	signingKeyPair, err := s.GetAccountKeyPair(userConfig.AccountID)
	if err != nil {
		return &user.Service{}, err
	}
	service, err := user.NewService(userConfig, signingKeyPair)
	if err != nil {
		return &user.Service{}, err
	}
	err = s.PutService(userConfig.AccountID, service)
	if err != nil {
		return &user.Service{}, err
	}
	return service, nil
}

// PutService stores a service with account scoping
// DeleteService removes a service identity from an account.
func (s *Store) DeleteService(accountID, id string) error {
	return s.DeleteScoped(accountID, "service", id)
}

func (s *Store) PutService(accountID string, service *user.Service) error {
	return s.PutScoped(accountID, "service", service.ID, service)
}

// GetServices returns all services for an account
func (s *Store) GetServices(accountID string) (*[]user.Service, error) {
	services := make([]user.Service, 0)
	err := s.ListScoped(accountID, "service", &services)
	return &services, err
}

// GetServicesStream streams services for an account
func (s *Store) GetServicesStream(w io.Writer, wg *sync.WaitGroup, accountID string) {
	s.GetScopedStream(w, wg, accountID, &store.Query{
		Prefix: "service",
	})
}

// GetServiceByID returns a service by ID within an account
func (s *Store) GetServiceByID(accountID, serviceID string) (*user.Service, error) {
	var service user.Service
	err := s.GetScoped(accountID, "service", serviceID, &service)
	return &service, err
}

// GetServiceByName returns a service by name within an account
func (s *Store) GetServiceByName(accountID, name string) (*user.Service, error) {
	var service user.Service
	filters := make([]store.Filter, 0)
	filters = append(filters, store.Filter{Path: "name", Value: name})
	err := s.GetScopedByFilter(accountID, "service", &filters, &service)
	return &service, err
}

// ///////////////////////////////////////////////////////////////////////////////////////
// UserPassword (Account-Scoped)
// ///////////////////////////////////////////////////////////////////////////////////////

// PutUserPassword stores a user password with account scoping
func (s *Store) PutUserPassword(accountID string, userPassword *user.UserPassword) error {
	return s.PutScoped(accountID, "userPassword", userPassword.ID, userPassword)
}

// GetUserPassword returns a user password within an account
func (s *Store) GetUserPassword(accountID, userID string) (user.UserPassword, error) {
	var userPassword user.UserPassword
	err := s.GetScoped(accountID, "userPassword", userID, &userPassword)
	return userPassword, err
}

/////////////////////////////////////////////////////////////////////////////////////////
// Operator
/////////////////////////////////////////////////////////////////////////////////////////

func (s *Store) NewOperator(operatorConfig user.OperatorConfig) (user.Operator, error) {
	operator, err := user.NewOperator(operatorConfig)
	if err != nil {
		return user.Operator{}, err
	}
	err = s.PutOperator(operator)
	if err != nil {
		return user.Operator{}, err
	}
	return operator, nil
}
func (s *Store) PutOperator(operator user.Operator) error {
	return s.PutOne(fmt.Sprintf("%s:%s", "operator", operator.ID), operator)
}
func (s *Store) GetOperators() (*[]user.Operator, error) {
	operators := make([]user.Operator, 0)
	err := s.Get("operator", &operators)
	return &operators, err
}
func (s *Store) GetOperatorsStream(w io.Writer, wg *sync.WaitGroup) {
	s.GetStream(w, wg, &store.Query{
		Prefix: "operator",
	})
}
func (s *Store) GetOperatorByID(id string) (user.Operator, error) {
	var o user.Operator
	err := s.GetOne(fmt.Sprintf("operator:%s", id), &o)
	return o, err
}
func (s *Store) GetOperatorKeyPair(id string) (nkeys.KeyPair, error) {
	var kp nkeys.KeyPair
	operator, err := s.GetOperatorByID(id)
	if err != nil {
		return kp, err
	}
	return nkeys.FromSeed([]byte(operator.Seed))
}
func (s *Store) GetOperatorPublicKey(id string) (string, error) {
	kp, err := s.GetOperatorKeyPair(id)
	if err != nil {
		return "", err
	}
	return kp.PublicKey()
}
func (s *Store) GetOperatorByName(name string) (user.Operator, error) {
	var operator user.Operator
	filters := make([]store.Filter, 0)
	filters = append(filters, store.Filter{Path: "name", Value: name})
	err := s.GetOneByFilter("operator", &filters, &operator)
	return operator, err
}

// ///////////////////////////////////////////////////////////////////////////////////////
// Activity (Account-Scoped)
// ///////////////////////////////////////////////////////////////////////////////////////

// PutActivity stores an activity with account scoping
func (s *Store) PutActivity(accountID string, activity user.Activity) error {
	return s.PutScoped(accountID, "activity", activity.ID, activity)
}

// GetActivities returns all activities for an account
func (s *Store) GetActivities(accountID string) (*[]user.Activity, error) {
	activities := make([]user.Activity, 0)
	err := s.ListScoped(accountID, "activity", &activities)
	return &activities, err
}

// GetActivityStream streams activities for an account
func (s *Store) GetActivityStream(w io.Writer, wg *sync.WaitGroup, accountID string) {
	s.GetScopedStream(w, wg, accountID, &store.Query{
		Prefix: "activity",
	})
}

// GetActivity returns an activity by ID within an account
func (s *Store) GetActivity(accountID, activityID string) (user.Activity, error) {
	var activity user.Activity
	err := s.GetScoped(accountID, "activity", activityID, &activity)
	return activity, err
}

// UpdateActivity updates an existing activity within an account
func (s *Store) UpdateActivity(accountID string, a user.Activity) error {
	activity, err := s.GetActivity(accountID, a.ID)
	if err != nil {
		return err
	}
	activity.Update(a)
	err = s.PutActivity(accountID, activity)
	if err != nil {
		return err
	}
	return nil
}

/////////////////////////////////////////////////////////////////////////////////////////
// Account
/////////////////////////////////////////////////////////////////////////////////////////

func (s *Store) NewAccount(accountConfig user.AccountConfig) (user.Account, error) {
	signingKeyPair, err := s.GetOperatorKeyPair(accountConfig.OperatorID)
	if err != nil {
		return user.Account{}, err
	}
	account, err := user.NewAccount(accountConfig, signingKeyPair)
	if err != nil {
		return user.Account{}, err
	}
	err = s.PutAccount(account)
	if err != nil {
		return user.Account{}, err
	}
	return account, nil
}
func (s *Store) PutAccount(account user.Account) error {
	return s.PutOne(fmt.Sprintf("%s:%s", "account", account.ID), account)
}
func (s *Store) GetAccounts() (*[]user.Account, error) {
	accounts := make([]user.Account, 0)
	err := s.Get("account", &accounts)
	return &accounts, err
}
func (s *Store) GetAccountsStream(w io.Writer, wg *sync.WaitGroup) {
	s.GetStream(w, wg, &store.Query{
		Prefix: "account",
	})
}
func (s *Store) GetAccountByID(id string) (user.Account, error) {
	var a user.Account
	err := s.GetOne(fmt.Sprintf("account:%s", id), &a)
	return a, err
}
func (s *Store) GetAccountByName(name string) (user.Account, error) {
	var a user.Account
	filters := make([]store.Filter, 0)
	filters = append(filters, store.Filter{Path: "name", Value: name})
	err := s.GetOneByFilter("account", &filters, &a)
	return a, err
}
func (s *Store) GetAccountKeyPair(id string) (nkeys.KeyPair, error) {
	var kp nkeys.KeyPair
	account, err := s.GetAccountByID(id)
	if err != nil {
		return kp, err
	}
	return nkeys.FromSeed([]byte(account.Seed))
}
func (s *Store) GetAccountPublicKey(id string) (string, error) {
	kp, err := s.GetAccountKeyPair(id)
	if err != nil {
		return "", err
	}
	return kp.PublicKey()
}
func (s *Store) DeleteAccount(accountID string) error {
	return s.DeleteOneByKey(fmt.Sprintf("account:%s", accountID))
}

// ///////////////////////////////////////////////////////////////////////////////////////
// Service Endpoints
// Endpoints are unique by URL
// ///////////////////////////////////////////////////////////////////////////////////////
func (s *Store) PutServiceEndpoint(serviceEndpoint ServiceEndpoint) error {
	return s.PutOne(fmt.Sprintf("%s:%s", "serviceendpoint", serviceEndpoint.Kind), serviceEndpoint)
}

func (s *Store) PutServiceEndpointsMap(serviceEndpoints map[string]ServiceEndpoint) error {
	for _, serviceEndpoint := range serviceEndpoints {
		err := s.PutOne(fmt.Sprintf("%s:%s", "serviceendpoint", serviceEndpoint.Kind), serviceEndpoint)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DeleteServiceEndpoint(serviceEndpoint ServiceEndpoint) error {
	return s.DeleteOneByKey(fmt.Sprintf("%s:%s", "serviceendpoint", serviceEndpoint.Kind))
}

func (s *Store) GetServiceEndpoints() (map[string]ServiceEndpoint, error) {
	serviceEndpoints := make([]ServiceEndpoint, 0)
	seMap := make(map[string]ServiceEndpoint)
	err := s.Get("serviceendpoint", &serviceEndpoints)
	for _, serviceEndpoint := range serviceEndpoints {
		seMap[serviceEndpoint.Kind] = serviceEndpoint
	}
	return seMap, err
}

func (s *Store) GetServiceEndpointsByKind(kind string) (*[]ServiceEndpoint, error) {
	serviceEndpoints := make([]ServiceEndpoint, 0)
	err := s.Get(fmt.Sprintf("%s:%s", "serviceendpoint", kind), &serviceEndpoints)
	return &serviceEndpoints, err
}

func (s *Store) GetServiceEndpointsStream(w io.Writer, wg *sync.WaitGroup) {
	s.GetStream(w, wg, &store.Query{
		Prefix: "serviceendpoint",
	})
}
func (s *Store) GetServiceEndpointsByKindStream(w io.Writer, wg *sync.WaitGroup, kind string) {
	s.GetStream(w, wg, &store.Query{
		Prefix: fmt.Sprintf("%s:%s", "serviceendpoint", kind),
	})
}

// ///////////////////////////////////////////////////////////////////////////////////////
// Proxy Config
// ///////////////////////////////////////////////////////////////////////////////////////
const PROXY_CONFIG_KEY = "proxy_config"

func (s *Store) PutProxyConfig(config ProxyConfig) error {
	return s.PutOne(PROXY_CONFIG_KEY, config)
}

func (s *Store) GetProxyConfig() (*ProxyConfig, error) {
	var config ProxyConfig
	err := s.GetOne(PROXY_CONFIG_KEY, &config)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func (s *Store) DeleteProxyConfig() error {
	return s.DeleteOneByKey(PROXY_CONFIG_KEY)
}

// ///////////////////////////////////////////////////////////////////////////////////////
// API Tokens (Account-Scoped)
// ///////////////////////////////////////////////////////////////////////////////////////

// PutAPIToken stores an API token. The scope decides the key: user-scoped
// tokens live under the owner's public key, account-scoped ones under
// APITokenAccountOwner. The secret is sealed before it is written; the
// caller's copy is left untouched.
func (s *Store) PutAPIToken(accountID string, token *APIToken) error {
	if token == nil {
		return errors.New("api token is nil")
	}
	token.Normalize()
	if err := token.Validate(); err != nil {
		return err
	}
	key, err := token.storeKey()
	if err != nil {
		return err
	}
	stored := *token
	stored.Token, err = s.secrets.seal(token.Token)
	if err != nil {
		return err
	}
	return s.PutScoped(accountID, "apitoken", key, &stored)
}

// getAPITokenByKey reads one record, opens the secret, and normalizes it.
func (s *Store) getAPITokenByKey(accountID, key string) (*APIToken, error) {
	var token APIToken
	if err := s.GetScoped(accountID, "apitoken", key, &token); err != nil {
		return nil, err
	}
	if err := s.openAPIToken(&token); err != nil {
		return nil, err
	}
	return &token, nil
}

func (s *Store) openAPIToken(token *APIToken) error {
	plain, err := s.secrets.open(token.Token)
	if err != nil {
		return fmt.Errorf("api token %s: %w", token.Name, err)
	}
	token.Token = plain
	token.Normalize()
	return nil
}

// GetAPIToken retrieves a user-scoped token by owner and name.
func (s *Store) GetAPIToken(accountID, userID, name string) (*APIToken, error) {
	key, err := apiTokenKey(APITokenScopeUser, userID, name)
	if err != nil {
		return nil, err
	}
	return s.getAPITokenByKey(accountID, key)
}

// GetAccountAPIToken retrieves an account-scoped token by name.
func (s *Store) GetAccountAPIToken(accountID, name string) (*APIToken, error) {
	key, _ := apiTokenKey(APITokenScopeAccount, "", name)
	return s.getAPITokenByKey(accountID, key)
}

// GetAPITokenVisibleTo resolves name for a user: their own user-scoped token
// first, then an account-scoped one. Backend callers that act on behalf of a
// user should use this rather than GetAPIToken.
func (s *Store) GetAPITokenVisibleTo(accountID, userID, name string) (*APIToken, error) {
	token, err := s.GetAPIToken(accountID, userID, name)
	if err == nil {
		return token, nil
	}
	return s.GetAccountAPIToken(accountID, name)
}

// listAPITokens returns every token in the account with secrets opened.
// A record whose secret cannot be opened (for example, sealed under a lost
// key) is skipped rather than failing the whole list.
func (s *Store) listAPITokens(accountID string) ([]APIToken, error) {
	all := make([]APIToken, 0)
	if err := s.ListScoped(accountID, "apitoken", &all); err != nil {
		return nil, err
	}
	tokens := make([]APIToken, 0, len(all))
	for i := range all {
		if err := s.openAPIToken(&all[i]); err != nil {
			continue
		}
		tokens = append(tokens, all[i])
	}
	return tokens, nil
}

func filterAPITokens(tokens []APIToken, keep func(*APIToken) bool) []APIToken {
	filtered := make([]APIToken, 0, len(tokens))
	for i := range tokens {
		if keep(&tokens[i]) {
			filtered = append(filtered, tokens[i])
		}
	}
	return filtered
}

// GetAPITokensByUser retrieves the user-scoped tokens a user owns.
func (s *Store) GetAPITokensByUser(accountID, userID string) ([]APIToken, error) {
	all, err := s.listAPITokens(accountID)
	if err != nil {
		return nil, err
	}
	return filterAPITokens(all, func(t *APIToken) bool {
		return t.Scope == APITokenScopeUser && t.UserID == userID
	}), nil
}

// GetAccountAPITokens retrieves every account-scoped token in the account.
func (s *Store) GetAccountAPITokens(accountID string) ([]APIToken, error) {
	all, err := s.listAPITokens(accountID)
	if err != nil {
		return nil, err
	}
	return filterAPITokens(all, func(t *APIToken) bool {
		return t.Scope == APITokenScopeAccount
	}), nil
}

// GetAPITokensVisibleTo retrieves the tokens a user can see: their own
// user-scoped tokens plus every account-scoped token.
func (s *Store) GetAPITokensVisibleTo(accountID, userID string) ([]APIToken, error) {
	all, err := s.listAPITokens(accountID)
	if err != nil {
		return nil, err
	}
	return filterAPITokens(all, func(t *APIToken) bool {
		return t.Scope == APITokenScopeAccount || t.UserID == userID
	}), nil
}

// GetAPITokensByType retrieves a user's own tokens of a given type.
func (s *Store) GetAPITokensByType(accountID, userID string, tokenType APITokenType) ([]APIToken, error) {
	own, err := s.GetAPITokensByUser(accountID, userID)
	if err != nil {
		return nil, err
	}
	return filterAPITokens(own, func(t *APIToken) bool { return t.Type == tokenType }), nil
}

// GetAPITokensVisibleToByType retrieves the tokens of a type a user can see.
func (s *Store) GetAPITokensVisibleToByType(accountID, userID string, tokenType APITokenType) ([]APIToken, error) {
	visible, err := s.GetAPITokensVisibleTo(accountID, userID)
	if err != nil {
		return nil, err
	}
	return filterAPITokens(visible, func(t *APIToken) bool { return t.Type == tokenType }), nil
}

// GetAPITokensByResource retrieves the tokens a user can see that are
// associated with a resource. This is how a backend finds the credential
// for the thing it is about to call on the user's behalf.
func (s *Store) GetAPITokensByResource(accountID, userID, resourceType, resourceID string) ([]APIToken, error) {
	visible, err := s.GetAPITokensVisibleTo(accountID, userID)
	if err != nil {
		return nil, err
	}
	return filterAPITokens(visible, func(t *APIToken) bool {
		return t.Metadata.ResourceType == resourceType && t.Metadata.ResourceID == resourceID
	}), nil
}

// DeleteAPIToken deletes a user-scoped token.
func (s *Store) DeleteAPIToken(accountID, userID, name string) error {
	key, err := apiTokenKey(APITokenScopeUser, userID, name)
	if err != nil {
		return err
	}
	return s.DeleteScoped(accountID, "apitoken", key)
}

// DeleteAccountAPIToken deletes an account-scoped token.
func (s *Store) DeleteAccountAPIToken(accountID, name string) error {
	key, _ := apiTokenKey(APITokenScopeAccount, "", name)
	return s.DeleteScoped(accountID, "apitoken", key)
}

// ///////////////////////////////////////////////////////////////////////////////////////
// OAuth User
// ///////////////////////////////////////////////////////////////////////////////////////

// NewOAuthUser creates a new OAuth user without password
func (s *Store) NewOAuthUser(config *user.OAuthUserConfig) (*user.User, error) {
	signingKeyPair, err := s.GetAccountKeyPair(config.AccountID)
	if err != nil {
		return &user.User{}, err
	}
	u, err := user.NewOAuthUser(config, signingKeyPair)
	if err != nil {
		return &user.User{}, err
	}
	err = s.PutUser(config.AccountID, u)
	if err != nil {
		return &user.User{}, err
	}
	return u, nil
}

// ///////////////////////////////////////////////////////////////////////////////////////
// OAuth Identity (Account-Scoped with Global Index)
// ///////////////////////////////////////////////////////////////////////////////////////

// PutOAuthIdentity stores an OAuth identity with account scoping and global index
func (s *Store) PutOAuthIdentity(accountID string, identity *user.OAuthIdentity) error {
	key := fmt.Sprintf("%s:%s", identity.Provider, identity.Subject)
	// Store identity in account scope
	err := s.PutScoped(accountID, "oauth", key, identity)
	if err != nil {
		return err
	}
	// Maintain global index for cross-account OAuth lookup
	index := &OAuthIndex{AccountID: accountID, UserID: identity.UserID}
	return s.PutOne(fmt.Sprintf("oauth_index:%s", key), index)
}

// GetOAuthIdentityBySubject retrieves an OAuth identity by provider and subject using the global index
// Returns the identity, accountID, and any error
func (s *Store) GetOAuthIdentityBySubject(provider, subject string) (*user.OAuthIdentity, string, error) {
	key := fmt.Sprintf("%s:%s", provider, subject)
	// Look up global index to find account
	var index OAuthIndex
	err := s.GetOne(fmt.Sprintf("oauth_index:%s", key), &index)
	if err != nil {
		return nil, "", err
	}
	// Fetch identity from account scope
	var identity user.OAuthIdentity
	err = s.GetScoped(index.AccountID, "oauth", key, &identity)
	if err != nil {
		return nil, "", err
	}
	return &identity, index.AccountID, nil
}

// GetOAuthIdentityByUserID retrieves an OAuth identity by user ID within an account
func (s *Store) GetOAuthIdentityByUserID(accountID, userID string) (*user.OAuthIdentity, error) {
	var identity user.OAuthIdentity
	filters := make([]store.Filter, 0)
	filters = append(filters, store.Filter{Path: "user_id", Value: userID})
	err := s.GetScopedByFilter(accountID, "oauth", &filters, &identity)
	if err != nil {
		return nil, err
	}
	return &identity, nil
}

// DeleteOAuthIdentity deletes an OAuth identity and its index
func (s *Store) DeleteOAuthIdentity(accountID, provider, subject string) error {
	key := fmt.Sprintf("%s:%s", provider, subject)
	// Delete global index
	s.DeleteOneByKey(fmt.Sprintf("oauth_index:%s", key))
	// Delete from account scope
	return s.DeleteScoped(accountID, "oauth", key)
}

// ///////////////////////////////////////////////////////////////////////////////////////
// OIDC Connections (Account-Scoped with Global Slug Index)
//
// Exactly one connection per account, stored under a fixed sub-key. A global
// index (oidc_slug_index:{slug}) maps the per-tenant login slug to its account
// so slug-routed handlers can resolve a connection without knowing the account.
// ///////////////////////////////////////////////////////////////////////////////////////

const oidcConnectionKey = "connection"

func oidcSlugIndexKey(slug string) string {
	return fmt.Sprintf("oidc_slug_index:%s", slug)
}

// GetOIDCConnectionByAccount returns the OIDC connection for an account, or an
// error if none is configured.
func (s *Store) GetOIDCConnectionByAccount(accountID string) (*user.OIDCConnection, error) {
	var conn user.OIDCConnection
	err := s.GetScoped(accountID, "oidcconn", oidcConnectionKey, &conn)
	if err != nil {
		return nil, err
	}
	return &conn, nil
}

// GetOIDCConnectionBySlug resolves a connection via the global slug index.
func (s *Store) GetOIDCConnectionBySlug(slug string) (*user.OIDCConnection, error) {
	var index OIDCSlugIndex
	if err := s.GetOne(oidcSlugIndexKey(slug), &index); err != nil {
		return nil, err
	}
	return s.GetOIDCConnectionByAccount(index.AccountID)
}

// PutOIDCConnection stores an account's OIDC connection and maintains the global
// slug index. Slug uniqueness is enforced across accounts: it is rejected if the
// slug already maps to a different account. If the account's slug changed, the
// stale slug-index entry is removed.
func (s *Store) PutOIDCConnection(accountID string, conn *user.OIDCConnection) error {
	conn.AccountID = accountID

	// Reject a slug already claimed by a different account.
	var existingIdx OIDCSlugIndex
	if err := s.GetOne(oidcSlugIndexKey(conn.Slug), &existingIdx); err == nil {
		if existingIdx.AccountID != accountID {
			return fmt.Errorf("oidc slug %q is already in use", conn.Slug)
		}
	}

	// If this account already had a connection under a different slug, drop the
	// stale slug-index entry (guarded so we never remove another account's slug).
	if prev, err := s.GetOIDCConnectionByAccount(accountID); err == nil && prev.Slug != "" && prev.Slug != conn.Slug {
		var prevIdx OIDCSlugIndex
		if idxErr := s.GetOne(oidcSlugIndexKey(prev.Slug), &prevIdx); idxErr == nil && prevIdx.AccountID == accountID {
			s.DeleteOneByKey(oidcSlugIndexKey(prev.Slug))
		}
	}

	if err := s.PutScoped(accountID, "oidcconn", oidcConnectionKey, conn); err != nil {
		return err
	}
	return s.PutOne(oidcSlugIndexKey(conn.Slug), &OIDCSlugIndex{AccountID: accountID})
}

// DeleteOIDCConnection removes an account's OIDC connection and its slug index.
// The slug index is only removed if it still points to this account, mirroring
// the user_email_index delete guard.
func (s *Store) DeleteOIDCConnection(accountID string) error {
	if conn, err := s.GetOIDCConnectionByAccount(accountID); err == nil && conn.Slug != "" {
		var idx OIDCSlugIndex
		if idxErr := s.GetOne(oidcSlugIndexKey(conn.Slug), &idx); idxErr == nil && idx.AccountID == accountID {
			s.DeleteOneByKey(oidcSlugIndexKey(conn.Slug))
		}
	}
	return s.DeleteScoped(accountID, "oidcconn", oidcConnectionKey)
}

// ResolveFederatedIdentity looks up a federated identity by OIDC issuer and
// subject. It first tries the issuer-based provider key
// (user.ProviderKeyForIssuer); when the issuer is Google it falls back to the
// legacy "google" provider key so identities created by the deprecated bespoke
// Google flow still resolve. It returns the identity, its account ID, the
// provider key it was found under (so the caller can lazily migrate), and any
// error.
func (s *Store) ResolveFederatedIdentity(issuerURL, subject string) (*user.OAuthIdentity, string, string, error) {
	providerKey := user.ProviderKeyForIssuer(issuerURL)
	identity, accountID, err := s.GetOAuthIdentityBySubject(providerKey, subject)
	if err == nil && identity != nil {
		return identity, accountID, providerKey, nil
	}
	if issuerURL == user.GoogleIssuerURL {
		legacyIdentity, legacyAccountID, legacyErr := s.GetOAuthIdentityBySubject(user.LegacyGoogleProvider, subject)
		if legacyErr == nil && legacyIdentity != nil {
			return legacyIdentity, legacyAccountID, user.LegacyGoogleProvider, nil
		}
	}
	return nil, "", "", err
}

// ResolveFederatedIdentityScoped looks up a federated identity WITHIN a specific
// account. Per-tenant OIDC login already knows the account (slug -> connection
// -> account), so the account-scoped identity record is read directly rather
// than through the global oauth_index. This is what keeps two tenants that use
// the same public issuer (for example both configuring Google) from cross-
// linking on a shared (issuer, subject): each tenant resolves only its own
// account's identity. Returns the identity, the provider key it was found under
// (for lazy migration), and any error.
func (s *Store) ResolveFederatedIdentityScoped(accountID, issuerURL, subject string) (*user.OAuthIdentity, string, error) {
	providerKey := user.ProviderKeyForIssuer(issuerURL)
	key := fmt.Sprintf("%s:%s", providerKey, subject)
	var identity user.OAuthIdentity
	if err := s.GetScoped(accountID, "oauth", key, &identity); err == nil && identity.UserID != "" {
		return &identity, providerKey, nil
	}
	// Fall back to the legacy Google provider key, still scoped to this account.
	if issuerURL == user.GoogleIssuerURL {
		legacyKey := fmt.Sprintf("%s:%s", user.LegacyGoogleProvider, subject)
		var legacy user.OAuthIdentity
		if err := s.GetScoped(accountID, "oauth", legacyKey, &legacy); err == nil && legacy.UserID != "" {
			return &legacy, user.LegacyGoogleProvider, nil
		}
	}
	return nil, "", fmt.Errorf("federated identity not found for account %s", accountID)
}

// ///////////////////////////////////////////////////////////////////////////////////////
// OAuth State (for CSRF protection during OAuth flow)
// ///////////////////////////////////////////////////////////////////////////////////////

// PutOAuthState stores an OAuth state with TTL
func (s *Store) PutOAuthState(state *user.OAuthState) error {
	return s.PutOne(fmt.Sprintf("oauthstate:%s", state.State), state)
}

// GetOAuthState retrieves an OAuth state
func (s *Store) GetOAuthState(state string) (*user.OAuthState, error) {
	var oauthState user.OAuthState
	err := s.GetOne(fmt.Sprintf("oauthstate:%s", state), &oauthState)
	if err != nil {
		return nil, err
	}
	return &oauthState, nil
}

// DeleteOAuthState deletes an OAuth state
func (s *Store) DeleteOAuthState(state string) error {
	return s.DeleteOneByKey(fmt.Sprintf("oauthstate:%s", state))
}

// ///////////////////////////////////////////////////////////////////////////////////////
// Invitation (Account-Scoped with Global Index)
// ///////////////////////////////////////////////////////////////////////////////////////

// PutInvitation stores an invitation with account scoping and global token index
func (s *Store) PutInvitation(inv *user.Invitation) error {
	// Store invitation in account scope
	err := s.PutScoped(inv.AccountID, "invitation", inv.Token, inv)
	if err != nil {
		return err
	}
	// Maintain global index for cross-account token lookup
	index := &InvitationIndex{AccountID: inv.AccountID}
	return s.PutOne(fmt.Sprintf("invitation_index:%s", inv.Token), index)
}

// GetInvitationByToken retrieves an invitation by its token using the global index
// Returns the invitation and any error
func (s *Store) GetInvitationByToken(token string) (*user.Invitation, error) {
	// Look up global index to find account
	var index InvitationIndex
	err := s.GetOne(fmt.Sprintf("invitation_index:%s", token), &index)
	if err != nil {
		return nil, err
	}
	// Fetch invitation from account scope
	var inv user.Invitation
	err = s.GetScoped(index.AccountID, "invitation", token, &inv)
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

// GetInvitationsByAccount retrieves all invitations for an account (now naturally scoped)
func (s *Store) GetInvitationsByAccount(accountID string) ([]*user.Invitation, error) {
	invitations := make([]user.Invitation, 0)
	err := s.ListScoped(accountID, "invitation", &invitations)
	if err != nil {
		return nil, err
	}
	// Convert to pointer slice
	result := make([]*user.Invitation, len(invitations))
	for i := range invitations {
		result[i] = &invitations[i]
	}
	return result, nil
}

// UpdateInvitation updates an existing invitation
func (s *Store) UpdateInvitation(inv *user.Invitation) error {
	// Store in account scope (index already exists)
	return s.PutScoped(inv.AccountID, "invitation", inv.Token, inv)
}

// DeleteInvitation deletes an invitation by token
func (s *Store) DeleteInvitation(token string) error {
	// Look up the account first
	var index InvitationIndex
	err := s.GetOne(fmt.Sprintf("invitation_index:%s", token), &index)
	if err != nil {
		return err
	}
	// Delete global index
	s.DeleteOneByKey(fmt.Sprintf("invitation_index:%s", token))
	// Delete from account scope
	return s.DeleteScoped(index.AccountID, "invitation", token)
}

// ///////////////////////////////////////////////////////////////////////////////////////
// Utilities
// ///////////////////////////////////////////////////////////////////////////////////////

// ValidateUserPassword validates a password for a user within an account
func (s *Store) ValidateUserPassword(accountID, userID, password string) (bool, error) {
	up, err := s.GetUserPassword(accountID, userID)
	if err != nil {
		return false, fmt.Errorf("issue with GetUserPassword: %v", err)
	}
	return user.ValidatePassword(up.PasswordHash, password), nil
}

// DeleteAccountData deletes all account-scoped data when deleting an account
func (s *Store) DeleteAccountData(accountID string) error {
	// Remove the global OIDC slug index before dropping the scoped connection,
	// otherwise it would be orphaned. Guarded inside DeleteOIDCConnection.
	if _, err := s.GetOIDCConnectionByAccount(accountID); err == nil {
		if delErr := s.DeleteOIDCConnection(accountID); delErr != nil {
			return fmt.Errorf("failed to delete oidc connection: %v", delErr)
		}
	}
	prefixes := []string{
		"user", "userPassword", "agent", "service",
		"apitoken", "invitation", "oauth", "activity", "oidcconn",
	}
	for _, prefix := range prefixes {
		err := s.DeleteScopedByPrefix(accountID, prefix)
		if err != nil {
			return fmt.Errorf("failed to delete %s data: %v", prefix, err)
		}
	}
	return nil
}
