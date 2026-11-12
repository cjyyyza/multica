package notify

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func readerServer(t *testing.T, resourcePath string, payload func() string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer member-token" || r.Header.Get("X-Workspace-ID") != "ws" || !strings.Contains(r.Header.Get("X-Client-Capabilities"), "requirement-candidate-provenance-v1") {
			t.Errorf("incorrect read-only request context: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/workspaces/ws":
			fmt.Fprint(w, `{"id":"ws","slug":"team"}`)
		case resourcePath:
			fmt.Fprint(w, payload())
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRequirementReaderTracksNativeStatesAndConcreteQuestion(t *testing.T) {
	payload := `{"id":"req","workspaceId":"ws","revision":7,"state":"blocked","sourceSnapshot":{"title":"Native requirement"},"issue":{"id":"issue","identifier":"MUL-7","title":"Fix delivery"},"blockedQuestion":{"commentId":"q-1","content":"应该保留哪个结果？","targetUserId":"a-multica-user","canReply":false}}`
	server := readerServer(t, "/api/workspaces/ws/requirements/req", func() string { return payload })
	reader := &Reader{Client: cli.NewAPIClient(server.URL, "ws", "member-token"), Source: Source{Kind: "requirement", ID: "req"}, AppURL: "https://multica.example"}
	snapshot, err := reader.Read(context.Background())
	if err != nil || snapshot.Status != "blocked" || snapshot.Title != "MUL-7: Fix delivery" || snapshot.QuestionID != "q-1" || snapshot.Question != "应该保留哪个结果？" || snapshot.URL != "https://multica.example/team/chat?requirement=req" {
		t.Fatalf("blocked detail: %+v %v", snapshot, err)
	}
	// Reading a notification never grants answer or approval permission.
	for _, state := range []string{"intake_pending", "clarifying", "awaiting_approval", "executing", "in_review", "done", "cancelled"} {
		payload = fmt.Sprintf(`{"id":"req","workspaceId":"ws","revision":8,"state":%q,"sourceSnapshot":{"title":"Native requirement"},"run":{"status":"completed"},"latestCandidate":{"id":"candidate-1"}}`, state)
		snapshot, err = reader.Read(context.Background())
		if err != nil || snapshot.Status != state || snapshot.CandidateID != "candidate-1" {
			t.Fatalf("state %s: %+v %v", state, snapshot, err)
		}
	}
}

func TestReaderRejectsMalformedOrCrossWorkspaceRequirement(t *testing.T) {
	for _, payload := range []string{
		`{}`,
		`{"id":"req","workspaceId":"another","revision":1,"state":"done"}`,
		`{"id":"wrong","workspaceId":"ws","revision":1,"state":"done"}`,
		`{"id":"req","workspaceId":"ws","revision":1,"state":"invented"}`,
		`{"id":"req","workspaceId":"ws","revision":1,"state":"blocked","blockedQuestion":{"commentId":"q"}}`,
		`{"id":"req","workspaceId":"ws","revision":1,"state":"cancelled","issueDeleted":true}`,
	} {
		server := readerServer(t, "/api/workspaces/ws/requirements/req", func() string { return payload })
		reader := &Reader{Client: cli.NewAPIClient(server.URL, "ws", "member-token"), Source: Source{Kind: "requirement", ID: "req"}, AppURL: "https://multica.example"}
		if _, err := reader.Read(context.Background()); err == nil {
			t.Fatalf("invalid requirement accepted: %s", payload)
		}
	}
}

func TestIssueReaderUsesCategoryAndKeepsRequirementOwnership(t *testing.T) {
	for _, tc := range []struct{ name, fields, want string }{
		{"custom done", `"status":"shipped","status_category":"done","requirement_binding_known":true`, "done"},
		{"built-in review", `"status":"in_review","requirement_binding_known":true`, "in_review"},
		{"unknown custom category", `"status":"shipped","requirement_binding_known":true`, ""},
		{"missing binding marker", `"status":"done"`, ""},
		{"requirement-owned", `"status":"done","requirement_binding_known":true,"requirement_id":"req"`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := `{"id":"issue","workspace_id":"ws","identifier":"MUL-7","title":"修复交付","revision":5,` + tc.fields + `}`
			server := readerServer(t, "/api/issues/MUL-7", func() string { return payload })
			reader := &Reader{Client: cli.NewAPIClient(server.URL, "ws", "member-token"), Source: Source{Kind: "issue", ID: "MUL-7"}, AppURL: "https://multica.example"}
			snapshot, err := reader.Read(context.Background())
			if tc.want == "" {
				if err == nil {
					t.Fatalf("invalid issue accepted: %+v", snapshot)
				}
				return
			}
			if err != nil || snapshot.ID != "issue" || snapshot.Status != tc.want || snapshot.URL != "https://multica.example/team/issues/MUL-7" {
				t.Fatalf("issue projection: %+v %v", snapshot, err)
			}
		})
	}
}
