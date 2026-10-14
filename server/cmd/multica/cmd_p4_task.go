package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/p4depot"
	"github.com/spf13/cobra"
)

var (
	p4SwarmURL        string
	p4ReviewID        string
	p4CommentBody     string
	p4Dest            string
	p4RevertUnchanged bool
	p4Force           bool
	p4NoShelve        bool
	p4Reviewers       []string
)

var p4EditCmd = &cobra.Command{
	Use:     "edit [files...]",
	Aliases: []string{"checkout"},
	Short:   "Open files for edit in the task Perforce client",
	Long:    "Runs `p4 edit` (checkout) through the daemon in the person's existing Helix workspace. Multica does not create a task client or sync a depot. Exclusive-lock files require this before they can be modified.",
	Args:    cobra.MinimumNArgs(1),
	RunE:    runP4Mutate("edit"),
}

var p4AddFilesCmd = &cobra.Command{
	Use:   "add-files [files...]",
	Short: "Mark new files for add in the task Perforce client",
	Long:  "Runs `p4 add` through the daemon. `multica p4 add` still adds a depot to the workspace registry.",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runP4Mutate("add"),
}

var p4DeleteCmd = &cobra.Command{
	Use:   "delete [files...]",
	Short: "Mark files for delete in the task Perforce client",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runP4Mutate("delete"),
}

var p4RevertCmd = &cobra.Command{
	Use:   "revert [files...]",
	Short: "Revert opened files in the task Perforce client",
	Args:  cobra.ArbitraryArgs,
	RunE:  runP4Mutate("revert"),
}

var p4OpenedCmd = &cobra.Command{
	Use:   "opened",
	Short: "List files opened in the task Perforce client",
	Args:  cobra.NoArgs,
	RunE:  runP4Mutate("opened"),
}

var p4ReconcileCmd = &cobra.Command{
	Use:     "reconcile [files...]",
	Aliases: []string{"rec"},
	Short:   "Reconcile local adds, edits, and deletes into the task client",
	Args:    cobra.ArbitraryArgs,
	RunE:    runP4Mutate("reconcile"),
}

var p4MoveCmd = &cobra.Command{
	Use:   "move [source]",
	Short: "Move or rename an opened file in the task Perforce client",
	Args:  cobra.ExactArgs(1),
	RunE:  runP4Mutate("move"),
}

var p4ReopenCmd = &cobra.Command{
	Use:   "reopen [files...]",
	Short: "Move opened files to a numbered changelist",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runP4Mutate("reopen"),
}

var p4ChangeCmd = &cobra.Command{
	Use:   "change",
	Short: "Create a pending numbered changelist in the task client",
	Args:  cobra.NoArgs,
	RunE:  runP4Mutate("change"),
}

var p4SubmitCmd = &cobra.Command{
	Use:   "submit",
	Short: "Submit the task changelist when the depot allows it",
	Long:  "Submits this task's pending changelist through the daemon when that depot's allow_submit switch is on in Settings → Code (default off).",
	Args:  cobra.NoArgs,
	RunE:  runP4Mutate("submit"),
}

var p4ShelveCmd = &cobra.Command{
	Use:   "shelve",
	Short: "Shelve a pending changelist so Swarm can review it",
	Args:  cobra.ArbitraryArgs,
	RunE:  runP4Mutate("shelve"),
}

var p4UnshelveCmd = &cobra.Command{
	Use:   "unshelve",
	Short: "Unshelve a changelist into the task client",
	Args:  cobra.NoArgs,
	RunE:  runP4Mutate("unshelve"),
}

var p4DescribeCmd = &cobra.Command{
	Use:   "describe",
	Short: "Describe a changelist",
	Args:  cobra.NoArgs,
	RunE:  runP4Mutate("describe"),
}

var p4StatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the Perforce workspace and changelist this task is bound to",
	Long:  "Reports the Helix client, workspace root, 易协作 ticket, task changelist, its files, and any warnings. Only available while the task runs in a human-owned workspace.",
	Args:  cobra.NoArgs,
	RunE:  runP4Status,
}

var p4SwarmCmd = &cobra.Command{
	Use:   "swarm",
	Short: "Work with Helix Swarm reviews",
}

