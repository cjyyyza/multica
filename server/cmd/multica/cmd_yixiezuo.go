package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
)

var yixiezuoCmd = &cobra.Command{
	Use:   "yixiezuo",
	Short: "Sync 易协作 with the Multica kanban from this machine",
	Long: `易协作 and popo-cli are only reachable on this Windows computer.
These commands call local popo-cli pmmcp and the Multica API.
The server never opens 易协作 or popo-cli.

Keep this running for live bidirectional updates:

  multica yixiezuo sync --watch

A saved 易协作 query_id is required. Unscoped full-board pulls are refused.
`,
}

var yixiezuoStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the workspace 易协作 connection",
	RunE:  runYixiezuoStatus,
}

var yixiezuoSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Pull 易协作 cards, then push Multica kanban changes",
	RunE:  runYixiezuoSync,
}

var yixiezuoPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Copy 易协作 cards onto the Multica kanban",
	RunE:  runYixiezuoPull,
}

var yixiezuoPushCmd = &cobra.Command{
	Use:   "push",
	Short: "Copy Multica kanban changes to 易协作",
	RunE:  runYixiezuoPush,
}

func init() {
	yixiezuoSyncCmd.Flags().Bool("watch", false, "Repeat sync until interrupted")
	yixiezuoSyncCmd.Flags().Duration("interval", 30*time.Second, "Watch interval")
	yixiezuoCmd.AddCommand(yixiezuoStatusCmd, yixiezuoSyncCmd, yixiezuoPullCmd, yixiezuoPushCmd)
}

type yixiezuoConnectionJSON struct {
	ID                string            `json:"id"`
	CLIBin            string            `json:"cli_bin"`
	GCPHost           string            `json:"gcp_host"`
	ListQueryID       string            `json:"list_query_id"`
	ExternalProjectID string            `json:"external_project_id"`
	TrackerID         string            `json:"tracker_id"`
	StatusMap         map[string]string `json:"status_map"`
}

type yixiezuoConnectionEnvelopeJSON struct {
	Connection *yixiezuoConnectionJSON `json:"connection"`
}

type yixiezuoPullResultJSON struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	Skipped int `json:"skipped"`
}

type yixiezuoExportIssueJSON struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	Priority    string  `json:"priority"`
	StartDate   *string `json:"start_date"`
	DueDate     *string `json:"due_date"`
	UpdatedAt   string  `json:"updated_at"`
	Revision    int64   `json:"revision"`
}

type yixiezuoExportItemJSON struct {
	Issue           yixiezuoExportIssueJSON `json:"issue"`
	ExternalIssueID string                  `json:"external_issue_id"`
	StatusName      string                  `json:"status_name"`
}

type yixiezuoExportJSON struct {
	Connection yixiezuoConnectionJSON   `json:"connection"`
	Updates    []yixiezuoExportItemJSON `json:"updates"`
	Creates    []yixiezuoExportItemJSON `json:"creates"`
}

func runYixiezuoStatus(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	env, err := loadYixiezuoConnection(ctx, client)
	if err != nil {
		return err
	}
	if env.Connection == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "易协作 is not connected. Save a connection in Settings → Integrations.")
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "cli_bin\t%s\n", env.Connection.CLIBin)
	if env.Connection.GCPHost != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "gcp_host\t%s\n", env.Connection.GCPHost)
	}
	if env.Connection.ExternalProjectID != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "external_project_id\t%s\n", env.Connection.ExternalProjectID)
	}
	if env.Connection.ListQueryID != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "list_query_id\t%s\n", env.Connection.ListQueryID)
	}
	if env.Connection.TrackerID != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "tracker_id\t%s\n", env.Connection.TrackerID)
	}
	return nil
}

func runYixiezuoSync(cmd *cobra.Command, _ []string) error {
	watch, _ := cmd.Flags().GetBool("watch")
	interval, _ := cmd.Flags().GetDuration("interval")
	for {
		if err := runYixiezuoPull(cmd, nil); err != nil {
			return err
		}
		if err := runYixiezuoPush(cmd, nil); err != nil {
			return err
		}
		if !watch {
			return nil
		}
		fmt.Fprintf(cmd.OutOrStdout(), "watching; next sync in %s\n", interval)
		timer := time.NewTimer(interval)
		select {
		case <-cmd.Context().Done():
			timer.Stop()
			return cmd.Context().Err()
		case <-timer.C:
		}
	}
}

func runYixiezuoPull(cmd *cobra.Command, _ []string) error {
	client, driver, err := yixiezuoRuntime(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	cards, err := driver.ListCards(ctx)
	if err != nil {
		return err
	}
	payload := map[string]any{"cards": cardsToPayload(cards)}
	var result yixiezuoPullResultJSON
	if err := client.PostJSON(ctx, yixiezuoAPIPath(client, "/pull"), payload, &result); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "pulled created=%d updated=%d skipped=%d\n", result.Created, result.Updated, result.Skipped)
	return nil
}

