package handler

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestRequirementReconcileRealtimeUsesMinimalPayload(t *testing.T) {
	withReconcileFlags(t, false)
	f := newRequirementReviewFixture(t, "98254")
	acceptRequirementForTest(t, f, "realtime-reconcile-accept-98254")
	originalID := requirementPublishOperationForTest(t, f)
	detail := reconcileRequirementForTest(t, testUserID, f.Detail.ID,
		service.ReconcileRequirementRequest{OperationID: ptr(originalID)}, "realtime-reconcile-98254")
	var reconcileOperation service.RequirementOperation
	for _, operation := range detail.Operations {
		if operation.Kind == "reconcile" {
			reconcileOperation = operation
		}
	}
	claimed := claimYixiezuoOperationForTest(t)
	if claimed.ID != reconcileOperation.ID {
		t.Fatalf("reconcile claim = %+v, want %s", claimed, reconcileOperation.ID)
	}
	capture := newRequirementRealtimeCapture(t)
	snapshot := f.Source.Snapshot
	completeYixiezuoOperationForTest(t, claimed, yixiezuo.Completion{
		State: "succeeded", TargetState: "succeeded", Snapshot: &snapshot,
	})
	if capture.count() == 0 || capture.last().Type != protocol.EventRequirementUpdated {
		t.Fatal("reconcile completion did not publish requirement realtime update")
	}
	assertRequirementRealtimeEvent(t, capture.last(), f.Detail.ID, detail.Revision)
}