var p4SwarmReviewsCmd = &cobra.Command{
	Use:   "reviews",
	Short: "List Helix Swarm reviews",
	Args:  cobra.NoArgs,
	RunE:  runP4Swarm("reviews"),
}

var p4SwarmCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a Helix Swarm review from a changelist",
	Long:  "Shelves the pending changelist (unless --no-shelve) and POSTs it to Helix Swarm using the host Helix ticket.",
	Args:  cobra.NoArgs,
	RunE:  runP4Swarm("create"),
}

var p4SwarmShowCmd = &cobra.Command{
	Use:   "show [review-id]",
	Short: "Show one Helix Swarm review",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runP4Swarm("show"),
}

var p4SwarmCommentCmd = &cobra.Command{
	Use:   "comment [review-id]",
	Short: "Comment on a Helix Swarm review",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runP4Swarm("comment"),
}

var p4SwarmLinkCmd = &cobra.Command{
	Use:   "link [review-id]",
	Short: "Attach a Helix Swarm review to the current issue",
	Long:  "Fetches the review and links it to MULTICA_ISSUE_ID (the issue this task is running on). Put the issue identifier in the review description to also auto-link.",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runP4Swarm("link"),
}

func addP4TaskFlags(cmd *cobra.Command, files bool) {
	cmd.Flags().StringVar(&p4Port, "port", "", "P4PORT (host:port or ssl:host:port)")
	cmd.Flags().StringVar(&p4DepotPath, "depot", "", "Depot path (//depot/project/...)")
	cmd.Flags().StringVar(&p4Stream, "stream", "", "Optional stream path")
	cmd.Flags().StringVar(&p4User, "user", "", "Optional P4USER override")
	cmd.Flags().StringVar(&p4Charset, "charset", "", "Optional P4CHARSET")
	cmd.Flags().StringVar(&p4Changelist, "changelist", "", "Pending changelist number")
	cmd.Flags().String("output", "json", "Output format: json or text")
	if files {
		cmd.Flags().StringVar(&p4Desc, "description", "", "Changelist description")
	}
}

// p4DaemonTimeout mirrors the daemon's own per-command Perforce budget plus a
// minute, so the CLI never gives up while the daemon is still waiting on p4.
func p4DaemonTimeout() time.Duration {
	timeout := 10 * time.Minute
	if value := strings.TrimSpace(os.Getenv("MULTICA_P4_TIMEOUT")); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			timeout = parsed
		}
	}
	return timeout + time.Minute
}

// p4DaemonRejection is a structured refusal from the daemon's Perforce routes:
// an invariant the task may not cross, not a transport failure.
type p4DaemonRejection struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Files   []string `json:"files,omitempty"`
	Holders []string `json:"holders,omitempty"`
}

func (e *p4DaemonRejection) Error() string {
	return e.Message
}

func p4DaemonPost(parent context.Context, path string, body map[string]any, timeout time.Duration) ([]byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	return p4DaemonCall(parent, http.MethodPost, path, data, timeout)
}

func p4DaemonCall(parent context.Context, method, path string, body []byte, timeout time.Duration) ([]byte, error) {
	daemonPort := os.Getenv("MULTICA_DAEMON_PORT")
	if daemonPort == "" {
		return nil, fmt.Errorf("MULTICA_DAEMON_PORT not set (this command is intended to be run by an agent inside a daemon task)")
	}
	taskToken := os.Getenv("MULTICA_TOKEN")
	if taskToken == "" {
		return nil, fmt.Errorf("MULTICA_TOKEN not set (p4 commands require the active task credential)")
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://127.0.0.1:%s%s", daemonPort, path), reader)
	if err != nil {
		return nil, fmt.Errorf("create daemon request: %w", err)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+taskToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read daemon response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var rejection p4DaemonRejection
		if json.Unmarshal(payload, &rejection) == nil && rejection.Code != "" {
			return nil, &rejection
		}
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(payload)))
	}
	return payload, nil
}

// reportP4Failure renders a daemon rejection the way the agent needs to read
// it — the reason first, then every file or holder on its own line — and
// returns a short error so the exit status still signals failure.
func reportP4Failure(op string, err error) error {
	var rejection *p4DaemonRejection
	if !errors.As(err, &rejection) {
		return fmt.Errorf("%s failed: %w", op, err)
	}
	fmt.Fprintln(os.Stderr, rejection.Message)
	for _, file := range rejection.Files {
		fmt.Fprintln(os.Stderr, file)
	}
	for _, holder := range rejection.Holders {
		fmt.Fprintln(os.Stderr, holder)
	}
	return fmt.Errorf("%s was rejected (%s)", op, rejection.Code)
}