func runYixiezuoPush(cmd *cobra.Command, _ []string) error {
	client, driver, err := yixiezuoRuntime(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var exp yixiezuoExportJSON
	if err := client.GetJSON(ctx, yixiezuoAPIPath(client, "/export"), &exp); err != nil {
		return err
	}
	acks := make([]map[string]string, 0, len(exp.Updates)+len(exp.Creates))
	for _, item := range exp.Updates {
		card, updateErr := driver.UpdateCard(ctx, item.ExternalIssueID, exportToFields(item.Issue), item.StatusName)
		acks = append(acks, pushAck(item.Issue.ID, item.ExternalIssueID, card, updateErr))
	}
	for _, item := range exp.Creates {
		card, createErr := driver.CreateCard(ctx, exportToFields(item.Issue), item.StatusName)
		acks = append(acks, pushAck(item.Issue.ID, card.ExternalID, card, createErr))
	}
	if err := client.PostJSON(ctx, yixiezuoAPIPath(client, "/push-ack"), map[string]any{"results": acks}, nil); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "pushed updates=%d creates=%d\n", len(exp.Updates), len(exp.Creates))
	return nil
}

func yixiezuoRuntime(cmd *cobra.Command) (*cli.APIClient, yixiezuo.Driver, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	env, err := loadYixiezuoConnection(ctx, client)
	if err != nil {
		return nil, nil, err
	}
	if env.Connection == nil {
		return nil, nil, fmt.Errorf("易协作 is not connected; save a connection in Settings → Integrations first")
	}
	bin := strings.TrimSpace(env.Connection.CLIBin)
	if override := strings.TrimSpace(os.Getenv("MULTICA_YIXIEZUO_CLI")); override != "" {
		bin = override
	}
	host := strings.TrimSpace(env.Connection.GCPHost)
	if override := strings.TrimSpace(os.Getenv("MULTICA_YIXIEZUO_GCP_HOST")); override != "" {
		host = override
	}
	queryID := strings.TrimSpace(env.Connection.ListQueryID)
	if override := strings.TrimSpace(os.Getenv("MULTICA_YIXIEZUO_QUERY_ID")); override != "" {
		queryID = override
	}
	if queryID == "" {
		return nil, nil, fmt.Errorf("list_query_id is required; save a 易协作 filter id before sync. Unscoped kanban pulls are refused")
	}
	return client, yixiezuo.NewExecDriver(yixiezuo.ExecOptions{
		Bin:               bin,
		GCPHost:           host,
		ExternalProjectID: env.Connection.ExternalProjectID,
		ListQueryID:       queryID,
		TrackerID:         env.Connection.TrackerID,
	}), nil
}

func loadYixiezuoConnection(ctx context.Context, client *cli.APIClient) (yixiezuoConnectionEnvelopeJSON, error) {
	var env yixiezuoConnectionEnvelopeJSON
	if err := client.GetJSON(ctx, yixiezuoAPIPath(client, ""), &env); err != nil {
		return env, err
	}
	return env, nil
}

func yixiezuoAPIPath(client *cli.APIClient, suffix string) string {
	ws := strings.TrimSpace(client.WorkspaceID)
	return "/api/workspaces/" + ws + "/yixiezuo" + suffix
}

func cardsToPayload(cards []yixiezuo.Card) []map[string]any {
	out := make([]map[string]any, 0, len(cards))
	for _, card := range cards {
		updated := ""
		if !card.UpdatedAt.IsZero() {
			updated = card.UpdatedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, map[string]any{
			"external_id": card.ExternalID,
			"title":       card.Title,
			"description": card.Description,
			"status_name": card.StatusName,
			"priority":    card.Priority,
			"start_date":  card.StartDate,
			"due_date":    card.DueDate,
			"updated_at":  updated,
		})
	}
	return out
}

func exportToFields(issue yixiezuoExportIssueJSON) yixiezuo.IssueFields {
	start, due := "", ""
	if issue.StartDate != nil {
		start = *issue.StartDate
	}
	if issue.DueDate != nil {
		due = *issue.DueDate
	}
	updated, _ := time.Parse(time.RFC3339, issue.UpdatedAt)
	return yixiezuo.IssueFields{
		Title:       issue.Title,
		Description: issue.Description,
		Status:      issue.Status,
		Priority:    issue.Priority,
		StartDate:   start,
		DueDate:     due,
		UpdatedAt:   updated,
		Revision:    issue.Revision,
	}
}

func pushAck(issueID, externalID string, card yixiezuo.Card, err error) map[string]string {
	updated := ""
	if !card.UpdatedAt.IsZero() {
		updated = card.UpdatedAt.UTC().Format(time.RFC3339)
	}
	ack := map[string]string{
		"issue_id":            issueID,
		"external_issue_id":   externalID,
		"external_updated_at": updated,
	}
	if err != nil {
		ack["error"] = err.Error()
	}
	if ack["external_issue_id"] == "" {
		ack["external_issue_id"] = card.ExternalID
	}
	return ack
}
