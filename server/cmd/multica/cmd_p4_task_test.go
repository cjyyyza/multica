package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestP4CommandOnlyExposesMulticaIntegrations(t *testing.T) {
	if p4Cmd.DisableFlagParsing {
		t.Fatal("p4 subcommand flags must use Cobra's normal parsing")
	}
	if err := runP4CommandGroup(p4Cmd, []string{"edit", "File.cpp"}); err == nil || !strings.Contains(err.Error(), "run directly with `p4`") {
		t.Fatalf("native verb was not rejected with direct-p4 guidance: %v", err)
	}
	for _, name := range []string{"registry", "changelist", "swarm"} {
		found := false
		for _, cmd := range p4Cmd.Commands() {
			if cmd.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("p4 %s command is not registered", name)
		}
	}
}

func TestP4ChangelistLinkUsesActiveTask(t *testing.T) {
	resetP4Flags()
	t.Cleanup(resetP4Flags)
	var body map[string]any
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/p4/changelist/link" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":1001,"status":"shelved"}`))
	}))
	defer daemon.Close()
	t.Setenv("MULTICA_DAEMON_PORT", strings.TrimPrefix(daemon.URL, "http://127.0.0.1:"))
	t.Setenv("MULTICA_TOKEN", "mat_test_p4")
	t.Setenv("MULTICA_TASK_ID", "task")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws")
	t.Setenv("MULTICA_ISSUE_ID", "MUL-7")
	p4Changelist = "1001"
	cmd := &cobra.Command{}
	cmd.Flags().String("output", "json", "")
	out, err := captureStdout(t, func() error { return runP4ChangelistLinkCommand(cmd, nil) })
	if err != nil {
		t.Fatal(err)
	}
	if body["changelist"] != "1001" || body["task_id"] != "task" || !strings.Contains(out, `"number": 1001`) {
		t.Fatalf("body=%+v output=%s", body, out)
	}
}

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
			changelistLinks := 0
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
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/p4/swarm":
					_, _ = w.Write([]byte(`{"action":"show","swarm_url":"https://configured.example/swarm","review":{"id":77,"url":"https://configured.example/swarm/reviews/77","description":"Fix issue","changes":[1001]}}`))
				case "/p4/changelist/link":
					changelistLinks++
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body["changelist"] != "1001" {
						t.Errorf("linked changelist = %v", body["changelist"])
					}
					_, _ = w.Write([]byte(`{"number":1001}`))
				default:
					t.Errorf("unexpected daemon path: %s", r.URL.Path)
				}
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
				t.Fatalf("must retain review identity: %v", err)
			}
			if reported["swarm_url"] != "https://configured.example/swarm" || reported["changelist"] != "1001" {
				t.Fatalf("wrong link payload: %+v", reported)
			}
			wantLinks := 0
			if tc.status == http.StatusOK && (tc.action == "create" || tc.action == "link") {
				wantLinks = 1
			}
			if changelistLinks != wantLinks {
				t.Fatalf("changelist link calls=%d want %d", changelistLinks, wantLinks)
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
		t.Fatalf("link without an issue must fail before requests: %v", err)
	}
}

func TestP4RegistryAddPreservesExistingMetadata(t *testing.T) {
	resetP4Flags()
	t.Cleanup(resetP4Flags)
	var patched map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"ws","name":"WS","slug":"ws","p4_depots":[{"port":"p4:1666","depot":"//depot/main","user":"alice","charset":"utf8","changelist":"100","description":"initial","swarm_url":"https://swarm.example"}]}`))
		case http.MethodPatch:
			if err := json.NewDecoder(r.Body).Decode(&patched); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte(`{"id":"ws","name":"WS","slug":"ws","p4_depots":[]}`))
		}
	}))
	defer api.Close()
	t.Setenv("MULTICA_TOKEN", "mat_test")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws")
	t.Setenv("MULTICA_SERVER_URL", api.URL)
	cmd := &cobra.Command{}
	addP4TaskFlags(cmd)
	if err := cmd.ParseFlags([]string{"--port", "p4:1666", "--depot", "//depot/main"}); err != nil {
		t.Fatal(err)
	}
	if err := runP4RegistryAdd(cmd, nil); err != nil {
		t.Fatal(err)
	}
	depots := patched["p4_depots"].([]any)
	got := depots[0].(map[string]any)
	for key, want := range map[string]any{"user": "alice", "charset": "utf8", "changelist": "100", "description": "initial", "swarm_url": "https://swarm.example"} {
		if got[key] != want {
			t.Fatalf("%s=%v want=%v", key, got[key], want)
		}
	}
}