func p4TaskEnv(ref p4depot.Ref) (map[string]any, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	return map[string]any{
		"port":         ref.Port,
		"depot":        ref.Depot,
		"stream":       ref.Stream,
		"user":         ref.User,
		"charset":      ref.Charset,
		"swarm_url":    firstNonEmpty(p4SwarmURL, ref.SwarmURL),
		"workspace_id": os.Getenv("MULTICA_WORKSPACE_ID"),
		"workdir":      workDir,
		"cwd":          workDir,
		"task_id":      os.Getenv("MULTICA_TASK_ID"),
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func printP4JSONOrText(cmd *cobra.Command, payload []byte, text string) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		var parsed any
		if err := json.Unmarshal(payload, &parsed); err != nil {
			return cli.PrintJSON(os.Stdout, map[string]any{"output": string(payload)})
		}
		return cli.PrintJSON(os.Stdout, parsed)
	}
	if strings.TrimSpace(text) == "" {
		text = strings.TrimSpace(string(payload))
	}
	fmt.Fprintln(os.Stdout, text)
	return nil
}

func runP4Mutate(action string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ref, err := optionalP4PortDepot()
		if err != nil {
			return err
		}
		body, err := p4TaskEnv(ref)
		if err != nil {
			return err
		}
		body["action"] = action
		if p4Changelist != "" {
			body["changelist"] = p4Changelist
		}
		if p4Desc != "" {
			body["description"] = p4Desc
		}
		if p4Dest != "" {
			body["dest"] = p4Dest
		}
		if p4RevertUnchanged {
			body["revert_unchanged"] = true
		}
		if p4Force {
			body["force"] = true
		}
		if len(args) > 0 {
			body["files"] = args
		}
		payload, err := p4DaemonPost(cmd.Context(), "/p4/run", body, p4DaemonTimeout())
		if err != nil {
			return reportP4Failure("p4 "+action, err)
		}
		var result struct {
			Output     string `json:"output"`
			Changelist string `json:"changelist"`
			Path       string `json:"path"`
			Client     string `json:"client"`
			Warnings   []struct {
				Kind  string   `json:"kind"`
				Files []string `json:"files"`
			} `json:"warnings"`
		}
		_ = json.Unmarshal(payload, &result)
		if action == "change" && result.Changelist != "" {
			fmt.Fprintf(os.Stderr, "Created changelist %s (client: %s)\n", result.Changelist, result.Client)
		}
		for _, warning := range result.Warnings {
			fmt.Fprintf(os.Stderr, "warning: %s: %s\n", warning.Kind, strings.Join(warning.Files, " "))
		}
		text := result.Output
		if text == "" && result.Changelist != "" {
			text = result.Changelist
		}
		return printP4JSONOrText(cmd, payload, text)
	}
}

func runP4Status(cmd *cobra.Command, _ []string) error {
	payload, err := p4DaemonCall(cmd.Context(), http.MethodGet, "/p4/status", nil, time.Minute)
	if err != nil {
		return reportP4Failure("p4 status", err)
	}
	var status struct {
		Client string `json:"client"`
		Root   string `json:"root"`
		Stream string `json:"stream"`
		Ticket string `json:"ticket"`
		Change string `json:"change"`
		Files  []struct {
			DepotFile string `json:"depot_file"`
			Action    string `json:"action"`
		} `json:"files"`
		Warnings []struct {
			Kind  string   `json:"kind"`
			Files []string `json:"files"`
		} `json:"warnings"`
	}
	_ = json.Unmarshal(payload, &status)
	var text strings.Builder
	fmt.Fprintf(&text, "client: %s\nroot: %s\n", status.Client, status.Root)
	if status.Stream != "" {
		fmt.Fprintf(&text, "stream: %s\n", status.Stream)
	}
	fmt.Fprintf(&text, "ticket: %s\nchangelist: %s\n", firstNonEmpty(status.Ticket, "(none)"), firstNonEmpty(status.Change, "(none)"))
	for _, file := range status.Files {
		fmt.Fprintf(&text, "%s\t%s\n", file.Action, file.DepotFile)
	}
	for _, warning := range status.Warnings {
		fmt.Fprintf(&text, "warning: %s: %s\n", warning.Kind, strings.Join(warning.Files, " "))
	}
	return printP4JSONOrText(cmd, payload, strings.TrimRight(text.String(), "\n"))
}

