package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/popo"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func newRequirementPopoBlockedFixture(t *testing.T, externalID string, participants []service.RequirementParticipantInput, targetUserID string) (requirementPopoFixture, requirementBlockResponse, requirementReviewFixture) {
	t.Helper()
	f := newRequirementPopoFixtureWithParticipants(t, externalID, participants)
	opened := openRequirementForTest(t, testUserID, f.detail.ID, 1, "reliability-open-"+externalID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, opened.PendingTask.TaskID)
	submitted := submitCandidateForTest(t, f.env.agentID, opened.PendingTask.TaskID, f.detail.ID, opened.Session.ID,
		validCandidateRequest(t, opened.Requirement, 2), "reliability-candidate-"+externalID)
	f.detail = submitted
	approved, _ := approveRequirementForTest(t, requirementApproveFixture{
		Detail: f.detail, Opened: opened, AgentID: f.env.agentID, Source: f.source,
		OperationID: f.operationID, InstallationID: f.env.install, BindingID: f.routeBinding,
	}, "reliability-approve-"+externalID)
	issueResponse := approveIssueForTest(t, approved)
	f.detail = approved.Requirement
	dbfx.Cleanup(t, `DELETE FROM issue WHERE origin_type = 'requirement_chat' AND origin_id = $1`, opened.Session.ID)
	dbfx.Cleanup(t, `DELETE FROM yixiezuo_import WHERE workspace_id = $1 AND source_host = $2 AND external_id = $3`, testWorkspaceID, f.source.Source.Host, f.source.Source.ID)
	dbfx.Cleanup(t, `DELETE FROM channel_issue_source WHERE installation_id = $1`, f.env.install)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE requirement_id = $1 OR issue_id IN (SELECT id FROM issue WHERE origin_type = 'requirement_chat' AND origin_id = $2)`, f.detail.ID, opened.Session.ID)
	dbfx.Cleanup(t, `DELETE FROM yixiezuo_operation WHERE issue_id IN (SELECT id FROM issue WHERE origin_type = 'requirement_chat' AND origin_id = $1)`, opened.Session.ID)

	var run AgentTaskResponse
	if err := json.Unmarshal(approved.Requirement.Run, &run); err != nil {
		t.Fatal(err)
	}
	issue, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issueResponse.ID))
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, run.ID)
	blockFixture := requirementReviewFixture{
		requirementApproveFixture: requirementApproveFixture{Detail: f.detail, Opened: opened, AgentID: f.env.agentID, Source: f.source},
		Issue:                     issue,
	}
	response := testutil.Call(t, testHandler.BlockRequirement, requirementBlockRequest(blockFixture,
		run.ID, "Original question", targetUserID, issue.Revision, "reliability-block-"+externalID)).Want(http.StatusOK)
	var blocked requirementBlockResponse
	response.JSON(&blocked)
	registerAllRequirementBottomCleanup(t, issue.ID)
	f.detail = blocked.Requirement
	return f, blocked, blockFixture
}

func requirementDeliveryForRecipientAndKind(t *testing.T, requirementID, userID, kind string) db.PopoBridgeCommand {
	t.Helper()
	for _, row := range requirementDeliveryCommandsForTest(t, requirementID) {
		if row.RequirementKind.String == kind && row.RecipientUserID.Valid && uuidToString(row.RecipientUserID) == userID {
			return row
		}
	}
	t.Fatalf("no %s delivery for recipient %s", kind, userID)
	return db.PopoBridgeCommand{}
}

func TestRequirementDeliveryRetryUsesFrozenPayloadAndRevisionFences(t *testing.T) {
	t.Run("issue revision and edited question", func(t *testing.T) {
		f, blocked, _ := newRequirementPopoBlockedFixture(t, "98301", nil, testUserID)
		enableRequirementReceiptAttempt(t, f)
		worker := requirementOutboxWorkerForTest()
		if _, err := worker.RunOnce(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
		delivery := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_blocked")
		original := decodeRequirementSendPayload(t, delivery)
		if original.RequestHash == "" || !delivery.RequestHash.Valid || original.RequestHash != delivery.RequestHash.String ||
			!strings.Contains(original.Text, "Original question") {
			t.Fatalf("initial frozen payload = %+v hash=%q", original, delivery.RequestHash.String)
		}
		frozen := append([]byte(nil), delivery.Payload...)
		receipt := popo.CommandReceipt{
			Status: "failed", Error: "confirmed not sent",
			NotSent: boolPtr(true), Retryable: boolPtr(true), EvidenceFinalized: boolPtr(true),
		}
		if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID), receipt); err != nil {
			t.Fatal(err)
		}
		delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_blocked")
		if delivery.Status != "pending" || delivery.Attempt != 2 {
			t.Fatalf("confirmed failed retry = %+v", delivery)
		}
		dbfx.Exec(t, `UPDATE comment SET content = 'Edited after enqueue' WHERE id = $1`, blocked.QuestionComment.ID)
		dbfx.Exec(t, `UPDATE issue SET description = 'Edited after enqueue', revision = revision + 1 WHERE id = $1`, blocked.Requirement.IssueID)
		if result, err := worker.RunOnce(context.Background(), 10); err != nil || result.Claimed != 0 {
			t.Fatalf("prepared retry was re-claimed: %+v, %v", result, err)
		}
		allowed, err := testHandler.PopoBridge.CanDeliver(context.Background(), delivery.ID, parseUUID(f.env.bridgeID))
		if err != nil || allowed {
			t.Fatalf("edited occurrence authorize = %v, %v", allowed, err)
		}
		delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_blocked")
		if delivery.Status != "superseded" || delivery.Attempt != 2 {
			t.Fatalf("edited occurrence was retried: %+v", delivery)
		}
		if string(delivery.Payload) != string(frozen) {
			t.Fatal("frozen delivery payload changed after the question edit")
		}
		if got := decodeRequirementSendPayload(t, delivery).Text; got != original.Text || strings.Contains(got, "Edited after enqueue") {
			t.Fatalf("retry changed notification content: before=%q after=%q", original.Text, got)
		}
	})

	t.Run("requirement revision without participant event", func(t *testing.T) {
		f := newRequirementPopoFixture(t, "98302")
		enableRequirementReceiptAttempt(t, f)
		worker := requirementOutboxWorkerForTest()
		if _, err := worker.RunOnce(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
		delivery := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
		original := decodeRequirementSendPayload(t, delivery)
		receipt := popo.CommandReceipt{
			Status: "failed", Error: "confirmed not sent",
			NotSent: boolPtr(true), Retryable: boolPtr(true), EvidenceFinalized: boolPtr(true),
		}
		if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID), receipt); err != nil {
			t.Fatal(err)
		}
		dbfx.Exec(t, `UPDATE requirement SET revision = revision + 1, source_snapshot = jsonb_set(source_snapshot, '{title}', '"Edited title"') WHERE id = $1`, f.detail.ID)
		delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
		if delivery.Status != "pending" || delivery.Attempt != 2 {
			t.Fatalf("requirement revision retry setup = %+v", delivery)
		}
		allowed, err := testHandler.PopoBridge.CanDeliver(context.Background(), delivery.ID, parseUUID(f.env.bridgeID))
		if err != nil || allowed {
			t.Fatalf("requirement revision authorize = %v, %v", allowed, err)
		}
		delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
		if delivery.Status != "superseded" || decodeRequirementSendPayload(t, delivery).Text != original.Text {
			t.Fatalf("requirement revision did not fence original identity: %+v", delivery)
		}
	})
}

func TestRequirementParticipantChangesPreserveValidBlockedOccurrence(t *testing.T) {
	viewer := dbfx.User(t, "Requirement unrelated viewer", "viewer-98303@example.test")
	dbfx.Member(t, testWorkspaceID, viewer, "member")
	f, blocked, blockFixture := newRequirementPopoBlockedFixture(t, "98303", []service.RequirementParticipantInput{
		{UserID: testUserID, Roles: []string{"requester", "approver"}},
		{UserID: viewer, Roles: []string{"viewer"}},
	}, testUserID)
	worker := requirementOutboxWorkerForTest()
	if _, err := worker.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	targetBefore := requirementDeliveryForRecipientAndKind(t, f.detail.ID, testUserID, "requirement_blocked")
	current := requirementDetailForTest(t, testUserID, testWorkspaceID, f.detail.ID)
	updated := updateRequirementParticipantsForTest(t, testUserID, f.detail.ID, requirementParticipantsBody(current.Revision,
		service.RequirementParticipantInput{UserID: testUserID, Roles: []string{"requester", "approver"}}), "participants-preserve-98303")
	if updated.Revision != current.Revision+1 {
		t.Fatalf("participant revision = %d, want %d", updated.Revision, current.Revision+1)
	}
	targetAfter := requirementDeliveryForRecipientAndKind(t, f.detail.ID, testUserID, "requirement_blocked")
	if targetAfter.DeliveryID != targetBefore.DeliveryID || targetAfter.ID != targetBefore.ID ||
		targetAfter.SupersededAt.Valid || targetAfter.Status != "pending" {
		t.Fatalf("valid blocked occurrence was superseded: before=%+v after=%+v", targetBefore, targetAfter)
	}
	for _, row := range requirementDeliveryCommandsForTest(t, f.detail.ID) {
		if row.RecipientUserID.Valid && uuidToString(row.RecipientUserID) == viewer && row.RequirementKind.String == "requirement_blocked" &&
			!row.SupersededAt.Valid {
			t.Fatalf("removed recipient delivery was not fenced: %+v", row)
		}
	}
	allowed, err := testHandler.PopoBridge.CanDeliver(context.Background(), targetBefore.ID, parseUUID(f.env.bridgeID))
	if err != nil || !allowed {
		t.Fatalf("valid blocked command was fenced by unrelated participant change: allowed=%v err=%v", allowed, err)
	}
	wakeup := requirementWakeupForTest(t, blockFixture, blocked.QuestionComment.ID)
	if !wakeup.Enabled || wakeup.DisabledAt.Valid || uuidToString(wakeup.RequirementID) != f.detail.ID {
		t.Fatalf("unrelated participant change disabled valid target wakeup: %+v", wakeup)
	}
	if _, err := worker.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if got := requirementDeliveryForRecipientAndKind(t, f.detail.ID, testUserID, "requirement_blocked"); got.Attempt != 1 {
		t.Fatalf("valid blocked delivery was retried: %+v", got)
	}
}

func TestRequirementParticipantRejectsRemovingCurrentBlockedTarget(t *testing.T) {
	target := dbfx.User(t, "Requirement blocked target", "target-98304@example.test")
	dbfx.Member(t, testWorkspaceID, target, "member")
	f, blocked, blockFixture := newRequirementPopoBlockedFixture(t, "98304", []service.RequirementParticipantInput{
		{UserID: testUserID, Roles: []string{"requester", "approver"}},
		{UserID: target, Roles: []string{"approver"}},
	}, target)
	dbfx.Insert(t, "channel_user_binding", testutil.Cols{
		"workspace_id": testWorkspaceID, "multica_user_id": target, "installation_id": f.env.install,
		"channel_type": "popo", "channel_user_id": "target-98304@corp.netease.com",
	})
	worker := requirementOutboxWorkerForTest()
	if _, err := worker.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	delivery := requirementDeliveryForRecipientAndKind(t, f.detail.ID, target, "requirement_blocked")
	current := requirementDetailForTest(t, testUserID, testWorkspaceID, f.detail.ID)
	response := testutil.Call(t, testHandler.UpdateRequirementParticipants, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+f.detail.ID+"/participants",
		requirementParticipantsBody(current.Revision, service.RequirementParticipantInput{UserID: testUserID, Roles: []string{"requester", "approver"}}),
		"participants-remove-target-98304", "id", testWorkspaceID, "requirementId", f.detail.ID)).Want(http.StatusConflict)
	if response.Map()["code"] != "requirement_blocked_target_required" {
		t.Fatalf("target removal response = %s", response.Body)
	}
	if got := requirementDetailForTest(t, testUserID, testWorkspaceID, f.detail.ID).Revision; got != current.Revision {
		t.Fatalf("rejected target removal advanced revision to %d", got)
	}
	if got := requirementDeliveryForRecipientAndKind(t, f.detail.ID, target, "requirement_blocked"); got.SupersededAt.Valid || got.Status != "pending" {
		t.Fatalf("rejected target removal changed delivery: %+v", got)
	}
	allowed, err := testHandler.PopoBridge.CanDeliver(context.Background(), delivery.ID, parseUUID(f.env.bridgeID))
	if err != nil || !allowed {
		t.Fatalf("rejected target removal fenced existing command: allowed=%v err=%v", allowed, err)
	}
	wakeup := requirementWakeupForTest(t, blockFixture, blocked.QuestionComment.ID)
	if !wakeup.Enabled || wakeup.DisabledAt.Valid {
		t.Fatalf("rejected target removal disabled wakeup: %+v", wakeup)
	}
}
