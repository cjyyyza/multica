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
	"regexp"
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
	// p4Format formats this task's opened files in the CLI. It is not forwarded
	// to p4. -c is the changelist alias, not a passthrough: opened -a and -x
	// stay rejected.
	p4Format      string
	p4FilelogMax  int
	p4FilelogLong bool
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
	Long:  "Runs `p4 add` through the daemon. `multica p4 add <files>` is the same command; `multica p4 add` with no files still registers a depot.",
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
	Short: "List files opened in this task's changelist",
	Long:  "Lists files opened in this task's changelist. -F/--format prints them with p4 -ztag field names (depotFile, clientFile, action, change, type, client). %change% is the number or default, not the words \"change N\". Other p4 flags are not accepted.",
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
	Use:   "move <source> [dest]",
	Short: "Move or rename an opened file in the task Perforce client",
	Long:  "Runs `p4 move`. The destination is the second argument or --dest.",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runP4Move,
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
	Use:   "describe [changelist]",
	Short: "Describe a changelist, including its description",
	Long:  "With no number, describes this task's changelist. A positional number or -c/--changelist reads that changelist when every file is in this task's authorized depots. The description is not returned if those files cannot be confirmed.",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runP4Describe,
}

var p4FilelogCmd = &cobra.Command{
	Use:   "filelog [files...]",
	Short: "Show file history and each revision's changelist description",
	Long: "Runs `p4 filelog` on individual files in this task's authorized depots. " +
		"Output includes each revision's changelist and description (-L, or the full text with --long). " +
		"It does not take -c; describe a number with `multica p4 describe -c <number>`.",
	Args: cobra.MinimumNArgs(1),
	RunE: runP4Filelog,
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

func rejectP4FormatFor(action string, cmd *cobra.Command) error {
	if strings.TrimSpace(p4Format) == "" {
		return nil
	}
	switch action {
	case "opened", "status":
		return nil
	default:
		path := action
		if cmd != nil && cmd.CommandPath() != "" {
			path = cmd.CommandPath()
		}
		return fmt.Errorf("%s does not support -F/--format; it only formats this task's opened files (`multica p4 opened -F \"%%depotFile%% %%change%%\"`)", path)
	}
}

func rejectP4FormatJSON(cmd *cobra.Command) error {
	if strings.TrimSpace(p4Format) == "" || cmd == nil || !cmd.Flags().Changed("output") {
		return nil
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return fmt.Errorf("-F/--format prints text; drop it and read files from --output json, or drop --output json")
	}
	return nil
}

func addP4DepotFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&p4Port, "port", "", "P4PORT. Omit in a bound workspace; the daemon fills it. Do not look it up")
	cmd.Flags().StringVar(&p4DepotPath, "depot", "", "Depot path. Omit in a bound workspace; the daemon fills it")
	cmd.Flags().StringVar(&p4Stream, "stream", "", "Optional stream path")
	cmd.Flags().StringVar(&p4User, "user", "", "Optional P4USER override")
	cmd.Flags().StringVar(&p4Charset, "charset", "", "Optional P4CHARSET")
}

func addP4TaskFlags(cmd *cobra.Command, files bool) {
	addP4DepotFlags(cmd)
	cmd.Flags().StringVarP(&p4Changelist, "changelist", "c", "", "Changelist number (`p4 -c`). A write must name this task's changelist; describe may read another")
	cmd.Flags().String("output", "json", "Output format: json or text")
	if files {
		cmd.Flags().StringVar(&p4Desc, "description", "", "Changelist description")
	}
}

func addP4FilelogFlags(cmd *cobra.Command) {
	addP4DepotFlags(cmd)
	cmd.Flags().String("output", "text", "Output format: text or json")
	cmd.Flags().IntVarP(&p4FilelogMax, "max", "m", 0, "Maximum revisions per file (default 20, at most 200)")
	cmd.Flags().BoolVarP(&p4FilelogLong, "long", "l", false, "Full changelist description (`p4 filelog -l`). Default is -L, truncated at 250 characters")
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
		if err := rejectP4FormatFor(action, cmd); err != nil {
			return err
		}
		if err := rejectP4FormatJSON(cmd); err != nil {
			return err
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
		if p4Dest != "" {
			body["dest"] = p4Dest
		}
		if p4RevertUnchanged {
			body["revert_unchanged"] = true
		}
		if p4Force {
			body["force"] = true
		}
		if action == "filelog" {
			if p4FilelogMax > 0 {
				body["max"] = p4FilelogMax
			}
			if p4FilelogLong {
				body["long"] = true
			}
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
		if strings.TrimSpace(p4Format) != "" {
			text, err := formatP4OpenedPayload(p4Format, payload, result.Client, result.Changelist, result.Output)
			if err != nil {
				return err
			}
			if text != "" {
				fmt.Fprintln(os.Stdout, text)
			}
			return nil
		}
		// Empty opened/describe text is a real result. Falling through would
		// print the JSON envelope and look like --output text was ignored.
		if (action == "opened" || action == "describe" || action == "filelog") && p4OutputText(cmd) {
			if result.Output != "" {
				fmt.Fprintln(os.Stdout, result.Output)
			}
			return nil
		}
		text := result.Output
		if text == "" && result.Changelist != "" {
			text = result.Changelist
		}
		return printP4JSONOrText(cmd, payload, text)
	}
}

func runP4Describe(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		if p4Changelist != "" && p4Changelist != args[0] {
			return fmt.Errorf("changelist specified both as %s and -c %s", args[0], p4Changelist)
		}
		p4Changelist = args[0]
	}
	return runP4Mutate("describe")(cmd, nil)
}

