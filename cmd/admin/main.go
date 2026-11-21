// Command admin runs the admin service on its own, with the admin
// SPA embedded when built with `-tags adminui`.
//
// `admin -init -config <file>` seeds a development profile (self-signed
// localhost certificate plus a minimal config) and exits; see run.sh.
//
// It reads the same configuration keys as a product server so an existing
// server configuration file can be reused: app.*, security.*, admin_server.*,
// logging.max_entries, metrics.max_entries, and controller.* for the
// optional NATS messenger. With controller.embedded = true it also runs the
// NATS controller in-process (operator JWT and system account come from the
// admin server), which is what feeds the Services, Logs, and Store metrics
// pages in a development profile. With queue_server.port set it also runs
// the job queue relay service, which feeds the Queue page.
//
// With a controller configured, the admin API is also served over NATS as
// admin_service in every account (admin_server.nats_api, default true), and
// the queue service as queue_server (queue_server.nats_api, default true);
// see package natsapi.
//
// The AI assistant (package ai) is hosted at /api/v1/ai and /mcp unless
// ai.enabled = false; ai.effort, ai.allow_writes, and ai.max_turns tune it
// and the model credentials come from the API token vault (docs/AI.md).
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bboxsys/platform/admin"
	"github.com/bboxsys/platform/config"
	"github.com/bboxsys/platform/controller"
	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/messenger"
	"github.com/bboxsys/platform/queue"
	"github.com/bboxsys/platform/server"
	"github.com/rs/zerolog"
)

