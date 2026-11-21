// Server for Admin Service
package admin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bboxsys/platform/admin/email"
	"github.com/bboxsys/platform/ai/instance"
	"github.com/bboxsys/platform/pki"
	"github.com/bboxsys/platform/server"
	"github.com/bboxsys/platform/user"
	"github.com/bboxsys/store"
	"github.com/gorilla/mux"
	"golang.org/x/image/draw"
)

type AdminServer struct {
	uiFS fs.FS
	*server.Server
	st                        *Store
	auth                      *Auth
	operatorID                string
	adminAccountID            string
	intermediateCA            *pki.Intermediate
	tokenMinutes              int
	mu                        sync.Mutex
	serviceCredentialsMu      sync.Mutex
	ready                     bool
	googleOAuth               *GoogleOAuth
	oauthConfig               *OAuthConfig
	oidc                      *OIDCProvider
	invitationDefaultTTLHours int
	emailSender               email.Sender
	inviteAcceptURL           string
	logStore                  *LogStore
	logStopChan               chan struct{}
	metricsStore              *MetricsStore
	metricsStopChan           chan struct{}
	aiUsageStore              *AIUsageStore
	aiUsageStopChan           chan struct{}
	monitoringPort            int
	configPersist             func(key string, value interface{})
	branding                  Branding
	collectors                *Collectors
	natsAPI                   *adminNATSAPI
	natsAPIMu                 sync.Mutex
	ai                        *instance.Instance
	aiOpts                    AIOptions
	aiMu                      sync.Mutex
	assistants                *AssistantGateway
}

func (adminServer *AdminServer) Ready() bool {
	adminServer.mu.Lock()
	defer adminServer.mu.Unlock()
	return adminServer.ready
}

func (adminServer *AdminServer) Close() {
	// Before the collectors: the gateway's default connections are theirs.
	adminServer.closeAssistantGateway()
	adminServer.closeNATSAPI()
	if adminServer.collectors != nil {
		adminServer.collectors.Close()
	}
	if adminServer.logStopChan != nil {
		close(adminServer.logStopChan)
	}
	if adminServer.metricsStopChan != nil {
		close(adminServer.metricsStopChan)
	}
	if adminServer.aiUsageStopChan != nil {
		close(adminServer.aiUsageStopChan)
	}
	err := adminServer.Srv.Close()
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("error closing admin server")
	}
	err = adminServer.st.Close()
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("error closing admin store")
	}
	if adminServer.logStore != nil {
		err = adminServer.logStore.Close()
		if err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("error closing log store")
		}
	}
	if adminServer.metricsStore != nil {
		err = adminServer.metricsStore.Close()
		if err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("error closing metrics store")
		}
	}
	adminServer.closeAI()
	// After the assistant: its usage recorder may report to this store
	// (usage.Local) while it shuts down.
	if adminServer.aiUsageStore != nil {
		err = adminServer.aiUsageStore.Close()
		if err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("error closing ai usage store")
		}
	}
}
func (adminServer *AdminServer) GetUserIDByEmail(email string) (string, string, error) {
	u, accountID, err := adminServer.st.GetUserByEmail(email)
	if err != nil {
		return "", "", err
	}
	return u.ID, accountID, nil

}
func (adminServer *AdminServer) PutServiceEndpointsMap(serviceEndpoints map[string]ServiceEndpoint) error {
	return adminServer.st.PutServiceEndpointsMap(serviceEndpoints)
}

func (adminServer *AdminServer) AddTrustedAccount(id string) {
	adminServer.auth.AddTrustedAccount(id)
}

// Store returns the admin server's data store.
func (adminServer *AdminServer) Store() *Store {
	return adminServer.st
}

type InitializerData struct {
	OperatorID     string `json:"operator_id"`
	AdminAccountID string `json:"admin_account_id"`
}

func (adminServer *AdminServer) GetSysAccountID() (string, error) {
	account, err := adminServer.st.GetAccountByName("SYS")
	if err != nil {
		return "", err
	}
	return account.ID, nil
}

func (adminServer *AdminServer) GetOperatorJWT() (string, error) {
	operator, err := adminServer.st.GetOperatorByID(adminServer.OperatorID())
	if err != nil {
		return "", err
	}
	return operator.Token, nil
}

// This is for bootstrapping services - e.g. queue server
// Account -> User
// the user is created and stored in datastore but credentials are not stored.
// returns JWT, seed, error
// This should potentially include other data such as certs, trustedAccounts, etc.
func (adminServer *AdminServer) ServiceCredentials(accountName string, serviceName string) (*user.Credentials, error) {
	// Serialize get-or-create so concurrent callers (e.g. two
	// services' bootstrap goroutines both calling with the same
	// accountName) cannot race past the existence check and both create
	// duplicate accounts with the same name.
	adminServer.serviceCredentialsMu.Lock()
	defer adminServer.serviceCredentialsMu.Unlock()

	var credentials user.Credentials
	// Check if account exists
	var account user.Account
	var err error
	account, err = adminServer.st.GetAccountByName(accountName)
	if err != nil {
		return &credentials, err
	}
	// Account does not exist - create
	if account.ID == "" {
		account, err = adminServer.st.NewAccount(user.AccountConfig{
			Name:       accountName,
			OperatorID: adminServer.OperatorID(),
		})
		if err != nil {
			return &credentials, err
		}
	}
	// Add all accounts as trusted so services can accept requests from any user
	allAccounts, err := adminServer.st.GetAccounts()
	if err == nil {
		for _, acc := range *allAccounts {
			credentials.AddTrustedAccount(acc.ID)
		}
	}
	sc, err := adminServer.serviceCredentialsForAccount(account.ID, serviceName)
	if err != nil {
		return &credentials, err
	}
	credentials.AccountID = sc.AccountID
	credentials.AddTrustedAccount(sc.AccountID)
	credentials.JWT = sc.JWT
	credentials.Seed = sc.Seed
	return &credentials, nil
}

// serviceCredentialsForAccount returns the NATS credentials of the named
// service user in an existing account, creating the user on first use. It
// does not fill TrustedAccounts; ServiceCredentials does that for services
// that also authenticate HTTP requests.
func (adminServer *AdminServer) serviceCredentialsForAccount(accountID string, serviceName string) (*user.Credentials, error) {
	credentials := &user.Credentials{AccountID: accountID}
	var service *user.Service
	services, err := adminServer.st.GetServices(accountID)
	if err == nil {
		for i := range *services {
			uu := &(*services)[i]
			if uu.ID == fmt.Sprintf("%v:%v", accountID, serviceName) && uu.Claims.Issuer == accountID {
				service = uu
				break
			}
		}
	}
	if service == nil {
		service, err = adminServer.st.NewService(&user.UserConfig{
			Name:      serviceName,
			AccountID: accountID,
		})
		if err != nil {
			return credentials, err
		}
	}
	credentials.JWT = service.Token
	credentials.Seed = service.Seed
	return credentials, nil
}

// publishTenantNotification publishes a tenant lifecycle notification over
// NATS. It is a no-op when the admin service has no messenger client
// (e.g., controller not configured), so callers don't need to guard.
// accountCreated runs the bookkeeping every new account needs: trust its
// tokens, start collecting its logs and metrics, and notify services.
func (adminServer *AdminServer) accountCreated(account user.Account) {
	adminServer.auth.AddTrustedAccount(account.ID)
	if adminServer.collectors != nil {
		go adminServer.collectors.AddAccount(account.ID, account.Name)
	}
	adminServer.publishTenantNotification(SubjectTenantCreated, &TenantCreatedNotification{
		AccountID:   account.ID,
		AccountName: account.Name,
		Timestamp:   time.Now().Unix(),
	})
}

func (adminServer *AdminServer) publishTenantNotification(subject string, msg interface{}) {
	if adminServer == nil || adminServer.MessengerClient == nil {
		return
	}
	adminServer.MessengerClient.Publish(subject, msg)
}

// configuration required for agent to run standalone
func (adminServer *AdminServer) AgentConfiguration(aco *user.AgentConfigurationOptions) (*user.AgentConfiguration, error) {
	var ac user.AgentConfiguration
	// check if account exists
	var account user.Account
	var err error
	account, err = adminServer.st.GetAccountByID(aco.AccountID)
	if err != nil {
		return &ac, err
	}
	// account does not exist
	if account.ID == "" {
		return &ac, errors.New("agent: configuration: account does not exist")
	}
	// check if agent exists
	agent := &user.Agent{}
	agentExists := false
	if aco.HostID != "" {
		agent, err = adminServer.st.GetAgentByHostID(account.ID, aco.HostID)
		if err != nil {
			adminServer.Logger.Info("agent id provided but no agent exists in db")
			agentExists = false
		} else if agent.Claims.Issuer == account.ID {
			adminServer.Logger.Info("agent id provided and exists in db")
			agentExists = true
		}
	} else if aco.HostID != "" {
		agent, err = adminServer.st.GetAgentByHostID(account.ID, aco.HostID)
		if err != nil {
			adminServer.Logger.Info("host id provided but no agent exits in db")
			agentExists = false
		} else if agent.Claims.Issuer == account.ID {
			adminServer.Logger.Info("host id provided and exists in db")
			agentExists = true
		}
	} else {
		return &ac, errors.New("no account id or host id provided")
	}
	if err := validateRoles(aco.Roles); err != nil {
		return &ac, err
	}
	if !agentExists {
		userConfig := user.UserConfig{
			HostID:      aco.HostID,
			Hostname:    aco.Hostname,
			AccountID:   account.ID,
			Roles:       aco.Roles,
			Permissions: aco.Permissions,
		}
		adminServer.Logger.InfoE().Str("hostID", aco.HostID).Str("hostname", aco.Hostname).Strs("roles", aco.Roles).Msg("the agent does not exist so creating")
		agent, err = adminServer.st.NewAgent(&userConfig)
		if err != nil {
			return &ac, err
		}
		adminServer.Logger.InfoE().Str("agentID", agent.ID).Msg("AgentConfiguration: created agent")
	} else if aco.Roles != nil || aco.Permissions != nil {
		// A re-registration that names roles or permissions updates the
		// agent and reissues its token so the JWT carries them.
		if aco.Roles != nil {
			agent.Roles = aco.Roles
		}
		if aco.Permissions != nil {
			agent.Permissions = aco.Permissions
			if agent.Permissions.IsEmpty() {
				agent.Permissions = nil
			}
		}
		kp, err := adminServer.st.GetAccountKeyPair(account.ID)
		if err != nil {
			return &ac, err
		}
		if err := agent.Reissue(kp); err != nil {
			return &ac, err
		}
		if err := adminServer.st.PutAgent(account.ID, agent); err != nil {
			return &ac, err
		}
		adminServer.Logger.InfoE().Str("agentID", agent.ID).Strs("roles", agent.Roles).Msg("AgentConfiguration: updated agent roles and permissions")
	}
	serviceEndpoints, err := adminServer.st.GetServiceEndpoints()
	if err != nil {
		return &ac, err
	}
	ac.ControllerURL = serviceEndpoints["controller_service"].URL
	ac.HostID = agent.HostID
	ac.AccountID = account.ID
	// todo: create a new token for each request
	ac.JWT = agent.Token
	ac.Seed = agent.Seed
	if adminServer.intermediateCA == nil {
		return &ac, errors.New("agent: configuration: no intermediate CA configured (admin_server.intermediate_{ca,cert,key}_file)")
	}
	cert, key, err := adminServer.intermediateCA.NewCertificate(aco.Hostname, []string{aco.Hostname}, []string{})
	if err != nil {
		return &ac, err
	}
	ac.Certificate = cert
	ac.PrivateKey = key
	if err != nil {
		return &ac, err
	}
	caChain, err := adminServer.intermediateCA.Chain()
	if err != nil {
		return &ac, err
	}
	// The agent verifies the controller's certificate with this bundle, so
	// it needs the CA the services trust as well as its own issuing chain.
	ac.CA = caChain
	if adminServer.ServerOptions != nil && len(adminServer.ServerOptions.CA) > 0 {
		if len(ac.CA) > 0 && ac.CA[len(ac.CA)-1] != '\n' {
			ac.CA.Append(pki.Certificate("\n"))
		}
		ac.CA.Append(pki.Certificate(adminServer.ServerOptions.CA))
	}
	return &ac, nil
}