func runP4Swarm(action string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if action == "link" && strings.TrimSpace(os.Getenv("MULTICA_ISSUE_ID")) == "" {
			return fmt.Errorf("swarm link requires MULTICA_ISSUE_ID (run it inside an issue task)")
		}
		ref, err := optionalP4PortDepot()
		if err != nil {
			return err
		}
		body, err := p4TaskEnv(ref)
		if err != nil {
			return err
		}
		body["action"] = action
		if p4Changelist != "" {
			body["changelist"] = p4Changelist
		}
		if p4Desc != "" {
			body["description"] = p4Desc
		}
		reviewID := p4ReviewID
		if len(args) > 0 {
			reviewID = args[0]
		}
		if reviewID != "" {
			body["review_id"] = reviewID
		}
		if p4CommentBody != "" {
			body["body"] = p4CommentBody
		}
		if len(p4Reviewers) > 0 {
			body["reviewers"] = p4Reviewers
		}
		if action == "create" && !p4NoShelve {
			body["shelve"] = true
		}
		daemonAction := action
		if action == "link" {
			daemonAction = "show"
			body["action"] = "show"
		}
		// Creating a review shelves the changelist first, so this budget has to
		// cover a Perforce shelve plus the Swarm round trip, not just the HTTP call.
		payload, err := p4DaemonPost(cmd.Context(), "/p4/swarm", body, 3*time.Minute)
		if err != nil {
			return reportP4Failure("p4 swarm "+action, err)
		}
		// Preserve the remote review receipt even if linking fails. Retrying a
		// link must not require creating the Swarm review again.
		if err := printP4JSONOrText(cmd, payload, ""); err != nil {
			return err
		}
		if daemonAction == "create" || daemonAction == "show" {
			return reportSwarmReviewToIssue(cmd, payload)
		}
		return nil
	}
}

func reportSwarmReviewToIssue(cmd *cobra.Command, payload []byte) error {
	issueID := strings.TrimSpace(os.Getenv("MULTICA_ISSUE_ID"))
	if issueID == "" {
		return nil
	}
	var parsed struct {
		SwarmURL string `json:"swarm_url"`
		Review   *struct {
			ID          int    `json:"id"`
			Author      string `json:"author"`
			State       string `json:"state"`
			Description string `json:"description"`
			Changes     []int  `json:"changes"`
			URL         string `json:"url"`
		} `json:"review"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return fmt.Errorf("parse Swarm review for issue link: %w", err)
	}
	if parsed.Review == nil || parsed.Review.ID <= 0 {
		return fmt.Errorf("daemon response did not include a Swarm review to link")
	}
	linkError := func(err error) error {
		return fmt.Errorf("Swarm review %d (%s) is available, but linking it to %s failed: %w; retry with `multica p4 swarm link --review %d` and the same depot flags", parsed.Review.ID, parsed.Review.URL, issueID, err, parsed.Review.ID)
	}
	if !p4depot.ValidSwarmURL(parsed.SwarmURL) {
		return linkError(fmt.Errorf("daemon response is missing a valid swarm_url"))
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return linkError(err)
	}
	changelist := p4Changelist
	if changelist == "" && len(parsed.Review.Changes) > 0 {
		changelist = strconv.Itoa(parsed.Review.Changes[0])
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	var out map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+url.PathEscape(issueID)+"/swarm-reviews", map[string]any{
		"review_id":   parsed.Review.ID,
		"swarm_url":   parsed.SwarmURL,
		"changelist":  changelist,
		"description": parsed.Review.Description,
		"author":      parsed.Review.Author,
		"state":       parsed.Review.State,
		"html_url":    parsed.Review.URL,
	}, &out); err != nil {
		return linkError(err)
	}
	fmt.Fprintf(os.Stderr, "Linked Swarm review %d to %s\n", parsed.Review.ID, issueID)
	return nil
}
