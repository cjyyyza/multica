package handler

import (
	"log/slog"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/popo"
)

func TestRequirementPopoNotifiesForDecisionsInsteadOfMirroringExecution(t *testing.T) {
	f, blocked, issue := newRequirementPopoBlockedFixture(t, "98290", nil, testUserID)
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE requirement_id = $1 AND requirement_kind = 'requirement_status' AND status = 'pending'`, f.detail.ID); got != 0 {
		t.Fatalf("routine opened/approved notifications remained pending: %d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE requirement_id = $1 AND requirement_kind = 'requirement_status' AND snapshot_payload->>'state' = 'candidate submitted'`, f.detail.ID); got != 1 {
		t.Fatalf("candidate approval notification count = %d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE requirement_id = $1 AND requirement_kind = 'requirement_blocked' AND occurrence_id = $2 AND status = 'pending'`, f.detail.ID, blocked.QuestionComment.ID); got != 1 {
		t.Fatalf("specific question notification count = %d", got)
	}
	// Reconciliation must respect the same owner as live comment/status events.
	out := popo.NewOutbound(testHandler.Queries, testHandler.PopoBridge, slog.Default())
	for range 2 {
		if _, err := out.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE requirement_id IS NULL AND payload->>'issue_id' = $1`, uuidToString(issue.Issue.ID)); got != 0 {
		t.Fatalf("generic issue notifications duplicated the requirement stream: %d", got)
	}
}