func NewAdminServer(options *server.ServerOptions) (*AdminServer, error) {
	storeOptions := &AdminStoreOptions{
		Location:    options.GetMetadata("storeLocation").(string),
		Application: options.Application,
		Component:   "idp_store",
	}
	// Optional: where the AES key that seals stored API token secrets lives.
	// Defaults to secrets.key beside the store directory.
	if keyFile, ok := options.GetMetadata("secretsKeyFile").(string); ok && keyFile != "" {
		storeOptions.SecretsKeyFile = keyFile
	}
	st, err := NewStore(storeOptions)
	if err != nil {
		return &AdminServer{}, err
	}
	s, err := server.NewServer(options)
	if err != nil {
		return &AdminServer{}, err
	}
	adminServer := &AdminServer{
		Server:                    s,
		st:                        st,
		auth:                      &Auth{},
		operatorID:                "",
		adminAccountID:            "",
		intermediateCA:            nil,
		tokenMinutes:              options.GetMetadata("tokenValidMinutes").(int),
		mu:                        sync.Mutex{},
		ready:                     false,
		googleOAuth:               nil,
		oauthConfig:               nil,
		invitationDefaultTTLHours: 72,
	}

	// Branding lets each host application (any product) present the
	// shared admin console under its own name and mark. Defaults to Black Box
	// when the host sets nothing.
	adminServer.branding = Branding{Name: "Black Box"}
	if name, ok := options.GetMetadata("branding_name").(string); ok && name != "" {
		adminServer.branding.Name = name
	}
	if logoURL, ok := options.GetMetadata("branding_logo_url").(string); ok && logoURL != "" {
		adminServer.branding.LogoURL = logoURL
	}
	if brand, ok := options.GetMetadata("branding_brand").(string); ok && brand != "" {
		adminServer.branding.Brand = brand
	}
	if css, ok := options.GetMetadata("branding_css").(string); ok && css != "" {
		adminServer.branding.CSS = css
	}

	// Initialize OAuth if configured
	if oauthEnabled, ok := options.GetMetadata("oauth_enabled").(bool); ok && oauthEnabled {
		clientID, _ := options.GetMetadata("google_client_id").(string)
		clientSecret, _ := options.GetMetadata("google_client_secret").(string)
		redirectURI, _ := options.GetMetadata("oauth_redirect_uri").(string)

		stateTTLMinutes := 10 // default
		if ttl, ok := options.GetMetadata("oauth_state_ttl_minutes").(int); ok && ttl > 0 {
			stateTTLMinutes = ttl
		}

		allowSelfService := false
		if allow, ok := options.GetMetadata("allow_self_service_accounts").(bool); ok {
			allowSelfService = allow
		}

		if clientID != "" && clientSecret != "" && redirectURI != "" {
			oauthConfig := &OAuthConfig{
				Enabled:                  true,
				GoogleClientID:           clientID,
				GoogleClientSecret:       clientSecret,
				RedirectURI:              redirectURI,
				StateTTL:                 time.Duration(stateTTLMinutes) * time.Minute,
				AllowSelfServiceAccounts: allowSelfService,
			}
			adminServer.oauthConfig = oauthConfig
			adminServer.googleOAuth = NewGoogleOAuth(oauthConfig, st)
		}
	}

	// The OIDC provider drives both per-tenant OIDC connections and the
	// deprecated Google shims (Google is modeled as an OIDC connection). It is
	// always available, independent of whether Google OAuth is configured.
	oidcStateTTL := 10 * time.Minute
	if adminServer.oauthConfig != nil && adminServer.oauthConfig.StateTTL > 0 {
		oidcStateTTL = adminServer.oauthConfig.StateTTL
	}
	adminServer.oidc = NewOIDCProvider(st, oidcStateTTL)

	// Initialize invitation TTL if configured
	if ttlHours, ok := options.GetMetadata("invitation_default_ttl_hours").(int); ok && ttlHours > 0 {
		adminServer.invitationDefaultTTLHours = ttlHours
	}

	// Initialize email sender. Uses SMTP submission when configured;
	// otherwise NoopSender logs the invitation token so dev environments
	// work without any provider configured.
	inviteAcceptURL, _ := options.GetMetadata("invite_accept_url").(string)
	appDisplayName, _ := options.GetMetadata("app_display_name").(string)
	adminServer.inviteAcceptURL = inviteAcceptURL

	smtpHost, _ := options.GetMetadata("smtp_host").(string)
	smtpUsername, _ := options.GetMetadata("smtp_username").(string)
	smtpPassword, _ := options.GetMetadata("smtp_password").(string)
	smtpFrom, _ := options.GetMetadata("smtp_from").(string)
	if smtpHost != "" && smtpUsername != "" && smtpPassword != "" && smtpFrom != "" && inviteAcceptURL != "" {
		smtpPort, _ := options.GetMetadata("smtp_port").(int)
		smtpFromName, _ := options.GetMetadata("smtp_from_name").(string)
		sender, err := email.NewSMTPSender(email.SMTPConfig{
			Host:            smtpHost,
			Port:            smtpPort,
			Username:        smtpUsername,
			Password:        smtpPassword,
			FromEmail:       smtpFrom,
			FromName:        smtpFromName,
			InviteAcceptURL: inviteAcceptURL,
			AppDisplayName:  appDisplayName,
		}, adminServer.Logger)
		if err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("email: failed to init SMTP sender, falling back to noop")
		} else {
			adminServer.emailSender = sender
		}
	}

	if adminServer.emailSender == nil {
		adminServer.emailSender = email.NewNoopSender(adminServer.Logger)
	}
	if options.GetMetadata("intermediate_cert_file") != nil && options.GetMetadata("intermediate_key_file") != nil && options.GetMetadata("intermediate_cert_file").(string) != "" && options.GetMetadata("intermediate_key_file").(string) != "" {
		intermediate, err := pki.NewIntermediate(
			options.GetMetadata("intermediate_ca_file").(string),
			options.GetMetadata("intermediate_cert_file").(string),
			options.GetMetadata("intermediate_key_file").(string),
			"blackbox",
		)
		if err != nil {
			return adminServer, err
		}
		adminServer.intermediateCA = intermediate
	}
	/////////////////////////////////////////////////////////////////////////////////////////
	// Initialize
	/////////////////////////////////////////////////////////////////////////////////////////
	initialized, err := adminServer.st.GetInitialized()
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("initialize: error getting initialized")
	}
	// If not initialized:
	// * Create Operator and add to store
	// * Create SYS Account and add to store
	// * Create ADMIN account and add to store
	// * Create default ADMIN user and add to store
	if initialized {
		adminServer.Logger.Info("initializer: this is NOT first run... initializing...")
		initializerData, err := adminServer.st.GetInitializerData()
		if err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("initialize: error getting initializerData")
		}
		adminServer.SetOperatorID(initializerData.OperatorID)
		adminServer.SetAdminAccountID(initializerData.AdminAccountID)
		adminServer.Logger.InfoE().Str("operatorID", adminServer.OperatorID()).Str("adminAccountID", adminServer.AdminAccountID()).Msg("initialize: already initialized")
	} else {
		adminServer.Logger.Info("initializing: first run.... initializing admin server")
		operator, err := adminServer.st.NewOperator(user.OperatorConfig{
			Name:             "operator",
			ServiceURL:       adminServer.ServerOptions.GetMetadata("operatorServiceURL").(string),
			AccountServerURL: adminServer.ServerOptions.GetMetadata("accountServerURL").(string),
		})
		if err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("initialize: error inititializing operator")
		}
		adminServer.SetOperatorID(operator.ID)
		adminServer.Logger.InfoE().Str("operatorID", adminServer.OperatorID()).Msg("initialize: initialized operator")

		// Create ADMIN Account
		adminAccount, err := adminServer.st.NewAccount(user.AccountConfig{
			Name:        "ADMIN",
			Description: "base administrative account for admin service",
			Owner:       adminServer.ServerOptions.GetMetadata("defaultAdminEmail").(string),
			OperatorID:  operator.ID,
		})
		if err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("initialize: error inititializing ADMIN account")
		}
		adminServer.SetAdminAccountID(adminAccount.ID)
		adminServer.Logger.InfoE().Str("adminAccountID", adminServer.AdminAccountID()).Msg("initialize: initialized ADMIN account")

		// Create default ADMIN user
		adminUserConfig := user.UserConfig{
			Email:    adminServer.ServerOptions.GetMetadata("defaultAdminEmail").(string),
			Password: adminServer.ServerOptions.GetMetadata("defaultAdminPassword").(string),
		}
		adminUserConfig.AccountID = adminAccount.ID
		adminUserConfig.Roles = []string{user.RoleAdministrator}
		adminUser, err := adminServer.st.NewUser(&adminUserConfig)
		if err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("initialize: error inititializing default ADMIN user")
		}
		adminServer.Logger.InfoE().Str("email", adminUser.Email).Str("userID", adminUser.ID).Str("accountID", adminAccount.ID).Msg("initialize: initialized default admin user")

		// Create SYS Account
		account, err := adminServer.st.NewAccount(user.AccountConfig{
			Name:        "SYS",
			Description: "used for NATS.io accounting service",
			Owner:       adminServer.ServerOptions.GetMetadata("defaultAdminEmail").(string),
			OperatorID:  operator.ID,
		})
		if err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("initialize: error inititializing SYS account")
		}
		err = adminServer.st.PutInitialized()
		if err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("initialize: error setting initialization")
		}
		adminServer.Logger.InfoE().Str("accountID", account.ID).Msg("initialize: initialized SYS account")

		// Put Initialized
		err = adminServer.st.PutInitialized()
		if err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("initialize: error setting initialization")
		}
		adminServer.Logger.Info("initialize: put initialized")

		// Put Initializer Data
		err = adminServer.st.PutInitializerData(InitializerData{
			OperatorID:     adminServer.OperatorID(),
			AdminAccountID: adminServer.AdminAccountID(),
		})
		if err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("initialize: error setting initialization")
		}
		adminServer.Logger.Info("initialize: put initialized")
		initialized, err := adminServer.st.GetInitialized()
		if err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("initialize: error getting initialized")
		}
		adminServer.Logger.InfoE().Any("initialized", initialized).Msg("initialize: put initialized result")

	}
	return adminServer, nil
}

func (adminServer *AdminServer) Start() {
	adminServer.AddHandlers()
	adminServer.SetReady()
	adminServer.Serve()
}

func (adminServer *AdminServer) SetReady() {
	adminServer.mu.Lock()
	defer adminServer.mu.Unlock()
	adminServer.ready = true
}

func (adminServer *AdminServer) AddHandlers() {

	/////////////////////////////////////////////////////////////////////////////////////////
	// Endpoints will have different authentication and authorization requirements.  We will
	// use middleware to populate the request context with grants.
	/////////////////////////////////////////////////////////////////////////////////////////

	/////////////////////////////////////////////////////////////////////////////////////////
	// Middleware
	/////////////////////////////////////////////////////////////////////////////////////////
	id := NewID(IDConfig{
		Store:     adminServer.st,
		Logger:    adminServer.Logger,
		BasicAuth: true,
	})
	adminServer.Router.Use(id.Middleware)
	adminServer.auth = NewAuth(AuthConfig{
		Store:           adminServer.st,
		Logger:          adminServer.Logger,
		BasicAuth:       true,
		TrustedAccounts: []string{adminServer.AdminAccountID()},
	})
	// Trust all existing accounts so tenant users can access admin API
	if allAccounts, err := adminServer.st.GetAccounts(); err == nil {
		for _, acc := range *allAccounts {
			adminServer.auth.AddTrustedAccount(acc.ID)
		}
	}

	/////////////////////////////////////////////////////////////////////////////////////////
	// Default
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.ServeOpenAPI(openapiSpec)
	adminServer.addUIHandlers()

	/////////////////////////////////////////////////////////////////////////////////////////
	// Noop used for authentication validation and session extension
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/noop", adminServer.auth.BasicOrBearer(adminServer.Noop)).Methods("GET")

	adminAccountID := adminServer.AdminAccountID()

	/////////////////////////////////////////////////////////////////////////////////////////
	// Operator Management (super admin only)
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/admin", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetOperators)).Methods("GET")
	//adminServer.s.Router.HandleFunc("/api/v1/admin", adminServer.auth.BasicOrBearer(adminServer.NewAdmin)).Methods("POST")
	adminServer.Router.HandleFunc("/api/v1/admin", adminServer.auth.RequireSuperAdmin(adminAccountID, server.EndpointNotImplemented)).Methods("PUT")
	adminServer.Router.HandleFunc("/api/v1/admin", adminServer.auth.RequireSuperAdmin(adminAccountID, server.EndpointNotImplemented)).Methods("DELETE")

	/////////////////////////////////////////////////////////////////////////////////////////
	// Account Management (super admin only, except /accounts/me)
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/accounts/me", adminServer.auth.RequireAdmin(adminAccountID, adminServer.GetMyAccount)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/accounts/stats", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetAccountStats)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/accounts", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetAccounts)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/accounts", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.NewAccount)).Methods("POST")
	adminServer.Router.HandleFunc("/api/v1/accounts", adminServer.auth.RequireSuperAdmin(adminAccountID, server.EndpointNotImplemented)).Methods("PUT")
	adminServer.Router.HandleFunc("/api/v1/accounts", adminServer.auth.RequireSuperAdmin(adminAccountID, server.EndpointNotImplemented)).Methods("DELETE")

	adminServer.Router.HandleFunc("/api/v1/accounts/name/{accountName}", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetAccountByName)).Methods("GET")

	adminServer.Router.HandleFunc("/api/v1/accounts/{accountID}", adminServer.auth.RequireSuperAdmin(adminAccountID, server.EndpointNotImplemented)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/accounts/{accountID}", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.DeleteAccount)).Methods("DELETE")
	adminServer.Router.HandleFunc("/api/v1/accounts/{accountID}", adminServer.auth.RequireSuperAdmin(adminAccountID, server.EndpointNotImplemented)).Methods("PUT")

	/////////////////////////////////////////////////////////////////////////////////////////
	// Roles
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/roles", adminServer.auth.Bearer(adminServer.GetRoles)).Methods("GET")

	/////////////////////////////////////////////////////////////////////////////////////////
	// User Management (admin role required)
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/users", adminServer.auth.RequireAdmin(adminAccountID, adminServer.GetUsers)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/users", adminServer.auth.RequireAdmin(adminAccountID, adminServer.NewUser)).Methods("POST")
	adminServer.Router.HandleFunc("/api/v1/users", adminServer.auth.RequireAdmin(adminAccountID, server.EndpointNotImplemented)).Methods("PUT")
	adminServer.Router.HandleFunc("/api/v1/users/{id}", adminServer.auth.RequireAdmin(adminAccountID, adminServer.DeleteUser)).Methods("DELETE")

	adminServer.Router.HandleFunc("/api/v1/users/{userID}", adminServer.auth.RequireAdmin(adminAccountID, adminServer.GetUser)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/users/{userID}", adminServer.auth.Bearer(server.EndpointNotImplemented)).Methods("DELETE")
	adminServer.Router.HandleFunc("/api/v1/users/{userID}", adminServer.auth.Bearer(server.EndpointNotImplemented)).Methods("PUT")

	adminServer.Router.HandleFunc("/api/v1/users/{userID}/token", adminServer.auth.BasicOrBearer(adminServer.CreateToken)).Methods("POST")
	adminServer.Router.HandleFunc("/api/v1/users/{userID}/token", server.EndpointNotImplemented).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/users/{userID}/token", server.EndpointNotImplemented).Methods("DELETE")
	adminServer.Router.HandleFunc("/api/v1/users/{userID}/token", server.EndpointNotImplemented).Methods("PUT")

	// Profile photo upload - users can upload their own photo
	adminServer.Router.HandleFunc("/api/v1/users/me/photo", adminServer.auth.Bearer(adminServer.UploadProfilePhoto)).Methods("POST")
	adminServer.Router.HandleFunc("/api/v1/users/me/photo", adminServer.auth.Bearer(adminServer.DeleteProfilePhoto)).Methods("DELETE")

	// Password change - users can change their own password
	adminServer.Router.HandleFunc("/api/v1/users/me/password", adminServer.auth.Bearer(adminServer.ChangeUserPassword)).Methods("PUT")

	// Theme preference - users can set their UI theme
	adminServer.Router.HandleFunc("/api/v1/users/me/theme", adminServer.auth.Bearer(adminServer.UpdateThemePreference)).Methods("PUT")

	// User preferences - generic key-value store
	adminServer.Router.HandleFunc("/api/v1/users/me/preferences", adminServer.auth.Bearer(adminServer.GetPreferences)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/users/me/preferences/{key}", adminServer.auth.Bearer(adminServer.SetPreference)).Methods("PUT")
	adminServer.Router.HandleFunc("/api/v1/users/me/preferences/{key}", adminServer.auth.Bearer(adminServer.DeletePreference)).Methods("DELETE")

	// Account unlock - account admin or super admin
	adminServer.Router.HandleFunc("/api/v1/accounts/{accountID}/users/{userID}/unlock", adminServer.auth.RequireAdmin(adminAccountID, adminServer.UnlockUser)).Methods("POST")

	// Activities - account admin or super admin
	adminServer.Router.HandleFunc("/api/v1/activities", adminServer.auth.RequireAdmin(adminAccountID, adminServer.GetActivities)).Methods("GET")

	adminServer.Router.HandleFunc("/api/v1/register", server.EndpointNotImplemented).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/login", adminServer.auth.Basic(adminServer.Login)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/logout", adminServer.auth.BasicOrBearer(adminServer.Logout)).Methods("GET")

	/////////////////////////////////////////////////////////////////////////////////////////
	// Service Management (super admin only)
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/services", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetServices)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/services", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.NewService)).Methods("POST")
	adminServer.Router.HandleFunc("/api/v1/services/{id}", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.DeleteService)).Methods("DELETE")

	/////////////////////////////////////////////////////////////////////////////////////////
	// Agent Management (super admin only)
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/agents", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetAgents)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/agents", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.NewAgent)).Methods("POST")

	adminServer.Router.HandleFunc("/api/v1/agents/{id}", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.DeleteAgent)).Methods("DELETE")

	adminServer.Router.HandleFunc("/api/v1/agents/configuration", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.NewAgentConfiguration)).Methods("POST")

	/////////////////////////////////////////////////////////////////////////////////////////
	// Account JWT (unchanged - internal)
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/jwt/v1/accounts/", adminServer.Ping).Methods("GET")
	adminServer.Router.HandleFunc("/jwt/v1/accounts/{accountID}", adminServer.ValidateAccountJWT).Methods("GET")
	adminServer.Router.HandleFunc("/jwt/v1/accounts/", adminServer.auth.Bearer(adminServer.PutAccount)).Methods("POST")

	/////////////////////////////////////////////////////////////////////////////////////////
	// Service Endpoints (super admin only)
	// Notes:
	//   -Service endpoints are unique by URL.
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/serviceendpoints", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetServiceEndpoints)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/serviceendpoints/kind/{kind}", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetServiceEndpointsByKind)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/serviceendpoints", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.NewServiceEndpoint)).Methods("POST")
	adminServer.Router.HandleFunc("/api/v1/serviceendpoints/{endpointURL}", server.EndpointNotImplemented).Methods("PUT")
	adminServer.Router.HandleFunc("/api/v1/serviceendpoints/{endpointURL}", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.DeleteServiceEndpoint)).Methods("DELETE")

	/////////////////////////////////////////////////////////////////////////////////////////
	// Service Credentials (super admin only)
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/servicecredentials/{accountName}/{serviceName}", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetServiceCredentials)).Methods("GET")

	/////////////////////////////////////////////////////////////////////////////////////////
	// PKI Management (super admin only)
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/pki/status", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetPKIStatus)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/pki/certificates", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetPKICertificates)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/pki/settings", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetPKISettings)).Methods("GET")

	/////////////////////////////////////////////////////////////////////////////////////////
	// Proxy Management (super admin only)
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/proxy", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetProxyConfig)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/proxy", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.UpdateProxyConfig)).Methods("POST")
	adminServer.Router.HandleFunc("/api/v1/proxy", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.UpdateProxyConfig)).Methods("PUT")
	adminServer.Router.HandleFunc("/api/v1/proxy/status", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetProxyStatus)).Methods("GET")

	/////////////////////////////////////////////////////////////////////////////////////////
	// API Token Management (unchanged - self-service)
	/////////////////////////////////////////////////////////////////////////////////////////
	// User-scoped API tokens: owner only. The list also includes the
	// account-scoped tokens the caller can use.
	adminServer.Router.HandleFunc("/api/v1/apitokens", adminServer.auth.Bearer(adminServer.GetAPITokens)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/apitokens", adminServer.auth.Bearer(adminServer.CreateAPIToken)).Methods("POST")
	adminServer.Router.HandleFunc("/api/v1/apitokens/{name}", adminServer.auth.Bearer(adminServer.GetAPIToken)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/apitokens/{name}", adminServer.auth.Bearer(adminServer.UpdateAPIToken)).Methods("PUT")
	adminServer.Router.HandleFunc("/api/v1/apitokens/{name}", adminServer.auth.Bearer(adminServer.DeleteAPIToken)).Methods("DELETE")
	// Account-scoped API tokens: any user in the account may read and use
	// them, administrators manage them.
	adminServer.Router.HandleFunc("/api/v1/accounts/me/apitokens", adminServer.auth.Bearer(adminServer.GetAccountAPITokens)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/accounts/me/apitokens", adminServer.auth.RequireAdmin(adminAccountID, adminServer.CreateAccountAPIToken)).Methods("POST")
	adminServer.Router.HandleFunc("/api/v1/accounts/me/apitokens/{name}", adminServer.auth.Bearer(adminServer.GetAccountAPIToken)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/accounts/me/apitokens/{name}", adminServer.auth.RequireAdmin(adminAccountID, adminServer.UpdateAccountAPIToken)).Methods("PUT")
	adminServer.Router.HandleFunc("/api/v1/accounts/me/apitokens/{name}", adminServer.auth.RequireAdmin(adminAccountID, adminServer.DeleteAccountAPIToken)).Methods("DELETE")

	/////////////////////////////////////////////////////////////////////////////////////////
	// OIDC (per-tenant federated login)
	// Public, slug-routed browser flow:
	adminServer.Router.HandleFunc("/api/v1/oidc/{slug}/authorize", adminServer.OIDCAuthorize).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/oidc/{slug}/callback", adminServer.OIDCCallback).Methods("GET")
	// Public secret-free config for a tenant's login UI:
	adminServer.Router.HandleFunc("/api/v1/oidc/config", adminServer.GetOIDCPublicConfig).Methods("GET")
	// Admin CRUD (account-scoped; super admin may target another account):
	adminServer.Router.HandleFunc("/api/v1/oidc/connection", adminServer.auth.RequireAdmin(adminAccountID, adminServer.GetOIDCConnection)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/oidc/connection", adminServer.auth.RequireAdmin(adminAccountID, adminServer.PutOIDCConnection)).Methods("PUT")
	adminServer.Router.HandleFunc("/api/v1/oidc/connection", adminServer.auth.RequireAdmin(adminAccountID, adminServer.DeleteOIDCConnection)).Methods("DELETE")

	// OAuth (DEPRECATED Google shims - delegate to the unified OIDC flow)
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/oauth/config", adminServer.GetOAuthConfig).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/oauth/google/authorize", adminServer.OAuthGoogleAuthorize).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/oauth/google/callback", adminServer.OAuthGoogleCallback).Methods("POST")
	adminServer.Router.HandleFunc("/api/v1/oauth/accounts", adminServer.auth.Bearer(adminServer.OAuthCreateAccount)).Methods("POST")

	/////////////////////////////////////////////////////////////////////////////////////////
	// Invitations (admin role required for management, public for lookup/accept)
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/invitations", adminServer.auth.RequireAdmin(adminAccountID, adminServer.CreateInvitation)).Methods("POST")
	adminServer.Router.HandleFunc("/api/v1/invitations", adminServer.auth.RequireAdmin(adminAccountID, adminServer.GetInvitations)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/invitations/{token}", adminServer.GetInvitationByToken).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/invitations/{token}/accept", adminServer.AcceptInvitation).Methods("POST")
	adminServer.Router.HandleFunc("/api/v1/invitations/{token}", adminServer.auth.RequireAdmin(adminAccountID, adminServer.RevokeInvitation)).Methods("DELETE")

	/////////////////////////////////////////////////////////////////////////////////////////
	// Log Management (super admin only)
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/logs", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetLogs)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/logs/count", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetLogsCount)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/logs/summary", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetLogsSummary)).Methods("GET")

	/////////////////////////////////////////////////////////////////////////////////////////
	// Store Metrics (super admin only)
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/metrics/stores", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetStoreMetrics)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/metrics/stores/history", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetStoreMetricsHistory)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/metrics/stores/history/count", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetStoreMetricsHistoryCount)).Methods("GET")

	/////////////////////////////////////////////////////////////////////////////////////////
	// AI usage (super admin only). Exact paths: the assistant's own routes
	// (sessions, status, documents, credentials) also live under /api/v1/ai.
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/ai/usage/instances", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetAIUsageInstances)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/ai/usage/runs", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetAIUsageRuns)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/ai/usage/summary", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetAIUsageSummary)).Methods("GET")

	/////////////////////////////////////////////////////////////////////////////////////////
	// Service Status (super admin only, proxies NATS monitoring)
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/services/status", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetServicesStatus)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/services/varz", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetServerVarz)).Methods("GET")

	/////////////////////////////////////////////////////////////////////////////////////////
	// Job queue (super admin only, proxies the queue_service endpoint)
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/queue", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetQueueJobs)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/queue/{id}", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetQueueJob)).Methods("GET")

	/////////////////////////////////////////////////////////////////////////////////////////
	// Log Level Configuration (super admin only)
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/config/loglevel", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetLogLevel)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/config/loglevel", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.SetLogLevel)).Methods("PUT")

	/////////////////////////////////////////////////////////////////////////////////////////
	// Log Max Entries Configuration (super admin only)
	/////////////////////////////////////////////////////////////////////////////////////////
	adminServer.Router.HandleFunc("/api/v1/config/logmaxentries", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.GetLogMaxEntries)).Methods("GET")
	adminServer.Router.HandleFunc("/api/v1/config/logmaxentries", adminServer.auth.RequireSuperAdmin(adminAccountID, adminServer.SetLogMaxEntries)).Methods("PUT")
}

