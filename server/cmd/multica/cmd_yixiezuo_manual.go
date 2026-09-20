package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/spf13/cobra"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var yixiezuoCmd = &cobra.Command{Use: "yixiezuo", Short: "Import a selected 易协作 issue and explicitly publish reviewed results"}

func init() {
	bridge := &cobra.Command{Use: "bridge", Short: "Process this member's explicit import and publication requests", RunE: runYixiezuoBridge}
	bridge.Flags().Bool("once", false, "Process one queued request and exit")
	bridge.Flags().String("cli", "popo-cli", "Local popo-cli executable")
	preview := &cobra.Command{Use: "preview <issue-url>", Short: "Read one source issue without creating a Multica issue", Args: cobra.ExactArgs(1), RunE: runYixiezuoPreview}
	preview.Flags().String("cli", "popo-cli", "Local popo-cli executable")
	importCmd := &cobra.Command{Use: "import <issue-url>", Short: "Preview one issue; --confirm imports it without assigning an agent", Args: cobra.ExactArgs(1), RunE: runYixiezuoImport}
	importCmd.Flags().String("cli", "popo-cli", "Local popo-cli executable")
	importCmd.Flags().String("project-id", "", "Target Multica project UUID")
	importCmd.Flags().Bool("confirm", false, "Import the displayed source material into Multica")
	yixiezuoCmd.AddCommand(bridge, preview, importCmd)
}

func localYixiezuoDriver(cmd *cobra.Command) *yixiezuo.ExecDriver {
	bin, _ := cmd.Flags().GetString("cli")
	if override := strings.TrimSpace(os.Getenv("MULTICA_YIXIEZUO_CLI")); override != "" {
		bin = override
	}
	return yixiezuo.NewExecDriver(yixiezuo.ExecOptions{Bin: bin})
}

func yixiezuoAPIPath(client *cli.APIClient, suffix string) string {
	return "/api/workspaces/" + strings.TrimSpace(client.WorkspaceID) + "/yixiezuo" + suffix
}

func runYixiezuoPreview(cmd *cobra.Command, args []string) error {
	source, err := yixiezuo.ParseSource(args[0])
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
	defer cancel()
	snapshot, err := localYixiezuoDriver(cmd).ReadSnapshot(ctx, source)
	if err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(snapshot)
}

func runYixiezuoImport(cmd *cobra.Command, args []string) error {
	confirmed, _ := cmd.Flags().GetBool("confirm")
	if !confirmed {
		return runYixiezuoPreview(cmd, args)
	}
	source, err := yixiezuo.ParseSource(args[0])
	if err != nil {
		return err
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	var queued yixiezuo.Operation
	ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
	defer cancel()
	if err = client.PostJSON(ctx, yixiezuoAPIPath(client, "/preview"), map[string]string{"url": source.URL}, &queued); err != nil {
		return err
	}
	// Claiming is shared with the UI bridge; only this member's requests are eligible.
	for {
		if _, err = processYixiezuoRequest(ctx, client, localYixiezuoDriver(cmd)); err != nil {
			return err
		}
		var status yixiezuo.Operation
		if err = client.GetJSON(ctx, yixiezuoAPIPath(client, "/operations/"+queued.ID), &status); err != nil {
			return err
		}
		if status.State == "succeeded" {
			break
		}
		if status.State != "pending" && status.State != "running" {
			return fmt.Errorf("source preview %s: %s", status.State, status.Error)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	project, _ := cmd.Flags().GetString("project-id")
	var result json.RawMessage
	if err = client.PostJSON(ctx, yixiezuoAPIPath(client, "/imports"), map[string]any{"operation_id": queued.ID, "project_id": project}, &result); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(result))
	return nil
}

func runYixiezuoBridge(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	once, _ := cmd.Flags().GetBool("once")
	fmt.Fprintln(cmd.OutOrStdout(), "易协作 bridge ready. Only explicit requests from this account/workspace are processed.")
	for {
		ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
		processed, runErr := processYixiezuoRequest(ctx, client, localYixiezuoDriver(cmd))
		cancel()
		if once {
			return runErr
		}
		if runErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "bridge: %v\n", runErr)
		}
		if processed && runErr == nil {
			continue
		}
		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func processYixiezuoRequest(ctx context.Context, client *cli.APIClient, driver *yixiezuo.ExecDriver) (bool, error) {
	var claim struct {
		Operation *yixiezuo.Operation `json:"operation"`
	}
	if err := client.PostJSON(ctx, yixiezuoAPIPath(client, "/bridge/claim"), map[string]any{}, &claim); err != nil {
		return false, err
	}
	if claim.Operation == nil {
		return false, nil
	}
	completion := driver.Execute(ctx, *claim.Operation)
	if completion.State == "succeeded" && claim.Operation.Kind == "preview" {
		if err := mirrorYixiezuoAttachments(ctx, client, driver, completion.Snapshot, yixiezuoDownloadClient()); err != nil {
			completion.State = "failed"
			completion.Error = err.Error()
			completion.Snapshot = nil
		}
	}
	// Retry only the same receipt. Never retry the external mutation implicitly.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		receiptCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		lastErr = client.PostJSON(receiptCtx, yixiezuoAPIPath(client, "/operations/"+claim.Operation.ID+"/complete"), completion, nil)
		cancel()
		if lastErr == nil {
			if completion.State != "succeeded" {
				return true, fmt.Errorf("operation %s %s: %s", claim.Operation.ID, completion.State, completion.Error)
			}
			return true, nil
		}
	}
	return true, fmt.Errorf("operation %s result was not acknowledged; check its status before retrying: %w", claim.Operation.ID, lastErr)
}

func yixiezuoDownloadClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || req.URL.Scheme != "https" || req.URL.User != nil {
			return fmt.Errorf("unsafe attachment redirect")
		}
		return nil
	}}
}

func mirrorYixiezuoAttachments(ctx context.Context, client *cli.APIClient, driver *yixiezuo.ExecDriver, snapshot *yixiezuo.Snapshot, httpClient *http.Client) error {
	const maxFile = 20 * 1024 * 1024
	const maxTotal = 80 * 1024 * 1024
	total := 0
	for i := range snapshot.Attachments {
		attachment := &snapshot.Attachments[i]
		download, err := driver.AttachmentDownloadURL(ctx, attachment.ID)
		if err != nil {
			return fmt.Errorf("attachment %s: %w", attachment.Name, err)
		}
		parsed, err := url.Parse(download)
		if err != nil || parsed.Scheme != "https" {
			return fmt.Errorf("invalid attachment download URL")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, download, nil)
		if err != nil {
			return err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("download attachment %s: %w", attachment.Name, err)
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return fmt.Errorf("download attachment %s: HTTP %d", attachment.Name, resp.StatusCode)
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxFile+1))
		resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		total += len(data)
		if len(data) > maxFile || total > maxTotal {
			return fmt.Errorf("attachments exceed the import limit (20 MB per file, 80 MB total)")
		}
		attachment.LocalID, attachment.LocalURL, err = client.UploadFileWithURL(ctx, data, attachment.Name)
		if err != nil {
			return fmt.Errorf("stage attachment %s: %w", attachment.Name, err)
		}
		if attachment.LocalID == "" {
			return fmt.Errorf("attachment %s was uploaded without a durable record", attachment.Name)
		}
	}
	return nil
}
