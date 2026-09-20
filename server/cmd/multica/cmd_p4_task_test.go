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
