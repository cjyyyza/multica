package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

var requirementWorkspaceTables = []string{
	"requirement_outbox",
	"requirement_action",
	"requirement_event",
	"requirement_wakeup",
	"requirement_approval",
	"requirement_candidate",
	"requirement_participant",
	"requirement",
}

type requirementDeleteFixture struct {
	workspaceID   string
	requirementID string
	issueID       string
	chatSessionID string
}

func newRequirementDeleteFixture(t *testing.T, workspaceID, slug, chatSessionID string) requirementDeleteFixture {
	t.Helper()
	ctx := context.Background()
	purgeRequirementDeleteFixture(t, workspaceID)

	if _, err := testPool.Exec(ctx, `
INSERT INTO workspace (id, name, slug) VALUES ($1, $2, $3)
`, workspaceID, "Requirement delete "+slug, slug); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		for _, table := range requirementWorkspaceTables {
			_, _ = testPool.Exec(context.Background(),
				`DELETE FROM `+table+` WHERE workspace_id = $1`, workspaceID)
		}
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
	})
	if _, err := testPool.Exec(ctx, `
INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')
`, workspaceID, testUserID); err != nil {
		t.Fatalf("create owner: %v", err)
	}

	f := requirementDeleteFixture{workspaceID: workspaceID, chatSessionID: chatSessionID}
	if err := testPool.QueryRow(ctx, `
INSERT INTO issue (workspace_id, title, creator_type, creator_id, origin_type, origin_id)
VALUES ($1, 'Requirement delete issue', 'member', $2, 'requirement_chat', $3)
RETURNING id
`, workspaceID, testUserID, chatSessionID).Scan(&f.issueID); err != nil {
		t.Fatalf("create requirement issue: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
INSERT INTO requirement (
    workspace_id, source_host, external_id, source_snapshot, source_digest,
    requester_id, agent_id, chat_session_id, issue_id, intake_operation_id
) VALUES ($1, 'popo', $2, '{}'::jsonb, 'source-digest', $3, $3, $4, $5, gen_random_uuid())
RETURNING id
`, workspaceID, "external-"+slug, testUserID, chatSessionID, f.issueID).Scan(&f.requirementID); err != nil {
		t.Fatalf("create requirement: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
INSERT INTO requirement_participant (workspace_id, requirement_id, user_id, role)
VALUES ($1, $2, $3, 'requester')
`, workspaceID, f.requirementID, testUserID); err != nil {
		t.Fatalf("create requirement participant: %v", err)
	}

	var candidateID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO requirement_candidate (
    workspace_id, requirement_id, revision, snapshot, digest, created_by_task_id
) VALUES ($1, $2, 1, '{}'::jsonb, 'candidate-digest', gen_random_uuid())
RETURNING id
`, workspaceID, f.requirementID).Scan(&candidateID); err != nil {
		t.Fatalf("create requirement candidate: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
INSERT INTO requirement_approval (
    workspace_id, requirement_id, candidate_id, candidate_digest,
    approved_by, issue_id, initial_task_id
) VALUES ($1, $2, $3, 'candidate-digest', $4, $5, gen_random_uuid())
`, workspaceID, f.requirementID, candidateID, testUserID, f.issueID); err != nil {
		t.Fatalf("create requirement approval: %v", err)
	}

	var eventID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO requirement_event (workspace_id, requirement_id, actor_type, actor_id, action)
VALUES ($1, $2, 'member', $3, 'approved')
RETURNING id
`, workspaceID, f.requirementID, testUserID).Scan(&eventID); err != nil {
		t.Fatalf("create requirement event: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
INSERT INTO requirement_wakeup (
    workspace_id, requirement_id, question_comment_id, current_wakeup_id, issue_revision
) VALUES ($1, $2, gen_random_uuid(), gen_random_uuid(), 1)
`, workspaceID, f.requirementID); err != nil {
		t.Fatalf("create requirement wakeup: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
INSERT INTO requirement_action (
    workspace_id, requirement_id, actor_type, actor_id, action,
    idempotency_key, request_hash, response_status, response_body
) VALUES ($1, $2, 'member', $3, 'approve', $4, 'request-hash', 200, '{}'::jsonb)
`, workspaceID, f.requirementID, testUserID, "action-"+f.requirementID); err != nil {
		t.Fatalf("create requirement action: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
INSERT INTO requirement_outbox (
    workspace_id, requirement_id, kind, occurrence_id, recipient_binding,
    delivery_id, requirement_revision
) VALUES ($1, $2, 'requirement_status', $3, '{}'::jsonb, gen_random_uuid(), 1)
`, workspaceID, f.requirementID, eventID); err != nil {
		t.Fatalf("create requirement outbox: %v", err)
	}
	return f
}

