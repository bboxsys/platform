package agent

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/bboxsys/platform/host"
	"github.com/spf13/cobra"
)

func (a *app) newRegisterCmd() *cobra.Command {
	var (
		accountID      string
		adminServerURL string
		authToken      string
	)
	cmd := &cobra.Command{
		Use:   "register",
		Short: "register this host with the admin service",
		Long:  `registers this host with the admin service and saves the agent configuration and credentials it returns`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.register(accountID, adminServerURL, authToken)
		},
	}
	cmd.Flags().StringVarP(&accountID, "account-id", "a", "", "account id to register the agent under")
	cmd.Flags().StringVarP(&adminServerURL, "admin-server-url", "u", "", "admin server url, e.g. https://admin.example.com:8900")
	cmd.Flags().StringVarP(&authToken, "token", "t", "", "bearer token authorizing the registration")
	return cmd
}

// register sends the host's name and ID to the admin server and writes the
// agent configuration it returns to the default agent config path.
func (a *app) register(accountID, adminServerURL, authToken string) error {
	hostname, err := host.GetHostname()
	if err != nil {
		return fmt.Errorf("error getting hostname: %w", err)
	}
	hostID, err := host.GetHostID()
	if err != nil {
		return fmt.Errorf("error getting host id: %w", err)
	}
	payload, err := json.Marshal(map[string]string{
		"account_id": accountID,
		"hostname":   hostname,
		"host_id":    hostID,
	})
	if err != nil {
		return fmt.Errorf("error marshalling payload: %w", err)
	}
	a.l.InfoE().Str("accountID", accountID).Str("hostname", hostname).Str("hostID", hostID).Msg("sending registration request")

	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/v1/agents/configuration", adminServerURL), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("error creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// The ID middleware reads the account from X-Blackbox-AccountID and the
	// Bearer check validates the token against that account.
	req.Header.Set("X-Blackbox-AccountID", accountID)
	req.Header.Set("Authorization", "Bearer "+authToken)

	// Admin servers on private networks run on the internal PKI, whose CA the
	// host does not trust until registration has delivered it.
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error sending request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading server response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("registration rejected: %s: %s", resp.Status, bytes.TrimSpace(body))
	}

	if err := a.c.ConfigDirExists(); err != nil {
		return fmt.Errorf("error with config directory: %w", err)
	}
	path := a.configFile
	if err := os.WriteFile(path, body, 0600); err != nil {
		return fmt.Errorf("error writing configuration to disk: %w", err)
	}
	a.l.InfoE().Str("path", path).Msg("agent configuration saved")
	return nil
}
