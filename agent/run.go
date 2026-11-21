package agent

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bboxsys/platform/host"
	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"
)

func (a *app) newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "run the agent in daemon mode",
		Long:  `runs the agent in daemon mode, restarting it whenever its configuration file changes`,
		Run: func(cmd *cobra.Command, args []string) {
			a.run()
		},
	}
}

// run loads the agent configuration, starts the agent and blocks until the
// process is signalled. A change to the configuration file closes the running
// agent and the loop starts a new one from the updated file.
func (a *app) run() {
	restart := a.watchConfig()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	for {
		agentConfiguration, err := host.AgentConfigFromFile(a.configFile)
		if err != nil {
			a.l.Fatal(fmt.Sprintf("error creating new agent configuration from file: %s error: %v", a.configFile, err))
			return
		}
		a.l.InfoE().Str("configFile", a.configFile).Str("hostID", agentConfiguration.HostID).Msg("creating new agent configuration from file")
		a.agent, err = host.NewAgent(agentConfiguration)
		if err != nil {
			a.l.Fatal(fmt.Sprintf("error creating new agent: %v", err))
			return
		}
		a.agent.SetConfigLocation(a.c.V.ConfigFileUsed())
		a.agent.ApplyPfConfigs()
		a.agent.Start()
		if mc := a.agent.Messenger; mc != nil {
			a.l.AttachPublisher(mc, host.LogService, host.LogPublishMaxPerSec)
		}

		select {
		case <-restart:
			a.l.Info("configuration changed, restarting agent")
			a.agent.SignalAndWaitAllClosed()
			a.l.Info("all agent routines closed")
		case sig := <-stop:
			a.l.InfoE().Str("signal", sig.String()).Msg("shutting down agent")
			a.agent.SignalAndWaitAllClosed()
			return
		}
	}
}

// watchConfig registers the file watcher once and returns a channel that
// receives a value whenever the configuration file changes. The channel is
// buffered so bursts of events while a restart is in progress collapse into
// one extra restart instead of blocking the watcher.
func (a *app) watchConfig() <-chan struct{} {
	restart := make(chan struct{}, 1)
	a.l.InfoE().Str("configFile", a.c.V.ConfigFileUsed()).Msg("watching configuration file for changes")
	a.c.V.OnConfigChange(func(e fsnotify.Event) {
		a.l.InfoE().Str("op", e.Op.String()).Msg("received a configuration change notification event")
		select {
		case restart <- struct{}{}:
		default:
		}
	})
	a.c.V.WatchConfig()
	return restart
}
