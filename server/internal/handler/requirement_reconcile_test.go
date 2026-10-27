package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func reconcileRequirementForTest(t *testing.T, userID, requirementID string, input service.ReconcileRequirementRequest, key string) service.RequirementDetail {
	t.Helper()
	targetOperationID := ""
	if input.OperationID != nil {
		targetOperationID = *input.OperationID
	}
	dbfx.Exec(t, `UPDATE yixiezuo_operation SET state = 'failed', completed_at = now()
		WHERE kind = 'reconcile' AND state IN ('pending', 'running') AND target_operation_id IS DISTINCT FROM NULLIF($1, '')::uuid`, targetOperationID)
	dbfx.Cleanup(t, `DELETE FROM yixiezuo_operation WHERE kind = 'reconcile' AND requirement_id = $1`, requirementID)
	var detail service.RequirementDetail
	testutil.Call(t, testHandler.ReconcileRequirement, requirementActionRequest(userID, "reconcile", requirementID, input, key)).Want(http.StatusOK).JSON(&detail)
	return detail
}

func requirementPublishOperationForTest(t *testing.T, f requirementReviewFixture) string {
	t.Helper()
	var operationID string
	dbfx.QueryRow(t, `SELECT id::text FROM yixiezuo_operation WHERE issue_id = $1 AND kind = 'publish'`, f.Issue.ID).Scan(&operationID)
	return operationID
}

func claimYixiezuoOperationForTest(t *testing.T) yixiezuo.Operation {
	t.Helper()
	var claimed struct {
		Operation yixiezuo.Operation `json:"operation"`
	}
	testutil.Call(t, testHandler.ClaimYixiezuoOperation, yixiezuoRequest("POST", "/claim", map[string]any{})).Want(http.StatusOK).JSON(&claimed)
	return claimed.Operation
}

func completeYixiezuoOperationForTest(t *testing.T, operation yixiezuo.Operation, completion yixiezuo.Completion) {
	t.Helper()
	completion.LeaseToken = operation.LeaseToken
	testutil.Call(t, testHandler.CompleteYixiezuoOperation, manualOperationRequest("POST", operation.ID, completion)).Want(http.StatusOK)
}

func withReconcileFlags(t *testing.T, writeEnabled bool) {
	t.Helper()
	provider := featureflag.NewStaticProvider()
	provider.Set(featureflags.RequirementFlow, featureflag.Rule{Default: true})
	provider.Set(featureflags.PMMCPWrite, featureflag.Rule{Default: writeEnabled})
	flags := featureflag.NewService(provider)
	previous := testHandler.FeatureFlags
	testHandler.FeatureFlags = flags
	t.Cleanup(func() { testHandler.FeatureFlags = previous })
}

