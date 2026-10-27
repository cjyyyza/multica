package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func draftIntakeForTest(t *testing.T, key string) (yixiezuo.Operation, string, requirementDraftRequest) {
	t.Helper()
	enableRequirementFlowForTest(t)
	withReconcileFlags(t, true)
	_, agentID := newRequirementClaudeAgent(t, "Draft intake "+key)
	input := requirementDraftRequest{TemplateURL: "https://dj01.pm.netease.com/issues/108402", Title: "Draft " + key, Body: "A new idea that must be clarified", AgentID: agentID}
	req := yixiezuoRequest(http.MethodPost, "/drafts", input)
	req.Header.Set("Idempotency-Key", key)
	var op yixiezuo.Operation
	testutil.Call(t, testHandler.CreateRequirementDraft, req).Want(http.StatusCreated).JSON(&op)
	dbfx.Cleanup(t, `DELETE FROM yixiezuo_operation WHERE workspace_id=$1 AND requested_by=$2 AND (id=$3 OR payload->>'draft_operation_id'=$3::text)`, testWorkspaceID, testUserID, op.ID)
	if op.Kind != "draft" || op.State != "pending" || op.RequirementID != "" {
		t.Fatalf("draft queued as %+v", op)
	}
	return op, agentID, input
}

func claimDraftForTest(t *testing.T, opID string) yixiezuo.Operation {
	t.Helper()
	var claim struct {
		Operation yixiezuo.Operation `json:"operation"`
	}
	testutil.Call(t, testHandler.ClaimYixiezuoOperation, yixiezuoRequest(http.MethodPost, "/claim", nil)).Want(http.StatusOK).JSON(&claim)
	if claim.Operation.ID != opID || claim.Operation.LeaseToken == "" {
		t.Fatalf("claimed %+v, want %s", claim.Operation, opID)
	}
	return claim.Operation
}

func draftSnapshotForTest(op yixiezuo.Operation, externalID string) yixiezuo.Snapshot {
	f := newRequirementSourceFixture(externalID)
	f.Snapshot.Title = op.Payload.Draft.Title
	f.Snapshot.Description = "<p>待澄清</p><p>" + op.Payload.Draft.Body + "</p><p>" + yixiezuo.DraftMarker(op.ID) + "</p>"
	f.Snapshot.Digest = f.Snapshot.Fingerprint()
	return f.Snapshot
}

