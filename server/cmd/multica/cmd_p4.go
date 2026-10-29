package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/p4depot"
	"github.com/spf13/cobra"
)

var p4Cmd = &cobra.Command{
	Use:   "p4",
	Short: "Work with Perforce depots",
}

var p4ListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workspace Perforce depots",
	Long:  "Lists the Perforce depot registry for the current workspace. These are workspace-level depots, separate from project resources.",
	Args:  cobra.NoArgs,
	RunE:  runP4List,
}

var p4AddCmd = &cobra.Command{
	Use:   "add [files...]",
	Short: "Add files to the task changelist, or register a depot",
	Long: "With file arguments, runs `p4 add` in the bound workspace (`add-files` is the same command). " +
		"With no files, --port and --depot register a depot in the workspace registry. Existing port+depot+stream identities are not duplicated.",
	Args: cobra.ArbitraryArgs,
	RunE: runP4Add,
}

var p4RemoveCmd = &cobra.Command{
	Use:     "remove",
	Aliases: []string{"rm"},
	Short:   "Remove a Perforce depot from the workspace registry",
	Args:    cobra.NoArgs,
	RunE:    runP4Remove,
}

var p4SyncCmd = &cobra.Command{
	Use:   "sync [files...]",
	Short: "Sync the bound workspace, or individual files in it",
	Long: "Syncs the Helix workspace this task is already bound to, and only when that depot's allow_sync switch is on in Settings → Code (default off). " +
		"Omitting files syncs the full client view, including stream imports. Named arguments must be individual files in an authorized depot; directories and wildcards are rejected. " +
		"It does not create a task client or download a new copy of the depot. -c/--changelist and --fresh are rejected.",
	Args: cobra.ArbitraryArgs,
	RunE: runP4Sync,
}

var (
	p4Port       string
	p4DepotPath  string
	p4Stream     string
	p4User       string
	p4Charset    string
	p4Changelist string
	p4Desc       string
	p4Fresh      bool
)

