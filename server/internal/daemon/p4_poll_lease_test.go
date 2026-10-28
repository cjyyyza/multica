package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
)

func TestP4PollRenewsLeaseAndDropsLostOwnership(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	t.Setenv("P4_FAKE_INFO", "... serverCluster cluster\n")
	t.Setenv("P4_FAKE_DESCRIBE", "... change 1001\n... status pending\n")
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "renewed", true: "ownership changed"}[lost], func(t *testing.T) {
			claims, reports := 0, 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/open"):
					json.NewEncoder(w).Encode(p4OpenChangelistsResponse{LeaseRequired: true, P4Depots: []P4DepotData{{Port: "p4:1666", Depot: "//depot", User: "alice"}}, Changelists: []p4OpenChangelist{{ID: "cl", Port: "p4:1666", Number: 1001}}})
				case strings.HasSuffix(r.URL.Path, "/claim"):
					var body struct {
						Generation int64 `json:"poll_generation"`
					}
					json.NewDecoder(r.Body).Decode(&body)
					if body.Generation != int64(claims) {
						t.Errorf("claim generation=%d, expected=%d", body.Generation, claims)
					}
					claims++
					if lost && claims == 2 {
						w.WriteHeader(http.StatusConflict)
						return
					}
					json.NewEncoder(w).Encode(map[string]any{"poll_generation": claims})
				case strings.HasSuffix(r.URL.Path, "/status"):
					reports++
					var body p4StatusUpdateRequest
					json.NewDecoder(r.Body).Decode(&body)
					if len(body.Updates) != 1 || body.Updates[0].PollGeneration != 2 {
						t.Errorf("unfenced update: %+v", body)
					}
					json.NewEncoder(w).Encode(p4StatusUpdateResponse{Updated: 1})
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
				}
			}))
			defer api.Close()
			d := &Daemon{client: NewClient(api.URL), p4Cache: &p4cache.Cache{P4Path: bin}, workspaces: map[string]*workspaceState{"ws": {}}}
			d.pollP4ChangelistStatuses(t.Context())
			if claims != 2 || (!lost && reports != 1) || (lost && reports != 0) {
				t.Fatalf("claims=%d reports=%d lost=%v", claims, reports, lost)
			}
		})
	}
}

func TestP4AlternatePollEndpointOnlyConfirmsSubmittedChanges(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	log := filepath.Join(t.TempDir(), "p4.log")
	t.Setenv("P4_FAKE_LOG", log)
	t.Setenv("P4_FAKE_INFO", "... serverCluster cluster\n")
	d := &Daemon{p4Cache: &p4cache.Cache{P4Path: bin}}
	cl := p4OpenChangelist{Port: "edge:1666", ServerKey: "cluster", Client: "client", DaemonID: "old", Number: 1001}
	depots := []P4DepotData{{Port: "commit:1666", Depot: "//depot", User: "alice"}}
	ref, ok := d.authorizedP4PollRef(t.Context(), depots, cl)
	if !ok || ref.Port != "commit:1666" {
		t.Fatalf("configured endpoint was not selected: %+v %v", ref, ok)
	}
	for _, tc := range []struct {
		name, body, failure string
		accepted            bool
	}{
		{"missing", "", "1001 - no such changelist.", false},
		{"pending", "... change 1001\n... status pending\n", "", false},
		{"submitted", "... change 1002\n... oldChange 1001\n... status submitted\n... time 1700000000\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("P4_FAKE_DESCRIBE", tc.body)
			t.Setenv("P4_FAKE_DESCRIBE_ERR", tc.failure)
			update, ok := d.observeP4Changelist(t.Context(), cl, ref)
			if ok != tc.accepted {
				t.Fatalf("observation=%+v accepted=%v", update, ok)
			}
		})
	}
	cl.Client = ""
	if _, ok := d.authorizedP4PollRef(t.Context(), depots, cl); ok {
		t.Fatal("manual row authorized another endpoint")
	}
	content, _ := os.ReadFile(log)
	if strings.Contains(string(content), "-p edge:1666") {
		t.Fatalf("background poll contacted an unconfigured historical endpoint: %s", content)
	}
}
