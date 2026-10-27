package handler

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestRequirementManualPublishRequiresCurrentApprover(t *testing.T) {
	withReconcileFlags(t, true)
	f := newRequirementReviewFixture(t, "98251")
	body := map[string]any{
		"summary": "Publish requirement result", "revision": f.Issue.Revision,
		"confirmed": true, "source_digest": f.Source.Digest,
	}
	outsider := dbfx.User(t, "Requirement publish nonparticipant", uuid.NewString()+"@example.test")
	dbfx.Member(t, testWorkspaceID, outsider, "member")
	req := manualIssueRequest("POST", uuidToString(f.Issue.ID), body)
	req.Header.Set("X-User-ID", outsider)
	testutil.Call(t, testHandler.PublishYixiezuoResult, req).Want(http.StatusForbidden)
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE issue_id = $1 AND kind = 'publish'`, f.Issue.ID); got != 0 {
		t.Fatalf("nonparticipant queued %d publish operations", got)
	}

	var operation yixiezuo.Operation
	testutil.Call(t, testHandler.PublishYixiezuoResult, manualIssueRequest("POST", uuidToString(f.Issue.ID), body)).Want(http.StatusCreated).JSON(&operation)
	if operation.RequirementID != f.Detail.ID {
		t.Fatalf("publish requirement_id = %q, want %q", operation.RequirementID, f.Detail.ID)
	}
	var storedRequirementID string
	dbfx.QueryRow(t, `SELECT requirement_id::text FROM yixiezuo_operation WHERE id = $1`, operation.ID).Scan(&storedRequirementID)
	if storedRequirementID != f.Detail.ID {
		t.Fatalf("stored requirement_id = %q, want %q", storedRequirementID, f.Detail.ID)
	}
}

func TestRequirementClaimRechecksCurrentApprover(t *testing.T) {
	withReconcileFlags(t, true)
	f := newRequirementReviewFixture(t, "98253")
	body := map[string]any{
		"summary": "Queue before participant removal", "revision": f.Issue.Revision,
		"confirmed": true, "source_digest": f.Source.Digest,
	}
	var operation yixiezuo.Operation
	testutil.Call(t, testHandler.PublishYixiezuoResult, manualIssueRequest("POST", uuidToString(f.Issue.ID), body)).Want(http.StatusCreated).JSON(&operation)
	dbfx.Exec(t, `DELETE FROM requirement_participant WHERE requirement_id = $1 AND user_id = $2`, f.Detail.ID, testUserID)
	if claimed := claimYixiezuoOperationForTest(t); claimed.ID != "" {
		t.Fatalf("former participant claimed requirement operation: %+v", claimed)
	}
	var state string
	var leased bool
	dbfx.QueryRow(t, `SELECT state, lease_token IS NOT NULL FROM yixiezuo_operation WHERE id = $1`, operation.ID).Scan(&state, &leased)
	if state != "pending" || leased {
		t.Fatalf("rejected claim changed operation state=%s leased=%v", state, leased)
	}
}

func TestRequirementManualPublishFlagOffCannotClaim(t *testing.T) {
	withReconcileFlags(t, false)
	f := newRequirementReviewFixture(t, "98252")
	body := map[string]any{
		"summary": "Queue requirement result", "revision": f.Issue.Revision,
		"confirmed": true, "source_digest": f.Source.Digest,
	}
	var requirementOperation yixiezuo.Operation
	testutil.Call(t, testHandler.PublishYixiezuoResult, manualIssueRequest("POST", uuidToString(f.Issue.ID), body)).Want(http.StatusCreated).JSON(&requirementOperation)
	if requirementOperation.RequirementID != f.Detail.ID {
		t.Fatalf("requirement publish identity = %+v", requirementOperation)
	}
	if claimed := claimYixiezuoOperationForTest(t); claimed.ID != "" {
		t.Fatalf("flag-off requirement publish was claimed: %+v", claimed)
	}
	var state string
	dbfx.QueryRow(t, `SELECT state FROM yixiezuo_operation WHERE id = $1`, requirementOperation.ID).Scan(&state)
	if state != "pending" {
		t.Fatalf("flag-off requirement operation state = %s", state)
	}
}

func TestOrdinaryPublishContinuesWithPMMCPWriteOff(t *testing.T) {
	withReconcileFlags(t, false)
	_, ordinaryIssue := prepareManualImport(t)
	var digest string
	dbfx.QueryRow(t, `SELECT snapshot->>'digest' FROM yixiezuo_import WHERE issue_id = $1`, ordinaryIssue.ID).Scan(&digest)
	var ordinaryOperation yixiezuo.Operation
	testutil.Call(t, testHandler.PublishYixiezuoResult, manualIssueRequest("POST", ordinaryIssue.ID, map[string]any{
		"summary": "Ordinary publication", "revision": ordinaryIssue.Revision,
		"confirmed": true, "source_digest": digest,
	})).Want(http.StatusCreated).JSON(&ordinaryOperation)
	if ordinaryOperation.RequirementID != "" {
		t.Fatalf("ordinary publish unexpectedly gained a requirement: %+v", ordinaryOperation)
	}
	claimed := claimYixiezuoOperationForTest(t)
	if claimed.ID != ordinaryOperation.ID || claimed.Kind != "publish" {
		t.Fatalf("ordinary publish did not continue with pmmcp_write off: %+v", claimed)
	}
}
