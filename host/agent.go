package host

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/messenger"
	"github.com/bboxsys/platform/pki"
	"github.com/bboxsys/platform/supervisor"
)

const (
	// LogService is the service name the agent publishes its log events
	// under (sys.log.agent); the Logs page filters on it.
	LogService = "agent"
	// LogPublishMaxPerSec caps how many log events per second each of the
	// agent's loggers publishes to NATS.
	LogPublishMaxPerSec = 100
)

var (
	agentLogger *logger.Logger
)

func init() {
	agentLogger = logger.NewLogger("blackbox-agent", "agent")
}

// AgentConfig represents the configuration for the host agent.
// This includes credentials for connecting to the controller and
// optional PF (physical function) configurations for SR-IOV networking.
type AgentConfig struct {
	AccountID string `json:"account_id"`
	HostID    string `json:"host_id"`

	// NATS credentials
	JWT  string `json:"jwt"`
	Seed string `json:"seed"`

	// TLS credentials
	Certificate pki.Certificate `json:"certificate"`
	PrivateKey  pki.PrivateKey  `json:"private_key"`
	CA          pki.Certificate `json:"ca"`

	// Controller connection
	ControllerURL string `json:"controller_url"`

	// PF configurations for SR-IOV
	PfConfigs []PfConfig `json:"pf_configs"`
}

// Agent represents the host agent runtime that manages
// network configurations and communicates with the controller.
type Agent struct {
	Config         *AgentConfig
	ConfigLocation string
	Supervisor     *supervisor.Supervisor
	// Messenger is the NATS connection to the controller. Start opens it
	// when the configuration carries a controller URL; it stays nil
	// otherwise. Callers attach their own loggers to it with
	// Logger.AttachPublisher(agent.Messenger, LogService, LogPublishMaxPerSec).
	Messenger *messenger.Client
}

// AgentConfigFromFile loads an AgentConfig from a JSON file.
func AgentConfigFromFile(filename string) (*AgentConfig, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config AgentConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return &config, nil
}

// NewAgent creates a new Agent instance with the given configuration.
func NewAgent(config *AgentConfig) (*Agent, error) {
	if config == nil {
		return nil, fmt.Errorf("agent config cannot be nil")
	}

	agent := &Agent{
		Config:     config,
		Supervisor: supervisor.NewSupervisor(),
	}

	return agent, nil
}

// SetConfigLocation sets the path to the configuration file.
func (a *Agent) SetConfigLocation(location string) {
	a.ConfigLocation = location
}

// ApplyPfConfigs applies the PF configurations for SR-IOV networking.
func (a *Agent) ApplyPfConfigs() {
	if a.Config == nil || len(a.Config.PfConfigs) == 0 {
		agentLogger.Info("no PF configs to apply")
		return
	}

	agentLogger.InfoE().Int("count", len(a.Config.PfConfigs)).Msg("applying PF configs")
	errs := ApplyPfConfigs(&a.Config.PfConfigs)
	if len(errs) > 0 {
		for _, err := range errs {
			agentLogger.ErrorE().Err(err).Msg("error applying PF config")
		}
	}
}

// Start begins the agent's main routines: it connects to the controller
// and starts publishing the agent's log events there.
func (a *Agent) Start() {
	agentLogger.InfoE().Str("hostID", a.Config.HostID).Msg("starting agent for host")
	a.connectController()
	a.Supervisor.Ready = true
}

// connectController opens the NATS connection with the credentials the
// admin server issued at registration and attaches a log publisher to the
// agent's loggers, so its events reach the admin server's log store under
// the agent's tenant account.
func (a *Agent) connectController() {
	if a.Config.ControllerURL == "" {
		agentLogger.Warn("no controller_url in agent configuration; logs stay local (re-register to refresh the configuration)")
		return
	}
	mc, err := messenger.NewClient(&messenger.ClientOptions{
		Application:   "blackbox-agent",
		Component:     LogService,
		ControllerURL: a.Config.ControllerURL,
		AccountID:     a.Config.AccountID,
		PrivateKey:    a.Config.PrivateKey,
		Certificate:   a.Config.Certificate,
		CA:            a.Config.CA,
		JWT:           a.Config.JWT,
		Seed:          a.Config.Seed,
	})
	if err != nil {
		agentLogger.ErrorE().Str("controllerURL", a.Config.ControllerURL).Err(err).Msg("controller: connect failed; logs stay local")
		return
	}
	a.Messenger = mc
	agentLogger.AttachPublisher(mc, LogService, LogPublishMaxPerSec)
	supervisor.Logger().AttachPublisher(mc, LogService, LogPublishMaxPerSec)
	agentLogger.InfoE().Str("controllerURL", a.Config.ControllerURL).Str("accountID", a.Config.AccountID).Msg("controller: connected; publishing agent logs")
}

// SignalAndWaitAllClosed signals all routines to close, waits for them, and
// then closes the controller connection.
func (a *Agent) SignalAndWaitAllClosed() {
	agentLogger.Info("signaling all agent routines to close")
	a.Supervisor.SignalAllClose()
	a.Supervisor.WaitAllClose()
	if a.Messenger != nil {
		a.Messenger.Close()
		a.Messenger = nil
	}
}
