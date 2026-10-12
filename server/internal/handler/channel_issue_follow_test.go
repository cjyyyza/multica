package handler

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func seedChannelSteerIssue(t *testing.T, provider string, negotiated bool) (issueID, agentID, taskID string) {
	t.Helper()
	if testHandler == nil {
		t.Skip("database not available")
	}
	runtimeID := dbfx.Runtime(t, "channel-steer-"+provider, testutil.Cols{"provider": provider})
	agentID = dbfx.Agent(t, "Channel steer "+provider, runtimeID)
	issueID = dbfx.Issue(t, "channel steer "+provider)
	dbfx.Exec(t, `UPDATE issue SET assignee_type = 'agent', assignee_id = $2 WHERE id = $1`, issueID, agentID)
	taskID = dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":   issueID,
		"runtime_id": runtimeID,
		"status":     "running",
		"started_at": testutil.Raw("now()"),
	})
	if negotiated {
		dbfx.Exec(t, `
			INSERT INTO task_supplement_capability (task_id, workspace_id, issue_id, capability)
			VALUES ($1, $2, $3, $4)
		`, taskID, testWorkspaceID, issueID, protocol.DaemonCapabilityTaskSupplementV1)
	}
	dbfx.Cleanup(t, `DELETE FROM comment WHERE issue_id = $1`, issueID)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	dbfx.Cleanup(t, `DELETE FROM task_supplement_capability WHERE issue_id = $1`, issueID)
	dbfx.Cleanup(t, `DELETE FROM task_supplement WHERE issue_id = $1`, issueID)
	return issueID, agentID, taskID
}

func addChannelSteerableRun(t *testing.T, issueID, name string) (agentID, taskID string) {
	t.Helper()
	runtimeID := dbfx.Runtime(t, "channel-steer-"+name, testutil.Cols{"provider": "claude"})
	agentID = dbfx.Agent(t, name, runtimeID)
	taskID = dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":   issueID,
		"runtime_id": runtimeID,
		"status":     "running",
		"started_at": testutil.Raw("now()"),
	})
	dbfx.Exec(t, `
		INSERT INTO task_supplement_capability (task_id, workspace_id, issue_id, capability)
		VALUES ($1, $2, $3, $4)
	`, taskID, testWorkspaceID, issueID, protocol.DaemonCapabilityTaskSupplementV1)
	return agentID, taskID
}

