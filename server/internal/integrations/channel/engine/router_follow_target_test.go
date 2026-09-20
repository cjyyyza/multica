package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type targetFollow struct {
	IssueFollow
	issue     FollowIssue
	runs      []FollowRun
	cancelled []pgtype.UUID
}

func (f *targetFollow) ResolveIssue(context.Context, pgtype.UUID, string) (FollowIssue, error) {
	return f.issue, nil
}
func (f *targetFollow) ListActiveRuns(context.Context, pgtype.UUID) ([]FollowRun, error) {
	return f.runs, nil
}
func (f *targetFollow) CancelRun(_ context.Context, in CancelRunInput) (FollowRun, error) {
	f.cancelled = append(f.cancelled, in.TaskID)
	return FollowRun{Task: db.AgentTaskQueue{ID: in.TaskID, IssueID: f.issue.Issue.ID}, Status: "cancelled"}, nil
}

func TestFollowStopRejectsConflictingQuoteAndAmbiguousIssue(t *testing.T) {
	a := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	b := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	task := util.MustParseUUID("33333333-3333-3333-3333-333333333333")
	for _, tc := range []struct {
		name  string
		quote QuotedTarget
		runs  []FollowRun
		want  string
	}{
		{"conflicting issue", QuotedTarget{IssueID: a, TaskID: task}, nil, "different tasks"},
		{"chat run with issue key", QuotedTarget{TaskID: task}, nil, "different tasks"},
		{"issue-only quote with multiple runs", QuotedTarget{IssueID: b}, []FollowRun{{}, {}}, "couldn't tell"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			follow := &targetFollow{issue: FollowIssue{Issue: db.Issue{ID: b}, Identifier: "TEST-2"}, runs: tc.runs}
			router := &Router{follow: follow}
			result, err := router.followStop(context.Background(), ResolvedInstallation{}, ResolvedIdentity{}, channel.InboundMessage{}, FollowCommand{Kind: FollowCommandStop, Identifier: "TEST-2"}, tc.quote, true)
			if err != nil || !strings.Contains(result.ReplyText, tc.want) || len(follow.cancelled) != 0 {
				t.Fatalf("result=%+v error=%v cancelled=%v", result, err, follow.cancelled)
			}
		})
	}
}