func main() {
	configFile := flag.String("config", os.Getenv("BBOXSYS_CONFIG"), "config file path")
	initOnly := flag.Bool("init", false, "write a self-signed localhost cert and a default config next to -config if missing, then exit")
	initPort := flag.Int("port", 8901, "admin port written by -init")
	flag.Parse()

	if *initOnly {
		if err := initConfig(*configFile, *initPort); err != nil {
			fmt.Fprintf(os.Stderr, "init: %v\n", err)
			os.Exit(1)
		}
		return
	}

	c := config.New("")
	c.Filename = *configFile
	if err := c.FromFile(); err != nil {
		fmt.Fprintf(os.Stderr, "error reading config: %v\n", err)
		os.Exit(1)
	}
	l := logger.NewLogger(c.V.GetString("app.name"), "admin")
	if levelStr := c.V.GetString("logging.level"); levelStr != "" {
		if level, err := zerolog.ParseLevel(levelStr); err == nil {
			zerolog.SetGlobalLevel(level)
		} else {
			l.ErrorE().Str("level", levelStr).Err(err).Msg("ignoring invalid logging.level")
		}
	}

	keyBytes, certBytes, caBytes, err := credentials(c)
	if err != nil {
		l.Fatal(err.Error())
	}

	opts := &server.ServerOptions{
		Application: c.V.GetString("app.name"),
		Component:   "admin_server",
		PrivateKey:  keyBytes,
		Certificate: certBytes,
		CA:          caBytes,
		HTTPPort:    c.V.GetInt("admin_server.port"),
		Messenger:   false,
		Metadata: map[string]interface{}{
			"storeLocation":                fmt.Sprintf("%s/%s", c.V.GetString("app.root_path"), "admin.db"),
			"tokenValidMinutes":            60,
			"accountServerURL":             c.V.GetString("admin_server.account_server"),
			"operatorServiceURL":           c.V.GetString("admin_server.url"),
			"defaultAdminEmail":            c.V.GetString("admin_server.admin_email"),
			"defaultAdminPassword":         c.V.GetString("admin_server.admin_temp_password"),
			"intermediate_ca_file":         c.V.GetString("admin_server.intermediate_ca_file"),
			"intermediate_cert_file":       c.V.GetString("admin_server.intermediate_cert_file"),
			"intermediate_key_file":        c.V.GetString("admin_server.intermediate_key_file"),
			"oauth_enabled":                c.V.GetBool("admin_server.oauth_enabled"),
			"google_client_id":             c.V.GetString("admin_server.google_client_id"),
			"google_client_secret":         c.V.GetString("admin_server.google_client_secret"),
			"oauth_redirect_uri":           c.V.GetString("admin_server.oauth_redirect_uri"),
			"oauth_state_ttl_minutes":      c.V.GetInt("admin_server.oauth_state_ttl_minutes"),
			"invitation_default_ttl_hours": c.V.GetInt("admin_server.invitation_default_ttl_hours"),
			"allow_self_service_accounts":  c.V.GetBool("admin_server.allow_self_service_accounts"),
			"smtp_host":                    c.V.GetString("admin_server.smtp_host"),
			"smtp_port":                    c.V.GetInt("admin_server.smtp_port"),
			"smtp_username":                c.V.GetString("admin_server.smtp_username"),
			"smtp_password":                c.V.GetString("admin_server.smtp_password"),
			"smtp_from":                    c.V.GetString("admin_server.smtp_from"),
			"smtp_from_name":               c.V.GetString("admin_server.smtp_from_name"),
			"invite_accept_url":            c.V.GetString("admin_server.invite_accept_url"),
		},
	}

	adminServer, err := admin.NewAdminServer(opts)
	if err != nil {
		l.Fatal(fmt.Sprintf("error creating admin server: %v", err))
	}
	if err := adminServer.PutServiceEndpointsMap(serviceEndpoints(c)); err != nil {
		l.Fatal(fmt.Sprintf("error storing service endpoints: %v", err))
	}

	if uiFS := admin.UIFS(); uiFS != nil {
		adminServer.ServeUI(uiFS)
		l.Info("serving embedded admin UI")
	} else {
		l.Info("built without ui tag; serving API only")
	}

	root := c.V.GetString("app.root_path")
	if err := adminServer.InitLogStore(root+"/logs.db", positive(c.V.GetInt("logging.max_entries"), 100000)); err != nil {
		l.ErrorE().Err(err).Msg("error initializing log store")
	}
	if err := adminServer.InitMetricsStore(root+"/metrics.db", positive(c.V.GetInt("metrics.max_entries"), 100000)); err != nil {
		l.ErrorE().Err(err).Msg("error initializing metrics store")
	}
	if err := adminServer.InitAIUsageStore(root+"/ai_usage.db", 0); err != nil {
		l.ErrorE().Err(err).Msg("error initializing ai usage store")
	}
	if port := c.V.GetInt("controller.monitoring_port"); port > 0 {
		adminServer.SetMonitoringPort(port)
	}
	startAI(c, l, adminServer)

	go adminServer.Start()
	ticker := time.NewTicker(10 * time.Millisecond)
	for !adminServer.Ready() {
		<-ticker.C
	}
	ticker.Stop()
	l.InfoE().Int("port", opts.HTTPPort).Msg("admin server ready")

	var controllerServer *controller.Controller
	if c.V.GetBool("controller.embedded") && c.V.GetInt("controller.port") > 0 {
		controllerServer = startController(c, l, adminServer)
	}
	if c.V.IsSet("controller") && c.V.GetInt("controller.port") > 0 && c.V.GetBool("admin_server.messenger") {
		connectMessenger(c, l, adminServer, keyBytes, certBytes, caBytes)
	}
	haveController := c.V.GetInt("controller.port") > 0 || c.V.GetString("controller.url") != ""
	if haveController && boolOr(c, "admin_server.nats_api", true) {
		if err := adminServer.StartNATSAPI(admin.NATSAPIOptions{Description: "admin API over NATS"}); err != nil {
			l.ErrorE().Err(err).Msg("nats api: admin start failed")
		} else {
			l.Info("nats api: admin_service enabled; served on every account connection")
		}
	}
	var queueServer *queue.QueueServer
	if haveController && c.V.GetInt("queue_server.port") > 0 {
		queueServer = startQueue(c, l, adminServer, keyBytes, certBytes, caBytes)
	}
	var col *collectors
	if haveController {
		col = startCollectors(c, l, adminServer, controllerServer, queueServer, keyBytes, certBytes, caBytes)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	l.Info("shutting down")
	if col != nil {
		col.Close()
	}
	if queueServer != nil {
		queueServer.Close()
	}
	adminServer.Close()
	if controllerServer != nil {
		if err := controllerServer.Close(); err != nil {
			l.ErrorE().Err(err).Msg("error closing controller")
		}
	}
}

// controllerURL is the NATS URL clients use to reach the controller:
// controller.url when set, otherwise the embedded controller on localhost.
func controllerURL(c *config.Config) string {
	if url := c.V.GetString("controller.url"); url != "" {
		return url
	}
	return fmt.Sprintf("nats://localhost:%d", c.V.GetInt("controller.port"))
}

// startController runs the NATS controller in this process, mirroring the
// controller routine of a product binary. The admin server must already be
// serving: the controller resolves account JWTs (including the system
// account, at startup) from its /jwt/v1/accounts/ endpoint.
func startController(c *config.Config, l *logger.Logger, adminServer *admin.AdminServer) *controller.Controller {
	systemAccountID := c.V.GetString("controller.system_account_id")
	if systemAccountID == "" {
		id, err := adminServer.GetSysAccountID()
		if err != nil {
			l.Fatal(fmt.Sprintf("controller: error getting system account id: %v", err))
		}
		systemAccountID = id
	}
	operatorJWT := c.V.GetString("controller.operator_jwt")
	if operatorJWT == "" {
		jwt, err := adminServer.GetOperatorJWT()
		if err != nil {
			l.Fatal(fmt.Sprintf("controller: error getting operator jwt: %v", err))
		}
		operatorJWT = jwt
	}
	resolverURL := c.V.GetString("controller.account_server")
	if resolverURL == "" {
		resolverURL = c.V.GetString("admin_server.url") + "/jwt/v1/accounts/"
	}

	ctrl := controller.NewController(controller.ControllerConfig{
		Application:        c.V.GetString("app.name"),
		Port:               c.V.GetInt("controller.port"),
		MonitorPort:        c.V.GetInt("controller.monitoring_port"),
		KeyFile:            c.V.GetString("security.key_file"),
		CertFile:           c.V.GetString("security.cert_file"),
		CAFile:             c.V.GetString("security.ca_file"),
		OperatorJWT:        operatorJWT,
		SystemAccountID:    systemAccountID,
		ResolverURL:        resolverURL,
		ResolverServerName: c.V.GetString("controller.account_server_name"),
		InsecureSkipVerify: c.V.GetBool("controller.insecure_skip_verify"),
	})
	ctrl.Start() // blocks until ready; Fatal after 10s
	l.InfoE().Int("port", c.V.GetInt("controller.port")).Int("monitoringPort", c.V.GetInt("controller.monitoring_port")).Msg("embedded controller ready")
	return ctrl
}

func credentials(c *config.Config) (key, cert, ca []byte, err error) {
	read := func(k string) ([]byte, error) {
		b, err := os.ReadFile(c.V.GetString(k))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", k, err)
		}
		return b, nil
	}
	if key, err = read("security.key_file"); err != nil {
		return
	}
	if cert, err = read("security.cert_file"); err != nil {
		return
	}
	ca, err = read("security.ca_file")
	return
}