func (adminServer *AdminServer) SetOperatorID(operatorID string) {
	adminServer.mu.Lock()
	defer adminServer.mu.Unlock()
	adminServer.operatorID = operatorID
}
func (adminServer *AdminServer) OperatorID() string {
	adminServer.mu.Lock()
	defer adminServer.mu.Unlock()
	return adminServer.operatorID
}

func (adminServer *AdminServer) SetAdminAccountID(accountID string) {
	adminServer.mu.Lock()
	defer adminServer.mu.Unlock()
	adminServer.adminAccountID = accountID
}
func (adminServer *AdminServer) AdminAccountID() string {
	adminServer.mu.Lock()
	defer adminServer.mu.Unlock()
	return adminServer.adminAccountID
}

// maxRequestBodySize bounds request bodies for admin JSON endpoints.
const maxRequestBodySize = 10 * 1024 * 1024 // 10MB

// forbidden is the JSON body returned for 403 responses.
const forbidden = `{"result":"error", "message": "forbidden"}`

// isSuperAdmin reports whether the authenticated caller belongs to the ADMIN
// account, which is how the platform designates super admins.
func (adminServer *AdminServer) isSuperAdmin(r *http.Request) bool {
	return AccountIDFromContext(r.Context()) == adminServer.AdminAccountID()
}

// resolveTargetAccountID returns the account ID a handler should operate on.
// Non-super-admins are always scoped to their own account and any account ID
// supplied by the client is ignored, which prevents cross-tenant reads and
// writes (including creating administrators in the ADMIN account). Super admins
// may target another account via requestedAccountID; an empty value falls back
// to the caller's own account.
func (adminServer *AdminServer) resolveTargetAccountID(r *http.Request, requestedAccountID string) string {
	callerAccountID := AccountIDFromContext(r.Context())
	if callerAccountID == adminServer.AdminAccountID() && requestedAccountID != "" {
		return requestedAccountID
	}
	return callerAccountID
}

// SetConfigPersister sets the function used to persist runtime config changes to the config file.
func (adminServer *AdminServer) SetConfigPersister(fn func(key string, value interface{})) {
	adminServer.configPersist = fn
}

// persistConfig persists a config key/value if a persister is set.
func (adminServer *AdminServer) persistConfig(key string, value interface{}) {
	if adminServer.configPersist != nil {
		adminServer.configPersist(key, value)
	}
}

// InitLogStore initializes the log store with the given location and max entries.
func (adminServer *AdminServer) InitLogStore(location string, maxEntries int) error {
	ls, err := NewLogStore(location, maxEntries, adminServer.Logger)
	if err != nil {
		return err
	}
	adminServer.logStore = ls
	adminServer.logStopChan = make(chan struct{})
	ls.StartGC(adminServer.logStopChan)
	adminServer.Logger.InfoE().Str("location", location).Int("maxEntries", maxEntries).Msg("log store initialized")
	return nil
}

// SetMonitoringPort sets the NATS monitoring port for the admin server to proxy.
func (adminServer *AdminServer) SetMonitoringPort(port int) {
	adminServer.Logger.InfoE().Int("port", port).Msg("setting monitoring port")
	adminServer.monitoringPort = port
}

// LogStore returns the log store for external use (e.g., NATS subscription handler).
func (adminServer *AdminServer) LogStore() *LogStore {
	return adminServer.logStore
}

// InitMetricsStore initializes the metrics store with the given location and max entries.
func (adminServer *AdminServer) InitMetricsStore(location string, maxEntries int) error {
	ms, err := NewMetricsStore(location, maxEntries, adminServer.Logger)
	if err != nil {
		return err
	}
	adminServer.metricsStore = ms
	adminServer.metricsStopChan = make(chan struct{})
	ms.StartGC(adminServer.metricsStopChan)
	adminServer.Logger.InfoE().Str("location", location).Int("maxEntries", maxEntries).Msg("metrics store initialized")
	return nil
}

// MetricsStore returns the metrics store for external use (e.g., NATS subscription handler).
func (adminServer *AdminServer) MetricsStore() *MetricsStore {
	return adminServer.metricsStore
}

// InitAIUsageStore opens the AI usage store at location, where the
// collectors keep every instance's records and heartbeats, and starts its
// daily pruning. A zero retention means DefaultAIUsageRetention.
func (adminServer *AdminServer) InitAIUsageStore(location string, retention time.Duration) error {
	us, err := NewAIUsageStore(location, retention, adminServer.Logger)
	if err != nil {
		return err
	}
	adminServer.aiUsageStore = us
	adminServer.aiUsageStopChan = make(chan struct{})
	us.StartGC(adminServer.aiUsageStopChan)
	adminServer.Logger.InfoE().Str("location", location).Str("retention", us.retention.String()).Msg("ai usage store initialized")
	return nil
}

// AIUsageStore returns the AI usage store, or nil when it was not
// initialized. It satisfies usage.Local for the admin's own assistant.
func (adminServer *AdminServer) AIUsageStore() *AIUsageStore {
	return adminServer.aiUsageStore
}

// GetBaseStore returns the underlying store.Store for metrics collection.
func (adminServer *AdminServer) GetBaseStore() *store.Store {
	return adminServer.st.Store
}

// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// Operators
// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////
func (adminServer *AdminServer) GetOperators(w http.ResponseWriter, r *http.Request) {
	var wg sync.WaitGroup
	w.Header().Set("Content-Type", "application/json")
	adminServer.st.GetOperatorsStream(w, &wg)
	wg.Wait()
}

// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// Account
// - must be in the ADMIN account to access this endpoint.
// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////
func (adminServer *AdminServer) NewAccount(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromContext(r.Context())
	if accountID == adminServer.AdminAccountID() {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize))
		if err != nil {
			http.Error(w, server.InternalServerError, http.StatusInternalServerError)
			adminServer.Logger.ErrorE().Err(err).Msg("new account: error reading body")
		} else {
			accountConfig := user.AccountConfig{}
			err := json.Unmarshal(body, &accountConfig)
			if err != nil {
				http.Error(w, server.InternalServerError, http.StatusInternalServerError)
				adminServer.Logger.ErrorE().Err(err).Msg("new account: error unmarshaling adminConfig from body")
			}
			accountConfig.OperatorID = adminServer.OperatorID()
			adminServer.Logger.InfoE().Any("accountConfig", accountConfig).Msg("new account: creating new account")
			a, err := adminServer.st.NewAccount(accountConfig)
			if err != nil {
				http.Error(w, server.InternalServerError, http.StatusInternalServerError)
				adminServer.Logger.ErrorE().Err(err).Msg("new account: error creating new account")
			}
			adminServer.Logger.InfoE().Str("accountID", a.ID).Msg("new account created")
			adminServer.accountCreated(a)
			ip, ipv4 := getClientIPs(r)
			activity := user.Activity{}
			activity.ID = a.ID
			activity.Active = true
			activity.LastIP = ip
			activity.LastIPv4 = ipv4
			activity.LoginCount = 0
			err = adminServer.st.UpdateActivity(a.ID, activity)
			if err != nil {
				adminServer.Logger.InfoE().Err(err).Msg("new account: error updating user activity")
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(a)

		}
	} else {
		adminServer.Logger.Error("new account: must be in ADMIN account to create a new account")
		http.Error(w, server.Unauthorized, http.StatusUnauthorized)
	}

}

// GetAccounts lists every account, by name. See writeList for paging.
func (adminServer *AdminServer) GetAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := adminServer.st.GetAccounts()
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("getAccounts: error getting accounts")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}
	writeList(w, r, *accounts, func(a, b user.Account) bool { return nameOrder(a.Name, a.ID, b.Name, b.ID) })
}
func (adminServer *AdminServer) GetAccountByName(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	accountName := vars["accountName"]
	w.Header().Set("Content-Type", "application/json")
	account, err := adminServer.st.GetAccountByName(accountName)
	if err != nil {
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		adminServer.Logger.ErrorE().Err(err).Msg("getAccountByName: error")
		return
	}

	json.NewEncoder(w).Encode(account)

}
func (adminServer *AdminServer) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	accountID := vars["accountID"]
	// Capture the name before deletion so the notification can carry it.
	var accountName string
	if a, err := adminServer.st.GetAccountByID(accountID); err == nil {
		accountName = a.Name
	}
	err := adminServer.st.DeleteAccount(accountID)
	if err != nil {
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		adminServer.Logger.ErrorE().Err(err).Msg("error deleting account")
		return
	}
	adminServer.Logger.InfoE().Str("accountID", accountID).Msg("account deleted")
	if g := adminServer.AssistantGateway(); g != nil {
		g.RemoveAccount(accountID)
	}
	if adminServer.collectors != nil {
		adminServer.collectors.RemoveAccount(accountID)
	}
	adminServer.publishTenantNotification(SubjectTenantDeleted, &TenantDeletedNotification{
		AccountID:   accountID,
		AccountName: accountName,
		Timestamp:   time.Now().Unix(),
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNoContent)
}

// GetMyAccount returns the authenticated user's own account
func (adminServer *AdminServer) GetMyAccount(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromContext(r.Context())
	if accountID == "" {
		http.Error(w, server.Unauthorized, http.StatusUnauthorized)
		return
	}
	account, err := adminServer.st.GetAccountByID(accountID)
	if err != nil {
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		adminServer.Logger.ErrorE().Str("accountID", accountID).Err(err).Msg("getMyAccount: error getting account")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(account)
}

// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// nats-account-server endpoints
// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////
func (adminServer *AdminServer) ValidateAccountJWT(w http.ResponseWriter, r *http.Request) {
	accountPublicKey := AccountPublicKeyFromContext(r.Context())
	// Our account ID and publicKey are synonymous
	account, err := adminServer.st.GetAccountByID(accountPublicKey)
	if err != nil {
		adminServer.Logger.ErrorE().Str("publicKey", accountPublicKey).Err(err).Msg("validateAccountJWT: no account for publicKey")
		http.Error(w, `{"result":"error", "message": "account not found"}`, http.StatusNotFound)
		return
	}
	w.Header().Add("Content-Type", "application/jwt")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write([]byte(account.Token))
	if err != nil {
		adminServer.Logger.ErrorE().Str("publicKey", accountPublicKey).Err(err).Msg("error writing JWT")
	}
}

func (adminServer *AdminServer) Ping(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "application/jwt")
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte(""))
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("error writing ping respnse")
	}
}

