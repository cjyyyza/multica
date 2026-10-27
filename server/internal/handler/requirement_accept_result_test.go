package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestRequirementAcceptPublishesReviewedSummaryAndIssueLink(t *testing.T) {
	enableRequirementFlowForTest(t)
	f := newRequirementReviewFixture(t, "98291")
	previousURL := testHandler.cfg.AppURL
	testHandler.cfg.AppURL = "https://app.example.test"
	t.Cleanup(func() { testHandler.cfg.AppURL = previousURL })
	input := map[string]any{
		"expectedIssueRevision": f.Issue.Revision,
		"summary":               "Reviewed delivery\n[Verification report](https://example.test/report)",
	}
	for range 2 {
		testutil.Call(t, testHandler.AcceptRequirement, requirementActionRequest(testUserID, "accept", f.Detail.ID, input, "accept-reviewed-98291")).Want(http.StatusOK)
	}
	input["summary"] = "Changed after the committed review"
	testutil.Call(t, testHandler.AcceptRequirement, requirementActionRequest(testUserID, "accept", f.Detail.ID, input, "accept-reviewed-98291")).Want(http.StatusConflict)
	var raw []byte
	dbfx.QueryRow(t, `SELECT payload FROM yixiezuo_operation WHERE requirement_id = $1 AND kind = 'publish'`, f.Detail.ID).Scan(&raw)
	var payload yixiezuo.OperationPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload.Summary, "Reviewed delivery") || !strings.Contains(payload.Summary, "https://example.test/report") || !strings.Contains(payload.Summary, "https://app.example.test/") || !strings.Contains(payload.Summary, uuidToString(f.Issue.ID)) {
		t.Fatalf("accepted result lost its reviewed content or issue link: %s", raw)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE requirement_id = $1 AND kind = 'publish'`, f.Detail.ID); got != 1 {
		t.Fatalf("acceptance retry queued %d publications", got)
	}
}

func TestRequirementAcceptKeepsReviewedSourceWorkflowStatus(t *testing.T) {
	enableRequirementFlowForTest(t)
	f := newRequirementReviewFixture(t, "98293")
	snapshot := f.Source.Snapshot
	snapshot.Statuses = []yixiezuo.Status{{ID: 5, Name: "Done"}}
	snapshot.Digest = snapshot.Fingerprint()
	dbfx.Exec(t, `UPDATE yixiezuo_import SET snapshot = $2 WHERE issue_id = $1`, f.Issue.ID, requirementJSON(snapshot))
	input := map[string]any{"expectedIssueRevision": f.Issue.Revision, "summary": "Reviewed source workflow", "statusName": "Done"}
	testutil.Call(t, testHandler.AcceptRequirement, requirementActionRequest(testUserID, "accept", f.Detail.ID, input, "accept-source-status-98293")).Want(http.StatusOK)
	var raw []byte
	dbfx.QueryRow(t, `SELECT payload FROM yixiezuo_operation WHERE requirement_id = $1 AND kind = 'publish'`, f.Detail.ID).Scan(&raw)
	var payload yixiezuo.OperationPayload
	if err := json.Unmarshal(raw, &payload); err != nil || payload.StatusName != "Done" || payload.ExpectedDigest != snapshot.Digest {
		t.Fatalf("source status or revision lost: %s (%v)", raw, err)
	}
}

func TestRequirementAcceptRejectsUnknownSourceStatusAtomically(t *testing.T) {
	enableRequirementFlowForTest(t)
	f := newRequirementReviewFixture(t, "98292")
	input := map[string]any{"expectedIssueRevision": f.Issue.Revision, "summary": "Reviewed result", "statusName": "not-in-source-workflow"}
	testutil.Call(t, testHandler.AcceptRequirement, requirementActionRequest(testUserID, "accept", f.Detail.ID, input, "accept-invalid-status-98292")).Want(http.StatusBadRequest)
	var status string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, f.Issue.ID).Scan(&status)
	if status != "in_review" || dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE requirement_id = $1 AND kind = 'publish'`, f.Detail.ID) != 0 {
		t.Fatal("rejected source status changed acceptance or queued a publication")
	}
}