// serviceEndpoints builds the endpoint map a product binary publishes so the
// SPA finds the same service URLs whichever binary hosts admin.
func serviceEndpoints(c *config.Config) map[string]admin.ServiceEndpoint {
	hostname := c.V.GetString("app.hostname")
	endpoints := make(map[string]admin.ServiceEndpoint)
	add := func(kind, scheme, portKey string) {
		url := c.V.GetString("app." + kind)
		if url == "" {
			if port := c.V.GetInt(portKey); port > 0 {
				url = fmt.Sprintf("%s://%s:%d", scheme, hostname, port)
			}
		}
		if url != "" {
			endpoints[kind] = admin.ServiceEndpoint{Kind: kind, URL: url}
		}
	}
	add("login_service", "https", "admin_server.port")
	add("admin_service", "https", "admin_server.port")
	add("proxy_service", "https", "proxy_server.port")
	add("queue_service", "https", "queue_server.port")
	add("blackbox_service", "https", "blackbox_server.port")
	add("media_service", "https", "media_server.port")
	add("host_service", "https", "host_server.port")
	add("comms_service", "https", "comms_server.port")
	if c.V.GetInt("controller.port") > 0 || c.V.GetString("controller.url") != "" {
		endpoints["controller_service"] = admin.ServiceEndpoint{Kind: "controller_service", URL: controllerURL(c)}
	}
	return endpoints
}

func connectMessenger(c *config.Config, l *logger.Logger, adminServer *admin.AdminServer, key, cert, ca []byte) {
	sc, err := adminServer.ServiceCredentials("blackbox", "admin_server")
	if err != nil {
		l.ErrorE().Err(err).Msg("messenger: service credentials failed")
		return
	}
	err = adminServer.AddMessenger(&messenger.ClientOptions{
		Application:   c.V.GetString("app.name"),
		Component:     "admin_server",
		ControllerURL: controllerURL(c),
		AccountID:     sc.AccountID,
		PrivateKey:    key,
		Certificate:   cert,
		CA:            ca,
		JWT:           sc.JWT,
		Seed:          sc.Seed,
	})
	if err != nil {
		l.ErrorE().Err(err).Msg("messenger: connect failed")
		return
	}
	l.Info("messenger client connected")
}

// boolOr reads a boolean key that defaults to def when it is not set.
func boolOr(c *config.Config, key string, def bool) bool {
	if !c.V.IsSet(key) {
		return def
	}
	return c.V.GetBool(key)
}

func positive(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
