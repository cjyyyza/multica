package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

func planTaskFixture() Task {
	return Task{ID: "task-1", Mode: "plan", WorkspaceID: "workspace-1", RequirementID: "req-1", AgentID: "agent-1", ChatSessionID: "chat-1", AuthToken: "mat_plan-only"}
}

func planDetailFixture() map[string]any {
	return map[string]any{"id": "req-1", "workspaceId": "workspace-1", "agentId": "agent-1", "chatSessionId": "chat-1", "issueId": nil, "revision": 7, "sourceSnapshot": map[string]any{"title": "Original source"}}
}

func TestRunTaskLoadsPlanContextOnlyAfterStartAcknowledgement(t *testing.T) {
	for _, rejectStart := range []bool{false, true} {
		name := "running task reads context"
		if rejectStart {
			name = "rejected start never reads context"
		}
		t.Run(name, func(t *testing.T) {
			var started atomic.Bool
			var reads atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/daemon/tasks/task-1/start":
					if rejectStart {
						w.WriteHeader(http.StatusConflict)
						return
					}
					started.Store(true)
				case "/api/workspaces/workspace-1/requirements/req-1":
					reads.Add(1)
					if !started.Load() {
						http.Error(w, "task is not running", http.StatusForbidden)
						return
					}
					_ = json.NewEncoder(w).Encode(planDetailFixture())
					return
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			d := &Daemon{
				client: NewClient(server.URL), logger: logger,
				workspaces: make(map[string]*workspaceState), activeEnvRoots: make(map[string]int),
				runtimeIndex: map[string]Runtime{"rt-1": {ID: "rt-1", Provider: "claude"}},
				cfg: Config{WorkspacesRoot: t.TempDir(), Agents: map[string]AgentEntry{
					"claude": {Path: filepath.Join(t.TempDir(), "missing-claude")},
				}},
			}
			task := planTaskFixture()
			task.PlanPermission = taskpolicy.PlanPermissionClaude
			task.RuntimeID = "rt-1"
			task.Agent = &AgentData{ID: task.AgentID, Name: "test-agent"}
			_, err := d.runTask(t.Context(), task, "claude", 0, logger)
			if rejectStart {
				if reads.Load() != 0 || err == nil || !strings.Contains(err.Error(), "start task failed") {
					t.Fatalf("rejected start: context reads=%d, error=%v", reads.Load(), err)
				}
			} else if !started.Load() || reads.Load() != 1 || (err != nil && strings.Contains(err.Error(), "load planning requirement")) {
				t.Fatalf("Plan context must follow the running acknowledgement: started=%v, reads=%d, error=%v", started.Load(), reads.Load(), err)
			}
		})
	}
}

func TestRequirementExecutePromptCarriesRecordedHumanApproval(t *testing.T) {
	for _, tc := range []struct {
		name     string
		task     Task
		approved bool
	}{
		{"approved Issue", Task{Mode: "execute", IssueID: "issue-1", RequirementID: "req-1"}, true},
		{"ordinary Issue", Task{Mode: "execute", IssueID: "issue-1"}, false},
		{"Plan", planTaskFixture(), false},
		{"unbound Requirement", Task{Mode: "execute", RequirementID: "req-1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prompt := BuildPrompt(tc.task, "claude")
			if strings.Contains(prompt, "[Approved Requirement execution]") != tc.approved {
				t.Fatalf("recorded approval present=%v, want %v: %s", !tc.approved, tc.approved, prompt)
			}
			if tc.approved {
				for _, want := range []string{"issue-1", "req-1", "already approved in Multica Chat", "Do not ask for the same approval again", "Issue comments"} {
					if !strings.Contains(prompt, want) {
						t.Errorf("missing approval context %q", want)
					}
				}
			}
		})
	}
}

func TestLoadRequirementPlanContextChecksBindingAndUsesTaskToken(t *testing.T) {
	for _, field := range []string{"", "id", "workspaceId", "agentId", "chatSessionId", "issueId", "revision"} {
		t.Run(field, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/workspaces/workspace-1/requirements/req-1" || r.Header.Get("Authorization") != "Bearer mat_plan-only" {
					t.Errorf("unexpected scoped requirement read: %s %s", r.Method, r.URL.Path)
				}
				detail := planDetailFixture()
				if field == "revision" {
					detail[field] = 0
				} else if field != "" {
					detail[field] = "another-resource"
				}
				_ = json.NewEncoder(w).Encode(detail)
			}))
			defer server.Close()
			client := NewClient(server.URL)
			client.token = "pat_control-plane"
			plan, err := client.loadRequirementPlanContext(t.Context(), planTaskFixture())
			if field != "" {
				if err == nil || plan != nil {
					t.Fatal("mismatched planning resource accepted")
				}
			} else if err != nil || plan.Revision != 7 || !strings.Contains(string(plan.Snapshot), "Original source") {
				t.Fatalf("captured plan = %+v, %v", plan, err)
			}
			if client.token != "pat_control-plane" {
				t.Fatal("task credential replaced the control-plane token")
			}
		})
	}
}