////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// User
////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

// role: admin
// Super admins can pass ?account_id=XXX to fetch users for a specific account
// GetUsers lists an account's users, by email. See writeList for paging.
func (adminServer *AdminServer) GetUsers(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromContext(r.Context())

	// Super admins can query users for any account
	if accountID == adminServer.AdminAccountID() {
		if queryAccountID := r.URL.Query().Get("account_id"); queryAccountID != "" {
			accountID = queryAccountID
		}
	}

	users, err := adminServer.st.GetUsers(accountID)
	if err != nil {
		adminServer.Logger.ErrorE().Str("accountID", accountID).Err(err).Msg("getUsers: error getting users")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}
	writeList(w, r, *users, func(a, b user.User) bool { return nameOrder(a.Email, a.ID, b.Email, b.ID) })
}

// NewUser godoc
// GET /api/v1/roles
// Returns the list of valid role names.
func (adminServer *AdminServer) GetRoles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user.AllRoles())
}

// @Summary Create a new user
// @Description Create a new user
// @Accept  json
// @Produce  json
// @Success 200 {object} user.User
// @Header 200 {string} Token "qwerty"
// @Router /accounts/ [post]

func (adminServer *AdminServer) NewUser(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize))
	if err != nil {
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		adminServer.Logger.ErrorE().Err(err).Msg("new user: error reading body")
		return
	} else {
		userConfig := user.UserConfig{}
		err := json.Unmarshal(body, &userConfig)
		if err != nil {
			http.Error(w, server.InternalServerError, http.StatusInternalServerError)
			adminServer.Logger.ErrorE().Err(err).Msg("new user: error unmarshaling userConfig from body")
		} else {
			// Scope the new user to the caller's account. Only super admins may
			// create users in another account by supplying account_id; for every
			// other caller a client-supplied account_id is ignored, so a tenant
			// admin cannot create users (or administrators) in the ADMIN account
			// or in another tenant.
			userConfig.AccountID = adminServer.resolveTargetAccountID(r, userConfig.AccountID)
			// Validate roles
			for _, role := range userConfig.Roles {
				if !user.IsValidRole(role) {
					http.Error(w, fmt.Sprintf("invalid role: %s", role), http.StatusBadRequest)
					return
				}
			}
			type userResponse struct {
				User user.User `json:"user"`
				Seed string    `json:"seed"`
			}
			u, err := adminServer.st.NewUser(&userConfig)
			if err != nil {
				http.Error(w, server.InternalServerError, http.StatusInternalServerError)
				adminServer.Logger.ErrorE().Err(err).Msg("new user: error creating new user")
				return
			}
			adminServer.Logger.InfoE().Str("email", u.Email).Str("userID", u.ID).Msg("new user: created new user")
			ip, ipv4 := getClientIPs(r)
			activity := user.Activity{}
			activity.ID = u.ID
			activity.Active = true
			activity.LastIP = ip
			activity.LastIPv4 = ipv4
			activity.LoginCount = 0
			err = adminServer.st.PutActivity(userConfig.AccountID, activity)
			if err != nil {
				adminServer.Logger.InfoE().Err(err).Msg("new user: error creating user activity")
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(u)
		}
	}

}

// Handle user registration. Uses a one time token that is passed as a query parameter
// This is not correct - just a placeholder
func (adminServer *AdminServer) Register(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromContext(r.Context())
	userID := UserIDFromContext(r.Context())
	if ValidBasicAuthFromContext(r.Context()) {
		uu, err := adminServer.st.GetUserByID(accountID, userID)
		if err != nil {
			adminServer.Logger.ErrorE().Str("userID", userID).Err(err).Msg("register: error getting user from database")
			http.Error(w, server.InternalServerError, http.StatusInternalServerError)
			return
		}
		adminServer.Logger.InfoE().Str("email", uu.Email).Str("userID", uu.ID).Msg("login: successfully logged in")
		ip, ipv4 := getClientIPs(r)
		activity := user.Activity{}
		activity.ID = uu.ID
		activity.Active = true
		activity.LastIP = ip
		activity.LastIPv4 = ipv4
		activity.LoggedIn = true
		activity.LoginCount = 1
		err = adminServer.st.UpdateActivity(accountID, activity)
		if err != nil {
			adminServer.Logger.InfoE().Str("userID", userID).Err(err).Msg("register: error updating activity")
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(uu)
	} else {
		adminServer.Logger.WarnE().Str("userID", userID).Msg("register: basic auth not valid")
		http.Error(w, server.Unauthorized, http.StatusUnauthorized)
		return
	}

}

// Handle user logins. Username/Password are sent via HTTP Basic Authentiation. We return the user token.
// todo: create tokens for each service account this use may be a member of. for now we could just have each
//
//	service trust the ADMIN account.
func (adminServer *AdminServer) Login(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromContext(r.Context())
	userID := UserIDFromContext(r.Context())
	if ValidBasicAuthFromContext(r.Context()) {
		if accountID != "" {
			uu, err := adminServer.st.GetUserByID(accountID, userID)
			if err != nil {
				adminServer.Logger.ErrorE().Str("userID", userID).Err(err).Msg("login: error getting user from database")
				http.Error(w, server.InternalServerError, http.StatusInternalServerError)
				return
			}
			resp, err := adminServer.issueSession(uu, r, false, "")
			if err != nil {
				adminServer.Logger.ErrorE().Str("userID", userID).Err(err).Msg("login: error issuing session")
				http.Error(w, server.InternalServerError, http.StatusInternalServerError)
				return
			}
			adminServer.Logger.InfoE().
				Str("email", uu.Email).
				Str("userID", uu.ID).
				Str("clientIP", getClientIP(r).String()).
				Msg("login: success")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(resp)
		} else {
			adminServer.Logger.ErrorE().Str("userID", userID).Msg("login: accountID not found for user")
			http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		}
	} else {
		// Log which email was attempted (never the password) so failed logins
		// are diagnosable. A bad credential is an expected operational event,
		// so it is logged at Warn, not Error. A request with no Authorization
		// header at all is a reachability probe (run-agent.sh, health checks),
		// not a failed login, so it only logs at Debug.
		attemptedEmail, _, _ := getEmailPassword(r)
		event := adminServer.Logger.WarnE()
		if r.Header.Get("Authorization") == "" {
			event = adminServer.Logger.DebugE()
		}
		event.
			Str("email", attemptedEmail).
			Str("clientIP", getClientIP(r).String()).
			Msg("login: invalid credentials")
		http.Error(w, server.Unauthorized, http.StatusUnauthorized)
		return
	}

}

// Handle user logouts - User must be authenticated.
func (adminServer *AdminServer) Logout(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromContext(r.Context())
	userID := UserIDFromContext(r.Context())
	if ValidBasicAuthFromContext(r.Context()) {
		uu, err := adminServer.st.GetUserByID(accountID, userID)
		if err != nil {
			adminServer.Logger.ErrorE().Str("userID", uu.ID).Err(err).Msg("logout: error getting user from database")
			http.Error(w, server.InternalServerError, http.StatusInternalServerError)
			return
		}
		adminServer.Logger.InfoE().Str("email", uu.Email).Str("userID", uu.ID).Msg("logout: successfully logged out")
		ip, ipv4 := getClientIPs(r)
		activity := user.Activity{}
		activity.ID = uu.ID
		activity.Active = true
		activity.LastIP = ip
		activity.LastIPv4 = ipv4
		activity.LoggedIn = false
		activity.LoginCount = 0
		err = adminServer.st.UpdateActivity(accountID, activity)
		if err != nil {
			adminServer.Logger.InfoE().Str("userID", uu.ID).Err(err).Msg("logout: error updating user activity")
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(uu)
		return
	} else {
		adminServer.Logger.WarnE().Str("userID", userID).Msg("logout: username/password not valid")
		http.Error(w, server.Unauthorized, http.StatusUnauthorized)
		return
	}
}

func (adminServer *AdminServer) CreateToken(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["userID"]
	accountID := AccountIDFromContext(r.Context())
	if ValidBasicAuthFromContext(r.Context()) {
		uu, err := adminServer.st.GetUserByID(accountID, id)
		if err != nil {
			adminServer.Logger.ErrorE().Str("userID", id).Err(err).Msg("createtoken: error getting user from database")
			http.Error(w, server.InternalServerError, http.StatusInternalServerError)
			return
		}
		kp, err := adminServer.st.GetAccountKeyPair(accountID)
		if err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("createtoken: error getting account KeyPair")
			http.Error(w, server.InternalServerError, http.StatusInternalServerError)
			return
		}
		token, err := user.NewUserToken(uu, adminServer.tokenMinutes, kp)
		if err != nil {
			adminServer.Logger.ErrorE().Str("userID", id).Err(err).Msg("createtoken: error creating token")
			http.Error(w, server.InternalServerError, http.StatusInternalServerError)
			return
		}
		err = adminServer.st.PutUser(accountID, uu)
		if err != nil {
			adminServer.Logger.ErrorE().Str("userID", id).Err(err).Msg("createtoken: error updating user in database")
			http.Error(w, server.InternalServerError, http.StatusInternalServerError)
			return
		}
		ip, ipv4 := getClientIPs(r)
		activity := user.Activity{}
		activity.ID = uu.ID
		activity.Active = true
		activity.LastIP = ip
		activity.LastIPv4 = ipv4
		activity.LoggedIn = true
		err = adminServer.st.UpdateActivity(accountID, activity)
		if err != nil {
			adminServer.Logger.InfoE().Err(err).Msg("new user: error updating user activity")
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(token)
		return
	} else {
		adminServer.Logger.WarnE().Str("userID", UserIDFromContext(r.Context())).Msg("createtoken: user UUID not valid")
		http.Error(w, server.Unauthorized, http.StatusUnauthorized)
		return
	}
}
func (adminServer *AdminServer) DeleteUser(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]
	accountID := AccountIDFromContext(r.Context())
	err := adminServer.st.DeleteUser(accountID, id)
	if err != nil {
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		adminServer.Logger.ErrorE().Err(err).Msg("error deleting user")
	}
	adminServer.Logger.InfoE().Str("userID", id).Msg("user deleted")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNoContent)
}

