package ai

import (
	"context"
	"time"
)

// RunRecord describes one run, one user message driven to completion, for
// usage accounting. Service fills it after every run, successful or not,
// and hands it to ServiceOptions.Usage. Package ai/usage stores and
// reports it; any other sink works too.
type RunRecord struct {
	AccountID string `json:"account_id"`
	UserID    string `json:"user_id"`
	SessionID string `json:"session_id"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	// Credential is the vault name of the model credential, never its secret.
	Credential string `json:"credential,omitempty"`
	// Turns is the number of model turns; ToolCalls the tools run in them.
	Turns     int   `json:"turns"`
	ToolCalls int   `json:"tool_calls"`
	Usage     Usage `json:"usage"`
	// StopReason is the final stop reason (end_turn, max_turns, refusal,
	// ...); Error is set when the run failed instead, with no secret in it.
	StopReason string        `json:"stop_reason,omitempty"`
	Error      string        `json:"error,omitempty"`
	Started    time.Time     `json:"started"`
	Duration   time.Duration `json:"duration"`
}

// UsageSink receives a RunRecord after every run. RecordRun must not block
// for long: it is called on the request path after the stream has ended.
type UsageSink interface {
	RecordRun(ctx context.Context, r RunRecord)
}
