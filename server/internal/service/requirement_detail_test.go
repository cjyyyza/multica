package service

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRequirementProjectionStates(t *testing.T) {
	pool := sharedTestPool(t)
	fx := testutil.New(pool, "", "")
	workspaceID := fx.Workspace(t, "Requirement projection", "requirement-projection-"+uuid.NewString())
	userID := fx.User(t, "Requirement projection user", uuid.NewString()+"@example.test")
	fx = testutil.New(pool, workspaceID, userID)
	svc := NewRequirementService(db.New(pool), pool)
	ctx := context.Background()

	cancelled := db.Requirement{WorkspaceID: requirementTestUUID(t, workspaceID), IntakeState: "awaiting_approval"}
	cancelled.CancelledAt.Valid = true
	projection, err := svc.requirementProjection(ctx, svc.Queries, cancelled, false)
	if err != nil || projection.State != "cancelled" {
		t.Fatalf("cancelled overlay = %+v, %v", projection, err)
	}

	deleted := db.Requirement{WorkspaceID: requirementTestUUID(t, workspaceID), IntakeState: "clarifying", IssueID: requirementTestUUID(t, uuid.NewString())}
	projection, err = svc.requirementProjection(ctx, svc.Queries, deleted, false)
	if err != nil || projection.State != "cancelled" || !projection.IssueDeleted {
		t.Fatalf("deleted issue projection = %+v, %v", projection, err)
	}

	issueID := fx.Issue(t, "Requirement projection review", testutil.Cols{"status": "in_review"})
	row := db.Requirement{WorkspaceID: requirementTestUUID(t, workspaceID), IntakeState: "clarifying", IssueID: requirementTestUUID(t, issueID)}
	projection, err = svc.requirementProjection(ctx, svc.Queries, row, false)
	if err != nil || projection.State != "in_review" || projection.IssueDeleted {
		t.Fatalf("review projection = %+v, %v", projection, err)
	}
	row.CancelledAt.Valid = true
	projection, err = svc.requirementProjection(ctx, svc.Queries, row, false)
	if err != nil || projection.State != "cancelled" {
		t.Fatalf("cancelled issue overlay = %+v, %v", projection, err)
	}

	fx.Insert(t, "issue_status", testutil.Cols{
		"workspace_id": workspaceID, "key": "custom_done", "name": "Custom done", "category": "done", "color": "#000000", "position": 1,
	})
	customID := fx.Issue(t, "Requirement projection custom", testutil.Cols{"status": "custom_done"})
	row = db.Requirement{WorkspaceID: requirementTestUUID(t, workspaceID), IntakeState: "clarifying", IssueID: requirementTestUUID(t, customID)}
	projection, err = svc.requirementProjection(ctx, svc.Queries, row, false)
	if err != nil || projection.State != "done" {
		t.Fatalf("custom category projection = %+v, %v", projection, err)
	}
}

func TestRequirementStateForIssueCategories(t *testing.T) {
	for input, want := range map[string]string{
		"backlog|": "executing", "blocked|": "blocked", "in_review|": "in_review",
		"custom|unstarted": "executing", "custom|started": "executing", "custom|done": "done", "custom|closed": "cancelled",
	} {
		status, category, _ := strings.Cut(input, "|")
		if got, ok := requirementStateForIssue(status, category); !ok || got != want {
			t.Errorf("state(%s, %s) = %s, %v; want %s", status, category, got, ok, want)
		}
	}
}

// A requirement with more delivery commands than one page must still project
// its newest deliveries.
func TestRequirementDeliveryPaginationKeepsNewestDelivery(t *testing.T) {
	pool := sharedTestPool(t)
	fx := testutil.New(pool, "", "")
	workspaceID := fx.Workspace(t, "Requirement outbox pagination", "requirement-outbox-pagination-"+uuid.NewString())
	userID := fx.User(t, "Requirement outbox user", uuid.NewString()+"@example.test")
	fx = testutil.New(pool, workspaceID, userID)
	fx.Member(t, workspaceID, userID, "owner")
	runtimeID := fx.Runtime(t, "Requirement outbox runtime")
	agentID := fx.Agent(t, "Requirement outbox agent", runtimeID)
	issueID := fx.Issue(t, "Requirement outbox issue", testutil.Cols{"status": "blocked"})
	questionID := fx.Comment(t, issueID, "Need an answer")
	requirementID := fx.Insert(t, "requirement", testutil.Cols{
		"workspace_id": workspaceID, "source_host": "dj01.pm.netease.com", "external_id": "94201",
		"source_url": "https://dj01.pm.netease.com/issues/94201", "source_snapshot": testutil.Raw(`'{}'::jsonb`),
		"source_digest": "outbox-pagination", "requester_id": userID, "agent_id": agentID,
		"issue_id": issueID, "intake_state": "clarifying",
	})
	fx.Cleanup(t, `DELETE FROM popo_bridge_command WHERE requirement_id = $1`, requirementID)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO popo_bridge_command (
			workspace_id, type, delivery_id, payload, status, requirement_id,
			requirement_kind, occurrence_id, recipient_user_id, requirement_revision,
			snapshot_payload, created_at, updated_at
		)
		SELECT $1, 'send', gen_random_uuid(), '{}'::jsonb, 'pending', $2,
			'requirement_status', gen_random_uuid(), $3, 1,
			'{"version":1}'::jsonb, now() + (i || ' seconds')::interval, now() + (i || ' seconds')::interval
		FROM generate_series(1, 250) AS i`, workspaceID, requirementID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO popo_bridge_command (
			workspace_id, type, delivery_id, payload, status, requirement_id,
			requirement_kind, occurrence_id, recipient_user_id, requirement_revision,
			snapshot_payload, created_at, updated_at
		) VALUES ($1, 'send', gen_random_uuid(), '{}'::jsonb, 'pending', $2,
			'requirement_blocked', $3, $4, 1, '{"version":1}'::jsonb,
			now() + interval '251 seconds', now() + interval '251 seconds')`,
		workspaceID, requirementID, questionID, userID); err != nil {
		t.Fatal(err)
	}

	svc := NewRequirementService(db.New(pool), pool)
	row := db.Requirement{
		ID: requirementTestUUID(t, requirementID), WorkspaceID: requirementTestUUID(t, workspaceID),
		IssueID: requirementTestUUID(t, issueID), AgentID: requirementTestUUID(t, agentID),
	}
	deliveries, err := svc.requirementDeliveries(ctx, svc.Queries, row)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 251 {
		t.Fatalf("requirement deliveries = %d, want all 251 delivery commands", len(deliveries))
	}
	if deliveries[len(deliveries)-1].Kind != "requirement_blocked" {
		t.Fatalf("newest delivery = %+v, want the blocked notification", deliveries[len(deliveries)-1])
	}
}
