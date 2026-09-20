package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

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
	Use:   "add",
	Short: "Add a Perforce depot to the workspace registry",
	Long:  "Adds one Perforce depot (P4PORT + depot path) to the current workspace. Existing port+depot+stream identities are not duplicated.",
	Args:  cobra.NoArgs,
	RunE:  runP4Add,
}

var p4RemoveCmd = &cobra.Command{
	Use:     "remove",
	Aliases: []string{"rm"},
	Short:   "Remove a Perforce depot from the workspace registry",
	Args:    cobra.NoArgs,
	RunE:    runP4Remove,
}

var p4SyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync a Perforce depot into the working directory",
	Long: "Creates a task-scoped P4 client and runs `p4 sync` through the daemon. Used by agents to fetch configured depots.\n\n" +
		"Uses the host Helix login (`p4 login`); Multica does not store a Perforce password. " +
		"Pass --fresh to delete the previous sync directory and start over.",
	Args: cobra.NoArgs,
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

	for _, cmd := range []*cobra.Command{p4AddCmd, p4RemoveCmd, p4SyncCmd} {
		cmd.Flags().StringVar(&p4Port, "port", "", "P4PORT (host:port or ssl:host:port)")
		cmd.Flags().StringVar(&p4DepotPath, "depot", "", "Depot path (//depot/project/...)")
		cmd.Flags().StringVar(&p4Stream, "stream", "", "Optional stream path")
	}
	p4AddCmd.Flags().StringVar(&p4User, "user", "", "Optional P4USER hint")
	p4AddCmd.Flags().StringVar(&p4Charset, "charset", "", "Optional P4CHARSET")
	p4AddCmd.Flags().StringVar(&p4Changelist, "changelist", "", "Optional baseline changelist")
	p4AddCmd.Flags().StringVar(&p4Desc, "description", "", "Optional description")
	p4AddCmd.Flags().StringVar(&p4SwarmURL, "swarm-url", "", "Optional Helix Swarm origin (https://swarm.example.com)")
	p4AddCmd.Flags().String("output", "json", "Output format: table or json")

	p4RemoveCmd.Flags().String("output", "json", "Output format: table or json")

	p4SyncCmd.Flags().StringVar(&p4User, "user", "", "Optional P4USER override")
	p4SyncCmd.Flags().StringVar(&p4Charset, "charset", "", "Optional P4CHARSET")
	p4SyncCmd.Flags().StringVar(&p4Changelist, "changelist", "", "Optional changelist, label, or #head")
	p4SyncCmd.Flags().BoolVar(&p4Fresh, "fresh", false, "delete the previous sync directory and run a clean sync")
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
	p4RevertCmd.Flags().BoolVar(&p4RevertUnchanged, "unchanged", false, "revert only unchanged files (`p4 revert -a`)")
	p4MoveCmd.Flags().StringVar(&p4Dest, "dest", "", "Destination path")
	_ = p4MoveCmd.MarkFlagRequired("dest")
	p4ShelveCmd.Flags().BoolVar(&p4Force, "force", false, "overwrite an existing shelf (`p4 shelve -f`)")

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
	for _, cmd := range mutateCmds {
		p4Cmd.AddCommand(cmd)
	}
	p4Cmd.AddCommand(p4SwarmCmd)
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

func runP4Add(cmd *cobra.Command, _ []string) error {
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

func runP4Sync(cmd *cobra.Command, _ []string) error {
	ref, err := requireP4PortDepot()
	if err != nil {
		return err
	}
	body, err := p4TaskEnv(ref)
	if err != nil {
		return err
	}
	body["changelist"] = ref.Changelist
	body["fresh"] = p4Fresh
	payload, err := p4DaemonPost(cmd.Context(), "/p4/sync", body, 5*time.Minute)
	if err != nil {
		return fmt.Errorf("p4 sync failed: %w", err)
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
	p4SwarmURL = ""
	p4ReviewID = ""
	p4CommentBody = ""
	p4Dest = ""
	p4RevertUnchanged = false
	p4Force = false
	p4NoShelve = false
	p4Reviewers = nil
}