// GetUser returns a single user with their activity data
// Super admins can pass ?account_id=XXX or the account ID is extracted from the userID
func (adminServer *AdminServer) GetUser(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	userID := vars["userID"]
	accountID := AccountIDFromContext(r.Context())

	// Super admins can query users for any account
	if accountID == adminServer.AdminAccountID() {
		if queryAccountID := r.URL.Query().Get("account_id"); queryAccountID != "" {
			accountID = queryAccountID
		} else if idx := strings.Index(userID, ":"); idx > 0 {
			// Extract account ID from user ID (format: {accountID}:{email})
			accountID = userID[:idx]
		}
	}

	u, err := adminServer.st.GetUserByID(accountID, userID)
	if err != nil {
		adminServer.Logger.ErrorE().Str("userID", userID).Err(err).Msg("getUser: error getting user")
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	activity, err := adminServer.st.GetActivity(accountID, userID)
	if err != nil {
		adminServer.Logger.InfoE().Str("userID", userID).Err(err).Msg("getUser: no activity found")
		// Activity not found is not an error - user may not have logged in yet
	}

	type UserDetailResponse struct {
		User     *user.User     `json:"user"`
		Activity *user.Activity `json:"activity,omitempty"`
	}

	response := UserDetailResponse{
		User: u,
	}
	if activity.ID != "" {
		response.Activity = &activity
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// Profile Photo
////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

const maxPhotoSize = 5 * 1024 * 1024 // 5MB max upload
const thumbnailSize = 128            // Max width/height for thumbnail

// UploadProfilePhoto handles profile photo upload, resizes to thumbnail, and stores as base64
func (adminServer *AdminServer) UploadProfilePhoto(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromContext(r.Context())
	userID := UserIDFromContext(r.Context())

	// Limit request body size
	r.Body = http.MaxBytesReader(w, r.Body, maxPhotoSize)

	// Parse multipart form
	err := r.ParseMultipartForm(maxPhotoSize)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("uploadProfilePhoto: error parsing form")
		http.Error(w, "file too large or invalid form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("photo")
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("uploadProfilePhoto: error getting file")
		http.Error(w, "no photo file provided", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Check content type
	contentType := header.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") {
		http.Error(w, "file must be an image", http.StatusBadRequest)
		return
	}

	// Decode image
	var img image.Image
	var decodeErr error

	if strings.Contains(contentType, "png") {
		img, decodeErr = png.Decode(file)
	} else if strings.Contains(contentType, "jpeg") || strings.Contains(contentType, "jpg") {
		img, decodeErr = jpeg.Decode(file)
	} else {
		// Try generic decode
		file.Seek(0, 0)
		img, _, decodeErr = image.Decode(file)
	}

	if decodeErr != nil {
		adminServer.Logger.ErrorE().Err(decodeErr).Msg("uploadProfilePhoto: error decoding image")
		http.Error(w, "could not decode image", http.StatusBadRequest)
		return
	}

	// Resize to thumbnail
	thumbnail := resizeImage(img, thumbnailSize, thumbnailSize)

	// Encode as JPEG to base64
	var buf bytes.Buffer
	err = jpeg.Encode(&buf, thumbnail, &jpeg.Options{Quality: 85})
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("uploadProfilePhoto: error encoding thumbnail")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	base64Photo := base64.StdEncoding.EncodeToString(buf.Bytes())

	// Get user by public key (userID from context is the NATS public key, not the store ID)
	u, err := adminServer.st.GetUserByPublicKey(accountID, userID)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("uploadProfilePhoto: error getting user")
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	u.ProfilePhoto = "data:image/jpeg;base64," + base64Photo
	err = adminServer.st.PutUser(accountID, u)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("uploadProfilePhoto: error saving user")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	adminServer.Logger.InfoE().Str("userID", userID).Msg("uploadProfilePhoto: updated photo")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"profile_photo": u.ProfilePhoto,
	})
}

// DeleteProfilePhoto removes the user's profile photo
func (adminServer *AdminServer) DeleteProfilePhoto(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromContext(r.Context())
	userID := UserIDFromContext(r.Context())

	// Get user by public key (userID from context is the NATS public key, not the store ID)
	u, err := adminServer.st.GetUserByPublicKey(accountID, userID)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("deleteProfilePhoto: error getting user")
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	u.ProfilePhoto = ""
	err = adminServer.st.PutUser(accountID, u)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("deleteProfilePhoto: error saving user")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	adminServer.Logger.InfoE().Str("userID", userID).Msg("deleteProfilePhoto: removed photo")
	w.WriteHeader(http.StatusNoContent)
}

// ChangeUserPassword allows an authenticated user to change their own password
func (adminServer *AdminServer) ChangeUserPassword(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromContext(r.Context())
	userID := UserIDFromContext(r.Context())

	body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("changeUserPassword: error reading body")
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.CurrentPassword == "" || req.NewPassword == "" {
		http.Error(w, `{"error":"current_password and new_password are required"}`, http.StatusBadRequest)
		return
	}

	if len(req.NewPassword) < 8 {
		http.Error(w, `{"error":"new password must be at least 8 characters"}`, http.StatusBadRequest)
		return
	}

	// Look up user by public key (userID from context is the NATS public key)
	u, err := adminServer.st.GetUserByPublicKey(accountID, userID)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("changeUserPassword: error getting user")
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	// Validate current password
	valid, err := adminServer.st.ValidateUserPassword(accountID, u.ID, req.CurrentPassword)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("changeUserPassword: error validating password")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}
	if !valid {
		http.Error(w, `{"error":"current password is incorrect"}`, http.StatusUnauthorized)
		return
	}

	// Hash new password and store
	up := &user.UserPassword{
		ID:           u.ID,
		HashCost:     12,
		PasswordHash: user.PasswordHashAndSalt(req.NewPassword, 12),
	}
	if err := adminServer.st.PutUserPassword(accountID, up); err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("changeUserPassword: error saving password")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	adminServer.Logger.InfoE().Str("userID", userID).Msg("changeUserPassword: password changed")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// UpdateThemePreference sets the user's UI theme preference (system, dark, or light).
func (adminServer *AdminServer) UpdateThemePreference(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromContext(r.Context())
	userID := UserIDFromContext(r.Context())

	body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("updateThemePreference: error reading body")
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var req struct {
		ThemePreference string `json:"theme_preference"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	switch req.ThemePreference {
	case "system", "dark", "light":
		// valid
	default:
		http.Error(w, `{"error":"theme_preference must be system, dark, or light"}`, http.StatusBadRequest)
		return
	}

	u, err := adminServer.st.GetUserByPublicKey(accountID, userID)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("updateThemePreference: error getting user")
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	u.ThemePreference = req.ThemePreference
	if err := adminServer.st.PutUser(accountID, u); err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("updateThemePreference: error saving user")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	adminServer.Logger.InfoE().Str("theme", req.ThemePreference).Str("userID", userID).Msg("updateThemePreference: theme set")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"theme_preference": u.ThemePreference,
	})
}

// GetPreferences returns all preferences for the authenticated user.
func (adminServer *AdminServer) GetPreferences(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromContext(r.Context())
	userID := UserIDFromContext(r.Context())

	u, err := adminServer.st.GetUserByPublicKey(accountID, userID)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("getPreferences: error getting user")
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	prefs := u.Preferences
	if prefs == nil {
		prefs = map[string]string{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(prefs)
}

// SetPreference sets a single preference key for the authenticated user.
func (adminServer *AdminServer) SetPreference(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromContext(r.Context())
	userID := UserIDFromContext(r.Context())

	key := mux.Vars(r)["key"]
	if len(key) > 64 {
		http.Error(w, "key must be 64 characters or less", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 2048))
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("setPreference: error reading body")
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var req struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.Value) > 1024 {
		http.Error(w, "value must be 1024 characters or less", http.StatusBadRequest)
		return
	}

	u, err := adminServer.st.GetUserByPublicKey(accountID, userID)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("setPreference: error getting user")
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	if u.Preferences == nil {
		u.Preferences = map[string]string{}
	}
	u.Preferences[key] = req.Value

	if err := adminServer.st.PutUser(accountID, u); err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("setPreference: error saving user")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(u.Preferences)
}

// DeletePreference removes a single preference key for the authenticated user.
func (adminServer *AdminServer) DeletePreference(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromContext(r.Context())
	userID := UserIDFromContext(r.Context())

	key := mux.Vars(r)["key"]

	u, err := adminServer.st.GetUserByPublicKey(accountID, userID)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("deletePreference: error getting user")
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	if u.Preferences != nil {
		delete(u.Preferences, key)
	}

	if err := adminServer.st.PutUser(accountID, u); err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("deletePreference: error saving user")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	prefs := u.Preferences
	if prefs == nil {
		prefs = map[string]string{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(prefs)
}

// GetActivities returns all activities for the authenticated user's account.
// Account admin or super admin required.
func (adminServer *AdminServer) GetActivities(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromContext(r.Context())

	// Super admins can query activities for any account
	if accountID == adminServer.AdminAccountID() {
		if queryAccountID := r.URL.Query().Get("account_id"); queryAccountID != "" {
			accountID = queryAccountID
		}
	}

	activities, err := adminServer.st.GetActivities(accountID)
	if err != nil {
		adminServer.Logger.ErrorE().Str("accountID", accountID).Err(err).Msg("getActivities: error getting activities")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	// Most recent activity first, so a page of the list is the recent
	// sign-ins. See writeList for paging.
	writeList(w, r, *activities, func(a, b user.Activity) bool {
		if !a.LastActivity.Equal(b.LastActivity) {
			return a.LastActivity.After(b.LastActivity)
		}
		return a.ID < b.ID
	})
}

// UnlockUser resets a locked user account. Requires account admin or super admin.
func (adminServer *AdminServer) UnlockUser(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	accountID := vars["accountID"]
	userID := vars["userID"]

	// Only super admins may unlock users in another account; a tenant admin is
	// restricted to their own account so they cannot reset lockouts (and aid
	// brute force) against other tenants.
	if !adminServer.isSuperAdmin(r) && accountID != AccountIDFromContext(r.Context()) {
		http.Error(w, forbidden, http.StatusForbidden)
		return
	}

	activity, err := adminServer.st.GetActivity(accountID, userID)
	if err != nil {
		adminServer.Logger.ErrorE().Str("userID", userID).Err(err).Msg("unlockUser: error getting activity")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	activity.Locked = false
	activity.LockedAt = time.Time{}
	activity.FailedLoginCount = 0
	err = adminServer.st.PutActivity(accountID, activity)
	if err != nil {
		adminServer.Logger.ErrorE().Str("userID", userID).Err(err).Msg("unlockUser: error saving activity")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	adminServer.Logger.InfoE().Str("userID", userID).Msg("unlockUser: account unlocked")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(activity)
}

// resizeImage resizes an image to fit within maxWidth x maxHeight while preserving aspect ratio
func resizeImage(img image.Image, maxWidth, maxHeight int) image.Image {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Calculate new dimensions preserving aspect ratio
	ratio := float64(width) / float64(height)
	newWidth := maxWidth
	newHeight := maxHeight

	if ratio > 1 {
		// Landscape: width is larger
		newHeight = int(float64(maxWidth) / ratio)
	} else {
		// Portrait or square: height is larger or equal
		newWidth = int(float64(maxHeight) * ratio)
	}

	// Create thumbnail
	thumbnail := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
	draw.CatmullRom.Scale(thumbnail, thumbnail.Bounds(), img, bounds, draw.Over, nil)

	return thumbnail
}

////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// Account Stats
////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

// AccountStats contains user and activity counts for an account
type AccountStats struct {
	AccountID   string `json:"account_id"`
	UserCount   int    `json:"user_count"`
	ActiveCount int    `json:"active_count"`
}

// GetAccountStats returns user counts and active user counts for all accounts
func (adminServer *AdminServer) GetAccountStats(w http.ResponseWriter, r *http.Request) {
	accounts, err := adminServer.st.GetAccounts()
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("getAccountStats: error getting accounts")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	stats := make(map[string]AccountStats)

	for _, account := range *accounts {
		users, err := adminServer.st.GetUsers(account.ID)
		userCount := 0
		if err == nil && users != nil {
			userCount = len(*users)
		}

		activities, err := adminServer.st.GetActivities(account.ID)
		activeCount := 0
		if err == nil && activities != nil {
			for _, activity := range *activities {
				if activity.LoggedIn {
					activeCount++
				}
			}
		}

		stats[account.ID] = AccountStats{
			AccountID:   account.ID,
			UserCount:   userCount,
			ActiveCount: activeCount,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// Service
////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

// GetServices lists the account's service identities, by name. See
// writeList for paging.
func (adminServer *AdminServer) GetServices(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromContext(r.Context())
	services, err := adminServer.st.GetServices(accountID)
	if err != nil {
		adminServer.Logger.ErrorE().Str("accountID", accountID).Err(err).Msg("getServices: error getting services")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}
	writeList(w, r, *services, func(a, b user.Service) bool { return nameOrder(a.Name, a.ID, b.Name, b.ID) })
}

func (adminServer *AdminServer) NewService(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize))
	if err != nil {
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		adminServer.Logger.ErrorE().Err(err).Msg("new user: error reading body")
		return
	} else {
		userConfig := user.UserConfig{}
		err := json.Unmarshal(body, &userConfig)
		if err != nil {
			http.Error(w, server.InternalServerError, http.StatusInternalServerError)
			adminServer.Logger.ErrorE().Err(err).Msg("new user: error unmarshaling userConfig from body")
		} else {
			if err := validateRoles(userConfig.Roles); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if userConfig.Permissions != nil && userConfig.Permissions.IsEmpty() {
				userConfig.Permissions = nil
			}
			u, err := adminServer.st.NewService(&userConfig)
			if err != nil {
				http.Error(w, server.InternalServerError, http.StatusInternalServerError)
				adminServer.Logger.ErrorE().Err(err).Msg("new user: error creating new service")
				return
			}
			adminServer.Logger.InfoE().Str("serviceID", u.ID).Str("name", u.Name).Msg("new user: created new service")
			ip, ipv4 := getClientIPs(r)
			activity := user.Activity{}
			activity.ID = u.ID
			activity.Active = true
			activity.LastIP = ip
			activity.LastIPv4 = ipv4
			activity.LoginCount = 0
			err = adminServer.st.UpdateActivity(userConfig.AccountID, activity)
			if err != nil {
				adminServer.Logger.InfoE().Err(err).Msg("new user: error updating service activity")
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(u)
		}
	}

}

func (adminServer *AdminServer) Noop(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromContext(r.Context())
	userID := UserIDFromContext(r.Context())
	// After Bearer the context user ID is the token subject (a NATS public
	// key), after Basic it is the store ID; look the identity up accordingly.
	var uu *user.User
	var err error
	if ValidTokenFromContext(r.Context()) {
		uu, err = adminServer.st.GetUserByPublicKey(accountID, userID)
		if err == nil && uu.PublicKey == "" {
			err = errors.New("no identity has the token's public key")
		}
	} else {
		uu, err = adminServer.st.GetUserByID(accountID, userID)
	}
	if err != nil {
		adminServer.Logger.ErrorE().Str("userID", userID).Err(err).Msg("noop: error getting user from database")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}
	ip, ipv4 := getClientIPs(r)
	activity := user.Activity{}
	activity.ID = uu.ID
	activity.Active = true
	activity.LastIP = ip
	activity.LastIPv4 = ipv4
	activity.LoggedIn = true
	err = adminServer.st.UpdateActivity(accountID, activity)
	if err != nil {
		adminServer.Logger.InfoE().Str("userID", userID).Err(err).Msg("noop: error updating activity")
	}

	if ValidBasicAuthFromContext(r.Context()) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"message": "valid basic user auth"})
		return
	} else if ValidTokenFromContext(r.Context()) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"message": "valid token auth"})
		return
	} else {
		adminServer.Logger.WarnE().Str("email", uu.Email).Str("userID", userID).Msg("noop: email/password not valid")
		http.Error(w, server.Unauthorized, http.StatusUnauthorized)
		return
	}
}

// /api/v1/jwt/accounts/{public_nkey}
func (adminServer *AdminServer) GetAccount(w http.ResponseWriter, r *http.Request) {
	pubkey := mux.Vars(r)["pubkey"]
	w.Header().Set("Content-Type", "application/jwt")
	account, err := adminServer.st.GetAccountByID(pubkey)
	if err != nil {
		adminServer.Logger.ErrorE().Any("pubkey", pubkey).Err(err).Msg("jwt: error getting by pubkey")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(account.Token))
}

func (adminServer *AdminServer) PutAccount(w http.ResponseWriter, r *http.Request) {
}

// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// Service Endpoints
// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// GET /api/v1/serviceendpoint
// GetServiceEndpoints answers the endpoint map keyed by kind, or, when
// the request pages, a ListPage of endpoints ordered by kind.
func (adminServer *AdminServer) GetServiceEndpoints(w http.ResponseWriter, r *http.Request) {
	serviceEndpoints, err := adminServer.st.GetServiceEndpoints()
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("error getting serviceEndpoints")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}
	if pagingRequested(r) {
		endpoints := make([]ServiceEndpoint, 0, len(serviceEndpoints))
		for _, endpoint := range serviceEndpoints {
			endpoints = append(endpoints, endpoint)
		}
		writeList(w, r, endpoints, func(a, b ServiceEndpoint) bool { return nameOrder(a.Kind, a.URL, b.Kind, b.URL) })
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(serviceEndpoints)
}

// GET /api/v1/servicendpoints/{kind}
func (adminServer *AdminServer) GetServiceEndpointsByKind(w http.ResponseWriter, r *http.Request) {
	kind := mux.Vars(r)["kind"]
	var wg sync.WaitGroup
	w.Header().Set("Content-Type", "application/json")
	adminServer.st.GetServiceEndpointsByKindStream(w, &wg, kind)
	wg.Wait()

}

// POST /api/v1/serviceendpoint
func (adminServer *AdminServer) NewServiceEndpoint(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize))
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("new serviceEndpoint: error reading body")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	} else {
		var serviceEndpoint ServiceEndpoint
		err := json.Unmarshal(body, &serviceEndpoint)
		if err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("new serviceEndpiont: error unmarshaling serviceEndpoint from body")
			http.Error(w, server.InternalServerError, http.StatusInternalServerError)
			return
		}
		err = adminServer.st.PutServiceEndpoint(serviceEndpoint)
		if err != nil {
			http.Error(w, server.InternalServerError, http.StatusInternalServerError)
			adminServer.Logger.ErrorE().Err(err).Msg("new account: error creating new account")
		}
		adminServer.Logger.InfoE().Str("kind", serviceEndpoint.Kind).Str("url", serviceEndpoint.URL).Msg("new serviceEndpoint created")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(serviceEndpoint)

	}

}

// DELETE /api/v1/serviceendpoint/{kind}/{serviceEndpoint}
func (adminServer *AdminServer) DeleteServiceEndpoint(w http.ResponseWriter, r *http.Request) {
	kind := mux.Vars(r)["kind"]
	endpointURL := mux.Vars(r)["endpointURL"]
	var serviceEndpoint ServiceEndpoint
	serviceEndpoint.Kind = kind
	serviceEndpoint.URL = endpointURL
	err := adminServer.st.DeleteServiceEndpoint(serviceEndpoint)
	if err != nil {
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		adminServer.Logger.ErrorE().Str("url", serviceEndpoint.URL).Err(err).Msg("error deleting serviceEndpoint")
		return
	}
	adminServer.Logger.InfoE().Str("kind", serviceEndpoint.Kind).Str("url", serviceEndpoint.URL).Msg("serviceEndpoint deleted")
	w.WriteHeader(http.StatusNoContent)

}

// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// Service Credentials
// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////
func (adminServer *AdminServer) GetServiceCredentials(w http.ResponseWriter, r *http.Request) {
	accountName := mux.Vars(r)["accountName"]
	serviceName := mux.Vars(r)["serviceName"]
	serviceCredentials, err := adminServer.ServiceCredentials(accountName, serviceName)
	if err != nil {
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		adminServer.Logger.ErrorE().Err(err).Msg("getServiceCredentials: error")
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(serviceCredentials)
}

////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// Agent
////////////////////////////////////////////////////////////////////////////////////////////////////////////////////

// GetAgents lists the account's agents, by hostname. See writeList for
// paging.
func (adminServer *AdminServer) GetAgents(w http.ResponseWriter, r *http.Request) {
	accountID := AccountIDFromContext(r.Context())
	agents, err := adminServer.st.GetAgents(accountID)
	if err != nil {
		adminServer.Logger.ErrorE().Str("accountID", accountID).Err(err).Msg("getAgents: error getting agents")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}
	writeList(w, r, *agents, func(a, b user.Agent) bool { return nameOrder(a.Hostname, a.ID, b.Hostname, b.ID) })
}

func (adminServer *AdminServer) NewAgent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize))
	if err != nil {
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		adminServer.Logger.ErrorE().Err(err).Msg("new user: error reading body")
		return
	} else {
		userConfig := user.UserConfig{}
		err := json.Unmarshal(body, &userConfig)
		if err != nil {
			http.Error(w, server.InternalServerError, http.StatusInternalServerError)
			adminServer.Logger.ErrorE().Err(err).Msg("new user: error unmarshaling userConfig from body")
		} else {
			if err := validateRoles(userConfig.Roles); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if userConfig.Permissions != nil && userConfig.Permissions.IsEmpty() {
				userConfig.Permissions = nil
			}
			u, err := adminServer.st.NewAgent(&userConfig)
			if err != nil {
				http.Error(w, server.InternalServerError, http.StatusInternalServerError)
				adminServer.Logger.ErrorE().Err(err).Msg("new user: error creating new agent")
				return
			}
			adminServer.Logger.InfoE().Str("agentID", u.ID).Str("hostID", u.HostID).Msg("new user: created new agent")
			ip, ipv4 := getClientIPs(r)
			activity := user.Activity{}
			activity.ID = u.ID
			activity.Active = true
			activity.LastIP = ip
			activity.LastIPv4 = ipv4
			activity.LoginCount = 0
			err = adminServer.st.UpdateActivity(userConfig.AccountID, activity)
			if err != nil {
				adminServer.Logger.InfoE().Err(err).Msg("new user: error updating agent activity")
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(u)
		}
	}

}

// DeleteService removes a service identity from the caller's account.
func (adminServer *AdminServer) DeleteService(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]
	accountID := AccountIDFromContext(r.Context())
	if err := adminServer.st.DeleteService(accountID, id); err != nil {
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		adminServer.Logger.ErrorE().Err(err).Msg("error deleting service")
		return
	}
	adminServer.Logger.InfoE().Str("serviceID", id).Msg("service deleted")
	w.WriteHeader(http.StatusNoContent)
}

func (adminServer *AdminServer) DeleteAgent(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]
	accountID := AccountIDFromContext(r.Context())
	err := adminServer.st.DeleteAgent(accountID, id)
	if err != nil {
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		adminServer.Logger.ErrorE().Err(err).Msg("error deleting agent")
	}
	adminServer.Logger.InfoE().Str("agentID", id).Msg("agent deleted")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNoContent)
}
func (adminServer *AdminServer) NewAgentConfiguration(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize))
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("new serviceEndpoint: error reading body")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	} else {
		var aco user.AgentConfigurationOptions
		if err := json.Unmarshal(body, &aco); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			adminServer.Logger.ErrorE().Err(err).Msg("NewAgentConfiguration: error unmarshaling body")
			return
		}
		agentConfiguration, err := adminServer.AgentConfiguration(&aco)
		if err != nil {
			http.Error(w, server.InternalServerError, http.StatusInternalServerError)
			adminServer.Logger.ErrorE().Err(err).Msg("NewAgentConfiguration: error")
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(agentConfiguration)
	}
	return
}

// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// Utilities
// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// Get username and password from HTTP request
func getEmailPassword(r *http.Request) (string, string, error) {
	s := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
	if len(s) != 2 {
		return "", "", fmt.Errorf("error splitting http authorization header")
	}
	b, err := base64.StdEncoding.DecodeString(s[1])
	if err != nil {
		return "", "", err
	}
	pair := strings.SplitN(string(b), ":", 2)
	if len(pair) != 2 {
		return "", "", fmt.Errorf("error splitting http authorization header")
	}
	return pair[0], pair[1], nil
}

// getClientIP returns the client IP for a request. A multi-hop
// X-Forwarded-For value ("client, proxy1, proxy2") is parsed by taking the
// left-most (originating client) entry that parses, rather than failing to
// parse the whole string. Falls back to the connection RemoteAddr.
func getClientIP(r *http.Request) net.IP {
	if xff := r.Header.Get("x-forwarded-for"); xff != "" {
		for _, part := range strings.Split(xff, ",") {
			if ip := net.ParseIP(strings.TrimSpace(part)); ip != nil {
				return ip
			}
		}
	}
	if r.RemoteAddr != "" {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			if parsed := net.ParseIP(r.RemoteAddr); parsed != nil {
				return parsed
			}
			return net.ParseIP("0.0.0.0")
		}
		return net.ParseIP(ip)
	}
	return net.ParseIP("0.0.0.0")
}

// getClientIPs returns both the raw IP and the IPv4 string for a request.
// If the client IP is IPv4 or IPv4-mapped IPv6, ipv4 will be the IPv4 string.
// For pure IPv6 addresses, ipv4 will be empty.
func getClientIPs(r *http.Request) (net.IP, string) {
	ip := getClientIP(r)
	if v4 := ip.To4(); v4 != nil {
		return ip, v4.String()
	}
	return ip, ""
}

// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// Proxy
// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////

// GET /api/v1/proxy
func (adminServer *AdminServer) GetProxyConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	config, err := adminServer.st.GetProxyConfig()
	if err != nil {
		// Return empty config if not found
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(ProxyConfig{})
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(config)
}

// POST/PUT /api/v1/proxy
func (adminServer *AdminServer) UpdateProxyConfig(w http.ResponseWriter, r *http.Request) {
	const maxBodySize = 1024 * 1024 // 1MB limit
	limitedReader := io.LimitReader(r.Body, maxBodySize)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("update proxy config: error reading body")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	var config ProxyConfig
	if err := json.Unmarshal(body, &config); err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("update proxy config: error unmarshaling")
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := adminServer.st.PutProxyConfig(config); err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("update proxy config: error storing")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	adminServer.Logger.InfoE().Bool("enabled", config.Enabled).Str("listenAddr", config.ListenAddr).Int("routes", len(config.Routes)).Msg("proxy config updated")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(config)
}

// GET /api/v1/proxy/status
func (adminServer *AdminServer) GetProxyStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	config, err := adminServer.st.GetProxyConfig()
	if err != nil {
		// Return disabled status if no config
		status := ProxyStatus{
			Running:    false,
			ListenAddr: "",
			RouteCount: 0,
			Backends:   make(map[string]*BackendHealth),
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(status)
		return
	}

	// Build status from config
	status := ProxyStatus{
		Running:    config.Enabled,
		ListenAddr: config.ListenAddr,
		RouteCount: len(config.Routes),
		Backends:   make(map[string]*BackendHealth),
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(status)
}

// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// API Tokens
//
// Every response is an APITokenView, which has no secret field. The stored
// secret is written once and only ever read by backend code through the
// store; no API path returns it at any privilege level.
// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////

// apiTokenMaxBodySize bounds a create or update body.
const apiTokenMaxBodySize = 10 * 1024

// apiTokenCaller returns the account and user from the request context, or
// writes 401 and returns ok=false when the user is missing.
func (adminServer *AdminServer) apiTokenCaller(w http.ResponseWriter, r *http.Request, op string) (accountID, userID string, ok bool) {
	accountID = AccountIDFromContext(r.Context())
	userID = UserIDFromContext(r.Context())
	if userID == "" {
		adminServer.Logger.Error(op + ": user ID not found in context")
		http.Error(w, server.Unauthorized, http.StatusUnauthorized)
		return "", "", false
	}
	return accountID, userID, true
}

// decodeAPITokenBody reads a bounded JSON body into an APIToken.
func (adminServer *AdminServer) decodeAPITokenBody(w http.ResponseWriter, r *http.Request, op string) (*APIToken, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, apiTokenMaxBodySize))
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg(op + ": error reading body")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return nil, false
	}
	var token APIToken
	if err := json.Unmarshal(body, &token); err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg(op + ": error unmarshaling token")
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return nil, false
	}
	return &token, true
}

// writeAPITokenError maps store validation errors to 400 and everything
// else to 500.
func (adminServer *AdminServer) writeAPITokenError(w http.ResponseWriter, err error, op string) {
	switch {
	case errors.Is(err, ErrInvalidAPITokenName):
		http.Error(w, "invalid token name", http.StatusBadRequest)
	case errors.Is(err, ErrInvalidAPITokenType):
		http.Error(w, "invalid token type", http.StatusBadRequest)
	case errors.Is(err, ErrInvalidAPITokenScope):
		http.Error(w, "invalid token scope", http.StatusBadRequest)
	case errors.Is(err, ErrReservedAPITokenUser):
		http.Error(w, forbidden, http.StatusForbidden)
	default:
		adminServer.Logger.ErrorE().Err(err).Msg(op + ": store error")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
	}
}

func writeAPITokenView(w http.ResponseWriter, status int, token *APIToken) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(token.View())
}

// writeAPITokenViews answers the tokens as views, by name then scope. See
// writeList for paging.
func writeAPITokenViews(w http.ResponseWriter, r *http.Request, tokens []APIToken) {
	writeList(w, r, APITokenViews(tokens), func(a, b APITokenView) bool {
		if a.Name != b.Name {
			return nameOrder(a.Name, "", b.Name, "")
		}
		return a.Scope < b.Scope
	})
}

// applyAPITokenUpdate copies the updatable fields of update onto existing.
// An absent or masked secret leaves the stored secret alone.
func applyAPITokenUpdate(existing, update *APIToken) {
	if update.ID != "" {
		existing.ID = update.ID
	}
	if update.Token != "" && update.Token != APITokenMaskedSecret {
		existing.Token = update.Token
	}
	if update.Type != "" {
		existing.Type = update.Type
	}
	if !update.Metadata.IsZero() {
		existing.Metadata = update.Metadata
	}
	existing.UpdatedAt = time.Now()
}

// createAPIToken validates and stores a new token in the given scope and
// writes the 201 response. Name conflicts within the scope are rejected.
func (adminServer *AdminServer) createAPIToken(w http.ResponseWriter, r *http.Request, scope APITokenScope, op string) {
	accountID, userID, ok := adminServer.apiTokenCaller(w, r, op)
	if !ok {
		return
	}
	req, ok := adminServer.decodeAPITokenBody(w, r, op)
	if !ok {
		return
	}
	if req.Type == "" {
		http.Error(w, "type is required", http.StatusBadRequest)
		return
	}
	req.Scope = scope
	token := NewAPIToken(*req, userID)
	if err := token.Validate(); err != nil {
		adminServer.writeAPITokenError(w, err, op)
		return
	}
	var existing *APIToken
	var err error
	if scope == APITokenScopeAccount {
		existing, err = adminServer.st.GetAccountAPIToken(accountID, token.Name)
	} else {
		existing, err = adminServer.st.GetAPIToken(accountID, userID, token.Name)
	}
	if err == nil && existing != nil {
		http.Error(w, "token already exists", http.StatusConflict)
		return
	}
	if err := adminServer.st.PutAPIToken(accountID, token); err != nil {
		adminServer.writeAPITokenError(w, err, op)
		return
	}
	adminServer.auditAPIToken(r, op, token)
	writeAPITokenView(w, http.StatusCreated, token)
}

// updateAPIToken applies a partial update to existing and writes 200.
func (adminServer *AdminServer) updateAPIToken(w http.ResponseWriter, r *http.Request, accountID string, existing *APIToken, op string) {
	update, ok := adminServer.decodeAPITokenBody(w, r, op)
	if !ok {
		return
	}
	applyAPITokenUpdate(existing, update)
	if err := adminServer.st.PutAPIToken(accountID, existing); err != nil {
		adminServer.writeAPITokenError(w, err, op)
		return
	}
	adminServer.auditAPIToken(r, op, existing)
	writeAPITokenView(w, http.StatusOK, existing)
}

// auditAPIToken logs a create, update, or delete with the acting user. The
// user's email is looked up so the line is attributable without decoding
// the public key; account-scoped tokens are shared, so this trail is what
// ties a change to a person.
func (adminServer *AdminServer) auditAPIToken(r *http.Request, op string, token *APIToken) {
	accountID := AccountIDFromContext(r.Context())
	userID := UserIDFromContext(r.Context())
	email := ""
	if u, err := adminServer.st.GetUserByPublicKey(accountID, userID); err == nil {
		email = u.Email
	}
	adminServer.Logger.InfoE().
		Str("name", token.Name).
		Str("type", string(token.Type)).
		Str("scope", string(token.Scope)).
		Str("resourceType", token.Metadata.ResourceType).
		Str("resourceID", token.Metadata.ResourceID).
		Str("accountID", accountID).
		Str("userID", userID).
		Str("email", email).
		Msg(op)
}

func apiTokenName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := mux.Vars(r)["name"]
	if name == "" {
		http.Error(w, "token name is required", http.StatusBadRequest)
		return "", false
	}
	return name, true
}

// GET /api/v1/apitokens
// Lists the caller's user-scoped tokens plus the account-scoped tokens they
// can use. Each entry carries its scope.
func (adminServer *AdminServer) GetAPITokens(w http.ResponseWriter, r *http.Request) {
	accountID, userID, ok := adminServer.apiTokenCaller(w, r, "getapitokens")
	if !ok {
		return
	}
	tokens, err := adminServer.st.GetAPITokensVisibleTo(accountID, userID)
	if err != nil {
		adminServer.Logger.ErrorE().Str("userID", userID).Err(err).Msg("getapitokens: error getting tokens")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}
	writeAPITokenViews(w, r, tokens)
}

// POST /api/v1/apitokens
func (adminServer *AdminServer) CreateAPIToken(w http.ResponseWriter, r *http.Request) {
	adminServer.createAPIToken(w, r, APITokenScopeUser, "createapitoken")
}

// GET /api/v1/apitokens/{name}
func (adminServer *AdminServer) GetAPIToken(w http.ResponseWriter, r *http.Request) {
	accountID, userID, ok := adminServer.apiTokenCaller(w, r, "getapitoken")
	if !ok {
		return
	}
	name, ok := apiTokenName(w, r)
	if !ok {
		return
	}
	token, err := adminServer.st.GetAPIToken(accountID, userID, name)
	if err != nil {
		http.Error(w, "token not found", http.StatusNotFound)
		return
	}
	writeAPITokenView(w, http.StatusOK, token)
}

// PUT /api/v1/apitokens/{name}
func (adminServer *AdminServer) UpdateAPIToken(w http.ResponseWriter, r *http.Request) {
	accountID, userID, ok := adminServer.apiTokenCaller(w, r, "updateapitoken")
	if !ok {
		return
	}
	name, ok := apiTokenName(w, r)
	if !ok {
		return
	}
	existing, err := adminServer.st.GetAPIToken(accountID, userID, name)
	if err != nil {
		http.Error(w, "token not found", http.StatusNotFound)
		return
	}
	adminServer.updateAPIToken(w, r, accountID, existing, "updateapitoken")
}

// DELETE /api/v1/apitokens/{name}
func (adminServer *AdminServer) DeleteAPIToken(w http.ResponseWriter, r *http.Request) {
	accountID, userID, ok := adminServer.apiTokenCaller(w, r, "deleteapitoken")
	if !ok {
		return
	}
	name, ok := apiTokenName(w, r)
	if !ok {
		return
	}
	existing, err := adminServer.st.GetAPIToken(accountID, userID, name)
	if err != nil {
		http.Error(w, "token not found", http.StatusNotFound)
		return
	}
	if err := adminServer.st.DeleteAPIToken(accountID, userID, name); err != nil {
		adminServer.writeAPITokenError(w, err, "deleteapitoken")
		return
	}
	adminServer.auditAPIToken(r, "deleteapitoken", existing)
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/v1/accounts/me/apitokens
func (adminServer *AdminServer) GetAccountAPITokens(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := adminServer.apiTokenCaller(w, r, "getaccountapitokens")
	if !ok {
		return
	}
	tokens, err := adminServer.st.GetAccountAPITokens(accountID)
	if err != nil {
		adminServer.Logger.ErrorE().Str("accountID", accountID).Err(err).Msg("getaccountapitokens: error getting tokens")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}
	writeAPITokenViews(w, r, tokens)
}

// POST /api/v1/accounts/me/apitokens (administrator)
func (adminServer *AdminServer) CreateAccountAPIToken(w http.ResponseWriter, r *http.Request) {
	adminServer.createAPIToken(w, r, APITokenScopeAccount, "createaccountapitoken")
}

// GET /api/v1/accounts/me/apitokens/{name}
func (adminServer *AdminServer) GetAccountAPIToken(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := adminServer.apiTokenCaller(w, r, "getaccountapitoken")
	if !ok {
		return
	}
	name, ok := apiTokenName(w, r)
	if !ok {
		return
	}
	token, err := adminServer.st.GetAccountAPIToken(accountID, name)
	if err != nil {
		http.Error(w, "token not found", http.StatusNotFound)
		return
	}
	writeAPITokenView(w, http.StatusOK, token)
}

// PUT /api/v1/accounts/me/apitokens/{name} (administrator)
func (adminServer *AdminServer) UpdateAccountAPIToken(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := adminServer.apiTokenCaller(w, r, "updateaccountapitoken")
	if !ok {
		return
	}
	name, ok := apiTokenName(w, r)
	if !ok {
		return
	}
	existing, err := adminServer.st.GetAccountAPIToken(accountID, name)
	if err != nil {
		http.Error(w, "token not found", http.StatusNotFound)
		return
	}
	adminServer.updateAPIToken(w, r, accountID, existing, "updateaccountapitoken")
}

// DELETE /api/v1/accounts/me/apitokens/{name} (administrator)
func (adminServer *AdminServer) DeleteAccountAPIToken(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := adminServer.apiTokenCaller(w, r, "deleteaccountapitoken")
	if !ok {
		return
	}
	name, ok := apiTokenName(w, r)
	if !ok {
		return
	}
	existing, err := adminServer.st.GetAccountAPIToken(accountID, name)
	if err != nil {
		http.Error(w, "token not found", http.StatusNotFound)
		return
	}
	if err := adminServer.st.DeleteAccountAPIToken(accountID, name); err != nil {
		adminServer.writeAPITokenError(w, err, "deleteaccountapitoken")
		return
	}
	adminServer.auditAPIToken(r, "deleteaccountapitoken", existing)
	w.WriteHeader(http.StatusNoContent)
}

// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// OAuth
// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////

// GET /api/v1/oauth/config
// Returns public OAuth configuration (client_id, redirect_uri - no secrets)
func (adminServer *AdminServer) GetOAuthConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if adminServer.googleOAuth == nil {
		// OAuth not enabled, return empty config
		json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled": false,
		})
		return
	}

	json.NewEncoder(w).Encode(adminServer.googleOAuth.GetPublicConfig())
}

// GET /api/v1/oauth/google/authorize
//
// DEPRECATED: Google is now modeled as a preconfigured OIDC connection. This
// shim is retained because the current Vue admin UI still calls it; it delegates
// to the unified OIDC flow. Prefer /api/v1/oidc/{slug}/authorize.
func (adminServer *AdminServer) OAuthGoogleAuthorize(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	conn := adminServer.googleConnection()
	if conn == nil {
		http.Error(w, "OAuth is not enabled", http.StatusServiceUnavailable)
		return
	}

	inviteToken := r.URL.Query().Get("invite_token")

	url, state, err := adminServer.oidc.AuthorizationURL(r.Context(), conn, inviteToken, "")
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("oauth(google) authorize")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{
		"authorization_url": url,
		"state":             state,
	})
}

// POST /api/v1/oauth/google/callback
//
// DEPRECATED: thin shim over the unified OIDC login. Retained for the current
// Vue admin UI, which POSTs {code, state} and expects a SessionResponse JSON.
// Prefer /api/v1/oidc/{slug}/callback.
func (adminServer *AdminServer) OAuthGoogleCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	conn := adminServer.googleConnection()
	if conn == nil {
		http.Error(w, "OAuth is not enabled", http.StatusServiceUnavailable)
		return
	}

	const maxBodySize = 10 * 1024
	limitedReader := io.LimitReader(r.Body, maxBodySize)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("oauth(google) callback: error reading body")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	var callbackRequest struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(body, &callbackRequest); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if callbackRequest.Code == "" || callbackRequest.State == "" {
		http.Error(w, "code and state are required", http.StatusBadRequest)
		return
	}

	response, status, err := adminServer.runOIDCLogin(r, conn, callbackRequest.Code, callbackRequest.State)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("oauth(google) callback")
		http.Error(w, err.Error(), status)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// POST /api/v1/oauth/accounts
// Create a new account for OAuth user (requires Bearer auth from temp token)
func (adminServer *AdminServer) OAuthCreateAccount(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if adminServer.oauthConfig == nil || !adminServer.oauthConfig.AllowSelfServiceAccounts {
		http.Error(w, "self-service account creation is not enabled", http.StatusForbidden)
		return
	}

	const maxBodySize = 10 * 1024
	limitedReader := io.LimitReader(r.Body, maxBodySize)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("oauth create account: error reading body")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	var request struct {
		AccountName  string `json:"account_name"`
		Description  string `json:"description"`
		OAuthSubject string `json:"oauth_subject"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if request.AccountName == "" || request.OAuthSubject == "" {
		http.Error(w, "account_name and oauth_subject are required", http.StatusBadRequest)
		return
	}

	// Get the temp OAuth identity
	identity, tempAccountID, err := adminServer.st.GetOAuthIdentityBySubject(string(user.OAuthProviderGoogle), request.OAuthSubject)
	if err != nil || identity == nil {
		http.Error(w, "OAuth identity not found", http.StatusBadRequest)
		return
	}

	if identity.UserID != "" {
		http.Error(w, "account already created for this OAuth identity", http.StatusConflict)
		return
	}

	// Create the account
	account, err := adminServer.st.NewAccount(user.AccountConfig{
		Name:        request.AccountName,
		Description: request.Description,
		Owner:       identity.Email,
		OperatorID:  adminServer.OperatorID(),
	})
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("oauth create account: error creating account")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	adminServer.accountCreated(account)

	// Create the user in this account
	u, err := adminServer.st.NewOAuthUser(&user.OAuthUserConfig{
		Email:         identity.Email,
		FirstName:     identity.Name,
		LastName:      "",
		Roles:         []string{user.RoleAdministrator},
		AccountID:     account.ID,
		OAuthProvider: string(identity.Provider),
		OAuthSubject:  identity.Subject,
		Picture:       identity.Picture,
	})
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("oauth create account: error creating user")
		// Rollback account creation
		adminServer.st.DeleteAccount(account.ID)
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	// Delete the temp identity and update with the new account's identity
	adminServer.st.DeleteOAuthIdentity(tempAccountID, string(identity.Provider), identity.Subject)
	identity.UserID = u.ID
	adminServer.st.PutOAuthIdentity(account.ID, identity)

	// Create login response
	response, err := adminServer.createOAuthLoginResponse(u, r, false, "")
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("oauth create account: error creating response")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	adminServer.Logger.InfoE().Str("accountID", account.ID).Str("userID", u.ID).Msg("oauth: created account")

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(response)
}

// Helper to create OAuth login response
// SessionResponse is the typed result of a successful authentication. Every
// login path -- local password and federated (OIDC) -- returns this one shape
// so the response contract has a single source of truth.
type SessionResponse struct {
	User             *user.User                 `json:"user"`
	Token            string                     `json:"token"`
	Activity         user.Activity              `json:"activity"`
	ServiceEndpoints map[string]ServiceEndpoint `json:"service_endpoints"`
	IsNewUser        bool                       `json:"is_new_user,omitempty"`
	IsSuperAdmin     bool                       `json:"is_super_admin"`
	InviteToken      string                     `json:"invite_token,omitempty"`
}

// issueSession mints an account-signed user token, records the successful login
// in the user's activity (incrementing the login count -- Activity.Update
// accumulates -- and clearing any failed-login counter), and assembles the
// typed session response. It is the single source of truth for session issuance
// across local and federated login.
func (adminServer *AdminServer) issueSession(u *user.User, r *http.Request, isNew bool, inviteToken string) (*SessionResponse, error) {
	kp, err := adminServer.st.GetAccountKeyPair(u.AccountID)
	if err != nil {
		return nil, err
	}
	token, err := u.CreateToken(adminServer.tokenMinutes, kp)
	if err != nil {
		return nil, err
	}
	serviceEndpoints, err := adminServer.st.GetServiceEndpoints()
	if err != nil {
		return nil, err
	}

	ip, ipv4 := getClientIPs(r)
	if err := adminServer.st.UpdateActivity(u.AccountID, user.Activity{
		ID:         u.ID,
		Active:     true,
		LastIP:     ip,
		LastIPv4:   ipv4,
		LoggedIn:   true,
		LoginCount: 1, // Activity.Update adds this to the stored count.
		LastLogin:  time.Now(),
	}); err != nil {
		adminServer.Logger.InfoE().
			Str("userID", u.ID).
			Err(err).
			Msg("issueSession: error updating activity")
	}

	// Read back the accumulated activity and clear any failed-login counter.
	activity, err := adminServer.st.GetActivity(u.AccountID, u.ID)
	if err == nil && activity.FailedLoginCount > 0 {
		activity.FailedLoginCount = 0
		adminServer.st.PutActivity(u.AccountID, activity)
	}

	return &SessionResponse{
		User:             u,
		Token:            token.Token,
		Activity:         activity,
		ServiceEndpoints: serviceEndpoints,
		IsNewUser:        isNew,
		IsSuperAdmin:     u.AccountID == adminServer.AdminAccountID(),
		InviteToken:      inviteToken,
	}, nil
}

// createOAuthLoginResponse is retained as a thin adapter over issueSession for
// the existing OAuth callback handlers. New code should call issueSession.
func (adminServer *AdminServer) createOAuthLoginResponse(u *user.User, r *http.Request, isNew bool, inviteToken string) (*SessionResponse, error) {
	return adminServer.issueSession(u, r, isNew, inviteToken)
}

// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// OIDC (per-tenant federated login)
// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////

// googleConnection builds the preconfigured OIDC connection representing Google
// from the legacy oauthConfig metadata, or nil when Google OAuth is not
// configured. It exists so the deprecated /oauth/google/* shims can run through
// the same unified OIDC flow as generic per-tenant connections. Google sends no
// group claims, so it has no GroupRoleMappings (roles are not IdP-driven for it).
func (adminServer *AdminServer) googleConnection() *user.OIDCConnection {
	if adminServer.oauthConfig == nil || !adminServer.oauthConfig.Enabled {
		return nil
	}
	if adminServer.oauthConfig.GoogleClientID == "" || adminServer.oauthConfig.GoogleClientSecret == "" || adminServer.oauthConfig.RedirectURI == "" {
		return nil
	}
	conn := &user.OIDCConnection{
		AccountID:         adminServer.adminAccountID,
		Slug:              "google",
		Enabled:           true,
		IssuerURL:         user.GoogleIssuerURL,
		ClientID:          adminServer.oauthConfig.GoogleClientID,
		ClientSecret:      adminServer.oauthConfig.GoogleClientSecret,
		RedirectURI:       adminServer.oauthConfig.RedirectURI,
		Scopes:            []string{"openid", "email", "profile"},
		GroupsClaim:       user.DefaultGroupsClaim,
		GroupRoleMappings: map[string]string{},
		AutoProvision:     adminServer.oauthConfig.AllowSelfServiceAccounts,
		DefaultRoles:      []string{user.RoleUser},
	}
	return conn
}

// runOIDCLogin performs the shared post-callback work: exchange + verify the
// code, resolve/provision the user, re-sync roles, accept an invitation if one
// was bound to the state, and issue the session. It returns the typed
// SessionResponse, an HTTP status to use on error, and the error. This is the
// single funnel behind both the generic OIDC callback and the Google shim.
func (adminServer *AdminServer) runOIDCLogin(r *http.Request, conn *user.OIDCConnection, code, state string) (*SessionResponse, int, error) {
	claims, stateRecord, err := adminServer.oidc.ExchangeAndVerify(r.Context(), conn, code, state)
	if err != nil {
		switch {
		case errors.Is(err, ErrOIDCStateInvalid), errors.Is(err, ErrOIDCStateExpired),
			errors.Is(err, ErrOIDCNonceMismatch), errors.Is(err, ErrOIDCNoIDToken),
			errors.Is(err, ErrOIDCIDTokenVerify):
			return nil, http.StatusBadRequest, err
		default:
			return nil, http.StatusInternalServerError, err
		}
	}

	// Determine the provisioning account and roles. Normally the connection's own
	// account, but an invitation may direct a brand-new user into a different
	// account with invitation-specified roles.
	provisionAccountID := conn.AccountID
	var provisionRoles []string
	var inv *user.Invitation
	if stateRecord.InviteToken != "" {
		invitation, invErr := adminServer.st.GetInvitationByToken(stateRecord.InviteToken)
		if invErr != nil || invitation == nil || !invitation.CanBeAccepted() {
			return nil, http.StatusBadRequest, fmt.Errorf("invalid or expired invitation")
		}
		inv = invitation
		provisionAccountID = invitation.AccountID
		provisionRoles = invitation.Roles
	}

	u, isNew, err := adminServer.syncFederatedUser(conn, claims, provisionAccountID, provisionRoles)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}

	if inv != nil {
		inv.Accept(u.ID)
		if err := adminServer.st.UpdateInvitation(inv); err != nil {
			adminServer.Logger.ErrorE().Err(err).Msg("oidc login: error updating invitation")
		}
	}

	resp, err := adminServer.issueSession(u, r, isNew, stateRecord.InviteToken)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	return resp, http.StatusOK, nil
}

// syncFederatedUser resolves (or JIT-provisions) the user for a verified OIDC
// login and makes the IdP authoritative over roles.
//
// Resolution order: (1) an existing federated identity for this issuer+subject
// (with a legacy-Google fallback + lazy migration to the issuer-based key);
// (2) an existing local user matched by verified email; (3) JIT provisioning
// into provisionAccountID when the connection allows AutoProvision.
//
// Role re-sync policy: when the connection has GroupRoleMappings configured, the
// user's roles are re-synced from the IdP groups on EVERY login (authoritative).
// When it has no group mappings (e.g. the Google shim, which receives no group
// claims), existing users keep their locally managed roles and only brand-new
// users are seeded with the resolved roles -- this avoids silently demoting
// users behind an IdP that does not emit groups.
func (adminServer *AdminServer) syncFederatedUser(conn *user.OIDCConnection, claims *OIDCClaims, provisionAccountID string, provisionRoles []string) (*user.User, bool, error) {
	if provisionAccountID == "" {
		provisionAccountID = conn.AccountID
	}
	providerKey := user.ProviderKeyForIssuer(conn.IssuerURL)
	roles := rolesFromGroups(conn, claims.Groups)
	if len(provisionRoles) == 0 {
		provisionRoles = roles
	}
	idpAuthoritative := len(conn.GroupRoleMappings) > 0

	// (1) Existing federated identity, resolved WITHIN the account this login
	// targets. Scoping the lookup to provisionAccountID (rather than a global
	// issuer:subject index) is what prevents two tenants using the same public
	// issuer from cross-linking on a shared (issuer, subject).
	identity, foundKey, err := adminServer.st.ResolveFederatedIdentityScoped(provisionAccountID, conn.IssuerURL, claims.Subject)
	if err == nil && identity != nil && identity.UserID != "" {
		u, uerr := adminServer.st.GetUserByID(provisionAccountID, identity.UserID)
		if uerr != nil {
			return nil, false, uerr
		}
		if idpAuthoritative {
			u.SetRoles(roles)
		}
		if claims.Picture != "" {
			u.Picture = claims.Picture
		}
		if err := adminServer.st.PutUser(provisionAccountID, u); err != nil {
			return nil, false, err
		}
		adminServer.linkFederatedIdentity(provisionAccountID, providerKey, foundKey, claims, u.ID)
		return u, false, nil
	}

	// (2) Existing local user by email -- link the federated identity to their
	// existing account. This is a takeover surface, so it is tightly gated:
	//   - The email must belong to the SAME account this login provisions into.
	//     GetUserByEmail resolves via the global email index, so without this
	//     check a tenant that controls its own IdP could assert another tenant's
	//     user's email and be logged into that other tenant's account.
	//   - The IdP must have marked the email verified, so an IdP cannot assert an
	//     unverified address to hijack a pre-existing local account.
	if claims.Email != "" {
		existing, existingAccount, eerr := adminServer.st.GetUserByEmail(claims.Email)
		if eerr == nil && existing != nil && existing.ID != "" {
			if existingAccount != provisionAccountID {
				return nil, false, fmt.Errorf("email %s is registered to a different account", claims.Email)
			}
			if !claims.EmailVerified {
				return nil, false, fmt.Errorf("identity provider has not verified email %s", claims.Email)
			}
			if idpAuthoritative {
				existing.SetRoles(roles)
			}
			existing.OAuthProvider = providerKey
			existing.OAuthSubject = claims.Subject
			if claims.Picture != "" {
				existing.Picture = claims.Picture
			}
			if err := adminServer.st.PutUser(existingAccount, existing); err != nil {
				return nil, false, err
			}
			adminServer.linkFederatedIdentity(existingAccount, providerKey, "", claims, existing.ID)
			return existing, false, nil
		}
	}

	// (3) JIT provisioning.
	if !conn.AutoProvision {
		return nil, false, fmt.Errorf("auto-provisioning is disabled for this connection")
	}
	if claims.Email == "" {
		return nil, false, fmt.Errorf("id_token has no email; cannot provision user")
	}
	u, err := adminServer.st.NewOAuthUser(&user.OAuthUserConfig{
		Email:         claims.Email,
		FirstName:     claims.Name,
		Roles:         provisionRoles,
		AccountID:     provisionAccountID,
		OAuthProvider: providerKey,
		OAuthSubject:  claims.Subject,
		Picture:       claims.Picture,
	})
	if err != nil {
		return nil, false, err
	}
	adminServer.linkFederatedIdentity(provisionAccountID, providerKey, "", claims, u.ID)
	return u, true, nil
}

// linkFederatedIdentity stores/refreshes the federated identity under the
// issuer-based provider key. When the identity was previously found under a
// different (legacy) key, that stale record and its index are removed -- lazily
// migrating legacy Google identities to the unified key on next login.
func (adminServer *AdminServer) linkFederatedIdentity(accountID, providerKey, foundUnderKey string, claims *OIDCClaims, userID string) {
	identity := user.NewFederatedIdentity(providerKey, claims.Subject, claims.Email, claims.Name, claims.Picture, userID, claims.EmailVerified)
	if err := adminServer.st.PutOAuthIdentity(accountID, identity); err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("oidc login: error storing federated identity")
		return
	}
	if foundUnderKey != "" && foundUnderKey != providerKey {
		adminServer.st.DeleteOAuthIdentity(accountID, foundUnderKey, claims.Subject)
	}
}

// oidcConnectionForRequest resolves the connection targeted by an admin CRUD
// request. Callers are account-scoped; a super admin may target another account
// via the ?account_id query parameter.
func (adminServer *AdminServer) oidcConnectionAccountID(r *http.Request) string {
	return adminServer.resolveTargetAccountID(r, r.URL.Query().Get("account_id"))
}

// GET /api/v1/oidc/{slug}/authorize
// Resolves the connection by slug and 302-redirects to the IdP authorize URL.
func (adminServer *AdminServer) OIDCAuthorize(w http.ResponseWriter, r *http.Request) {
	slug := mux.Vars(r)["slug"]
	conn, err := adminServer.st.GetOIDCConnectionBySlug(slug)
	if err != nil || conn == nil || !conn.Enabled {
		http.Error(w, "unknown or disabled oidc connection", http.StatusNotFound)
		return
	}

	inviteToken := r.URL.Query().Get("invite_token")
	returnTo := r.URL.Query().Get("return_to")

	url, _, err := adminServer.oidc.AuthorizationURL(r.Context(), conn, inviteToken, returnTo)
	if err != nil {
		adminServer.Logger.ErrorE().Str("slug", slug).Err(err).Msg("oidc authorize")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}

// GET /api/v1/oidc/{slug}/callback
// Completes the flow and redirects the browser to the stored same-origin return
// path with the session encoded in the URL fragment (fragments are not sent to
// servers or written to logs). The redirect target is keyed off the single-use
// state, not off any callback-time client input.
func (adminServer *AdminServer) OIDCCallback(w http.ResponseWriter, r *http.Request) {
	slug := mux.Vars(r)["slug"]
	conn, err := adminServer.st.GetOIDCConnectionBySlug(slug)
	if err != nil || conn == nil || !conn.Enabled {
		http.Error(w, "unknown or disabled oidc connection", http.StatusNotFound)
		return
	}

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		http.Error(w, "code and state are required", http.StatusBadRequest)
		return
	}

	// Recover the return path from the (single-use) state before it is consumed,
	// so we can redirect errors back to the SPA too.
	returnTo := "/"
	if sr, srErr := adminServer.st.GetOAuthState(state); srErr == nil && sr.RedirectURI != "" {
		returnTo = sr.RedirectURI
	}

	response, _, err := adminServer.runOIDCLogin(r, conn, code, state)
	if err != nil {
		adminServer.Logger.ErrorE().Str("slug", slug).Err(err).Msg("oidc callback")
		http.Redirect(w, r, returnTo+"#oidc_error="+urlQueryEscape(err.Error()), http.StatusFound)
		return
	}

	encoded, err := encodeSessionFragment(response)
	if err != nil {
		adminServer.Logger.ErrorE().Str("slug", slug).Err(err).Msg("oidc callback: encode session")
		http.Redirect(w, r, returnTo+"#oidc_error=session_encode_failed", http.StatusFound)
		return
	}
	http.Redirect(w, r, returnTo+"#session="+encoded, http.StatusFound)
}

// encodeSessionFragment serializes a SessionResponse as base64url(JSON) for
// delivery in a URL fragment.
func encodeSessionFragment(resp *SessionResponse) (string, error) {
	b, err := json.Marshal(resp)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// urlQueryEscape escapes an error string for safe inclusion in a URL fragment.
func urlQueryEscape(s string) string {
	return url.QueryEscape(s)
}

// GET /api/v1/oidc/config
// Public, secret-free login config for the current tenant's UI: whether a
// connection is enabled and its slug. Never returns ClientSecret.
func (adminServer *AdminServer) GetOIDCPublicConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	slug := r.URL.Query().Get("slug")
	var conn *user.OIDCConnection
	var err error
	if slug != "" {
		conn, err = adminServer.st.GetOIDCConnectionBySlug(slug)
	} else if accountID := r.URL.Query().Get("account_id"); accountID != "" {
		conn, err = adminServer.st.GetOIDCConnectionByAccount(accountID)
	}
	if err != nil || conn == nil || !conn.Enabled {
		json.NewEncoder(w).Encode(map[string]interface{}{"enabled": false})
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"enabled": true,
		"slug":    conn.Slug,
	})
}