func TestRequirementDraftCreatesOneIntakeThenRequiresChatApproval(t *testing.T) {
	op, agentID, input := draftIntakeForTest(t, "intake-once")
	claimed := claimDraftForTest(t, op.ID)
	snapshot := draftSnapshotForTest(op, "991201")
	wrong := snapshot
	wrong.Description = "unrelated source"
	testutil.Call(t, testHandler.CompleteYixiezuoOperation, manualOperationRequest(http.MethodPost, op.ID, yixiezuo.Completion{LeaseToken: claimed.LeaseToken, State: "succeeded", Snapshot: &wrong})).Want(http.StatusBadRequest)
	completion := yixiezuo.Completion{LeaseToken: claimed.LeaseToken, State: "succeeded", Snapshot: &snapshot}
	var completed yixiezuo.Operation
	for range 2 {
		testutil.Call(t, testHandler.CompleteYixiezuoOperation, manualOperationRequest(http.MethodPost, op.ID, completion)).Want(http.StatusOK).JSON(&completed)
	}
	if completed.RequirementID == "" || completed.Payload.Source != snapshot.Source {
		t.Fatalf("draft did not bind the newly created source: %+v", completed)
	}
	registerRequirementCleanup(t, completed.RequirementID)
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement WHERE intake_operation_id=$1`, op.ID); got != 1 {
		t.Fatalf("receipt replay created %d requirements", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE requirement_id=$1`, completed.RequirementID); got != 0 {
		t.Fatalf("POPO intake started %d tasks before opening Chat", got)
	}
	req := yixiezuoRequest(http.MethodPost, "/drafts", input)
	req.Header.Set("Idempotency-Key", "intake-once")
	testutil.Call(t, testHandler.CreateRequirementDraft, req).Want(http.StatusCreated)
	input.Body = "changed input under the same key"
	req = yixiezuoRequest(http.MethodPost, "/drafts", input)
	req.Header.Set("Idempotency-Key", "intake-once")
	testutil.Call(t, testHandler.CreateRequirementDraft, req).Want(http.StatusConflict)

	opened := openRequirementForTest(t, testUserID, completed.RequirementID, 1, "draft-open")
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='running', started_at=now() WHERE id=$1`, opened.PendingTask.TaskID)
	detail := submitCandidateForTest(t, agentID, opened.PendingTask.TaskID, completed.RequirementID, opened.Session.ID,
		validCandidateRequest(t, opened.Requirement, 2), "draft-candidate")
	f := requirementApproveFixture{Detail: detail, AgentID: agentID, Opened: opened}
	approved, _ := approveRequirementForTest(t, f, "draft-approve")
	issueID := approveIssueForTest(t, approved).ID
	dbfx.Cleanup(t, `DELETE FROM issue WHERE id=$1`, issueID)
	dbfx.Cleanup(t, `DELETE FROM yixiezuo_import WHERE issue_id=$1`, issueID)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id=$1`, issueID)
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_import WHERE issue_id=$1 AND external_id=$2`, issueID, snapshot.Source.ID); got != 1 {
		t.Fatal("Chat approval did not retain the new source draft")
	}
}

func TestRequirementDraftUnknownOnlyResolvesFromReadOnlyMarkerProof(t *testing.T) {
	op, _, input := draftIntakeForTest(t, "intake-unknown")
	claimed := claimDraftForTest(t, op.ID)
	unknown := yixiezuo.Completion{LeaseToken: claimed.LeaseToken, State: "unknown", Error: "response was lost"}
	testutil.Call(t, testHandler.CompleteYixiezuoOperation, manualOperationRequest(http.MethodPost, op.ID, unknown)).Want(http.StatusOK)
	metrics, err := testHandler.Queries.GetRequirementReconcileMetrics(context.Background(), parseUUID(testWorkspaceID))
	if err != nil || metrics.OperationUnknown != 1 {
		t.Fatalf("unbound draft was absent from diagnostics: metrics=%+v err=%v", metrics, err)
	}
	req := yixiezuoRequest(http.MethodPost, "/drafts", input)
	req.Header.Set("Idempotency-Key", "intake-unknown")
	var replay yixiezuo.Operation
	testutil.Call(t, testHandler.CreateRequirementDraft, req).Want(http.StatusCreated).JSON(&replay)
	if replay.State != "unknown" || replay.ID != op.ID {
		t.Fatal("unknown create was restarted")
	}
	var empty struct {
		Operation *yixiezuo.Operation `json:"operation"`
	}
	testutil.Call(t, testHandler.ClaimYixiezuoOperation, yixiezuoRequest(http.MethodPost, "/claim", nil)).Want(http.StatusOK).JSON(&empty)
	if empty.Operation != nil {
		t.Fatal("unknown draft was reclaimed for another create")
	}
	snapshot := draftSnapshotForTest(op, "991202")
	readback, err := testHandler.queueRequirementDraftReconcile(context.Background(), parseUUID(testWorkspaceID), parseUUID(testUserID), parseUUID(op.ID), snapshot.Source.URL, "draft-proof-once", nil)
	if err != nil {
		t.Fatal(err)
	}
	claimedRead := claimDraftForTest(t, uuidToString(readback.ID))
	wrong := snapshot
	wrong.Description = "待澄清 but no operation marker"
	testutil.Call(t, testHandler.CompleteYixiezuoOperation, manualOperationRequest(http.MethodPost, claimedRead.ID, yixiezuo.Completion{LeaseToken: claimedRead.LeaseToken, State: "succeeded", Snapshot: &wrong})).Want(http.StatusConflict)
	proof := yixiezuo.Completion{LeaseToken: claimedRead.LeaseToken, State: "succeeded", Snapshot: &snapshot}
	for range 2 {
		testutil.Call(t, testHandler.CompleteYixiezuoOperation, manualOperationRequest(http.MethodPost, claimedRead.ID, proof)).Want(http.StatusOK)
	}
	resolved, err := testHandler.Queries.GetYixiezuoOperation(context.Background(), db.GetYixiezuoOperationParams{WorkspaceID: parseUUID(testWorkspaceID), RequestedBy: parseUUID(testUserID), ID: parseUUID(op.ID)})
	if err != nil || resolved.State != "succeeded" || !resolved.RequirementID.Valid {
		t.Fatalf("readback did not recover intake: row=%+v err=%v", resolved, err)
	}
	registerRequirementCleanup(t, uuidToString(resolved.RequirementID))
	if _, err := testHandler.queueRequirementDraftReconcile(context.Background(), parseUUID(testWorkspaceID), parseUUID(testUserID), parseUUID(op.ID), snapshot.Source.URL, "draft-proof-once", nil); err != nil {
		t.Fatalf("resolved readback lost idempotency: %v", err)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE workspace_id=$1 AND kind='draft' AND id=$2`, testWorkspaceID, op.ID); got != 1 {
		t.Fatalf("readback queued %d creates", got)
	}
}

