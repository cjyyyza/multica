package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestRequirementIssueProjectionAcrossSurfaces(t *testing.T) {
	enableRequirementFlowForTest(t)
	f := newRequirementReviewFixture(t, "998902")
	issueID := uuidToString(f.Issue.ID)
	check := func(t *testing.T, issues []IssueResponse) {
		t.Helper()
		for _, issue := range issues {
			if issue.ID != issueID {
				continue
			}
			if issue.RequirementID == nil || *issue.RequirementID != f.Detail.ID {
				t.Fatalf("bound issue %s projects requirement_id=%v, want %s", issue.ID, issue.RequirementID, f.Detail.ID)
			}
			return
		}
		t.Fatalf("requirement issue %s missing from response", issueID)
	}

	t.Run("search", func(t *testing.T) {
		var out struct {
			Issues []IssueResponse `json:"issues"`
		}
		testutil.Call(t, testHandler.SearchIssues, newRequest(http.MethodGet, "/api/issues/search?q="+url.QueryEscape(f.Issue.Title), nil)).
			Want(http.StatusOK).JSON(&out)
		check(t, out.Issues)
	})
	t.Run("grouped", func(t *testing.T) {
		var out GroupedIssuesResponse
		testutil.Call(t, testHandler.ListGroupedIssues, newRequest(http.MethodGet, "/api/issues/grouped?group_by=assignee&statuses=in_review", nil)).
			Want(http.StatusOK).JSON(&out)
		var issues []IssueResponse
		for _, group := range out.Groups {
			issues = append(issues, group.Issues...)
		}
		check(t, issues)
	})
	t.Run("table rows", func(t *testing.T) {
		var out issueTableRowsResponse
		body := map[string]any{
			"query": map[string]any{"scope": map[string]any{"kind": "workspace"}, "sort": map[string]any{"field": "position", "direction": "asc"}},
			"group": map[string]any{"kind": "none"}, "hierarchy": map[string]any{"enabled": false}, "page": map[string]any{"limit": 50},
		}
		testutil.Call(t, testHandler.ListIssueTableRows, newRequest(http.MethodPost, "/api/issues/table/rows", body)).
			Want(http.StatusOK).JSON(&out)
		var issues []IssueResponse
		for _, row := range out.Rows {
			issues = append(issues, row.Issue)
		}
		check(t, issues)
	})
	t.Run("search index", func(t *testing.T) {
		row, err := testHandler.Queries.GetIssue(context.Background(), f.Issue.ID)
		if err != nil {
			t.Fatal(err)
		}
		rows := testHandler.searchIndexIssues(context.Background(), row.WorkspaceID, []db.Issue{row})
		var issues []IssueResponse
		for _, row := range rows {
			issues = append(issues, row.IssueResponse)
		}
		check(t, issues)
	})
	t.Run("batch update event", func(t *testing.T) {
		capture := newRequirementRealtimeCapture(t)
		testutil.Call(t, testHandler.BatchUpdateIssues, newRequest(http.MethodPost, "/api/issues/batch-update", map[string]any{
			"issue_ids": []string{issueID}, "updates": map[string]any{"priority": "high", "suppress_run": true},
		})).Want(http.StatusOK)
		var issues []IssueResponse
		for _, event := range capture.events {
			if event.Type != protocol.EventIssueUpdated {
				continue
			}
			raw, err := json.Marshal(event.Payload)
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				Issue IssueResponse `json:"issue"`
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			issues = append(issues, out.Issue)
		}
		check(t, issues)
	})
}
