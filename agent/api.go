package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bboxsys/platform/host"
	"github.com/bboxsys/platform/messenger"
	"github.com/bboxsys/platform/natsapi"
	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
)

// errExit is returned by commands whose outcome is already on stderr; the
// caller exits non-zero without printing it again.
var errExit = errors.New("exit status 1")

// connectFunc opens the NATS connection the api and services commands use.
// Tests replace it to reach an embedded server without TLS.
type connectFunc func(cfg *host.AgentConfig) (*nats.Conn, error)

// connect dials the controller with the agent's mTLS and JWT credentials.
func connect(cfg *host.AgentConfig) (*nats.Conn, error) {
	if cfg.ControllerURL == "" {
		return nil, errors.New("agent configuration has no controller_url; run register first")
	}
	mc, err := messenger.NewClient(&messenger.ClientOptions{
		Application:   "bboxsys-agent",
		Component:     "api",
		ControllerURL: cfg.ControllerURL,
		AccountID:     cfg.AccountID,
		PrivateKey:    cfg.PrivateKey,
		Certificate:   cfg.Certificate,
		CA:            cfg.CA,
		JWT:           cfg.JWT,
		Seed:          cfg.Seed,
	})
	if err != nil {
		return nil, err
	}
	return mc.Client.Conn, nil
}

// loadAgentConfig reads the agent configuration file the run command uses.
func (a *app) loadAgentConfig() (*host.AgentConfig, error) {
	cfg, err := host.AgentConfigFromFile(a.configFile)
	if err != nil {
		return nil, fmt.Errorf("error reading agent configuration %s: %w", a.configFile, err)
	}
	return cfg, nil
}

func (a *app) newAPICmd() *cobra.Command {
	var (
		data    string
		service string
		headers []string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "api METHOD PATH",
		Short: "call a service's HTTP API over NATS",
		Long: `calls a service's HTTP API over the NATS controller using the agent's own
credentials. METHOD is an HTTP verb and PATH the API path under /api/, for
example:

  agent api GET /api/v1/users
  agent api POST /api/v1/users --data '{"email":"a@example.com"}'
  agent api PUT /api/v1/users/42 --data @user.json
  agent api DELETE /api/v1/users/42 --service admin_service

The response body is written to stdout and the status line to stderr. The
command exits 1 when the status is 400 or higher or the call fails.`,
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.api(cmd, args[0], args[1], data, service, headers, timeout)
		},
	}
	cmd.Flags().StringVarP(&data, "data", "d", "", "request body: JSON, @file, or - for stdin")
	cmd.Flags().StringVarP(&service, "service", "s", "admin_service", "service to call")
	cmd.Flags().StringArrayVarP(&headers, "header", "H", nil, "extra request header 'Key: value' (repeatable)")
	cmd.Flags().DurationVar(&timeout, "timeout", natsapi.DefaultTimeout, "wait for each response message")
	return cmd
}

// api sends one request over NATS and streams the response to the command's
// output streams.
func (a *app) api(cmd *cobra.Command, method, path, data, service string, headers []string, timeout time.Duration) error {
	method = strings.ToUpper(method)
	if !strings.HasPrefix(path, "/api/") {
		return errors.New("PATH must start with /api/")
	}
	body, err := readData(data, cmd.InOrStdin())
	if err != nil {
		return err
	}
	cfg, err := a.loadAgentConfig()
	if err != nil {
		return err
	}
	nc, err := a.connect(cfg)
	if err != nil {
		return fmt.Errorf("error connecting to the controller: %w", err)
	}
	defer nc.Close()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := natsapi.NewRequest(context.Background(), method, service, path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.JWT)
	req.Header.Set(natsapi.HeaderAccountID, cfg.AccountID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, h := range headers {
		key, value, ok := strings.Cut(h, ":")
		if !ok || strings.TrimSpace(key) == "" {
			return fmt.Errorf("invalid header %q, want 'Key: value'", h)
		}
		req.Header.Set(strings.TrimSpace(key), strings.TrimSpace(value))
	}

	client := &http.Client{Transport: &natsapi.Transport{Conn: nc, Timeout: timeout}}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, natsapi.ErrNoService) {
			return fmt.Errorf("no %s instance answered in this account", service)
		}
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	fmt.Fprintln(cmd.ErrOrStderr(), resp.Status)
	out := cmd.OutOrStdout()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("error reading response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return errExit
	}
	return nil
}

// readData resolves the --data flag: inline JSON, @file, or - for stdin. A
// nil result means no body.
func readData(data string, stdin io.Reader) ([]byte, error) {
	switch {
	case data == "":
		return nil, nil
	case data == "-":
		b, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("error reading body from stdin: %w", err)
		}
		return b, nil
	case strings.HasPrefix(data, "@"):
		b, err := os.ReadFile(data[1:])
		if err != nil {
			return nil, fmt.Errorf("error reading body file: %w", err)
		}
		return b, nil
	default:
		return []byte(data), nil
	}
}