func TestRequirementReconcilePermissionTerminalOwnershipAndIdempotency(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementReviewFixture(t, "98201")
	accepted := acceptRequirementForTest(t, f, "reconcile-accept-98201")
	operationID := requirementPublishOperationForTest(t, f)
	operation := ptr(operationID)
	input := service.ReconcileRequirementRequest{OperationID: operation}
	detail := reconcileRequirementForTest(t, testUserID, f.Detail.ID, input, "reconcile-98201")
	if !detail.Permissions.CanReconcile {
		t.Fatal("done requirement cannot be reconciled")
	}
	var reconcileID string
	found := false
	for _, operation := range detail.Operations {
		if operation.Kind == "reconcile" && operation.TargetOperationID != nil && *operation.TargetOperationID == operationID {
			found = true
			reconcileID = operation.ID
			if operation.RequestHash == nil || *operation.RequestHash == "" {
				t.Fatal("reconcile request hash is missing")
			}
		}
		if operation.ID == operationID && operation.RequestHash == nil {
			t.Fatal("accept request hash is missing")
		}
	}
	if !found || reconcileID == "" {
		t.Fatalf("reconcile operation missing: %+v", detail.Operations)
	}
	replayed := reconcileRequirementForTest(t, testUserID, f.Detail.ID, input, "reconcile-98201")
	if !requirementSameJSON(t, string(requirementJSON(detail)), string(requirementJSON(replayed))) {
		t.Fatal("reconcile replay changed the original response")
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE kind = 'reconcile' AND target_operation_id = $1`, operationID); got != 1 {
		t.Fatalf("reconcile operations after replay = %d", got)
	}
	_ = accepted

	viewer := dbfx.User(t, "Requirement reconcile viewer", uuid.NewString()+"@example.test")
	dbfx.Member(t, testWorkspaceID, viewer, "member")
	dbfx.Insert(t, "requirement_participant", testutil.Cols{"workspace_id": testWorkspaceID, "requirement_id": f.Detail.ID, "user_id": viewer, "role": "viewer"})
	testutil.Call(t, testHandler.ReconcileRequirement, requirementActionRequest(viewer, "reconcile", f.Detail.ID, input, "reconcile-viewer-98201")).Want(http.StatusForbidden)

	other := newRequirementReviewFixture(t, "98202")
	acceptRequirementForTest(t, other, "reconcile-other-accept-98202")
	otherOperationID := requirementPublishOperationForTest(t, other)
	mismatch := service.ReconcileRequirementRequest{OperationID: ptr(otherOperationID)}
	testutil.Call(t, testHandler.ReconcileRequirement, requirementActionRequest(testUserID, "reconcile", f.Detail.ID, mismatch, "reconcile-other-98202")).Want(http.StatusForbidden)

	secondOperationID := dbfx.Insert(t, "yixiezuo_operation", testutil.Cols{
		"workspace_id": testWorkspaceID, "requested_by": testUserID, "kind": "publish", "payload": []byte(`{}`),
		"requirement_id": f.Detail.ID, "issue_id": f.Issue.ID,
	})
	otherWorkspace := dbfx.Workspace(t, "reconcile-other-workspace", "reconcile-other-"+uuid.NewString())
	foreign := dbfx.Insert(t, "yixiezuo_operation", testutil.Cols{
		"workspace_id": otherWorkspace, "requested_by": testUserID, "kind": "publish", "payload": []byte(`{}`),
	})
	testutil.Call(t, testHandler.ReconcileRequirement, requirementActionRequest(testUserID, "reconcile", f.Detail.ID,
		service.ReconcileRequirementRequest{OperationID: ptr(foreign)}, "reconcile-foreign-98203")).Want(http.StatusNotFound)

	var deliveryID string
	dbfx.QueryRow(t, `SELECT delivery_id::text FROM popo_bridge_command WHERE requirement_id = $1 AND operation_id = $2`, f.Detail.ID, operationID).Scan(&deliveryID)
	both := service.ReconcileRequirementRequest{OperationID: ptr(secondOperationID), DeliveryID: ptr(deliveryID)}
	testutil.Call(t, testHandler.ReconcileRequirement, requirementActionRequest(testUserID, "reconcile", f.Detail.ID, both, "reconcile-mismatch-98204")).Want(http.StatusBadRequest)

	cancelFixture := newRequirementReviewFixture(t, "98205")
	dbfx.Exec(t, `UPDATE issue SET status = 'in_progress', revision = revision + 1 WHERE id = $1`, cancelFixture.Issue.ID)
	cancelOperationID := dbfx.Insert(t, "yixiezuo_operation", testutil.Cols{
		"workspace_id": testWorkspaceID, "requested_by": testUserID, "kind": "publish", "state": "pending",
		"issue_id": cancelFixture.Issue.ID, "requirement_id": cancelFixture.Detail.ID, "payload": []byte(`{}`),
	})
	issue, err := testHandler.Queries.GetIssue(context.Background(), cancelFixture.Issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	testutil.Call(t, testHandler.CancelRequirement, requirementActionRequest(testUserID, "cancel", cancelFixture.Detail.ID,
		service.CancelRequirementRequest{ExpectedIssueRevision: ptr(issue.Revision)}, "reconcile-cancel-98205")).Want(http.StatusOK)
	cancelled := reconcileRequirementForTest(t, testUserID, cancelFixture.Detail.ID,
		service.ReconcileRequirementRequest{OperationID: ptr(cancelOperationID)}, "reconcile-cancelled-98205")
	if cancelled.State != "cancelled" || !cancelled.Permissions.CanReconcile {
		t.Fatalf("cancelled requirement reconcile = %+v", cancelled)
	}
}

func TestRequirementOperationReadbackOutcomesAndDuplicateReceipt(t *testing.T) {
	for _, tc := range []struct {
		name          string
		externalID    string
		targetState   string
		wantOriginal  string
		wantReconcile string
	}{
		{name: "applied", externalID: "98211", targetState: "succeeded", wantOriginal: "succeeded", wantReconcile: "succeeded"},
		{name: "not applied", externalID: "98212", targetState: "failed", wantOriginal: "failed", wantReconcile: "succeeded"},
		{name: "indeterminate", externalID: "98213", targetState: "needs_reconcile", wantOriginal: "needs_reconcile", wantReconcile: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableRequirementFlowForTest(t)
			f := newRequirementReviewFixture(t, tc.externalID)
			acceptRequirementForTest(t, f, "readback-accept-"+tc.externalID)
			withReconcileFlags(t, false)
			originalID := requirementPublishOperationForTest(t, f)
			detail := reconcileRequirementForTest(t, testUserID, f.Detail.ID,
				service.ReconcileRequirementRequest{OperationID: ptr(originalID)}, "readback-reconcile-"+tc.externalID)
			var reconcileOperation service.RequirementOperation
			for _, operation := range detail.Operations {
				if operation.Kind == "reconcile" {
					reconcileOperation = operation
				}
			}
			claimed := claimYixiezuoOperationForTest(t)
			if claimed.ID != reconcileOperation.ID || claimed.Kind != "reconcile" || claimed.TargetOperationID != originalID {
				t.Fatalf("claimed operation = %+v, want %s", claimed, reconcileOperation.ID)
			}
			snapshot := f.Source.Snapshot
			completion := yixiezuo.Completion{State: tc.wantReconcile, TargetState: tc.targetState, Snapshot: &snapshot, Error: "readback " + tc.name}
			var capture *requirementRealtimeCapture
			if tc.targetState == "succeeded" {
				capture = newRequirementRealtimeCapture(t)
			}
			completeYixiezuoOperationForTest(t, claimed, completion)
			if capture != nil && (capture.count() == 0 || capture.last().Type != protocol.EventRequirementUpdated) {
				t.Fatal("reconcile completion did not publish requirement realtime update")
			}
			var originalState, originalHash string
			dbfx.QueryRow(t, `SELECT state, request_hash FROM yixiezuo_operation WHERE id = $1`, originalID).Scan(&originalState, &originalHash)
			if originalState != tc.wantOriginal || originalHash == "" {
				t.Fatalf("original operation = state %s hash %q, want %s", originalState, originalHash, tc.wantOriginal)
			}
			completeYixiezuoOperationForTest(t, claimed, completion)
			dbfx.QueryRow(t, `SELECT state FROM yixiezuo_operation WHERE id = $1`, originalID).Scan(&originalState)
			if originalState != tc.wantOriginal {
				t.Fatalf("duplicate receipt changed original state to %s", originalState)
			}
			if tc.targetState == "succeeded" {
				var publishedRevision int64
				dbfx.QueryRow(t, `SELECT published_revision FROM yixiezuo_import WHERE issue_id = $1`, f.Issue.ID).Scan(&publishedRevision)
				if publishedRevision == 0 {
					t.Fatal("applied readback did not record publication")
				}
			}
		})
	}
}

func TestRequirementReconcileRejectsAppliedWithoutSnapshot(t *testing.T) {
	enableRequirementFlowForTest(t)
	f := newRequirementReviewFixture(t, "98242")
	acceptRequirementForTest(t, f, "snapshot-accept-98242")
	withReconcileFlags(t, false)
	originalID := requirementPublishOperationForTest(t, f)
	detail := reconcileRequirementForTest(t, testUserID, f.Detail.ID,
		service.ReconcileRequirementRequest{OperationID: ptr(originalID)}, "snapshot-reconcile-98242")
	var reconcileOperation service.RequirementOperation
	for _, operation := range detail.Operations {
		if operation.Kind == "reconcile" {
			reconcileOperation = operation
		}
	}
	claimed := claimYixiezuoOperationForTest(t)
	if claimed.ID != reconcileOperation.ID {
		t.Fatalf("snapshot claim = %+v", claimed)
	}
	testutil.Call(t, testHandler.CompleteYixiezuoOperation, manualOperationRequest("POST", claimed.ID,
		yixiezuo.Completion{LeaseToken: claimed.LeaseToken, State: "succeeded", TargetState: "succeeded"})).Want(http.StatusBadRequest)
	var originalState string
	dbfx.QueryRow(t, `SELECT state FROM yixiezuo_operation WHERE id = $1`, originalID).Scan(&originalState)
	if originalState != "pending" {
		t.Fatalf("missing snapshot changed original state to %s", originalState)
	}
}

type pmmcpWriteSequenceProvider struct {
	featureflag.Provider
	decisions []bool
	reads     int
}

func (p *pmmcpWriteSequenceProvider) Lookup(ctx context.Context, key string) (featureflag.Decision, bool) {
	if key != featureflags.PMMCPWrite {
		return p.Provider.Lookup(ctx, key)
	}
	enabled := false
	if p.reads < len(p.decisions) {
		enabled = p.decisions[p.reads]
	}
	p.reads++
	return featureflag.Decision{Key: key, Enabled: enabled, Reason: featureflag.ReasonStatic, Source: "test"}, true
}

func TestRequirementPMMCPWriteGateReleasesBlockedClaimResponse(t *testing.T) {
	withReconcileFlags(t, true)
	f := newRequirementReviewFixture(t, "98243")
	acceptRequirementForTest(t, f, "pmmcp-response-accept-98243")
	operationID := requirementPublishOperationForTest(t, f)
	provider := &pmmcpWriteSequenceProvider{
		Provider: testHandler.FeatureFlags.Provider(), decisions: []bool{true, true, false},
	}
	previous := testHandler.FeatureFlags
	testHandler.FeatureFlags = featureflag.NewService(provider)
	t.Cleanup(func() { testHandler.FeatureFlags = previous })

	claimed := claimYixiezuoOperationForTest(t)
	if claimed.ID != "" {
		t.Fatalf("blocked claim was returned: %+v", claimed)
	}
	if provider.reads < 3 {
		t.Fatalf("response flag decision was not evaluated: %d reads", provider.reads)
	}
	var state string
	var leased bool
	dbfx.QueryRow(t, `SELECT state, lease_token IS NOT NULL FROM yixiezuo_operation WHERE id = $1`, operationID).Scan(&state, &leased)
	if state != "pending" || leased {
		t.Fatalf("blocked claim response left operation state=%s leased=%v", state, leased)
	}
}

func TestRequirementPMMCPWriteGateOnlyBlocksRequirementWrites(t *testing.T) {
	withReconcileFlags(t, false)
	f := newRequirementReviewFixture(t, "98221")
	acceptRequirementForTest(t, f, "pmmcp-gate-accept-98221")
	originalID := requirementPublishOperationForTest(t, f)
	var gateReason string
	dbfx.QueryRow(t, `SELECT error FROM yixiezuo_operation WHERE id = $1`, originalID).Scan(&gateReason)
	if !strings.Contains(gateReason, "pmmcp_write") {
		t.Fatalf("write gate reason = %q", gateReason)
	}
	detail := reconcileRequirementForTest(t, testUserID, f.Detail.ID,
		service.ReconcileRequirementRequest{OperationID: ptr(originalID)}, "pmmcp-gate-reconcile-98221")
	var reconcileID string
	for _, operation := range detail.Operations {
		if operation.Kind == "reconcile" {
			reconcileID = operation.ID
		}
	}
	if reconcileID != "" {
		dbfx.Cleanup(t, `DELETE FROM yixiezuo_operation WHERE id = $1`, reconcileID)
	}
	claimed := claimYixiezuoOperationForTest(t)
	if claimed.ID != reconcileID || claimed.Kind != "reconcile" {
		t.Fatalf("readback was blocked with writes: %+v", claimed)
	}

	var preview yixiezuo.Operation
	testutil.Call(t, testHandler.PreviewYixiezuoIssue, yixiezuoRequest("POST", "/preview", map[string]any{"url": "https://dj01.pm.netease.com/issues/98222"})).Want(http.StatusCreated).JSON(&preview)
	dbfx.Cleanup(t, `DELETE FROM yixiezuo_operation WHERE id = $1`, preview.ID)
	claimed = claimYixiezuoOperationForTest(t)
	if claimed.ID != preview.ID || claimed.Kind != "preview" {
		t.Fatalf("ordinary operation was blocked: %+v", claimed)
	}
	ordinaryPublishID := dbfx.Insert(t, "yixiezuo_operation", testutil.Cols{
		"workspace_id": testWorkspaceID, "requested_by": testUserID, "kind": "publish", "payload": []byte(`{}`),
	})
	claimed = claimYixiezuoOperationForTest(t)
	if claimed.ID != ordinaryPublishID || claimed.Kind != "publish" {
		t.Fatalf("ordinary publish was blocked: %+v", claimed)
	}
	withReconcileFlags(t, true)
	dbfx.Exec(t, `UPDATE yixiezuo_operation SET state = 'failed', completed_at = now() WHERE id IN ($1, $2, $3)`, reconcileID, preview.ID, ordinaryPublishID)
	claimed = claimYixiezuoOperationForTest(t)
	if claimed.ID != originalID || claimed.Kind != "publish" {
		t.Fatalf("flag on did not release requirement write: %+v", claimed)
	}
}

func TestRequirementSyncMetricsCountsAndOldestAge(t *testing.T) {
	enableRequirementFlowForTest(t)
	wirePopo(t)
	f := newRequirementReviewFixture(t, "98241")
	acceptRequirementForTest(t, f, "metrics-accept-98241")
	withReconcileFlags(t, false)
	originalID := requirementPublishOperationForTest(t, f)
	detail := reconcileRequirementForTest(t, testUserID, f.Detail.ID,
		service.ReconcileRequirementRequest{OperationID: ptr(originalID)}, "metrics-reconcile-98241")
	var reconcileOperation service.RequirementOperation
	for _, operation := range detail.Operations {
		if operation.Kind == "reconcile" {
			reconcileOperation = operation
		}
	}
	claimed := claimYixiezuoOperationForTest(t)
	if claimed.ID != reconcileOperation.ID {
		t.Fatalf("metrics claim = %+v", claimed)
	}
	snapshot := f.Source.Snapshot
	completeYixiezuoOperationForTest(t, claimed, yixiezuo.Completion{State: "unknown", TargetState: "needs_reconcile", Snapshot: &snapshot})
	var response struct {
		RequirementOperations struct {
			Pending                     int64   `json:"pending"`
			Unknown                     int64   `json:"unknown"`
			NeedsReconcile              int64   `json:"needs_reconcile"`
			OldestPendingSeconds        float64 `json:"oldest_pending_seconds"`
			OldestUnknownSeconds        float64 `json:"oldest_unknown_seconds"`
			OldestNeedsReconcileSeconds float64 `json:"oldest_needs_reconcile_seconds"`
		} `json:"requirement_operations"`
		RequirementDeliveries struct {
			Pending              int64   `json:"pending"`
			Unknown              int64   `json:"unknown"`
			NeedsReconcile       int64   `json:"needs_reconcile"`
			OldestPendingSeconds float64 `json:"oldest_pending_seconds"`
		} `json:"requirement_deliveries"`
	}
	testutil.Call(t, testHandler.GetPopoStatus, withURLParam(newRequest(http.MethodGet, "/popo/status", nil), "id", testWorkspaceID)).Want(http.StatusOK).JSON(&response)
	if response.RequirementOperations.Pending != 0 || response.RequirementOperations.Unknown != 1 || response.RequirementOperations.NeedsReconcile != 1 {
		t.Fatalf("operation metrics = %+v", response.RequirementOperations)
	}
	if response.RequirementDeliveries.Pending == 0 || response.RequirementDeliveries.OldestPendingSeconds < 0 ||
		response.RequirementOperations.OldestUnknownSeconds < 0 || response.RequirementOperations.OldestNeedsReconcileSeconds < 0 {
		t.Fatalf("delivery metrics = %+v operation ages = %+v", response.RequirementDeliveries, response.RequirementOperations)
	}
}
