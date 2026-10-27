package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type requirementProjectionErrorQuerier struct{ err error }

func (q requirementProjectionErrorQuerier) GetRequirementByIssue(context.Context, db.GetRequirementByIssueParams) (db.Requirement, error) {
	return db.Requirement{}, q.err
}
func (requirementProjectionErrorQuerier) GetIssueRefInWorkspace(context.Context, db.GetIssueRefInWorkspaceParams) (db.GetIssueRefInWorkspaceRow, error) {
	panic("unexpected duplicate lookup")
}
func (requirementProjectionErrorQuerier) GetIssueStatusEntryByKey(context.Context, db.GetIssueStatusEntryByKeyParams) (db.IssueStatus, error) {
	panic("unexpected status lookup")
}
func (requirementProjectionErrorQuerier) ListIssueStatusEntries(context.Context, db.ListIssueStatusEntriesParams) ([]db.IssueStatus, error) {
	panic("unexpected status list")
}
func (requirementProjectionErrorQuerier) SeedIssueStatusEntries(context.Context, pgtype.UUID) error {
	panic("unexpected status seed")
}
func (requirementProjectionErrorQuerier) ListIssueStatusKeysByCategories(context.Context, db.ListIssueStatusKeysByCategoriesParams) ([]string, error) {
	panic("unexpected status keys")
}

func TestIssueToMapResolvedOmitsRequirementIDOnLookupFailure(t *testing.T) {
	issue := db.Issue{Status: "in_progress"}
	payload := IssueToMapResolved(context.Background(), requirementProjectionErrorQuerier{err: errors.New("database unavailable")}, issue, "REQ")
	if value, exists := payload["requirement_id"]; exists {
		t.Fatalf("failed requirement lookup emitted %#v", value)
	}
}

func TestIssueToMapOmitsUnresolvedRequirementID(t *testing.T) {
	payload := IssueToMap(db.Issue{Status: "in_progress"}, "REQ")
	if value, exists := payload["requirement_id"]; exists {
		t.Fatalf("unresolved requirement binding emitted %#v", value)
	}
}