func runP4Move(cmd *cobra.Command, args []string) error {
	if len(args) == 2 {
		if p4Dest != "" && p4Dest != args[1] {
			return fmt.Errorf("destination specified both as %s and --dest %s", args[1], p4Dest)
		}
		p4Dest = args[1]
	}
	if strings.TrimSpace(p4Dest) == "" {
		return fmt.Errorf("move requires a destination: `multica p4 move <source> <dest>` or --dest")
	}
	return runP4Mutate("move")(cmd, args[:1])
}

func runP4Filelog(cmd *cobra.Command, args []string) error {
	if strings.TrimSpace(p4Changelist) != "" {
		return fmt.Errorf("filelog does not take -c/--changelist; describe that number with `multica p4 describe -c %s`", p4Changelist)
	}
	if cmd.Flags().Changed("max") && (p4FilelogMax < 1 || p4FilelogMax > 200) {
		return fmt.Errorf("filelog --max must be between 1 and 200")
	}
	return runP4Mutate("filelog")(cmd, args)
}

func p4OutputText(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	output, _ := cmd.Flags().GetString("output")
	return output == "text"
}

// p4OpenedLine is one opened file addressed by p4 -ztag field names.
type p4OpenedLine struct {
	DepotFile  string
	ClientFile string
	Action     string
	Change     string
	Type       string
	Client     string
}

var p4FormatToken = regexp.MustCompile(`%(%)|%([A-Za-z][A-Za-z0-9]*)%`)

func formatP4OpenedPayload(format string, payload []byte, client, change, output string) (string, error) {
	var parsed struct {
		Client     string `json:"client"`
		Changelist string `json:"changelist"`
		Output     string `json:"output"`
		Files      []struct {
			DepotFile  string `json:"depot_file"`
			ClientFile string `json:"client_file"`
			Action     string `json:"action"`
			Change     string `json:"change"`
			Type       string `json:"type"`
		} `json:"files"`
	}
	_ = json.Unmarshal(payload, &parsed)
	client = firstNonEmpty(parsed.Client, client)
	change = firstNonEmpty(parsed.Changelist, change)
	lines := make([]p4OpenedLine, 0, len(parsed.Files))
	for _, file := range parsed.Files {
		lines = append(lines, p4OpenedLine{
			DepotFile:  file.DepotFile,
			ClientFile: file.ClientFile,
			Action:     file.Action,
			Change:     firstNonEmpty(file.Change, change),
			Type:       file.Type,
			Client:     client,
		})
	}
	if len(lines) == 0 {
		raw := firstNonEmpty(parsed.Output, output)
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			depot, action, _ := strings.Cut(line, "#")
			lines = append(lines, p4OpenedLine{
				DepotFile: depot,
				Action:    action,
				Change:    change,
				Client:    client,
			})
		}
	}
	return formatP4Opened(format, lines)
}

func formatP4Opened(format string, files []p4OpenedLine) (string, error) {
	format = strings.TrimSpace(format)
	if format == "" {
		return "", fmt.Errorf("-F/--format requires a format string")
	}
	if err := validateP4Format(format); err != nil {
		return "", err
	}
	var b strings.Builder
	for i, file := range files {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(p4FormatToken.ReplaceAllStringFunc(format, func(token string) string {
			if token == "%%" {
				return "%"
			}
			field := strings.Trim(token, "%")
			switch field {
			case "depotFile":
				return file.DepotFile
			case "clientFile":
				return file.ClientFile
			case "action":
				return file.Action
			case "change":
				return file.Change
			case "type":
				return file.Type
			case "client":
				return file.Client
			default:
				return ""
			}
		}))
	}
	return b.String(), nil
}

func validateP4Format(format string) error {
	withoutLiteral := strings.ReplaceAll(format, "%%", "")
	if strings.Count(withoutLiteral, "%")%2 != 0 {
		return fmt.Errorf("-F format has an unmatched %%")
	}
	var unknown []string
	seen := map[string]bool{}
	for _, match := range p4FormatToken.FindAllStringSubmatch(format, -1) {
		if match[1] == "%" {
			continue
		}
		field := match[2]
		switch field {
		case "depotFile", "clientFile", "action", "change", "type", "client":
		default:
			if !seen[field] {
				seen[field] = true
				unknown = append(unknown, field)
			}
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("unsupported -F field %s; supported fields are depotFile, clientFile, action, change, type, client (%%change%% is the number or default, not \"change N\")", strings.Join(unknown, ", "))
	}
	return nil
}

func runP4Status(cmd *cobra.Command, _ []string) error {
	if err := rejectP4FormatFor("status", cmd); err != nil {
		return err
	}
	if err := rejectP4FormatJSON(cmd); err != nil {
		return err
	}
	payload, err := p4DaemonCall(cmd.Context(), http.MethodGet, "/p4/status", nil, p4DaemonTimeout())
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
			Type      string `json:"type"`
		} `json:"files"`
		Warnings []struct {
			Kind  string   `json:"kind"`
			Files []string `json:"files"`
		} `json:"warnings"`
	}
	_ = json.Unmarshal(payload, &status)
	if strings.TrimSpace(p4Format) != "" {
		lines := make([]p4OpenedLine, 0, len(status.Files))
		for _, file := range status.Files {
			lines = append(lines, p4OpenedLine{
				DepotFile: file.DepotFile,
				Action:    file.Action,
				Change:    status.Change,
				Type:      file.Type,
				Client:    status.Client,
			})
		}
		text, err := formatP4Opened(p4Format, lines)
		if err != nil {
			return err
		}
		if text != "" {
			fmt.Fprintln(os.Stdout, text)
		}
		return nil
	}
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
		payload, err := p4DaemonPost(cmd.Context(), "/p4/swarm", body, p4DaemonTimeout())
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
