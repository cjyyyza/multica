package p4cache

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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
			// The v11 create answer documented for Swarm 2024.2.
			_, _ = w.Write([]byte(`{"error":null,"messages":[],"data":{"review":[{"id":77,"author":"alice","state":"needsReview","changes":[1001]}]}}`))
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

// The Swarm 2024.2 API docs give the v11 create answer as a one-element array
// under data.review. The parser read only an object there, and the create fake
// answered with an object, so the real create would have failed after Swarm had
// already made the review.
func TestParseReviewAcceptsEveryDocumentedShape(t *testing.T) {
	t.Parallel()
	const one = `{"id":12346,"author":"bruno","state":"needsReview","changes":[12345]}`
	for _, tc := range []struct{ name, body string }{
		{"v11 create", `{"error":null,"messages":[],"data":{"review":[` + one + `]}}`},
		{"v11 get", `{"error":null,"messages":[],"data":{"reviews":[` + one + `]}}`},
		{"object under data", `{"data":{"review":` + one + `}}`},
		{"v9", `{"review":` + one + `}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			review, err := parseReview("https://swarm.example", json.RawMessage(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if review.ID != 12346 || review.Author != "bruno" || len(review.Changes) != 1 || review.Changes[0] != 12345 {
				t.Fatalf("review = %+v", review)
			}
			if review.URL != "https://swarm.example/reviews/12346" {
				t.Fatalf("url = %q", review.URL)
			}
		})
	}
	_, err := parseReview("https://swarm.example", json.RawMessage(`{"data":{"review":[]}}`))
	if err == nil || !strings.Contains(err.Error(), `"review":[]`) {
		t.Fatalf("empty create answer: err = %v, want it to quote the body", err)
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

// `p4 tickets` on the DJ01 machine prints the commit server's ticket under its
// cluster name, so the port never matches. Swarm review creation failed with
// "no Helix ticket" in the live acceptance test until the cluster was tried.
func TestTicketForCluster(t *testing.T) {
	t.Parallel()
	real := "localhost:p4_dj01 (yujian01) 0123456789ABCDEF0123456789ABCDEF\r\n"
	for _, tc := range []struct{ name, output, cluster, user, want string }{
		{"DJ01 cluster-keyed ticket", real, "p4_dj01", "yujian01", "0123456789ABCDEF0123456789ABCDEF"},
		{"cluster match is case-insensitive", real, "P4_DJ01", "yujian01", "0123456789ABCDEF0123456789ABCDEF"},
		{"other user", real, "p4_dj01", "alice", ""},
		{"other cluster", real, "p4_other", "yujian01", ""},
		{"no cluster", real, "", "yujian01", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ticketForCluster(tc.output, tc.cluster, tc.user); got != tc.want {
				t.Fatalf("ticket = %q, want %q", got, tc.want)
			}
		})
	}
	if got := ticketForPort(real, "p4-commit.example.com:1666", "yujian01"); got != "" {
		t.Fatalf("port lookup unexpectedly matched the cluster-keyed ticket: %q", got)
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

// On DJ01's Swarm 2024.2 the reviews list ignored a bare `change` filter and
// returned its newest reviews. The daemon took the first one as the task
// changelist's existing review and attached a colleague's review (795321) to
// the issue. The list must send change[] and keep only reviews that name the
// changelist, whatever the server returns.
func TestSwarmListKeepsOnlyReviewsOfTheChangelist(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/reviews") {
			gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			// The filter is ignored: unrelated reviews come first.
			_, _ = w.Write([]byte(`{"reviews":[
				{"id":795321,"author":"carol","state":"needsReview","changes":[795151,795322]},
				{"id":795233,"author":"dave","state":"needsReview","changes":[795231,795234]}
			],"totalCount":2}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("P4_FAKE_LOG", filepath.Join(dir, "p4.log"))
	t.Setenv("P4USER", "alice")
	cache := &Cache{P4Path: writeFakeP4(t, dir), HTTPClient: srv.Client()}
	result, err := cache.Swarm(context.Background(), SwarmParams{
		Ref:        p4depot.Ref{Port: "p4:1666", Depot: "//depot/UE", SwarmURL: srv.URL},
		Action:     SwarmList,
		Changelist: "795343",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Reviews) != 0 {
		t.Fatalf("reviews = %+v, want none: no returned review lists CL 795343", result.Reviews)
	}
	values, _ := url.ParseQuery(gotQuery)
	if gotPath != "/api/v9/reviews" || values.Get("change[]") != "795343" {
		t.Fatalf("request = %s?%s, want /api/v9/reviews?change[]=795343", gotPath, gotQuery)
	}
}

// DJ01's Swarm 2024.2 ignores the change filter on v11 and answers with its 50
// newest reviews, which buries the review of any older changelist; v9 filters.
// The fixture is the v9 answer the real server gave for the run-4 changelist.
func TestSwarmListFiltersByChangelistOnV9(t *testing.T) {
	real, err := os.ReadFile(filepath.Join("testdata", "real", "swarm-v9-reviews-change.json"))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/api/v9/reviews" || r.URL.Query().Get("change[]") != "795543" {
			// What v11 does on DJ01: drop the filter, list the newest reviews.
			_, _ = w.Write([]byte(`{"error":null,"messages":[],"data":{"reviews":[{"id":1,"author":"bob","changes":[2]}]}}`))
			return
		}
		_, _ = w.Write(real)
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("P4_FAKE_LOG", filepath.Join(dir, "p4.log"))
	t.Setenv("P4USER", "alice")
	cache := &Cache{P4Path: writeFakeP4(t, dir), HTTPClient: srv.Client()}
	result, err := cache.Swarm(context.Background(), SwarmParams{
		Ref:        p4depot.Ref{Port: "p4:1666", Depot: "//DJ01_Proj/Dev", SwarmURL: srv.URL},
		Action:     SwarmList,
		Changelist: "795543",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Reviews) != 1 {
		t.Fatalf("reviews = %+v, want the one review of CL 795543 (requests: %v)", result.Reviews, paths)
	}
	review := result.Reviews[0]
	if review.ID != 795547 || review.Author != "yujian01" || review.State != "needsReview" {
		t.Fatalf("review = %+v", review)
	}
	if review.URL != srv.URL+"/reviews/795547" {
		t.Fatalf("url = %q", review.URL)
	}
	if len(paths) != 1 || paths[0] != "/api/v9/reviews" {
		t.Fatalf("requests = %v, want a single v9 list", paths)
	}
}

func TestReviewsForChangeMatchesAnyListedChange(t *testing.T) {
	reviews := []SwarmReview{
		{ID: 1, Changes: []int{100, 101}},
		{ID: 2, Changes: []int{795343, 795344}},
		{ID: 3, Changes: []int{795343}},
	}
	got := reviewsForChange(reviews, "795343")
	if len(got) != 2 || got[0].ID != 2 || got[1].ID != 3 {
		t.Fatalf("got %+v, want reviews 2 and 3", got)
	}
	if got := reviewsForChange(reviews, "not-a-number"); got != nil {
		t.Fatalf("non-numeric changelist matched %+v", got)
	}
}