func TestRequirementDraftWriteGateAndLeaseExpiryDoNotRepeatCreate(t *testing.T) {
	op, _, _ := draftIntakeForTest(t, "intake-gate")
	withReconcileFlags(t, false)
	var claim struct {
		Operation *yixiezuo.Operation `json:"operation"`
	}
	testutil.Call(t, testHandler.ClaimYixiezuoOperation, yixiezuoRequest(http.MethodPost, "/claim", nil)).Want(http.StatusOK).JSON(&claim)
	if claim.Operation != nil {
		t.Fatal("disabled source write was leased")
	}
	withReconcileFlags(t, true)
	_ = claimDraftForTest(t, op.ID)
	dbfx.Exec(t, `UPDATE yixiezuo_operation SET started_at=now()-interval '4 minutes' WHERE id=$1`, op.ID)
	testutil.Call(t, testHandler.ClaimYixiezuoOperation, yixiezuoRequest(http.MethodPost, "/claim", nil)).Want(http.StatusOK).JSON(&claim)
	var state, message string
	dbfx.QueryRow(t, `SELECT state,error FROM yixiezuo_operation WHERE id=$1`, op.ID).Scan(&state, &message)
	if state != "unknown" || !strings.Contains(message, yixiezuo.DraftMarker(op.ID)) || claim.Operation != nil {
		t.Fatalf("expired draft state=%s message=%s claim=%+v", state, message, claim)
	}
}