func init() {
	p4ListCmd.Flags().String("output", "table", "Output format: table or json")

	for _, cmd := range []*cobra.Command{p4AddCmd, p4RemoveCmd} {
		cmd.Flags().StringVar(&p4Port, "port", "", "P4PORT (host:port or ssl:host:port)")
		cmd.Flags().StringVar(&p4DepotPath, "depot", "", "Depot path (//depot/project/...)")
		cmd.Flags().StringVar(&p4Stream, "stream", "", "Optional stream path")
	}
	p4SyncCmd.Flags().StringVar(&p4Port, "port", "", "P4PORT. Omit in a bound workspace; the daemon fills it. Do not look it up")
	p4SyncCmd.Flags().StringVar(&p4DepotPath, "depot", "", "Depot path. Omit in a bound workspace; the daemon fills it")
	p4SyncCmd.Flags().StringVar(&p4Stream, "stream", "", "Optional stream path")
	p4AddCmd.Flags().StringVar(&p4User, "user", "", "Optional P4USER hint")
	p4AddCmd.Flags().StringVar(&p4Charset, "charset", "", "Optional P4CHARSET")
	p4AddCmd.Flags().StringVarP(&p4Changelist, "changelist", "c", "", "With files, this task's changelist (`p4 -c`). Without files, the depot's baseline changelist")
	p4AddCmd.Flags().StringVar(&p4Desc, "description", "", "Optional description")
	p4AddCmd.Flags().StringVar(&p4SwarmURL, "swarm-url", "", "Optional Helix Swarm origin (https://swarm.example.com)")
	p4AddCmd.Flags().String("output", "json", "Output format: table or json")

	p4RemoveCmd.Flags().String("output", "json", "Output format: table or json")

	p4SyncCmd.Flags().StringVar(&p4User, "user", "", "Optional P4USER override")
	p4SyncCmd.Flags().StringVar(&p4Charset, "charset", "", "Optional P4CHARSET")
	p4SyncCmd.Flags().StringVarP(&p4Changelist, "changelist", "c", "", "Unsupported for workspace sync; returns an error")
	p4SyncCmd.Flags().BoolVar(&p4Fresh, "fresh", false, "Unsupported for workspace sync; returns an error")
	p4SyncCmd.Flags().String("output", "text", "Output format: text or json")

	mutateCmds := []*cobra.Command{
		p4EditCmd, p4AddFilesCmd, p4DeleteCmd, p4RevertCmd, p4OpenedCmd,
		p4ReconcileCmd, p4MoveCmd, p4ReopenCmd, p4ChangeCmd, p4SubmitCmd,
		p4ShelveCmd, p4UnshelveCmd, p4DescribeCmd,
	}
	for _, cmd := range mutateCmds {
		addP4TaskFlags(cmd, true)
		cmd.Flags().StringVar(&p4SwarmURL, "swarm-url", "", "Helix Swarm URL (must match the configured depot)")
	}
	p4RevertCmd.Flags().BoolVarP(&p4RevertUnchanged, "unchanged", "a", false, "revert only unchanged files (`p4 revert -a`)")
	p4MoveCmd.Flags().StringVar(&p4Dest, "dest", "", "Destination path (`p4 move <source> <dest>`)")
	p4ShelveCmd.Flags().BoolVarP(&p4Force, "force", "f", false, "overwrite an existing shelf (`p4 shelve -f`)")
	addP4FilelogFlags(p4FilelogCmd)

	swarmCmds := []*cobra.Command{p4SwarmReviewsCmd, p4SwarmCreateCmd, p4SwarmShowCmd, p4SwarmCommentCmd, p4SwarmLinkCmd}
	for _, cmd := range swarmCmds {
		addP4TaskFlags(cmd, true)
		cmd.Flags().StringVar(&p4SwarmURL, "swarm-url", "", "Helix Swarm URL (must match the configured depot)")
	}
	p4SwarmCreateCmd.Flags().StringArrayVar(&p4Reviewers, "reviewer", nil, "Reviewer P4USER (repeatable)")
	p4SwarmCreateCmd.Flags().BoolVar(&p4NoShelve, "no-shelve", false, "do not shelve the pending changelist before creating the review")
	p4SwarmShowCmd.Flags().StringVar(&p4ReviewID, "review", "", "Swarm review id")
	p4SwarmCommentCmd.Flags().StringVar(&p4ReviewID, "review", "", "Swarm review id")
	p4SwarmCommentCmd.Flags().StringVar(&p4CommentBody, "body", "", "Comment body")
	p4SwarmLinkCmd.Flags().StringVar(&p4ReviewID, "review", "", "Swarm review id")

	p4SwarmCmd.AddCommand(p4SwarmReviewsCmd)
	p4SwarmCmd.AddCommand(p4SwarmCreateCmd)
	p4SwarmCmd.AddCommand(p4SwarmShowCmd)
	p4SwarmCmd.AddCommand(p4SwarmCommentCmd)
	p4SwarmCmd.AddCommand(p4SwarmLinkCmd)

	p4Cmd.AddCommand(p4ListCmd)
	p4Cmd.AddCommand(p4AddCmd)
	p4Cmd.AddCommand(p4RemoveCmd)
	p4Cmd.AddCommand(p4SyncCmd)
	p4StatusCmd.Flags().String("output", "text", "Output format: text or json")
	p4Cmd.AddCommand(p4StatusCmd)
	for _, cmd := range mutateCmds {
		p4Cmd.AddCommand(cmd)
	}
	p4Cmd.AddCommand(p4SwarmCmd)

	p4Cmd.AddCommand(p4FilelogCmd)

	// -F is a presentation flag, not a p4 passthrough. Persistent so both
	// `p4 -F ... opened` and `p4 opened -F ...` parse; only opened and status honor it.
	// -c is a per-command alias of --changelist, not a global p4 -c client.
	p4Cmd.PersistentFlags().StringVarP(&p4Format, "format", "F", "", "Format this task's opened files with p4 -ztag field names (depotFile, clientFile, action, change, type, client). Not passed through to p4")
	p4Cmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		return rejectP4FormatFor(cmd.Name(), cmd)
	}
	setP4FlagError(p4Cmd)
}

func explainP4FlagError(_ *cobra.Command, err error) error {
	return fmt.Errorf("%w\nmultica p4 does not pass options through to p4. -c is --changelist (`multica p4 describe -c 123`); file history is `multica p4 filelog -- <file>`. List this task's opens with `multica p4 opened -F \"%%depotFile%% %%change%%\"` or `multica p4 opened --output json`", err)
}