// GET /api/v1/oidc/connection
// Returns the account's OIDC connection WITHOUT the client secret.
func (adminServer *AdminServer) GetOIDCConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	accountID := adminServer.oidcConnectionAccountID(r)

	conn, err := adminServer.st.GetOIDCConnectionByAccount(accountID)
	if err != nil {
		http.Error(w, `{"result":"error","message":"no oidc connection configured"}`, http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(conn.Public())
}

// PUT /api/v1/oidc/connection
// Creates or replaces the account's OIDC connection.
func (adminServer *AdminServer) PutOIDCConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	accountID := adminServer.oidcConnectionAccountID(r)

	const maxBodySize = 64 * 1024
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize))
	if err != nil {
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	var conn user.OIDCConnection
	if err := json.Unmarshal(body, &conn); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	conn.AccountID = accountID

	// Preserve the stored secret when the caller omits it (so a client that
	// received the secret-free GET can PUT edits back without resubmitting it).
	if conn.ClientSecret == "" {
		if existing, exErr := adminServer.st.GetOIDCConnectionByAccount(accountID); exErr == nil {
			conn.ClientSecret = existing.ClientSecret
		}
	}

	conn.ApplyDefaults()
	if err := conn.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := adminServer.st.PutOIDCConnection(accountID, &conn); err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("oidc connection put")
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	// Drop any cached provider for this issuer so config changes take effect.
	adminServer.oidc.invalidate(conn.IssuerURL)

	json.NewEncoder(w).Encode(conn.Public())
}

