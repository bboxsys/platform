// Package agent is the headless host agent: a CLI that registers a workload
// with a central admin server and then runs on that host, applying the
// configuration the server handed back and restarting itself when that
// configuration changes on disk.
//
// A product builds its own agent binary by wrapping the command with its
// branding:
//
//	func main() {
//		if err := agent.Execute(agent.Options{Name: "acme-agent"}); err != nil {
//			os.Exit(1)
//		}
//	}
//
// The `register` subcommand POSTs the host's FQDN and host ID to
// `{admin-server-url}/api/v1/agents/configuration` with a bearer token and
// writes the returned agent configuration (credentials, controller URL, PF
// configs) to the default agent config path for the chosen kind (`--as-service`
// selects the system-wide config directory). The `run` subcommand loads that
// file through host.AgentConfigFromFile, starts a host.Agent and watches the
// file, recreating the agent whenever it changes.
//
// The `api` subcommand calls a service's HTTP API over NATS with the agent's
// own credentials (`agent api GET /api/v1/users`), and `services` lists the
// services answering in the agent's account. Both need only the controller
// URL and credentials that `register` stored; see natsapi for the protocol.
package agent

import (
	"github.com/bboxsys/platform/config"
	"github.com/bboxsys/platform/host"
	"github.com/bboxsys/platform/logger"
	"github.com/spf13/cobra"
)

// DefaultName is the binary name used when Options.Name is empty.
const DefaultName = "bboxsys-agent"

// Options brands the agent command for a product.
type Options struct {
	// Name is the binary name shown in help text and used as the logger's
	// application name. Defaults to DefaultName.
	Name string
	// Short and Long override the root command's help text. Both default to
	// generic descriptions that mention Name.
	Short string
	Long  string
}

func (o Options) withDefaults() Options {
	if o.Name == "" {
		o.Name = DefaultName
	}
	if o.Short == "" {
		o.Short = o.Name + " registers this host with the admin server and manages its network services configuration"
	}
	if o.Long == "" {
		o.Long = o.Name + ` is the headless Black Box Systems host agent. It registers a
workload with the central admin service, stores the credentials and
configuration it receives, and keeps the host's network services in step
with that configuration while it runs.`
	}
	return o
}

// app holds the state shared by the root command and its subcommands. One
// app is created per NewCommand call so two commands never share flags.
type app struct {
	opts       Options
	c          *config.Config
	l          *logger.Logger
	configFile string
	asService  bool
	agent      *host.Agent
	// connect opens the NATS connection for the api and services commands.
	connect connectFunc
}

// NewCommand returns the root cobra command with the `register`, `run`,
// `api` and `services` subcommands attached. Callers that embed the agent in a larger CLI add the
// returned command to their own root.
func NewCommand(opts Options) *cobra.Command {
	root, _ := newCommand(opts)
	return root
}

// newCommand builds the root command and returns the app behind it, so
// tests can swap the NATS connector before running a command.
func newCommand(opts Options) (*cobra.Command, *app) {
	a := &app{opts: opts.withDefaults(), connect: connect}
	a.l = logger.NewLogger(a.opts.Name, "rootCmd")

	root := &cobra.Command{
		Use:               a.opts.Name,
		Short:             a.opts.Short,
		Long:              a.opts.Long,
		SilenceUsage:      true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return a.initConfig() },
	}
	root.PersistentFlags().StringVar(&a.configFile, "config", "", "config file with full path")
	root.PersistentFlags().BoolVar(&a.asService, "as-service", false, "run as a service (use the system-wide config directory)")

	root.AddCommand(a.newRegisterCmd(), a.newRunCmd(), a.newAPICmd(), a.newServicesCmd())
	return root, a
}

// Execute builds the command and runs it against os.Args.
func Execute(opts Options) error {
	return NewCommand(opts).Execute()
}

// initConfig resolves the agent config path from --config, --as-service and
// the platform config defaults, then loads whatever is already there. A
// missing file is not an error: `register` is what writes it.
func (a *app) initConfig() error {
	kind := "user"
	if a.asService {
		kind = "service"
	}
	a.c = config.New(kind)
	if a.configFile == "" {
		a.configFile = a.c.DefaultAgentConfigFullPath()
	}
	// Pin viper to the exact file so the watcher and host.AgentConfigFromFile
	// read the same path. The agent config is JSON (the admin server's
	// registration response written verbatim), whatever the extension.
	a.c.V.SetConfigFile(a.configFile)
	a.c.V.SetConfigType("json")
	a.c.Filename = a.configFile
	if err := a.c.FromFile(); err != nil {
		a.l.DebugE().Err(err).Str("configFile", a.configFile).Msg("agent configuration not loaded")
	}
	a.l.InfoE().Str("configFile", a.c.V.ConfigFileUsed()).Msg("using config file")
	return nil
}