func setP4FlagError(cmd *cobra.Command) {
	cmd.SetFlagErrorFunc(explainP4FlagError)
	for _, child := range cmd.Commands() {
		setP4FlagError(child)
	}
}

type p4WorkspaceResponse struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Slug     string        `json:"slug"`
	P4Depots []p4depot.Ref `json:"p4_depots"`
}

func fetchP4Workspace(ctx context.Context, client *cli.APIClient, workspaceID string) (p4WorkspaceResponse, error) {
	var ws p4WorkspaceResponse
	if err := client.GetJSON(ctx, "/api/workspaces/"+workspaceID, &ws); err != nil {
		return p4WorkspaceResponse{}, fmt.Errorf("get workspace: %w", err)
	}
	if ws.P4Depots == nil {
		ws.P4Depots = []p4depot.Ref{}
	}
	return ws, nil
}

func patchWorkspaceP4Depots(ctx context.Context, client *cli.APIClient, workspaceID string, depots []p4depot.Ref) (p4WorkspaceResponse, error) {
	var ws p4WorkspaceResponse
	if err := client.PatchJSON(ctx, "/api/workspaces/"+workspaceID, map[string]any{"p4_depots": depots}, &ws); err != nil {
		return p4WorkspaceResponse{}, fmt.Errorf("update workspace p4_depots: %w", err)
	}
	if ws.P4Depots == nil {
		ws.P4Depots = []p4depot.Ref{}
	}
	return ws, nil
}

func requireP4PortDepot() (p4depot.Ref, error) {
	ref, err := p4depot.Normalize(p4depot.Ref{
		Port:        p4Port,
		Depot:       p4DepotPath,
		Stream:      p4Stream,
		User:        p4User,
		Charset:     p4Charset,
		Changelist:  p4Changelist,
		Description: p4Desc,
		SwarmURL:    p4SwarmURL,
	})
	if err != nil {
		return p4depot.Ref{}, err
	}
	return ref, nil
}

// optionalP4PortDepot validates whichever depot flags the caller passed.
// A task bound to a human-owned Perforce workspace omits --port/--depot and
// the daemon fills both from the binding. Passing only one of them is also
// forwarded: requiring the pair made the CLI answer "port is required" before
// the daemon could complete it. Outside workspace mode the daemon still
// refuses a request with no depot.
func optionalP4PortDepot() (p4depot.Ref, error) {
	return p4depot.ValidateProvided(p4depot.Ref{
		Port:        p4Port,
		Depot:       p4DepotPath,
		Stream:      p4Stream,
		User:        p4User,
		Charset:     p4Charset,
		Changelist:  p4Changelist,
		Description: p4Desc,
		SwarmURL:    p4SwarmURL,
	})
}

func runP4List(cmd *cobra.Command, _ []string) error {
	client, workspaceID, err := repoCommandClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	ws, err := fetchP4Workspace(ctx, client, workspaceID)
	if err != nil {
		return err
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, ws.P4Depots)
	}
	if len(ws.P4Depots) == 0 {
		fmt.Fprintln(os.Stderr, "No Perforce depots found.")
		return nil
	}
	rows := make([][]string, 0, len(ws.P4Depots))
	for _, depot := range ws.P4Depots {
		rows = append(rows, []string{depot.Port, depot.Depot, depot.Stream, depot.SwarmURL, depot.Description})
	}
	cli.PrintTable(os.Stdout, []string{"PORT", "DEPOT", "STREAM", "SWARM", "DESCRIPTION"}, rows)
	return nil
}

func runP4Add(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		// `p4 add file` is the native verb. The no-file form stays the registry
		// command so `multica p4 add --port --depot` does not change meaning.
		return runP4Mutate("add")(cmd, args)
	}
	return runP4AddDepot(cmd, args)
}

