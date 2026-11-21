package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/bboxsys/platform/ai"
)

var (
	_ ai.Checker     = (*Provider)(nil)
	_ ai.ModelLister = (*Provider)(nil)
)

// maxListed is how many of the server's models a not-found message names.
const maxListed = 10

// Check verifies the endpoint, the key, and the model by listing the
// server's models (GET /models), which spends no tokens. The model must be
// listed; Ollama's ":latest" suffix may be left off. An Ollama server is
// also asked for the model's details (POST /api/show) so the answer
// carries the context length and refuses a model that cannot call tools;
// a server without that route (llama-server) is checked by the listing
// alone.
func (p *Provider) Check(ctx context.Context) (ai.CheckResult, error) {
	models, err := p.listModels(ctx)
	if err != nil {
		return ai.CheckResult{}, err
	}
	if p.model == "" {
		return ai.CheckResult{}, &ai.CheckError{Kind: ai.CheckNotFound, Message: "no model is set" + offers(models)}
	}
	var found *ai.ModelInfo
	for i := range models {
		if models[i].ID == p.model || models[i].ID == p.model+":latest" {
			found = &models[i]
			break
		}
	}
	if found == nil {
		return ai.CheckResult{}, &ai.CheckError{Kind: ai.CheckNotFound, Message: fmt.Sprintf("model %s was not found", p.model) + offers(models)}
	}
	res := ai.CheckResult{Model: found.ID, DisplayName: found.ID, ContextWindow: found.ContextWindow}
	if p.root != "" {
		if err := p.show(ctx, &res); err != nil {
			return ai.CheckResult{}, err
		}
	}
	if res.ContextWindow == 0 && p.window > 0 {
		res.ContextWindow = int64(p.window)
	}
	return res, nil
}

// ListModels returns the models the endpoint serves (GET /models), sorted
// by id. llama-server's listing carries the loaded context size (n_ctx)
// and the trained one (n_ctx_train); the first is what a session gets, so
// it wins. Ollama's listing carries neither.
func (p *Provider) ListModels(ctx context.Context) ([]ai.ModelInfo, error) {
	return p.listModels(ctx)
}

type listedModel struct {
	ID   string `json:"id"`
	Meta *struct {
		NCtx      int64 `json:"n_ctx"`
		NCtxTrain int64 `json:"n_ctx_train"`
	} `json:"meta"`
}

func (p *Provider) listModels(ctx context.Context) ([]ai.ModelInfo, error) {
	resp, err := p.do(ctx, http.MethodGet, p.base+"/models", nil, "application/json")
	if err != nil {
		return nil, classify(err, "")
	}
	defer resp.Body.Close()
	var body struct {
		Data []listedModel `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, &ai.CheckError{Kind: ai.CheckRejected, Message: "the models listing could not be read: " + err.Error()}
	}
	out := make([]ai.ModelInfo, 0, len(body.Data))
	for _, m := range body.Data {
		if m.ID == "" {
			continue
		}
		info := ai.ModelInfo{ID: m.ID}
		if m.Meta != nil {
			info.ContextWindow = m.Meta.NCtx
			if info.ContextWindow == 0 {
				info.ContextWindow = m.Meta.NCtxTrain
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
}

// show asks an Ollama server about the model. A missing route is not an
// error (the server is not Ollama); a listed model that Ollama says cannot
// use tools is, since the assistant needs them.
func (p *Provider) show(ctx context.Context, res *ai.CheckResult) error {
	body, _ := json.Marshal(map[string]string{"model": p.model})
	resp, err := p.do(ctx, http.MethodPost, p.root+"/api/show", body, "application/json")
	if err != nil {
		var he *httpError
		if errors.As(err, &he) && he.status != http.StatusUnauthorized && he.status != http.StatusForbidden {
			return nil
		}
		return classify(err, p.model)
	}
	defer resp.Body.Close()
	var info struct {
		Capabilities []string       `json:"capabilities"`
		ModelInfo    map[string]any `json:"model_info"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&info); err != nil {
		return nil
	}
	if len(info.Capabilities) > 0 {
		hasTools := false
		for _, c := range info.Capabilities {
			if c == "tools" {
				hasTools = true
			}
		}
		if !hasTools {
			return &ai.CheckError{Kind: ai.CheckRejected, Message: fmt.Sprintf("model %s cannot call tools (capabilities: %s)", p.model, strings.Join(info.Capabilities, ", "))}
		}
	}
	for k, v := range info.ModelInfo {
		if strings.HasSuffix(k, ".context_length") {
			if n, ok := v.(float64); ok && n > 0 {
				res.ContextWindow = int64(n)
			}
		}
	}
	return nil
}

func offers(models []ai.ModelInfo) string {
	if len(models) == 0 {
		return "; the server lists no models"
	}
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	more := ""
	if len(ids) > maxListed {
		more = fmt.Sprintf(" and %d more", len(ids)-maxListed)
		ids = ids[:maxListed]
	}
	return "; the server offers: " + strings.Join(ids, ", ") + more
}

// classify turns a request error into an ai.CheckError.
func classify(err error, model string) error {
	var he *httpError
	if !errors.As(err, &he) {
		return &ai.CheckError{Kind: ai.CheckUnreachable, Message: "the endpoint could not be reached: " + strings.TrimPrefix(err.Error(), "openai: ")}
	}
	switch he.status {
	case http.StatusUnauthorized:
		return &ai.CheckError{Kind: ai.CheckAuth, Message: "the API key was rejected" + suffix(he.message)}
	case http.StatusForbidden:
		return &ai.CheckError{Kind: ai.CheckAuth, Message: "the API key is not allowed to use this API" + suffix(he.message)}
	case http.StatusNotFound:
		if model != "" {
			return &ai.CheckError{Kind: ai.CheckNotFound, Message: fmt.Sprintf("model %s was not found", model) + suffix(he.message)}
		}
		return &ai.CheckError{Kind: ai.CheckNotFound, Message: "the endpoint has no models route; check the base URL" + suffix(he.message)}
	default:
		return &ai.CheckError{Kind: ai.CheckRejected, Message: fmt.Sprintf("the API answered %d", he.status) + suffix(he.message)}
	}
}

func suffix(detail string) string {
	if detail == "" {
		return ""
	}
	return ": " + detail
}
