package main

import (
	"context"
	"fmt"
	"os"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/p4depot"
	"github.com/spf13/cobra"
)

var p4Cmd = &cobra.Command{
	Use:   "p4",
	Short: "Manage Perforce depots, changelist links, and Helix Swarm",
	RunE:  runP4CommandGroup,
}

func runP4CommandGroup(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return cmd.Help()
	}
	return fmt.Errorf("native Perforce commands must be run directly with `p4`; `multica p4` only supports `registry`, `changelist`, and `swarm`")
}

var p4RegistryCmd = &cobra.Command{
	Use:   "registry",
	Short: "Manage workspace Perforce depots",
}

var p4RegistryListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workspace Perforce depots",
	Args:  cobra.NoArgs,
	RunE:  runP4RegistryList,
}

var p4RegistryAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add or update a workspace Perforce depot",
	Args:  cobra.NoArgs,
	RunE:  runP4RegistryAdd,
}

var p4RegistryRemoveCmd = &cobra.Command{
	Use:     "remove",
	Aliases: []string{"rm"},
	Short:   "Remove a Perforce depot from the workspace registry",
	Args:    cobra.NoArgs,
	RunE:    runP4RegistryRemove,
}

var p4ChangelistCmd = &cobra.Command{
	Use:   "changelist",
	Short: "Associate native Perforce changelists with the current issue",
}

var p4ChangelistLinkCmd = &cobra.Command{
	Use:   "link",
	Short: "Link a native changelist to the current issue",
	Args:  cobra.NoArgs,
	RunE:  runP4ChangelistLinkCommand,
}

var (
	p4Port       string
	p4DepotPath  string
	p4Stream     string
	p4User       string
	p4Charset    string
	p4Changelist string
	p4Desc       string
)

func init() {
	p4RegistryListCmd.Flags().String("output", "table", "Output format: table or json")
	for _, cmd := range []*cobra.Command{p4RegistryAddCmd, p4RegistryRemoveCmd} {
		cmd.Flags().StringVar(&p4Port, "port", "", "P4PORT (host:port, ssl:host:port)")
		cmd.Flags().StringVar(&p4DepotPath, "depot", "", "Perforce depot path (//depot/project/...)")
		cmd.Flags().StringVar(&p4Stream, "stream", "", "Optional P4 stream")
	}
	p4RegistryAddCmd.Flags().StringVar(&p4User, "user", "", "Optional P4USER hint")
	p4RegistryAddCmd.Flags().StringVar(&p4Charset, "charset", "", "Optional P4CHARSET")
	p4RegistryAddCmd.Flags().StringVar(&p4Changelist, "changelist", "", "Optional baseline changelist")
	p4RegistryAddCmd.Flags().StringVar(&p4Desc, "description", "", "Optional description")
	p4RegistryAddCmd.Flags().StringVar(&p4SwarmURL, "swarm-url", "", "Optional Helix Swarm origin")
	p4RegistryAddCmd.Flags().String("output", "json", "Output format: table or json")
	p4RegistryRemoveCmd.Flags().String("output", "json", "Output format: table or json")
	p4RegistryCmd.AddCommand(p4RegistryListCmd, p4RegistryAddCmd, p4RegistryRemoveCmd)

	p4ChangelistLinkCmd.Flags().StringVarP(&p4Changelist, "changelist", "c", "", "Changelist number")
	p4ChangelistLinkCmd.Flags().String("output", "json", "Output format: json or text")
	p4ChangelistCmd.AddCommand(p4ChangelistLinkCmd)

	configureP4SwarmCommands()
	p4Cmd.AddCommand(p4RegistryCmd, p4SwarmCmd, p4ChangelistCmd)
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
		return p4WorkspaceResponse{}, fmt.Errorf("update workspace: %w", err)
	}
	return ws, nil
}

func requireP4PortDepot() (p4depot.Ref, error) {
	return p4depot.Normalize(p4depot.Ref{
		Port: p4Port, Depot: p4DepotPath, Stream: p4Stream, User: p4User,
		Charset: p4Charset, Changelist: p4Changelist, Description: p4Desc, SwarmURL: p4SwarmURL,
	})
}

func optionalP4PortDepot() (p4depot.Ref, error) {
	return p4depot.ValidateProvided(p4depot.Ref{
		Port: p4Port, Depot: p4DepotPath, Stream: p4Stream, User: p4User,
		Charset: p4Charset, Changelist: p4Changelist, Description: p4Desc, SwarmURL: p4SwarmURL,
	})
}

func runP4RegistryList(cmd *cobra.Command, _ []string) error {
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
		fmt.Fprintln(os.Stderr, "No Perforce depots configured.")
		return nil
	}
	rows := make([][]string, 0, len(ws.P4Depots))
	for _, depot := range ws.P4Depots {
		rows = append(rows, []string{depot.Port, depot.Depot, depot.Stream, depot.SwarmURL, depot.Description})
	}
	cli.PrintTable(os.Stdout, []string{"PORT", "DEPOT", "STREAM", "SWARM", "DESCRIPTION"}, rows)
	return nil
}

func runP4RegistryAdd(cmd *cobra.Command, _ []string) error {
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
	depots := append([]p4depot.Ref{}, ws.P4Depots...)
	identity := p4depot.Identity(ref)
	updated := false
	for i, existing := range depots {
		if p4depot.Identity(existing) != identity {
			continue
		}
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
		depots[i] = ref
		updated = true
		break
	}
	if !updated {
		depots = append(depots, ref)
	}
	ws, err = patchWorkspaceP4Depots(ctx, client, workspaceID, depots)
	if err != nil {
		return err
	}
	action := "added"
	if updated {
		action = "updated"
	}
	return printP4RegistryMutation(cmd, ws.P4Depots, action)
}

func runP4RegistryRemove(cmd *cobra.Command, _ []string) error {
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
	identity := p4depot.Identity(ref)
	kept := make([]p4depot.Ref, 0, len(ws.P4Depots))
	removed := false
	for _, existing := range ws.P4Depots {
		if p4depot.Identity(existing) == identity {
			removed = true
			continue
		}
		kept = append(kept, existing)
	}
	if !removed {
		return fmt.Errorf("Perforce depot %s %s is not in the workspace registry", ref.Port, ref.Depot)
	}
	ws, err = patchWorkspaceP4Depots(ctx, client, workspaceID, kept)
	if err != nil {
		return err
	}
	return printP4RegistryMutation(cmd, ws.P4Depots, "removed")
}

func printP4RegistryMutation(cmd *cobra.Command, depots []p4depot.Ref, action string) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, map[string]any{"action": action, "p4_depots": depots})
	}
	fmt.Fprintf(os.Stdout, "%s.\n", action)
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
	p4SwarmURL = ""
	p4ReviewID = ""
	p4CommentBody = ""
	p4Reviewers = nil
}
