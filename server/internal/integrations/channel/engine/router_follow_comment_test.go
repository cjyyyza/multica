package engine

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type ackFollow struct {
	IssueFollow
	result MemberCommentResult
	err    error
}

func (f *ackFollow) CreateMemberComment(context.Context, MemberCommentInput) (MemberCommentResult, error) {
	return f.result, f.err
}

func TestCreateFollowCommentAckUsesSteerResult(t *testing.T) {
	t.Parallel()
	issueID := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	commentID := util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	issue := FollowIssue{
		Issue:      db.Issue{ID: issueID, Title: "Login", Status: "in_progress", Number: 12},
		Identifier: "HAN-12",
		Slug:       "han",
	}
	for _, tc := range []struct {
		name    string
		result  MemberCommentResult
		want    string
		outcome Outcome
	}{
		{
			name:    "injected",
			result:  MemberCommentResult{Comment: db.Comment{ID: commentID}, Delivery: CommentDeliveryInjected},
			want:    "✅ Added your comment to the current run on HAN-12.",
			outcome: OutcomeIssueFollow,
		},
		{
			name:    "waiting",
			result:  MemberCommentResult{Comment: db.Comment{ID: commentID}, Delivery: CommentDeliveryWaiting},
			want:    "✅ Commented on HAN-12. It will wait for the next run.",
			outcome: OutcomeIssueFollow,
		},
		{
			name:    "note",
			result:  MemberCommentResult{Comment: db.Comment{ID: commentID}, Delivery: CommentDeliveryNote},
			want:    "✅ Commented on HAN-12",
			outcome: OutcomeIssueFollow,
		},
		{
			name:    "replay stays silent",
			result:  MemberCommentResult{Comment: db.Comment{ID: commentID}, IdempotentReplay: true, Delivery: CommentDeliveryInjected},
			want:    "",
			outcome: OutcomeDropped,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			router := &Router{follow: &ackFollow{result: tc.result}}
			res, err := router.createFollowComment(context.Background(), ResolverSet{}, ResolvedInstallation{}, ResolvedIdentity{}, channel.InboundMessage{}, issue, "please continue", pgtype.UUID{})
			if err != nil {
				t.Fatal(err)
			}
			if res.ReplyText != tc.want || res.Outcome != tc.outcome {
				t.Fatalf("reply %q outcome %q, want %q / %q", res.ReplyText, res.Outcome, tc.want, tc.outcome)
			}
			if res.CommentID != commentID {
				t.Fatalf("comment id = %v, want %v", res.CommentID, commentID)
			}
		})
	}
}
