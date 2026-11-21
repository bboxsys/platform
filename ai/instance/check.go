package instance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/ai/rag"
)

// CheckInput is the body of POST {prefix}/credentials/test. Kind is the
// credential's resource type; the name is resolved like every other use of
// a credential (for the vault resolver, the caller's own first and then
// the account's).
type CheckInput struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// CheckResult is the answer. A failed test is a 200 with OK false: the
// request succeeded, the credential did not.
type CheckResult struct {
	OK        bool   `json:"ok"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	LatencyMS int64  `json:"latency_ms"`
	// Message is a one-line summary for a toast.
	Message string `json:"message"`
	// ErrorKind is set on failure: auth, not_found, unreachable, rejected,
	// or unsupported.
	ErrorKind string `json:"error_kind,omitempty"`
	ai.CheckResult
	// Dimensions is the embedding vector size, for an embedding credential.
	Dimensions int `json:"dimensions,omitempty"`
	// Tools is the number of tools a remote MCP server lists.
	Tools int `json:"tools,omitempty"`
}

// TestCredential checks that a credential works, without running the
// assistant: a model credential reads its model from the provider (no
// tokens spent), an embedding credential embeds one short string, and an
// MCP server is connected and its tools listed. The secret never appears
// in the answer.
func (i *Instance) TestCredential(w http.ResponseWriter, r *http.Request) {
	var in CheckInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	caller := ai.CallerFromRequest(r)
	ctx, cancel := context.WithTimeout(r.Context(), CheckTimeout)
	defer cancel()

	var (
		cred ai.Credential
		err  error
	)
	creds := i.opts.Credentials
	switch in.Kind {
	case ai.ResourceTypeModel:
		cred, err = creds.Model(ctx, caller.AccountID, caller.UserID, in.Name)
	case ai.ResourceTypeEmbedding:
		cred, err = creds.Embedding(ctx, caller.AccountID, caller.UserID, in.Name)
	case ai.ResourceTypeMCPServer:
		cred, err = i.mcpCredential(ctx, caller, in.Name)
	default:
		writeError(w, http.StatusBadRequest, "kind must be ai_model, ai_embedding, or mcp_server")
		return
	}
	if errors.Is(err, ai.ErrNoCredential) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("no %s credential named %q", in.Kind, in.Name))
		return
	}
	out := CheckResult{Name: in.Name, Kind: in.Kind}
	start := time.Now()
	if err == nil {
		switch in.Kind {
		case ai.ResourceTypeModel:
			err = i.checkModel(ctx, cred, &out)
		case ai.ResourceTypeEmbedding:
			err = checkEmbedding(ctx, cred, &out)
		case ai.ResourceTypeMCPServer:
			err = checkMCP(ctx, cred, &out)
		}
	}
	out.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		out.OK = false
		out.ErrorKind = string(ai.CheckRejected)
		var ce *ai.CheckError
		if errors.As(err, &ce) {
			out.ErrorKind = string(ce.Kind)
		} else if errors.Is(err, context.DeadlineExceeded) {
			out.ErrorKind = string(ai.CheckUnreachable)
		}
		out.Message = Redact(err.Error(), cred.Bearer)
		i.log.InfoE().Str("credential", in.Name).Str("kind", in.Kind).Str("result", out.ErrorKind).Msg("ai: credential test failed")
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.OK = true
	writeJSON(w, http.StatusOK, out)
}

// ModelsInput is the body of POST {prefix}/credentials/models: the
// provider and endpoint a form holds, with either the key typed into it
// (Token) or the name of a saved credential whose stored key, and endpoint
// when Endpoint is empty, are used instead. Nothing typed is stored.
type ModelsInput struct {
	Provider string `json:"provider"`
	Endpoint string `json:"endpoint"`
	Token    string `json:"token"`
	Name     string `json:"name"`
}

// ModelsResult is the answer: the models the endpoint serves, or why it
// could not say (a 200 with OK false, as for a credential test).
type ModelsResult struct {
	OK        bool           `json:"ok"`
	Provider  string         `json:"provider"`
	Models    []ai.ModelInfo `json:"models"`
	Message   string         `json:"message,omitempty"`
	ErrorKind string         `json:"error_kind,omitempty"`
}

// ListModels answers the models a provider's endpoint offers, for the
// credential form to pick from instead of asking for an id. It builds the
// provider the way a session would, from the body or the saved credential,
// and needs a provider that implements ai.ModelLister; the secret never
// appears in the answer.
func (i *Instance) ListModels(w http.ResponseWriter, r *http.Request) {
	var in ModelsInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	in.Provider = strings.TrimSpace(in.Provider)
	in.Name = strings.TrimSpace(in.Name)
	if in.Provider == "" {
		writeError(w, http.StatusBadRequest, "provider is required")
		return
	}
	factory, ok := i.providers[in.Provider]
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("no model provider %q on this server", in.Provider))
		return
	}
	caller := ai.CallerFromRequest(r)
	ctx, cancel := context.WithTimeout(r.Context(), CheckTimeout)
	defer cancel()

	cred := ai.Credential{Name: in.Name, ResourceType: ai.ResourceTypeModel, Provider: in.Provider, Endpoint: strings.TrimSpace(in.Endpoint), Bearer: in.Token}
	if cred.Bearer == "" && in.Name != "" {
		saved, err := i.opts.Credentials.Model(ctx, caller.AccountID, caller.UserID, in.Name)
		switch {
		case errors.Is(err, ai.ErrNoCredential):
			// A new credential whose name is not saved yet: list without a key.
		case err != nil:
			i.log.ErrorE().Err(err).Str("credential", in.Name).Msg("ai: models: resolving the credential failed")
			writeError(w, http.StatusInternalServerError, "resolving the credential failed")
			return
		default:
			cred.Bearer = saved.Bearer
			if cred.Endpoint == "" {
				cred.Endpoint = saved.Endpoint
			}
		}
	}
	out := ModelsResult{Provider: in.Provider, Models: []ai.ModelInfo{}}
	fail := func(kind, msg string) {
		out.OK = false
		out.ErrorKind = kind
		out.Message = Redact(msg, cred.Bearer)
		writeJSON(w, http.StatusOK, out)
	}
	provider, err := factory(cred)
	if err != nil {
		fail(string(ai.CheckRejected), err.Error())
		return
	}
	lister, ok := provider.(ai.ModelLister)
	if !ok {
		fail("unsupported", fmt.Sprintf("provider %q does not list its models", in.Provider))
		return
	}
	models, err := lister.ListModels(ctx)
	if err != nil {
		kind := string(ai.CheckRejected)
		var ce *ai.CheckError
		if errors.As(err, &ce) {
			kind = string(ce.Kind)
		} else if errors.Is(err, context.DeadlineExceeded) {
			kind = string(ai.CheckUnreachable)
		}
		fail(kind, err.Error())
		return
	}
	out.OK = true
	if models != nil {
		out.Models = models
	}
	writeJSON(w, http.StatusOK, out)
}

// mcpCredential finds an MCP server credential by name among those the
// caller can see.
func (i *Instance) mcpCredential(ctx context.Context, caller ai.Caller, name string) (ai.Credential, error) {
	creds, err := i.opts.Credentials.MCPServers(ctx, caller.AccountID, caller.UserID)
	if err != nil {
		return ai.Credential{}, err
	}
	for _, c := range creds {
		if c.Name == name {
			return c, nil
		}
	}
	return ai.Credential{}, ai.ErrNoCredential
}

func (i *Instance) checkModel(ctx context.Context, cred ai.Credential, out *CheckResult) error {
	providerName := cred.Provider
	if providerName == "" {
		providerName = ai.ProviderAnthropic
	}
	factory, ok := i.providers[providerName]
	if !ok {
		return &ai.CheckError{Kind: "unsupported", Message: fmt.Sprintf("no model provider %q on this server", providerName)}
	}
	provider, err := factory(cred)
	if err != nil {
		return fmt.Errorf("building the provider: %w", err)
	}
	checker, ok := provider.(ai.Checker)
	if !ok {
		return &ai.CheckError{Kind: "unsupported", Message: fmt.Sprintf("provider %q cannot be tested", providerName)}
	}
	res, err := checker.Check(ctx)
	if err != nil {
		return err
	}
	out.CheckResult = res
	name := res.DisplayName
	if name == "" {
		name = res.Model
	}
	out.Message = fmt.Sprintf("%s is reachable with this key", name)
	if res.ContextWindow > 0 {
		out.Message += fmt.Sprintf(" (%d token context)", res.ContextWindow)
	}
	return nil
}

func checkEmbedding(ctx context.Context, cred ai.Credential, out *CheckResult) error {
	embed, err := rag.Embedder(cred)
	if err != nil {
		return &ai.CheckError{Kind: "unsupported", Message: err.Error()}
	}
	vec, err := embed(ctx, "connection test")
	if err != nil {
		return &ai.CheckError{Kind: ai.CheckRejected, Message: "the embedding request failed: " + err.Error()}
	}
	out.Dimensions = len(vec)
	out.Message = fmt.Sprintf("embeddings work: %d dimensions", len(vec))
	return nil
}

func checkMCP(ctx context.Context, cred ai.Credential, out *CheckResult) error {
	tools, closer, err := connectMCP(ctx, cred)
	if err != nil {
		return &ai.CheckError{Kind: ai.CheckUnreachable, Message: "connecting to the server failed: " + err.Error()}
	}
	closer.Close()
	out.Tools = len(tools)
	out.Message = fmt.Sprintf("connected: %d tools", len(tools))
	return nil
}

// Redact removes the secret from a message in case an upstream library
// echoed it, so a failure can be shown in the browser safely. Secrets
// shorter than eight bytes are left alone (they would match too much).
func Redact(msg, secret string) string {
	if len(secret) >= 8 {
		msg = strings.ReplaceAll(msg, secret, "[redacted]")
	}
	return msg
}