func purgeRequirementDeleteFixture(t *testing.T, workspaceID string) {
	t.Helper()
	for _, table := range requirementWorkspaceTables {
		if _, err := testPool.Exec(context.Background(),
			`DELETE FROM `+table+` WHERE workspace_id = $1`, workspaceID); err != nil {
			t.Fatalf("purge %s: %v", table, err)
		}
	}
	if _, err := testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID); err != nil {
		t.Fatalf("purge workspace: %v", err)
	}
}

func TestRequirementIssueDeletePreservesBinding(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementDeleteFixture(t,
		"9f26f085-fc1b-4f26-9f26-000000000101", "requirement-delete-binding",
		"9f26f085-fc1b-4f26-9f26-000000000201")

	if _, err := testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, f.issueID); err != nil {
		t.Fatalf("delete issue: %v", err)
	}
	var issueID, chatSessionID string
	if err := testPool.QueryRow(context.Background(), `
SELECT issue_id::text, chat_session_id::text FROM requirement WHERE id = $1
`, f.requirementID).Scan(&issueID, &chatSessionID); err != nil {
		t.Fatalf("read preserved requirement: %v", err)
	}
	if issueID != f.issueID || chatSessionID != f.chatSessionID {
		t.Fatalf("ordinary issue delete changed permanent binding: issue=%s chat=%s", issueID, chatSessionID)
	}
}

func TestDeleteWorkspaceRequirementData(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	victim := newRequirementDeleteFixture(t,
		"9f26f085-fc1b-4f26-9f26-000000000102", "requirement-delete-victim",
		"9f26f085-fc1b-4f26-9f26-000000000202")
	neighbour := newRequirementDeleteFixture(t,
		"9f26f085-fc1b-4f26-9f26-000000000103", "requirement-delete-neighbour",
		"9f26f085-fc1b-4f26-9f26-000000000203")

	w := httptest.NewRecorder()
	req := newRequest(http.MethodDelete, "/api/workspaces/"+victim.workspaceID, nil)
	req = withURLParam(req, "id", victim.workspaceID)
	testHandler.DeleteWorkspace(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DeleteWorkspace = %d, want 204: %s", w.Code, w.Body.String())
	}

	for _, table := range requirementWorkspaceTables {
		var victimCount, neighbourCount int
		if err := testPool.QueryRow(context.Background(), `
SELECT count(*) FILTER (WHERE workspace_id = $1),
       count(*) FILTER (WHERE workspace_id = $2)
FROM `+table, victim.workspaceID, neighbour.workspaceID).Scan(&victimCount, &neighbourCount); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if victimCount != 0 || neighbourCount != 1 {
			t.Errorf("%s rows after delete: victim=%d neighbour=%d, want 0 and 1", table, victimCount, neighbourCount)
		}
	}
	if rowExists(t, "workspace", victim.workspaceID) {
		t.Error("victim workspace still exists")
	}
	if !rowExists(t, "workspace", neighbour.workspaceID) {
		t.Error("neighbour workspace was deleted")
	}
}

func TestRequirementChatOriginIsUnique(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	first := newRequirementDeleteFixture(t,
		"9f26f085-fc1b-4f26-9f26-000000000104", "requirement-origin-first",
		"9f26f085-fc1b-4f26-9f26-000000000204")
	second := newRequirementDeleteFixture(t,
		"9f26f085-fc1b-4f26-9f26-000000000105", "requirement-origin-second",
		"9f26f085-fc1b-4f26-9f26-000000000205")

	_, err := testPool.Exec(context.Background(), `
INSERT INTO issue (workspace_id, title, creator_type, creator_id, origin_type, origin_id)
VALUES ($1, 'Duplicate requirement origin', 'member', $2, 'requirement_chat', $3)
`, second.workspaceID, testUserID, first.chatSessionID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("duplicate requirement_chat origin error = %v, want SQLSTATE 23505", err)
	}
}
