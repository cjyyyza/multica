package p4cache

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/multica-ai/multica/server/internal/p4depot"
)

func TestSwarmCreateUsesHelixTicket(t *testing.T) {
	var gotUser, gotTicket, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUser, gotTicket, _ = r.BasicAuth()
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reviews") {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &gotBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"review":{"id":77,"author":"alice","state":"needsReview","changes":[1001]}}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("P4_FAKE_LOG", filepath.Join(dir, "p4.log"))
	t.Setenv("P4_FAKE_SPEC", filepath.Join(dir, "client.spec"))
	t.Setenv("P4USER", "alice")
	cache := &Cache{P4Path: writeFakeP4(t, dir), HTTPClient: srv.Client()}
	work := filepath.Join(dir, "work")
	ref := p4depot.Ref{Port: "p4:1666", Depot: "//depot/UE", SwarmURL: srv.URL}
	if _, err := cache.Sync(context.Background(), SyncParams{
		WorkspaceID: "ws", TaskID: "task", WorkDir: work, Ref: ref,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := cache.Swarm(context.Background(), SwarmParams{
		WorkspaceID: "ws",
		TaskID:      "task",
		WorkDir:     work,
		Ref:         ref,
		Action:      SwarmCreate,
		Changelist:  "1001",
		Description: "review the crash fix",
		Reviewers:   []string{"bob"},
		Shelve:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Review == nil || result.Review.ID != 77 {
		t.Fatalf("review = %+v", result.Review)
	}
	if gotUser != "alice" || gotTicket != "TESTTICKET" {
		t.Fatalf("basic auth user=%q ticket=%q", gotUser, gotTicket)
	}
	if !strings.Contains(gotPath, "/api/v11/reviews") {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody["change"] != "1001" {
		t.Fatalf("body = %+v", gotBody)
	}
	if result.Review.URL != srv.URL+"/reviews/77" {
		t.Fatalf("url = %q", result.Review.URL)
	}
	if result.SwarmURL != srv.URL {
		t.Fatalf("effective Swarm URL = %q", result.SwarmURL)
	}
}

func TestSwarmRequiresConfiguredURL(t *testing.T) {
	cache := &Cache{P4Path: writeFakeP4(t, t.TempDir())}
	_, err := cache.Swarm(context.Background(), SwarmParams{
		Ref:    p4depot.Ref{Port: "p4:1666", Depot: "//depot/UE"},
		Action: SwarmList,
	})
	if err == nil || !strings.Contains(err.Error(), "swarm_url") {
		t.Fatalf("error = %v", err)
	}
}

func TestTicketForPort(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, output, port, user, want string }{
		{"CLI output", "p4:1666 (alice) ABC\n", "p4:1666", "alice", "ABC"},
		{"SSL prefix", "p4:1666 (alice) ABC\r\n", "ssl:p4:1666", "alice", "ABC"},
		{"multiple accounts", "p4:1666 (bob) OTHER\np4:1666 (alice) ABC\n", "p4:1666", "alice", "ABC"},
		{"other user", "p4:1666 (bob) OTHER\n", "p4:1666", "alice", ""},
		{"other port", "p4:1667 (alice) OTHER\n", "p4:1666", "alice", ""},
		{"ticket file is not CLI output", "p4:1666=alice:ABC\n", "p4:1666", "alice", ""},
		{"malformed user", "p4:1666 alice ABC\n", "p4:1666", "alice", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ticketForPort(tc.output, tc.port, tc.user); got != tc.want {
				t.Fatalf("ticket = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSwarmDoesNotForwardTicketsThroughRedirects(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Store(true)
		_, _ = w.Write([]byte(`{"reviews":[]}`))
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	cache := &Cache{HTTPClient: source.Client()}
	client := swarmClient{baseURL: source.URL, user: "alice", ticket: "TESTTICKET", http: cache.httpClient()}
	if _, err := client.listReviews(context.Background(), ""); err == nil {
		t.Error("redirect should fail instead of forwarding the credential")
	}
	if redirected.Load() {
		t.Error("request reached an unconfigured destination")
	}
}
