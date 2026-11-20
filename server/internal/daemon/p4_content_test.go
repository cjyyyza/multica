package daemon

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestP4ContentHeartbeatRead(t *testing.T) {
	t.Setenv("P4_FAKE_DESCRIBE", "... change 1001\n... status submitted\n... time 1700000000\n... depotFile0 //depot/UE/A.cpp\n... action0 edit\n... type0 text\n... rev0 3\n")
	// A list read does not run a diff or resolve any installed agent CLI.
	var reported struct {
		Result *protocol.P4ChangelistContent `json:"result"`
		Error  string                        `json:"error"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"workspace_id": "ws", "port": "p4:1666", "p4_user": "alice", "number": 1001,
				"p4_depots": []map[string]string{{"port": "p4:1666", "depot": "//depot/UE", "user": "alice"}},
			})
		case http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&reported); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	d := &Daemon{
		client: NewClient(server.URL), logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		p4Cache:    &p4cache.Cache{P4Path: writeDaemonFakeP4(t)},
		workspaces: map[string]*workspaceState{"ws": {runtimeIDs: []string{"rt"}}},
	}
	d.handleP4Content(t.Context(), Runtime{ID: "rt"}, "request-1")
	if reported.Error != "" || reported.Result == nil || len(reported.Result.Files) != 1 || reported.Result.Files[0].DepotPath != "//depot/UE/A.cpp" {
		t.Fatalf("report = %+v", reported)
	}
	d.handleP4Content(t.Context(), Runtime{ID: "foreign-runtime"}, "request-2")
	if reported.Error == "" || reported.Result != nil {
		t.Fatalf("foreign runtime read content: %+v", reported)
	}
}

func TestP4ContentCrossDaemonAndHostAuthFallback(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	t.Setenv("P4USER", "host-user")
	t.Setenv("P4_FAKE_AUTH_USER", "host-user")
	t.Setenv("P4_FAKE_DESCRIBE", "... change 838194\n... status pending\n... client origin_DEV\n")
	t.Setenv("P4_FAKE_SHELF", "... change 838194\n... depotFile0 //depot/UE/A.cpp\n... action0 edit\n... type0 text\n... rev0 2\n")

	for _, tc := range []struct {
		name              string
		contextPayload    map[string]any
		wantErr           bool
		forbidSavedUser   bool
		wantSavedUserCall bool
	}{
		{
			name: "different daemon skips changelist author user",
			contextPayload: map[string]any{
				"workspace_id": "ws", "port": "p4:1666", "daemon_id": "origin-daemon", "p4_user": "origin-user", "number": 838194,
				"p4_depots": []map[string]string{{"port": "p4:1666", "depot": "//depot/UE"}},
			},
			forbidSavedUser: true,
		},
		{
			name: "unowned row falls back to host login on P4 auth failure",
			contextPayload: map[string]any{
				"workspace_id": "ws", "port": "p4:1666", "daemon_id": "", "p4_user": "origin-user", "number": 838194,
				"p4_depots": []map[string]string{{"port": "p4:1666", "depot": "//depot/UE"}},
			},
			wantSavedUserCall: true,
		},
		{
			name: "configured depot user does not fall back to host login",
			contextPayload: map[string]any{
				"workspace_id": "ws", "port": "p4:1666", "daemon_id": "origin-daemon", "p4_user": "origin-user", "number": 838194,
				"p4_depots": []map[string]string{{"port": "p4:1666", "depot": "//depot/UE", "user": "configured-user"}},
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "p4.log")
			t.Setenv("P4_FAKE_LOG", logPath)
			var reported struct {
				Result *protocol.P4ChangelistContent `json:"result"`
				Error  string                        `json:"error"`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					_ = json.NewEncoder(w).Encode(tc.contextPayload)
				case http.MethodPost:
					if err := json.NewDecoder(r.Body).Decode(&reported); err != nil {
						t.Error(err)
					}
					_, _ = w.Write([]byte(`{}`))
				}
			}))
			defer server.Close()
			d := &Daemon{
				cfg:        Config{DaemonID: "other-daemon"},
				client:     NewClient(server.URL),
				logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
				p4Cache:    &p4cache.Cache{P4Path: bin},
				workspaces: map[string]*workspaceState{"ws": {runtimeIDs: []string{"rt"}}},
			}
			d.handleP4Content(t.Context(), Runtime{ID: "rt"}, "req-1")
			rawLog, _ := os.ReadFile(logPath)
			commands := string(rawLog)
			if tc.wantErr {
				if reported.Error == "" || reported.Result != nil {
					t.Fatalf("expected error, got %+v", reported)
				}
				return
			}
			if reported.Error != "" || reported.Result == nil || len(reported.Result.Files) != 1 || reported.Result.Files[0].DepotPath != "//depot/UE/A.cpp" {
				t.Fatalf("report = %+v (log:\n%s)", reported, commands)
			}
			if tc.forbidSavedUser && strings.Contains(commands, "-u origin-user") {
				t.Fatalf("cross-daemon read attempted origin-user:\n%s", commands)
			}
			if tc.wantSavedUserCall && !strings.Contains(commands, "-u origin-user") {
				t.Fatalf("unowned row did not try saved user first:\n%s", commands)
			}
			if !strings.Contains(commands, "-u host-user") {
				t.Fatalf("expected host-user invocation:\n%s", commands)
			}
		})
	}
}
