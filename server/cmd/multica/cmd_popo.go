package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var popoCmd = &cobra.Command{
	Use:   "popo",
	Short: "Inspect POPO Windows-bridge installations",
	Long: `POPO transport is a Windows process that pairs with Multica and
holds the dj01bot connection. The Go CLI no longer bridges POPO itself.

Run the Windows bridge:

  python -m nanobot.multica_bridge pair --server https://... --pairing-code ...
  python -m nanobot.multica_bridge run --server https://...
`,
}

var popoStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show POPO installations on this workspace",
	RunE:  runPopoStatus,
}

var popoGatewayCmd = &cobra.Command{
	Use:   "gateway",
	Short: "Removed; use python -m nanobot.multica_bridge",
	RunE:  runPopoGateway,
}

func init() {
	popoCmd.AddCommand(popoStatusCmd, popoGatewayCmd)
}

type popoInstallationJSON struct {
	ID      string `json:"id"`
	RobotID string `json:"robot_id"`
	Status  string `json:"status"`
}

type popoInstallationsEnvelopeJSON struct {
	Installations    []popoInstallationJSON `json:"installations"`
	Configured       bool                   `json:"configured"`
	InstallSupported bool                   `json:"install_supported"`
}

func runPopoStatus(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	env, err := loadPopoInstallations(ctx, client)
	if err != nil {
		return err
	}
	if !env.Configured {
		fmt.Fprintln(cmd.OutOrStdout(), "POPO is not enabled on this Multica server (MULTICA_POPO_ENABLED).")
		return nil
	}
	if len(env.Installations) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No POPO bots connected. Pair a Windows bridge, then connect a robot in Settings → Integrations → POPO.")
		return nil
	}
	for _, inst := range env.Installations {
		fmt.Fprintf(cmd.OutOrStdout(), "robot_id\t%s\tstatus\t%s\n", inst.RobotID, inst.Status)
	}
	return nil
}

func runPopoGateway(_ *cobra.Command, _ []string) error {
	return fmt.Errorf("multica popo gateway was removed; run: python -m nanobot.multica_bridge")
}

func loadPopoInstallations(ctx context.Context, client *cli.APIClient) (popoInstallationsEnvelopeJSON, error) {
	var env popoInstallationsEnvelopeJSON
	if err := client.GetJSON(ctx, "/api/workspaces/"+strings.TrimSpace(client.WorkspaceID)+"/popo/installations", &env); err != nil {
		return env, err
	}
	return env, nil
}
