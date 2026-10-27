package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/popo"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestRequirementDeliveryReconcileUnknownDoesNotRetry(t *testing.T) {
	f := newRequirementPopoFixture(t, "98231")
	worker := requirementOutboxWorkerForTest()
	if _, err := worker.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	delivery := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID), popo.CommandReceipt{Status: "unknown", Error: "unconfirmed"}); err != nil {
		t.Fatal(err)
	}
	detail := reconcileRequirementForTest(t, testUserID, f.detail.ID,
		service.ReconcileRequirementRequest{DeliveryID: ptr(uuidToString(delivery.DeliveryID))}, "delivery-unknown-98231")
	if detail.Deliveries[0].State != "unknown" {
		t.Fatalf("unknown delivery reconcile = %+v", detail.Deliveries[0])
	}
	delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	if delivery.Status != "unknown" || delivery.Attempt != 1 || requirementCommandCount(t, f.detail.ID) != 1 {
		t.Fatalf("unknown delivery was retried: %+v", delivery)
	}
	for _, operation := range detail.Operations {
		if operation.Kind == "reconcile" {
			t.Fatal("delivery-only reconcile created a source operation")
		}
	}
}

func TestRequirementDeliveryReconcileConfirmedFailedFailsClosedWithoutAttemptCapability(t *testing.T) {
	f := newRequirementPopoFixture(t, "98238")
	worker := requirementOutboxWorkerForTest()
	if _, err := worker.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	delivery := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	receipt := popo.CommandReceipt{
		Status: "failed", Error: "confirmed not delivered",
		NotSent: boolPtr(true), Retryable: boolPtr(true), EvidenceFinalized: boolPtr(true),
	}
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID), receipt); err != nil {
		t.Fatal(err)
	}
	response := testutil.Call(t, testHandler.ReconcileRequirement, requirementActionRequest(testUserID, "reconcile", f.detail.ID,
		service.ReconcileRequirementRequest{DeliveryID: ptr(uuidToString(delivery.DeliveryID))}, "delivery-failed-no-capability-98238")).Want(http.StatusConflict)
	if response.Map()["code"] != "reconcile_delivery_recovery_not_allowed" {
		t.Fatalf("uncapable recovery response = %s", response.Body)
	}
	delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	if delivery.Status != "failed" || delivery.Attempt != 1 || requirementCommandCount(t, f.detail.ID) != 1 {
		t.Fatalf("uncapable reconcile retried delivery: %+v", delivery)
	}
}

func TestRequirementDeliveryReconcileConfirmedFailedUsesOriginalIdentity(t *testing.T) {
	f := newRequirementPopoFixture(t, "98232")
	worker := requirementOutboxWorkerForTest()
	if _, err := worker.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	delivery := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	receipt := popo.CommandReceipt{
		Status: "failed", Error: "confirmed not delivered",
		NotSent: boolPtr(true), Retryable: boolPtr(true), EvidenceFinalized: boolPtr(true),
	}
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID), receipt); err != nil {
		t.Fatal(err)
	}
	enableRequirementReceiptAttempt(t, f)
	deliveryID := uuidToString(delivery.DeliveryID)
	input := service.ReconcileRequirementRequest{DeliveryID: ptr(deliveryID)}
	detail := reconcileRequirementForTest(t, testUserID, f.detail.ID, input, "delivery-failed-98232")
	if detail.Deliveries[0].State != "accepted" || detail.Deliveries[0].RequestHash == nil {
		t.Fatalf("recovered delivery = %+v", detail.Deliveries[0])
	}
	recovered := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	if recovered.OccurrenceID != delivery.OccurrenceID || recovered.DeliveryID != delivery.DeliveryID ||
		recovered.ID != delivery.ID || recovered.Attempt != 2 || recovered.Status != "pending" {
		t.Fatalf("recovery identity = %+v", recovered)
	}
	replayed := reconcileRequirementForTest(t, testUserID, f.detail.ID, input, "delivery-failed-98232")
	if !requirementSameJSON(t, string(requirementJSON(detail)), string(requirementJSON(replayed))) {
		t.Fatal("delivery recovery replay changed the response")
	}
	if got := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft").Attempt; got != 2 {
		t.Fatalf("replayed recovery advanced attempt to %d", got)
	}
}

func TestRequirementDeliveryReconcileRejectsHashMismatch(t *testing.T) {
	f := newRequirementPopoFixture(t, "98237")
	worker := requirementOutboxWorkerForTest()
	if _, err := worker.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	delivery := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	receipt := popo.CommandReceipt{
		Status: "failed", Error: "confirmed not delivered",
		NotSent: boolPtr(true), Retryable: boolPtr(true), EvidenceFinalized: boolPtr(true),
	}
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID), receipt); err != nil {
		t.Fatal(err)
	}
	enableRequirementReceiptAttempt(t, f)
	dbfx.Exec(t, `UPDATE popo_bridge_command SET request_hash = 'tampered' WHERE id = $1`, delivery.ID)
	response := testutil.Call(t, testHandler.ReconcileRequirement, requirementActionRequest(testUserID, "reconcile", f.detail.ID,
		service.ReconcileRequirementRequest{DeliveryID: ptr(uuidToString(delivery.DeliveryID))}, "delivery-hash-98237")).Want(http.StatusConflict)
	if response.Map()["code"] != "reconcile_delivery_hash_mismatch" {
		t.Fatalf("hash mismatch response = %s", response.Body)
	}
	if got := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft"); got.Status != "failed" || got.Attempt != 1 {
		t.Fatalf("hash mismatch retried delivery: %+v", got)
	}
}

func TestRequirementDeliveryReconcileRejectsForeignAndMismatchedTargets(t *testing.T) {
	first := newRequirementPopoFixture(t, "98233")
	second := newRequirementPopoFixture(t, "98234")
	firstDelivery := requirementDeliveryCommandForKind(t, first.detail.ID, "requirement_draft")
	secondDelivery := requirementDeliveryCommandForKind(t, second.detail.ID, "requirement_draft")
	testutil.Call(t, testHandler.ReconcileRequirement, requirementActionRequest(testUserID, "reconcile", first.detail.ID,
		service.ReconcileRequirementRequest{DeliveryID: ptr(uuidToString(secondDelivery.DeliveryID))}, "delivery-foreign-98234")).Want(http.StatusForbidden)

	operationID := first.operationID
	dbfx.Exec(t, `UPDATE yixiezuo_operation SET kind = 'publish', requirement_id = $2 WHERE id = $1`, operationID, first.detail.ID)
	_ = reconcileRequirementForTest(t, testUserID, first.detail.ID,
		service.ReconcileRequirementRequest{OperationID: ptr(operationID), DeliveryID: ptr(uuidToString(firstDelivery.DeliveryID))}, "delivery-both-98235")
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE kind = 'reconcile' AND requirement_id = $1 AND target_operation_id = $2`, first.detail.ID, operationID); got != 1 {
		t.Fatalf("matching operation and delivery were not reconciled: %d", got)
	}

	otherOperationID := dbfx.Insert(t, "yixiezuo_operation", testutil.Cols{
		"workspace_id": testWorkspaceID, "requested_by": testUserID, "kind": "publish", "payload": []byte(`{}`),
		"requirement_id": first.detail.ID,
	})
	testutil.Call(t, testHandler.ReconcileRequirement, requirementActionRequest(testUserID, "reconcile", first.detail.ID,
		service.ReconcileRequirementRequest{OperationID: ptr(otherOperationID), DeliveryID: ptr(uuidToString(firstDelivery.DeliveryID))}, "delivery-mismatch-98236")).Want(http.StatusBadRequest)
}
