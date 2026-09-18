package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/integrations/popo"
)

var popoCmd = &cobra.Command{
	Use:   "popo",
	Short: "Bridge POPO through the Windows dj01bot gateway",
	Long: `POPO and dj01bot are only reachable on this Windows computer.
These commands call local dj01bot and the Multica API.
The server never opens POPO or dj01bot.

Keep this running so Multica agents can chat and /issue from POPO:

  multica popo gateway
`,
}

var popoStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show POPO installations and local dj01bot health",
	RunE:  runPopoStatus,
}

var popoGatewayCmd = &cobra.Command{
	Use:   "gateway",
	Short: "Receive POPO Open events and deliver queued replies via dj01bot",
	RunE:  runPopoGateway,
}

func init() {
	popoGatewayCmd.Flags().String("listen", "127.0.0.1:18681", "Local POPO Open callback listen address")
	popoGatewayCmd.Flags().Duration("interval", 2*time.Second, "Outbound poll interval")
	popoCmd.AddCommand(popoStatusCmd, popoGatewayCmd)
}

type popoInstallationJSON struct {
	ID         string `json:"id"`
	RobotID    string `json:"robot_id"`
	RobotName  string `json:"robot_name"`
	WebhookURL string `json:"webhook_url"`
	Status     string `json:"status"`
}

type popoInstallationsEnvelopeJSON struct {
	Installations    []popoInstallationJSON `json:"installations"`
	Configured       bool                   `json:"configured"`
	InstallSupported bool                   `json:"install_supported"`
}

type popoOutboundJSON struct {
	ID             string `json:"id"`
	InstallationID string `json:"installation_id"`
	ChatID         string `json:"chat_id"`
	RobotID        string `json:"robot_id"`
	Content        string `json:"content"`
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
		fmt.Fprintln(cmd.OutOrStdout(), "POPO is not enabled on this Multica server (MULTICA_POPO_SECRET_KEY).")
		return nil
	}
	if len(env.Installations) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No POPO bots connected. Use Settings → Integrations → POPO.")
		return nil
	}
	for _, inst := range env.Installations {
		fmt.Fprintf(cmd.OutOrStdout(), "robot_id\t%s\tstatus\t%s\twebhook\t%s\n", inst.RobotID, inst.Status, inst.WebhookURL)
	}
	if err := popo.RequireWindowsLocal(); err != nil {
		fmt.Fprintln(cmd.OutOrStdout(), err.Error())
		return nil
	}
	active := firstActivePopo(env.Installations)
	if active.WebhookURL == "" {
		return nil
	}
	gw := &popo.GatewayClient{BaseURL: active.WebhookURL}
	if token := strings.TrimSpace(os.Getenv("MULTICA_POPO_WEBHOOK_TOKEN")); token != "" {
		gw.Token = token
	}
	if err := gw.Health(ctx); err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "dj01bot\tunreachable\t%s\n", err)
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), "dj01bot\tok")
	return nil
}

func runPopoGateway(cmd *cobra.Command, _ []string) error {
	if err := popo.RequireWindowsLocal(); err != nil {
		return err
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	listen, _ := cmd.Flags().GetString("listen")
	interval, _ := cmd.Flags().GetDuration("interval")
	ctx := cmd.Context()
	env, err := loadPopoInstallations(ctx, client)
	if err != nil {
		return err
	}
	if !env.Configured {
		return fmt.Errorf("POPO is not enabled on this Multica server")
	}
	active := firstActivePopo(env.Installations)
	if active.ID == "" {
		return fmt.Errorf("no active POPO installation; connect a bot in Settings → Integrations")
	}
	gw := &popo.GatewayClient{BaseURL: active.WebhookURL}
	if token := strings.TrimSpace(os.Getenv("MULTICA_POPO_WEBHOOK_TOKEN")); token != "" {
		gw.Token = token
	}
	if err := gw.Health(ctx); err != nil {
		return fmt.Errorf("dj01bot webhook is not reachable (enable gateway.webhook in ~/.dj01bot/config.json): %w", err)
	}
	mux := http.NewServeMux()
	handle := func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			nonce := r.URL.Query().Get("nonce")
			_ = json.NewEncoder(w).Encode(map[string]string{"nonce": nonce})
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		robotID := strings.TrimSpace(r.PathValue("robot_id"))
		if robotID == "" {
			robotID = active.RobotID
		}
		ingestCtx, cancel := cli.APIContext(context.Background())
		defer cancel()
		if err := client.PostJSON(ingestCtx, popoAPIPath(client, "/inbound"), map[string]any{
			"robot_id": robotID,
			"event":    json.RawMessage(body),
		}, nil); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
	mux.HandleFunc("/api/popo_open", handle)
	mux.HandleFunc("/api/popo_open/{robot_id}", handle)
	srv := &http.Server{Addr: listen, Handler: mux}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	fmt.Fprintf(cmd.OutOrStdout(), "listening %s ; delivering via %s\n", listen, active.WebhookURL)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
			return ctx.Err()
		case err := <-errCh:
			if err != nil && err != http.ErrServerClosed {
				return err
			}
			return nil
		case <-ticker.C:
			if err := deliverPopoOutbound(ctx, cmd, client, gw); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "outbound: %v\n", err)
			}
		}
	}
}

func deliverPopoOutbound(ctx context.Context, cmd *cobra.Command, client *cli.APIClient, gw *popo.GatewayClient) error {
	var out struct {
		Items []popoOutboundJSON `json:"items"`
	}
	if err := client.GetJSON(ctx, popoAPIPath(client, "/outbound"), &out); err != nil {
		return err
	}
	acks := make([]map[string]string, 0, len(out.Items))
	for _, item := range out.Items {
		sendErr := gw.Send(ctx, popo.OutboundRequest{
			Content: item.Content,
			Channel: "popo_open",
			ChatID:  item.ChatID,
			RobotID: item.RobotID,
		})
		ack := map[string]string{"id": item.ID}
		if sendErr != nil {
			ack["error"] = sendErr.Error()
			fmt.Fprintf(cmd.ErrOrStderr(), "outbound %s: %v\n", item.ID, sendErr)
		}
		acks = append(acks, ack)
	}
	if len(acks) == 0 {
		return nil
	}
	return client.PostJSON(ctx, popoAPIPath(client, "/outbound-ack"), map[string]any{"results": acks}, nil)
}

func loadPopoInstallations(ctx context.Context, client *cli.APIClient) (popoInstallationsEnvelopeJSON, error) {
	var env popoInstallationsEnvelopeJSON
	if err := client.GetJSON(ctx, popoAPIPath(client, "/installations"), &env); err != nil {
		return env, err
	}
	return env, nil
}

func firstActivePopo(rows []popoInstallationJSON) popoInstallationJSON {
	for _, row := range rows {
		if row.Status == "active" {
			return row
		}
	}
	return popoInstallationJSON{}
}

func popoAPIPath(client *cli.APIClient, suffix string) string {
	return "/api/workspaces/" + strings.TrimSpace(client.WorkspaceID) + "/popo" + suffix
}