// DELETE /api/v1/oidc/connection
// Removes the account's OIDC connection and its slug index.
func (adminServer *AdminServer) DeleteOIDCConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	accountID := adminServer.oidcConnectionAccountID(r)

	if err := adminServer.st.DeleteOIDCConnection(accountID); err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("oidc connection delete")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"result": "ok"})
}

// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////
// Invitations
// //////////////////////////////////////////////////////////////////////////////////////////////////////////////////

// POST /api/v1/invitations
// Create a new invitation (requires admin role)
func (adminServer *AdminServer) CreateInvitation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	userID := UserIDFromContext(r.Context())
	accountID := AccountIDFromContext(r.Context())

	if userID == "" || accountID == "" {
		http.Error(w, server.Unauthorized, http.StatusUnauthorized)
		return
	}

	const maxBodySize = 10 * 1024
	limitedReader := io.LimitReader(r.Body, maxBodySize)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("create invitation: error reading body")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	var config user.InvitationConfig
	if err := json.Unmarshal(body, &config); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if config.Email == "" {
		http.Error(w, "email is required", http.StatusBadRequest)
		return
	}

	if config.TTLHours <= 0 {
		config.TTLHours = adminServer.invitationDefaultTTLHours
	}

	if len(config.Roles) == 0 {
		config.Roles = []string{user.RoleUser}
	}

	// Validate roles
	for _, role := range config.Roles {
		if !user.IsValidRole(role) {
			http.Error(w, fmt.Sprintf("invalid role: %s", role), http.StatusBadRequest)
			return
		}
	}

	// Scope the invitation to the caller's account. Only super admins may
	// invite into another account by supplying account_id; for every other
	// caller a client-supplied account_id is ignored, preventing a tenant
	// admin from minting an administrator invitation into another tenant.
	targetAccountID := adminServer.resolveTargetAccountID(r, config.AccountID)

	inv, err := user.NewInvitation(&config, targetAccountID, userID)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("create invitation: error creating invitation")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	if err := adminServer.st.PutInvitation(inv); err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("create invitation: error storing invitation")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	adminServer.Logger.InfoE().Str("invitationID", inv.ID).Str("email", inv.Email).Str("accountID", accountID).Msg("invitation created")

	// Look up inviter email best-effort; not fatal if it's unavailable.
	inviterEmail := ""
	if u, err := adminServer.st.GetUserByPublicKey(accountID, userID); err == nil && u != nil {
		inviterEmail = u.Email
	}

	// Send the invitation email asynchronously so an SES hiccup doesn't
	// fail the API call. The invitation is already persisted; the inviter
	// can fall back to copying the link from the UI.
	go func(inv *user.Invitation, inviterEmail string) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := adminServer.emailSender.SendInvitation(ctx, inv, inviterEmail); err != nil {
			adminServer.Logger.ErrorE().Str("invitationID", inv.ID).Str("email", inv.Email).Err(err).Msg("send invitation email failed")
		}
	}(inv, inviterEmail)

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(inv)
}