func postChannelComment(t *testing.T, issueID, content string) engine.MemberCommentResult {
	t.Helper()
	result, err := testHandler.CreateMemberComment(t.Context(), engine.MemberCommentInput{
		WorkspaceID: parseUUID(testWorkspaceID),
		IssueID:     parseUUID(issueID),
		ActorUserID: parseUUID(testUserID),
		Content:     content,
		ChannelType: "popo",
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCreateMemberCommentSteersTheSingleSteerableRun(t *testing.T) {
	issueID, agentID, taskID := seedChannelSteerIssue(t, "claude", true)

	result := postChannelComment(t, issueID, "please continue")
	if result.Delivery != engine.CommentDeliveryInjected {
		t.Fatalf("delivery = %d, want injected", result.Delivery)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM task_supplement WHERE comment_id = $1 AND task_id = $2`, result.Comment.ID, taskID); n != 1 {
		t.Fatalf("supplements for the running turn = %d, want 1", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, issueID, agentID); n != 1 {
		t.Fatalf("runs for the steered agent = %d, want the original turn only", n)
	}
}

func TestCreateMemberCommentWaitsWhenSteerIsRefused(t *testing.T) {
	// The run looks steerable (running, capability, actor may invoke) but the
	// bind only accepts codex and claude. The ack must follow that refusal.
	issueID, agentID, taskID := seedChannelSteerIssue(t, "grok", true)

	result := postChannelComment(t, issueID, "please continue")
	if result.Delivery != engine.CommentDeliveryWaiting {
		t.Fatalf("delivery = %d, want waiting", result.Delivery)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE id = $1`, result.Comment.ID); n != 1 {
		t.Fatal("refused steer removed the comment")
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM task_supplement WHERE comment_id = $1`, result.Comment.ID); n != 0 {
		t.Fatalf("refused steer stored %d receipts", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND id <> $3`, issueID, agentID, taskID); n != 1 {
		t.Fatalf("follow-up runs = %d, want the normal trigger", n)
	}
}

func TestCreateMemberCommentWaitsWhenNoRunIsSteerable(t *testing.T) {
	issueID, agentID, taskID := seedChannelSteerIssue(t, "claude", false)

	result := postChannelComment(t, issueID, "please continue")
	if result.Delivery != engine.CommentDeliveryWaiting {
		t.Fatalf("delivery = %d, want waiting", result.Delivery)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM task_supplement WHERE comment_id = $1`, result.Comment.ID); n != 0 {
		t.Fatalf("unsteerable run stored %d receipts", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND id <> $3`, issueID, agentID, taskID); n != 1 {
		t.Fatalf("follow-up runs = %d, want the normal trigger", n)
	}
}

func TestCreateMemberCommentWaitsWhenSeveralRunsAreSteerable(t *testing.T) {
	issueID, agentID, taskID := seedChannelSteerIssue(t, "claude", true)
	otherAgentID, _ := addChannelSteerableRun(t, issueID, "Second channel runner")

	result := postChannelComment(t, issueID, "please continue")
	if result.Delivery != engine.CommentDeliveryWaiting {
		t.Fatalf("delivery = %d, want waiting", result.Delivery)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM task_supplement WHERE comment_id = $1`, result.Comment.ID); n != 0 {
		t.Fatalf("ambiguous runs stored %d receipts", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND id <> $3`, issueID, agentID, taskID); n != 1 {
		t.Fatalf("assignee follow-up runs = %d, want the normal trigger", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id = $1`, otherAgentID); n != 1 {
		t.Fatalf("the other running agent gained %d runs, want its original turn only", n)
	}
}

func TestCreateMemberCommentNoteDoesNotSteer(t *testing.T) {
	issueID, agentID, _ := seedChannelSteerIssue(t, "claude", true)

	result := postChannelComment(t, issueID, "/note keep going")
	if result.Delivery != engine.CommentDeliveryNote {
		t.Fatalf("delivery = %d, want note", result.Delivery)
	}
	if result.Comment.Content != "/note keep going" {
		t.Fatalf("content = %q", result.Comment.Content)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM task_supplement WHERE comment_id = $1`, result.Comment.ID); n != 0 {
		t.Fatalf("note stored %d receipts", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, issueID, agentID); n != 1 {
		t.Fatalf("note started %d runs, want the original turn only", n)
	}
}

func TestChannelFollowSteerSkipsCommentsWithAttachments(t *testing.T) {
	issueID, _, taskID := seedChannelSteerIssue(t, "claude", true)
	commentID := dbfx.Comment(t, issueID, "see the file")
	dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id":  testWorkspaceID,
		"issue_id":      issueID,
		"comment_id":    commentID,
		"uploader_type": "member",
		"uploader_id":   testUserID,
		"filename":      "notes.txt",
		"url":           "https://example.test/notes.txt",
		"content_type":  "text/plain",
		"size_bytes":    4,
	})
	ctx := t.Context()
	issue, err := testHandler.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID: parseUUID(issueID), WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatal(err)
	}
	comment, err := testHandler.Queries.GetComment(ctx, parseUUID(commentID))
	if err != nil {
		t.Fatal(err)
	}
	ids, err := testHandler.channelFollowSteerTaskIDs(ctx, issue, comment, parseUUID(testUserID))
	if err != nil || len(ids) != 0 {
		t.Fatalf("steer ids = %v err = %v, want none for a comment that carries a file", ids, err)
	}

	plainID := dbfx.Comment(t, issueID, "text only")
	plain, err := testHandler.Queries.GetComment(ctx, parseUUID(plainID))
	if err != nil {
		t.Fatal(err)
	}
	ids, err = testHandler.channelFollowSteerTaskIDs(ctx, issue, plain, parseUUID(testUserID))
	if err != nil || len(ids) != 1 || uuidToString(ids[0]) != taskID {
		t.Fatalf("steer ids = %v err = %v, want the single running turn %s", ids, err, taskID)
	}
}
