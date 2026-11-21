package user

import (
	"github.com/bboxsys/platform/pki"
	"github.com/nats-io/nkeys"
)

type AgentConfiguration struct {
	AccountID string `json:"account_id"`
	HostID    string `json:"host_id"`

	// NATS
	JWT  string `json:"jwt"`
	Seed string `json:"seed"`

	// TLS
	Certificate pki.Certificate `json:"certificate"`
	PrivateKey  pki.PrivateKey  `json:"private_key"`
	CA          pki.Certificate `json:"ca"`

	ControllerURL string `json:"controller_url"`
}

type AgentConfigurationOptions struct {
	AccountID string `json:"account_id"`
	HostID    string `json:"host_id"`
	Hostname  string `json:"hostname"`
	// Roles grants the agent application roles so its JWT passes the same
	// role gates a user with those roles would (none by default).
	Roles []string `json:"roles,omitempty"`
	// Permissions restricts the NATS subjects the agent may use (nil
	// means unrestricted).
	Permissions *NATSPermissions `json:"permissions,omitempty"`
}

type Agent struct {
	UserBase
	HostID   string `json:"host_id"`
	Hostname string `json:"hostname"`
}

func NewAgent(agentConfig *UserConfig, signingKeyPair nkeys.KeyPair) (*Agent, error) {
	agent := Agent{
		UserBase: UserBase{
			AccountID:   agentConfig.AccountID,
			Roles:       agentConfig.Roles,
			Permissions: agentConfig.Permissions,
		},
		HostID:   agentConfig.HostID,
		Hostname: agentConfig.Hostname,
	}
	err := agent.New(signingKeyPair)
	if err != nil {
		return &agent, err
	}
	return &agent, nil
}
