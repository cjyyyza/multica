package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestP4SwarmReportsEffectiveURLAndLinkFailure(t *testing.T) {
	for _, tc := range []struct {
		name, action string
		status       int
	}{
		{"link", "link", http.StatusOK},
		{"create", "create", http.StatusOK},
		{"show", "show", http.StatusOK},
		{"link failed", "link", http.StatusBadRequest},
		{"create succeeded but link failed", "create", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetP4Flags()
			t.Cleanup(resetP4Flags)
			var reported map[string]any
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/issues/MUL-7/swarm-reviews" {
					t.Errorf("unexpected API path: %s", r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&reported); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":"link rejected"}`))
			}))
			defer api.Close()
			daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/p4/swarm" {
					t.Errorf("unexpected daemon path: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"action":"show","swarm_url":"https://configured.example/swarm","review":{"id":77,"url":"https://configured.example/swarm/reviews/77","description":"Fix issue","changes":[1001]}}`))
			}))
			defer daemon.Close()
			t.Setenv("MULTICA_DAEMON_PORT", strings.TrimPrefix(daemon.URL, "http://127.0.0.1:"))
			t.Setenv("MULTICA_TOKEN", "mat_test_p4")
			t.Setenv("MULTICA_TASK_ID", "task")
			t.Setenv("MULTICA_ISSUE_ID", "MUL-7")
			t.Setenv("MULTICA_WORKSPACE_ID", "ws")
			t.Setenv("MULTICA_SERVER_URL", api.URL)
			p4Port, p4DepotPath = "p4:1666", "//depot/UE"
			cmd := &cobra.Command{}
			cmd.Flags().String("output", "json", "")
			err := runP4Swarm(tc.action)(cmd, []string{"77"})
			if tc.status == http.StatusOK && err != nil {
				t.Fatalf("command failed: %v", err)
			}
			if tc.status != http.StatusOK && (err == nil || !strings.Contains(err.Error(), "77")) {
				t.Fatalf("must report failed linking and retain review identity: %v", err)
			}
			if reported["swarm_url"] != "https://configured.example/swarm" || reported["changelist"] != "1001" {
				t.Fatalf("wrong link payload: %+v", reported)
			}
		})
	}
}

func TestOptionalP4PortDepotForwardsDepotWithoutPort(t *testing.T) {
	resetP4Flags()
	t.Cleanup(resetP4Flags)
	p4DepotPath = "//depot/UE"
	ref, err := optionalP4PortDepot()
	if err != nil {
		t.Fatal(err)
	}
	if ref.Port != "" || ref.Depot != "//depot/UE" {
		t.Fatalf("partial ref = %+v, want depot forwarded and port empty", ref)
	}
	p4Port = "not a port"
	if _, err := optionalP4PortDepot(); err == nil || !strings.Contains(err.Error(), "P4PORT") {
		t.Fatalf("invalid port = %v", err)
	}
}

func TestP4SwarmLinkRequiresIssueContext(t *testing.T) {
	t.Setenv("MULTICA_ISSUE_ID", "")
	resetP4Flags()
	t.Cleanup(resetP4Flags)
	p4Port, p4DepotPath = "p4:1666", "//depot/UE"
	err := runP4Swarm("link")(&cobra.Command{}, []string{"77"})
	if err == nil || !strings.Contains(err.Error(), "MULTICA_ISSUE_ID") {
		t.Fatalf("link without an issue must fail before making requests: %v", err)
	}
}

func TestReportSwarmReviewRejectsMalformedReceipt(t *testing.T) {
	t.Setenv("MULTICA_ISSUE_ID", "MUL-7")
	for _, payload := range []string{
		`not JSON`,
		`{"swarm_url":"https://configured.example"}`,
		`{"review":{"id":77,"url":"https://configured.example/reviews/77"}}`,
	} {
		if err := reportSwarmReviewToIssue(&cobra.Command{}, []byte(payload)); err == nil {
			t.Fatalf("malformed receipt accepted: %s", payload)
		}
	}
}

func TestP4SyncRejectsUnsupportedOptionsBeforeCallingDaemon(t *testing.T) {
	for _, args := range [][]string{{"--changelist", "1234"}, {"--fresh"}, {"--fresh=false"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			resetP4Flags()
			t.Cleanup(resetP4Flags)
			cmd := &cobra.Command{}
			cmd.Flags().StringVar(&p4Changelist, "changelist", "", "")
			cmd.Flags().BoolVar(&p4Fresh, "fresh", false, "")
			if err := cmd.ParseFlags(args); err != nil {
				t.Fatal(err)
			}
			if err := runP4Sync(cmd, nil); err == nil || !strings.Contains(err.Error(), "does not support") {
				t.Fatalf("unsupported option was ignored: %v", err)
			}
		})
	}
}

func TestP4AddPreservesExistingMetadataWhenFlagsOmitted(t *testing.T) {
	resetP4Flags()
	t.Cleanup(resetP4Flags)
	var patched map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"ws","name":"WS","slug":"ws","p4_depots":[{"port":"p4:1666","depot":"//depot/main","user":"alice","charset":"utf8","changelist":"100","description":"initial","swarm_url":"https://swarm.example","allow_submit":true,"allow_exclusive":true}]}`))
		case http.MethodPatch:
			if err := json.NewDecoder(r.Body).Decode(&patched); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte(`{"id":"ws","name":"WS","slug":"ws","p4_depots":[]}`))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer api.Close()
	t.Setenv("MULTICA_TOKEN", "mat_test")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws")
	t.Setenv("MULTICA_SERVER_URL", api.URL)

	cmd := &cobra.Command{}
	addP4TaskFlags(cmd, true)
	cmd.Flags().StringVar(&p4SwarmURL, "swarm-url", "", "")
	if err := cmd.ParseFlags([]string{"--port", "p4:1666", "--depot", "//depot/main"}); err != nil {
		t.Fatal(err)
	}
	if err := runP4Add(cmd, nil); err != nil {
		t.Fatalf("runP4Add: %v", err)
	}
	depots, _ := patched["p4_depots"].([]any)
	if len(depots) != 1 {
		t.Fatalf("patched depots = %#v", patched)
	}
	got, _ := depots[0].(map[string]any)
	if got["user"] != "alice" || got["charset"] != "utf8" || got["changelist"] != "100" || got["description"] != "initial" || got["swarm_url"] != "https://swarm.example" || got["allow_submit"] != true || got["allow_exclusive"] != true {
		t.Fatalf("metadata lost on update: %#v", got)
	}
}