func runP4AddDepot(cmd *cobra.Command, _ []string) error {
	ref, err := requireP4PortDepot()
	if err != nil {
		return err
	}
	client, workspaceID, err := repoCommandClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	ws, err := fetchP4Workspace(ctx, client, workspaceID)
	if err != nil {
		return err
	}
	id := p4depot.Identity(ref)
	depots := append([]p4depot.Ref{}, ws.P4Depots...)
	for i, existing := range depots {
		if p4depot.Identity(existing) == id {
			// `p4 add` rebuilds the depot from port and path. Keep the
			// switches edited in Settings → Code and any existing metadata
			// omitted by this invocation.
			if ref.User == "" && !cmd.Flags().Changed("user") {
				ref.User = existing.User
			}
			if ref.Charset == "" && !cmd.Flags().Changed("charset") {
				ref.Charset = existing.Charset
			}
			if ref.Changelist == "" && !cmd.Flags().Changed("changelist") {
				ref.Changelist = existing.Changelist
			}
			if ref.Description == "" && !cmd.Flags().Changed("description") {
				ref.Description = existing.Description
			}
			if ref.SwarmURL == "" && !cmd.Flags().Changed("swarm-url") {
				ref.SwarmURL = existing.SwarmURL
			}
			ref.AllowExclusive = existing.AllowExclusive
			ref.NoAutoReview = existing.NoAutoReview
			ref.AllowSubmit = existing.AllowSubmit
			ref.AllowSync = existing.AllowSync
			ref.AllowUnshelve = existing.AllowUnshelve
			depots[i] = ref
			ws, err = patchWorkspaceP4Depots(ctx, client, workspaceID, depots)
			if err != nil {
				return err
			}
			return printP4Mutation(cmd, ws.P4Depots, "updated")
		}
	}
	depots = append(depots, ref)
	ws, err = patchWorkspaceP4Depots(ctx, client, workspaceID, depots)
	if err != nil {
		return err
	}
	return printP4Mutation(cmd, ws.P4Depots, "added")
}

func runP4Remove(cmd *cobra.Command, _ []string) error {
	ref, err := requireP4PortDepot()
	if err != nil {
		return err
	}
	client, workspaceID, err := repoCommandClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	ws, err := fetchP4Workspace(ctx, client, workspaceID)
	if err != nil {
		return err
	}
	id := p4depot.Identity(ref)
	kept := make([]p4depot.Ref, 0, len(ws.P4Depots))
	removed := false
	for _, existing := range ws.P4Depots {
		if p4depot.Identity(existing) == id {
			removed = true
			continue
		}
		kept = append(kept, existing)
	}
	if !removed {
		return fmt.Errorf("Perforce depot not found in workspace registry: %s %s", ref.Port, ref.Depot)
	}
	ws, err = patchWorkspaceP4Depots(ctx, client, workspaceID, kept)
	if err != nil {
		return err
	}
	return printP4Mutation(cmd, ws.P4Depots, "removed")
}

func printP4Mutation(cmd *cobra.Command, depots []p4depot.Ref, action string) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, map[string]any{"action": action, "p4_depots": depots})
	}
	fmt.Fprintf(os.Stdout, "%s.\n", action)
	return nil
}

func runP4Sync(cmd *cobra.Command, args []string) error {
	if p4Changelist != "" || p4Fresh || cmd.Flags().Changed("changelist") || cmd.Flags().Changed("fresh") {
		return fmt.Errorf("workspace sync does not support -c/--changelist or --fresh; pass individual files, or omit them to sync the whole client view")
	}
	ref, err := optionalP4PortDepot()
	if err != nil {
		return err
	}
	body, err := p4TaskEnv(ref)
	if err != nil {
		return err
	}
	if len(args) > 0 {
		body["files"] = args
	}
	payload, err := p4DaemonPost(cmd.Context(), "/p4/sync", body, p4DaemonTimeout())
	if err != nil {
		return reportP4Failure("p4 sync", err)
	}
	var result struct {
		Path   string `json:"path"`
		Client string `json:"client"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	fmt.Fprintln(os.Stdout, result.Path)
	fmt.Fprintf(os.Stderr, "Synced %s %s → %s (client: %s)\n", ref.Port, ref.Depot, result.Path, result.Client)
	return nil
}

func resetP4Flags() {
	p4Port = ""
	p4DepotPath = ""
	p4Stream = ""
	p4User = ""
	p4Charset = ""
	p4Changelist = ""
	p4Desc = ""
	p4Fresh = false
	p4FilelogMax = 0
	p4FilelogLong = false
	p4SwarmURL = ""
	p4ReviewID = ""
	p4CommentBody = ""
	p4Dest = ""
	p4RevertUnchanged = false
	p4Force = false
	p4NoShelve = false
	p4Reviewers = nil
	p4Format = ""
}
