package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
)

// serviceInfo is the subset of the nats micro INFO document the command
// prints. The raw document is kept for --json.
type serviceInfo struct {
	Name        string `json:"name"`
	ID          string `json:"id"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Endpoints   []struct {
		Name    string `json:"name"`
		Subject string `json:"subject"`
	} `json:"endpoints"`
	raw json.RawMessage
}

func (a *app) newServicesCmd() *cobra.Command {
	var (
		asJSON bool
		wait   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "services",
		Short: "list the services reachable over NATS in this account",
		Long: `discovers the services answering the nats micro protocol in the agent's
account and prints each one's name, version, description and endpoint
subjects. --json prints the raw INFO documents instead.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.services(cmd, asJSON, wait)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the raw service info documents")
	cmd.Flags().DurationVar(&wait, "wait", 500*time.Millisecond, "how long to collect replies")
	return cmd
}

func (a *app) services(cmd *cobra.Command, asJSON bool, wait time.Duration) error {
	cfg, err := a.loadAgentConfig()
	if err != nil {
		return err
	}
	nc, err := a.connect(cfg)
	if err != nil {
		return fmt.Errorf("error connecting to the controller: %w", err)
	}
	defer nc.Close()

	infos, err := discoverServices(nc, wait)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if asJSON {
		docs := make([]json.RawMessage, 0, len(infos))
		for _, info := range infos {
			docs = append(docs, info.raw)
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(docs)
	}
	if len(infos) == 0 {
		fmt.Fprintln(out, "no services answered")
		return nil
	}
	for _, info := range infos {
		subjects := make([]string, 0, len(info.Endpoints))
		for _, e := range info.Endpoints {
			subjects = append(subjects, e.Subject)
		}
		sort.Strings(subjects)
		line := fmt.Sprintf("%s %s", info.Name, info.Version)
		if info.Description != "" {
			line += " " + info.Description
		}
		if len(subjects) > 0 {
			line += " " + strings.Join(subjects, " ")
		}
		fmt.Fprintln(out, line)
	}
	return nil
}

// discoverServices asks every micro service in the account for its INFO
// document and collects the replies for the wait window. Several instances
// of one service answer separately; they are collapsed by name and sorted.
func discoverServices(nc *nats.Conn, wait time.Duration) ([]serviceInfo, error) {
	inbox := nats.NewInbox()
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		return nil, fmt.Errorf("error subscribing for replies: %w", err)
	}
	defer sub.Unsubscribe()
	if err := nc.PublishRequest("$SRV.INFO", inbox, nil); err != nil {
		return nil, fmt.Errorf("error publishing discovery request: %w", err)
	}
	byName := map[string]serviceInfo{}
	deadline := time.Now().Add(wait)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		msg, err := sub.NextMsg(remaining)
		if err != nil {
			break
		}
		var info serviceInfo
		if err := json.Unmarshal(msg.Data, &info); err != nil || info.Name == "" {
			continue
		}
		info.raw = append(json.RawMessage(nil), msg.Data...)
		if _, seen := byName[info.Name]; !seen {
			byName[info.Name] = info
		}
	}
	infos := make([]serviceInfo, 0, len(byName))
	for _, info := range byName {
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos, nil
}