func TestLoadRequirementPlanContextDoesNotUsePATForMissingTaskToken(t *testing.T) {
	client := NewClient("http://unused.test")
	client.token = "pat_control-plane"
	task := planTaskFixture()
	task.Mode = "execute"
	if plan, err := client.loadRequirementPlanContext(t.Context(), task); plan != nil || err != nil {
		t.Fatalf("execute task tried to load Plan context: %+v %v", plan, err)
	}
	task.Mode = "plan"
	for _, token := range []string{"", "pat_control-plane"} {
		task.AuthToken = token
		if plan, err := client.loadRequirementPlanContext(t.Context(), task); plan != nil || err == nil {
			t.Fatal("missing task credential accepted")
		}
	}
}

func TestDecodeRequirementPlanOutputRequiresExplicitStructuredEnvelope(t *testing.T) {
	for _, invalid := range []string{"", `null`, `{}`, `{"reply":"Ready"}`, `{"reply":" ","candidate":null}`, `{"reply":"Ready","candidate":[]}`, `{"reply":"Ready","candidate":null,"approve":true}`, `{"reply":"Ready","candidate":null} {}`} {
		if _, _, err := decodeRequirementPlanOutput(json.RawMessage(invalid)); err == nil {
			t.Errorf("accepted invalid output %q", invalid)
		}
	}
	reply, candidate, err := decodeRequirementPlanOutput(json.RawMessage(`{"reply":"What is the acceptance criterion?","candidate":null}`))
	if err != nil || candidate != nil || reply != "What is the acceptance criterion?" {
		t.Fatalf("clarification = %q, %s, %v", reply, candidate, err)
	}
}

func TestSubmitRequirementPlanOutputRetriesSameCandidateWithoutApproving(t *testing.T) {
	for _, tc := range []struct {
		name      string
		firstCode int
		wantCalls int
		wantErr   bool
		lostReply bool
	}{
		{name: "saved", wantCalls: 1},
		{name: "lost success response", wantCalls: 2, lostReply: true},
		{name: "stale revision", firstCode: http.StatusConflict, wantCalls: 1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			var firstKey, firstBody string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/api/workspaces/workspace-1/requirements/req-1/candidates" || r.Header.Get("Authorization") != "Bearer mat_plan-only" {
					t.Errorf("unexpected candidate request: %s %s", r.Method, r.URL.Path)
				}
				data, _ := io.ReadAll(r.Body)
				key := r.Header.Get("Idempotency-Key")
				if calls == 1 {
					firstKey, firstBody = key, string(data)
				} else if key != firstKey || string(data) != firstBody {
					t.Error("retry changed candidate identity or content")
				}
				if !strings.HasPrefix(key, "plan:task-1:") {
					t.Errorf("candidate has no stable task identity: %q", key)
				}
				var body struct {
					ExpectedRevision int64           `json:"expectedRevision"`
					Snapshot         json.RawMessage `json:"snapshot"`
				}
				if err := json.Unmarshal(data, &body); err != nil || body.ExpectedRevision != 7 || !strings.Contains(string(body.Snapshot), "Temporary directory only") {
					t.Errorf("candidate lost captured revision or content: %s", data)
				}
				if calls == 1 && tc.lostReply {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				if calls == 1 && tc.firstCode != 0 {
					w.WriteHeader(tc.firstCode)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			client := NewClient(server.URL)
			client.token = "pat_control-plane"
			raw := json.RawMessage(`{"reply":"Please review this plan in Multica Chat.","candidate":{"goal":"Acceptance test","nonGoals":["No business changes"],"scope":["Temporary directory only"],"constraints":["No external writes"],"acceptanceCriteria":["One file"],"projectId":null,"agentId":"agent-1"}}`)
			reply, err := client.submitRequirementPlanOutput(context.Background(), planTaskFixture(), &requirementPlanContext{Revision: 7}, raw)
			if (err != nil) != tc.wantErr || calls != tc.wantCalls {
				t.Fatalf("save plan: reply=%q err=%v calls=%d", reply, err, calls)
			}
			if !tc.wantErr && reply != "Please review this plan in Multica Chat." {
				t.Fatalf("structured JSON leaked into the chat reply: %q", reply)
			}
		})
	}
}

func TestSubmitRequirementPlanClarificationDoesNotCreateCandidate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("clarification made a write: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	reply, err := NewClient(server.URL).submitRequirementPlanOutput(t.Context(), planTaskFixture(), &requirementPlanContext{Revision: 7}, json.RawMessage(`{"reply":"Which deliverable do you need?","candidate":null}`))
	if err != nil || reply != "Which deliverable do you need?" {
		t.Fatalf("clarification reply=%q err=%v", reply, err)
	}
}

func TestPlanPromptUsesFreshSnapshotWithoutShellInstructions(t *testing.T) {
	task := planTaskFixture()
	task.ChatMessage = "Use a temporary directory only."
	task.PriorSessionResumeUnavailable = true
	task.ChatMessageAttachments = []ChatAttachmentMeta{{ID: "private-attachment", Filename: "context.txt"}}
	prompt := BuildPrompt(task, "claude", WithRequirementPlanSnapshot(json.RawMessage(`{"revision":8,"sourceSnapshot":{"title":"Latest source"}}`)))
	for _, want := range []string{"Latest source", `"revision":8`, task.ChatMessage, "Only the user can approve in Multica Chat", "continuity gap"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("plan prompt missing %q", want)
		}
	}
	for _, forbidden := range []string{"multica chat history", "multica attachment download", "candidate.json", "multica requirement candidate"} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("plan prompt advertised denied command %q", forbidden)
		}
	}
}