// GET /api/v1/invitations
// List invitations for the current account (or a target account via ?account_id=)
func (adminServer *AdminServer) GetInvitations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	accountID := AccountIDFromContext(r.Context())
	if accountID == "" {
		http.Error(w, server.Unauthorized, http.StatusUnauthorized)
		return
	}

	// Only super admins may query invitations for another account; for every
	// other caller a client-supplied account_id is ignored so invitation
	// tokens for other tenants are never disclosed.
	targetAccountID := adminServer.resolveTargetAccountID(r, r.URL.Query().Get("account_id"))

	invitations, err := adminServer.st.GetInvitationsByAccount(targetAccountID)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("get invitations: error")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	// Mark expired invitations
	for _, inv := range invitations {
		if inv.IsPending() && inv.IsExpired() {
			inv.Status = user.InvitationExpired
		}
	}

	// Newest first. See writeList for paging.
	writeList(w, r, invitations, func(a, b *user.Invitation) bool {
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID < b.ID
	})
}

// GET /api/v1/invitations/{token}
// Get invitation details (public - no auth required)
func (adminServer *AdminServer) GetInvitationByToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	token := mux.Vars(r)["token"]
	if token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}

	inv, err := adminServer.st.GetInvitationByToken(token)
	if err != nil {
		http.Error(w, "invitation not found", http.StatusNotFound)
		return
	}

	// Check if expired
	if inv.IsPending() && inv.IsExpired() {
		inv.Status = user.InvitationExpired
	}

	// Get account name for display
	account, err := adminServer.st.GetAccountByID(inv.AccountID)
	accountName := ""
	if err == nil {
		accountName = account.Name
	}

	// Get inviter name
	inviter, err := adminServer.st.GetUserByID(inv.AccountID, inv.InvitedBy)
	inviterName := ""
	if err == nil {
		inviterName = fmt.Sprintf("%s %s", inviter.FirstName, inviter.LastName)
	}

	// Return public info (not the full invitation)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":              inv.ID,
		"token":           inv.Token,
		"account_id":      inv.AccountID,
		"account_name":    accountName,
		"invited_by_name": inviterName,
		"email":           inv.Email,
		"roles":           inv.Roles,
		"status":          inv.Status,
		"expires_at":      inv.ExpiresAt,
	})
}

// POST /api/v1/invitations/{token}/accept
// Accept an invitation
func (adminServer *AdminServer) AcceptInvitation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	token := mux.Vars(r)["token"]
	if token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}

	inv, err := adminServer.st.GetInvitationByToken(token)
	if err != nil {
		http.Error(w, "invitation not found", http.StatusNotFound)
		return
	}

	if !inv.CanBeAccepted() {
		status := "expired"
		if inv.Status != user.InvitationPending {
			status = string(inv.Status)
		}
		http.Error(w, fmt.Sprintf("invitation cannot be accepted: %s", status), http.StatusBadRequest)
		return
	}

	const maxBodySize = 10 * 1024
	limitedReader := io.LimitReader(r.Body, maxBodySize)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("accept invitation: error reading body")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	var request struct {
		Email     string `json:"email"`
		Password  string `json:"password"`
		FirstName string `json:"firstname"`
		LastName  string `json:"lastname"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Create user with password
	if request.Email == "" {
		request.Email = inv.Email
	}

	if request.Password == "" {
		http.Error(w, "password is required", http.StatusBadRequest)
		return
	}

	// Check if user already exists in this account
	userID := fmt.Sprintf("%v:%v", inv.AccountID, request.Email)
	existingUser, _ := adminServer.st.GetUserByID(inv.AccountID, userID)
	if existingUser != nil && existingUser.ID != "" {
		http.Error(w, "user already exists in this account", http.StatusConflict)
		return
	}

	// Create the user
	u, err := adminServer.st.NewUser(&user.UserConfig{
		Email:     request.Email,
		FirstName: request.FirstName,
		LastName:  request.LastName,
		Password:  request.Password,
		Roles:     inv.Roles,
		AccountID: inv.AccountID,
	})
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("accept invitation: error creating user")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	// Mark invitation as accepted
	inv.Accept(u.ID)
	if err := adminServer.st.UpdateInvitation(inv); err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("accept invitation: error updating invitation")
	}

	// Create login response
	response, err := adminServer.createOAuthLoginResponse(u, r, false, "")
	if err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("accept invitation: error creating response")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	adminServer.Logger.InfoE().Str("invitationID", inv.ID).Str("userID", u.ID).Msg("invitation accepted")

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// DELETE /api/v1/invitations/{token}
// Revoke an invitation (requires admin role)
func (adminServer *AdminServer) RevokeInvitation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	accountID := AccountIDFromContext(r.Context())
	if accountID == "" {
		http.Error(w, server.Unauthorized, http.StatusUnauthorized)
		return
	}

	token := mux.Vars(r)["token"]
	if token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}

	inv, err := adminServer.st.GetInvitationByToken(token)
	if err != nil {
		http.Error(w, "invitation not found", http.StatusNotFound)
		return
	}

	// Admins from the admin account (super admins) can revoke any
	// invitation; otherwise the invitation must belong to the caller's
	// account. RequireAdmin has already ensured the caller has admin
	// privileges either globally or within their own account.
	if inv.AccountID != accountID && accountID != adminServer.AdminAccountID() {
		http.Error(w, server.Unauthorized, http.StatusUnauthorized)
		return
	}

	// Revoke the invitation
	inv.Revoke()
	if err := adminServer.st.UpdateInvitation(inv); err != nil {
		adminServer.Logger.ErrorE().Err(err).Msg("revoke invitation: error updating invitation")
		http.Error(w, server.InternalServerError, http.StatusInternalServerError)
		return
	}

	adminServer.Logger.InfoE().Str("invitationID", inv.ID).Msg("invitation revoked")

	w.WriteHeader(http.StatusNoContent)
}
