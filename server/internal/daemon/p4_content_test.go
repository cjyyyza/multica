package daemon

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
