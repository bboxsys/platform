package main

import (
	"github.com/bboxsys/platform/admin"
	"github.com/bboxsys/platform/ai/anthropic"
	"github.com/bboxsys/platform/config"
	"github.com/bboxsys/platform/logger"
)

// startAI hosts the assistant in the admin server unless ai.enabled is
// false. Configuration keys, all optional:
//
//	ai.enabled       host the assistant (default true)
//	ai.effort        reasoning effort: low, medium, high, xhigh, max; when set it
//	                 overrides every credential's ai_effort tag, when unset the tag applies
//	                 and medium is the fallback
//	ai.allow_writes  let the model run tools that are not read-only (default false)
//	ai.max_turns     model turns per run (default the loop's 25)
//
// The model and embedding credentials themselves are API tokens in the
// vault (resource types ai_model and ai_embedding), not config. The
// assistant is an ai/instance with its own store and index under
// {app.root_path}/ai (ai.db, index) and reports its usage into the
// ai_usage.db store in-process. It is called before adminServer.Start so
// the routes are registered before the server serves (StartAI resolves
// the auth wrappers per request, so the order relative to AddHandlers does
// not matter), after InitAIUsageStore so usage reaches that store, and
// before startCollectors so the store metrics include ai/ai.db.
func startAI(c *config.Config, l *logger.Logger, adminServer *admin.AdminServer) {
	if !boolOr(c, "ai.enabled", true) {
		l.Info("ai: disabled by ai.enabled")
		return
	}
	// Only a configured effort is passed to the default factories: an empty
	// one lets each credential's ai_effort tag apply, then
	// anthropic.DefaultEffort for Anthropic and the server default for the
	// OpenAI-dialect providers.
	effort := c.V.GetString("ai.effort")
	logEffort := effort
	if logEffort == "" {
		logEffort = "credential or " + anthropic.DefaultEffort
	}
	err := adminServer.StartAI(admin.AIOptions{
		Dir:         c.V.GetString("app.root_path"),
		Effort:      effort,
		MaxTurns:    c.V.GetInt("ai.max_turns"),
		AllowWrites: c.V.GetBool("ai.allow_writes"),
		// The admin SPA renders echarts blocks with Apache ECharts.
		Charts: true,
	})
	if err != nil {
		l.ErrorE().Err(err).Msg("ai: start failed")
		return
	}
	// The console's service selector: the admin's own assistant plus any
	// service assistant a product registers (admin.AssistantGateway). The
	// standalone admin hosts none, so the list holds the admin alone;
	// relayed conversations would travel on the collectors' per-account
	// connections.
	adminServer.MountAssistantGateway(admin.AssistantGatewayOptions{})
	l.InfoE().Str("effort", logEffort).Bool("allowWrites", c.V.GetBool("ai.allow_writes")).Msg("ai: assistant enabled at /api/v1/ai and /mcp")
}
