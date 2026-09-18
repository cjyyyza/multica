package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/integrations/popo"
	"github.com/spf13/cobra"
)

func TestPopoAPIPath(t *testing.T) {
	got := popoAPIPath(&cli.APIClient{WorkspaceID: "ws-1"}, "/inbound")
	if got != "/api/workspaces/ws-1/popo/inbound" {
		t.Fatalf("path = %s", got)
	}
}

func TestDeliverPopoOutboundUsesDj01botContract(t *testing.T) {
	t.Setenv(popo.AllowNonWindowsEnv, "1")
	var outboundBody map[string]any
	dj01 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/outbound" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &outboundBody)
		_, _ = w.Write([]byte(`{"status":"attempted"}`))
	}))
	t.Cleanup(dj01.Close)

	var acked bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/popo/outbound"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]string{{
					"id":       "11111111-1111-1111-1111-111111111111",
					"chat_id":  "yujian01@corp.netease.com",
					"robot_id": "default",
					"content":  "hello from multica",
				}},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/popo/outbound-ack"):
			acked = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected API %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(api.Close)

	client := cli.NewAPIClient(api.URL, "ws-1", "tok")
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	gw := &popo.GatewayClient{BaseURL: dj01.URL}
	if err := deliverPopoOutbound(context.Background(), cmd, client, gw); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if !acked {
		t.Fatal("expected outbound-ack")
	}
	if outboundBody["channel"] != "popo_open" || outboundBody["content"] != "hello from multica" {
		t.Fatalf("dj01bot body = %+v", outboundBody)
	}
}

func TestRequireWindowsLocalHonorsOverride(t *testing.T) {
	t.Setenv(popo.AllowNonWindowsEnv, "1")
	if err := popo.RequireWindowsLocal(); err != nil {
		t.Fatalf("override should allow: %v", err)
	}
	_ = os.Unsetenv(popo.AllowNonWindowsEnv)
}