func TestPopoRequirementIntakeUsesNaturalIdeaAndKeepsApprovalInChat(t *testing.T) {
	enableRequirementFlowForTest(t)
	withReconcileFlags(t, true)
	e := popoP2Setup(t)
	runtimeID, _ := newRequirementClaudeAgent(t, "Natural requirement Plan")
	dbfx.Exec(t, `UPDATE agent SET runtime_id=$2 WHERE id=$1`, e.agentID, runtimeID)
	settingsReq := withURLParams(newRequest(http.MethodPatch, "/intake", map[string]any{"template_url": "https://dj01.pm.netease.com/issues/108402"}), "id", testWorkspaceID, "installationId", e.install)
	testutil.Call(t, testHandler.SetPopoRequirementIntake, settingsReq).Want(http.StatusOK)
	dbfx.Cleanup(t, `DELETE FROM yixiezuo_operation WHERE workspace_id=$1 AND payload->'channel'->>'installation_id'=$2`, testWorkspaceID, e.install)
	for _, message := range []struct{ id, text string }{
		{"natural-idea", "Build a temporary acceptance report"},
		{"natural-idea", "Build a temporary acceptance report"},
		{"followup", "Please approve it"},
		{"approve", "/approve"},
		{"new-idea", "/new A separate acceptance idea"},
		{"new-idea", "/new A separate acceptance idea"},
	} {
		e.inbound(t, message.id, message.text, nil)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE workspace_id=$1 AND kind='draft' AND payload->'channel'->>'installation_id'=$2`, testWorkspaceID, e.install); got != 2 {
		t.Fatalf("natural idea, followup or replay created %d drafts; want 2", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, e.agentID); got != 0 {
		t.Fatalf("POPO entry scheduled %d execution tasks", got)
	}
	var payloads [][]byte
	var operationIDs []string
	rows, err := testPool.Query(context.Background(), `SELECT id,payload FROM yixiezuo_operation WHERE workspace_id=$1 AND kind='draft' AND payload->'channel'->>'installation_id'=$2 ORDER BY created_at,id`, testWorkspaceID, e.install)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var operationID string
		var raw []byte
		if err := rows.Scan(&operationID, &raw); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, raw)
		operationIDs = append(operationIDs, operationID)
	}
	rows.Close()
	var second yixiezuo.OperationPayload
	if err := json.Unmarshal(payloads[1], &second); err != nil || second.Draft.Body != "A separate acceptance idea" {
		t.Fatalf("/new lost its idea: %s", payloads[1])
	}
	first := claimDraftForTest(t, operationIDs[0])
	snapshot := draftSnapshotForTest(first, "991203")
	var completed yixiezuo.Operation
	testutil.Call(t, testHandler.CompleteYixiezuoOperation, manualOperationRequest(http.MethodPost, first.ID, yixiezuo.Completion{LeaseToken: first.LeaseToken, State: "succeeded", Snapshot: &snapshot})).Want(http.StatusOK).JSON(&completed)
	registerRequirementCleanup(t, completed.RequirementID)
	if got := dbfx.Count(t, `SELECT count(*) FROM channel_chat_session_binding WHERE installation_id=$1`, e.install); got != 0 {
		t.Fatal("intake created an ordinary POPO Chat route")
	}
	opened := openRequirementForTest(t, testUserID, completed.RequirementID, 1, "natural-draft-open")
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='running', started_at=now() WHERE id=$1`, opened.PendingTask.TaskID)
	detail := submitCandidateForTest(t, e.agentID, opened.PendingTask.TaskID, completed.RequirementID, opened.Session.ID,
		validCandidateRequest(t, opened.Requirement, 2), "natural-draft-candidate")
	approved, _ := approveRequirementForTest(t, requirementApproveFixture{Detail: detail, AgentID: e.agentID, Opened: opened}, "natural-draft-approve")
	issueID := approveIssueForTest(t, approved).ID
	dbfx.Cleanup(t, `DELETE FROM issue WHERE id=$1`, issueID)
	dbfx.Cleanup(t, `DELETE FROM yixiezuo_import WHERE issue_id=$1`, issueID)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id=$1`, issueID)
	if got := dbfx.Count(t, `SELECT count(*) FROM channel_issue_source WHERE issue_id=$1 AND installation_id=$2 AND binding_id IS NULL`, issueID, e.install); got != 1 {
		t.Fatal("Chat approval required a fabricated ordinary POPO route")
	}
}

func TestPopoRequirementDraftRevokedRobotIsFencedBeforeCreate(t *testing.T) {
	enableRequirementFlowForTest(t)
	withReconcileFlags(t, true)
	e := popoP2Setup(t)
	runtimeID, _ := newRequirementClaudeAgent(t, "Revoked intake Plan")
	dbfx.Exec(t, `UPDATE agent SET runtime_id=$2 WHERE id=$1`, e.agentID, runtimeID)
	dbfx.Exec(t, `UPDATE channel_installation SET config=jsonb_set(config,'{requirement_template_url}','"https://dj01.pm.netease.com/issues/108402"') WHERE id=$1`, e.install)
	dbfx.Cleanup(t, `DELETE FROM yixiezuo_operation WHERE workspace_id=$1 AND payload->'channel'->>'installation_id'=$2`, testWorkspaceID, e.install)
	e.inbound(t, "revoked-idea", "A request before disconnect", nil)
	dbfx.Exec(t, `UPDATE channel_installation SET status='revoked' WHERE id=$1`, e.install)
	var claim struct {
		Operation *yixiezuo.Operation `json:"operation"`
	}
	testutil.Call(t, testHandler.ClaimYixiezuoOperation, yixiezuoRequest(http.MethodPost, "/claim", nil)).Want(http.StatusOK).JSON(&claim)
	var state string
	dbfx.QueryRow(t, `SELECT state FROM yixiezuo_operation WHERE workspace_id=$1 AND payload->'channel'->>'installation_id'=$2`, testWorkspaceID, e.install).Scan(&state)
	if state != "failed" || claim.Operation != nil {
		t.Fatalf("revoked source robot reached bridge: state=%s claim=%+v", state, claim)
	}
}

func TestRequirementDraftUnavailableAgentIsFencedBeforeBridgeWrite(t *testing.T) {
	op, agentID, _ := draftIntakeForTest(t, "intake-agent-change")
	dbfx.Exec(t, `UPDATE agent SET archived_at=now() WHERE id=$1`, agentID)
	var claim struct {
		Operation *yixiezuo.Operation `json:"operation"`
	}
	testutil.Call(t, testHandler.ClaimYixiezuoOperation, yixiezuoRequest(http.MethodPost, "/claim", nil)).Want(http.StatusOK).JSON(&claim)
	var state string
	dbfx.QueryRow(t, `SELECT state FROM yixiezuo_operation WHERE id=$1`, op.ID).Scan(&state)
	if state != "failed" || claim.Operation != nil {
		t.Fatalf("unavailable agent reached bridge: state=%s claim=%+v", state, claim)
	}
}

func TestPopoRequirementIntakeSettingsRequireManagerAndPreserveRobotConfig(t *testing.T) {
	enableRequirementFlowForTest(t)
	e := popoP2Setup(t)
	runtimeID, _ := newRequirementClaudeAgent(t, "Configured intake Plan")
	dbfx.Exec(t, `UPDATE agent SET runtime_id=$2 WHERE id=$1`, e.agentID, runtimeID)
	memberID := dbfx.User(t, "Intake non-manager", "intake-member-"+e.bridgeID+"@example.test")
	dbfx.Member(t, testWorkspaceID, memberID, "member")
	request := func(actor, value string) *http.Request {
		req := withURLParams(newRequest(http.MethodPatch, "/intake", map[string]any{"template_url": value}), "id", testWorkspaceID, "installationId", e.install)
		req.Header.Set("X-User-ID", actor)
		return req
	}
	testutil.Call(t, testHandler.SetPopoRequirementIntake, request(memberID, "https://dj01.pm.netease.com/issues/108402")).Want(http.StatusForbidden)
	testutil.Call(t, testHandler.SetPopoRequirementIntake, request(testUserID, "javascript:alert(1)")).Want(http.StatusBadRequest)
	var saved PopoInstallationResponse
	testutil.Call(t, testHandler.SetPopoRequirementIntake, request(testUserID, "https://dj01.pm.netease.com/v6/kanban?project_id=dj01&issue_id=108402")).Want(http.StatusOK).JSON(&saved)
	if saved.RequirementTemplateURL != "https://dj01.pm.netease.com/issues/108402" || saved.BridgeID != e.bridgeID || saved.RobotID != e.robotID {
		t.Fatalf("intake setting changed the robot route: %+v", saved)
	}
	withFeatureFlag(t, testHandler, featureflags.RequirementFlow, false)
	testutil.Call(t, testHandler.SetPopoRequirementIntake, request(testUserID, "")).Want(http.StatusOK).JSON(&saved)
	if saved.RequirementTemplateURL != "" || saved.BridgeID != e.bridgeID {
		t.Fatal("could not disable an existing intake while its feature was off")
	}
}
