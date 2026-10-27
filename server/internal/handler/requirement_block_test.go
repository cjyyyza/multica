package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

type requirementBlockResponse struct {
	Requirement     service.RequirementDetail `json:"requirement"`
	QuestionComment struct {
		ID            string `json:"id"`
		Content       string `json:"content"`
		AuthorType    string `json:"author_type"`
		SourceTaskID  string `json:"source_task_id"`
		IssueRevision int64  `json:"issue_revision"`
	} `json:"question_comment"`
	Wakeup struct {
		ID                string `json:"id"`
		QuestionCommentID string `json:"question_comment_id"`
		Revision          int64  `json:"revision"`
		Generation        int64  `json:"generation"`
		State             string `json:"state"`
	} `json:"wakeup"`
}

func newRequirementBlockFixture(t *testing.T, externalID string) requirementReviewFixture {
	t.Helper()
	f := newRequirementReviewFixture(t, externalID)
	dbfx.Exec(t, `UPDATE issue SET status = 'in_progress', revision = revision + 1 WHERE id = $1`, f.Issue.ID)
	issue, err := testHandler.Queries.GetIssue(context.Background(), f.Issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.Issue = issue
	return f
}

func newRequirementBlockTask(t *testing.T, f requirementReviewFixture) string {
	t.Helper()
	var runtimeID string
	dbfx.QueryRow(t, `SELECT runtime_id::text FROM agent WHERE id = $1`, f.AgentID).Scan(&runtimeID)
	return dbfx.Task(t, f.AgentID, testutil.Cols{
		"issue_id": f.Issue.ID, "runtime_id": runtimeID, "requirement_id": f.Detail.ID,
		"mode": "execute", "status": "running", "started_at": testutil.Raw("now()"),
	})
}

func requirementBlockRequest(f requirementReviewFixture, taskID, question, targetID string, revision int64, key string) *http.Request {
	req := requirementActionRequest(testUserID, "block", f.Detail.ID, service.BlockRequirementRequest{
		Question: question, TargetUserID: targetID, ExpectedIssueRevision: revision,
	}, key)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set(taskpolicy.HeaderTaskMode, taskpolicy.ModeExecute)
	req.Header.Set("X-Task-ID", taskID)
	req.Header.Set("X-Agent-ID", f.AgentID)
	req.Header.Set(taskpolicy.HeaderRequirementID, f.Detail.ID)
	return req
}

func registerRequirementBottomCleanup(t *testing.T, issueID pgtype.UUID, wakeupID string) {
	t.Helper()
	dbfx.Cleanup(t, `DELETE FROM issue_wakeup WHERE id = $1`, wakeupID)
	dbfx.Cleanup(t, `DELETE FROM issue_wakeup_receipt WHERE wakeup_id = $1`, wakeupID)
	dbfx.Cleanup(t, `DELETE FROM activity_log WHERE issue_id = $1 AND action IN ('wakeup_created', 'wakeup_triggered')`, issueID)
}

func TestBlockRequirementPersistsQuestionWakeupOutboxAndReplay(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementBlockFixture(t, "97001")
	taskID := newRequirementBlockTask(t, f)
	request := func() *http.Request {
		return requirementBlockRequest(f, taskID, "  Need the deployment target  ", testUserID, f.Issue.Revision, "block-success-97001")
	}
	response := testutil.Call(t, testHandler.BlockRequirement, request()).Want(http.StatusOK)
	var blocked requirementBlockResponse
	response.JSON(&blocked)
	registerRequirementBottomCleanup(t, f.Issue.ID, blocked.Wakeup.ID)
	if blocked.Requirement.State != "blocked" || blocked.QuestionComment.Content != "Need the deployment target" ||
		blocked.QuestionComment.AuthorType != "agent" || blocked.QuestionComment.SourceTaskID != taskID ||
		blocked.QuestionComment.ID != blocked.Wakeup.QuestionCommentID || blocked.Wakeup.Revision != 1 ||
		blocked.Wakeup.Generation != 1 || blocked.Wakeup.State != "armed" {
		t.Fatalf("block response = %s", response.Body)
	}
	issue, err := testHandler.Queries.GetIssue(context.Background(), f.Issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Status != "blocked" || issue.Revision != f.Issue.Revision+2 || blocked.QuestionComment.IssueRevision != issue.Revision {
		t.Fatalf("blocked issue = status %s revision %d, want revision %d, comment revision %d, body %s", issue.Status, issue.Revision, f.Issue.Revision+2, blocked.QuestionComment.IssueRevision, response.Body)
	}
	var bottomAgent, bottomMode, actorType, actorID, parentID string
	dbfx.QueryRow(t, `SELECT agent_id::text, mode, filter_actor_type, filter_actor_id::text, parent_comment_id::text FROM issue_wakeup WHERE id = $1`,
		blocked.Wakeup.ID).Scan(&bottomAgent, &bottomMode, &actorType, &actorID, &parentID)
	if bottomAgent != f.AgentID || bottomMode != "once" || actorType != "member" || actorID != testUserID || parentID != blocked.QuestionComment.ID {
		t.Fatalf("bottom wakeup = agent %s mode %s actor %s/%s parent %s", bottomAgent, bottomMode, actorType, actorID, parentID)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE requirement_id = $1 AND requirement_kind = 'requirement_blocked' AND occurrence_id = $2 AND status = 'pending' AND recipient_user_id = $3::uuid`, f.Detail.ID, blocked.QuestionComment.ID, testUserID); got != 1 {
		t.Fatalf("blocked outbox = %d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement_action WHERE requirement_id = $1 AND action = 'block' AND actor_type = 'agent' AND actor_id = $2`, f.Detail.ID, f.AgentID); got != 1 {
		t.Fatalf("block actions = %d", got)
	}

	replayed := testutil.Call(t, testHandler.BlockRequirement, request()).Want(http.StatusOK)
	if !requirementSameJSON(t, response.Body.String(), replayed.Body.String()) {
		t.Fatalf("block replay changed response: %s", replayed.Body)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1`, f.Issue.ID); got != 1 {
		t.Fatalf("comments after replay = %d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM issue_wakeup WHERE requirement_id = $1`, f.Detail.ID); got != 1 {
		t.Fatalf("requirement wakeups after replay = %d", got)
	}

	second := testutil.Call(t, testHandler.BlockRequirement, requirementBlockRequest(f, taskID, "Need a second answer", testUserID, issue.Revision, "block-second-97001")).Want(http.StatusOK)
	var again requirementBlockResponse
	second.JSON(&again)
	registerRequirementBottomCleanup(t, f.Issue.ID, again.Wakeup.ID)
	if again.QuestionComment.ID == blocked.QuestionComment.ID || again.Wakeup.ID == blocked.Wakeup.ID {
		t.Fatalf("second block reused question/wakeup: %s", second.Body)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM issue_wakeup WHERE id = $1 AND enabled = false AND disabled_at IS NOT NULL`, blocked.Wakeup.ID); got != 1 {
		t.Fatal("previous question wakeup was not fenced")
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM issue_wakeup WHERE id = $1 AND revision = 1 AND enabled AND disabled_at IS NULL`, again.Wakeup.ID); got != 1 {
		t.Fatal("new question did not start armed at revision one")
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE requirement_id = $1 AND requirement_kind = 'requirement_blocked' AND occurrence_id = $2 AND status = 'superseded'`, f.Detail.ID, blocked.QuestionComment.ID); got != 1 {
		t.Fatal("previous blocked notification was not superseded")
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE requirement_id = $1 AND requirement_kind = 'requirement_blocked' AND occurrence_id = $2 AND status = 'pending'`, f.Detail.ID, again.QuestionComment.ID); got != 1 {
		t.Fatal("new blocked notification is not pending")
	}
}

func TestBlockRequirementValidatesTargetTaskAndRevision(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementBlockFixture(t, "97002")
	taskID := newRequirementBlockTask(t, f)
	other := dbfx.User(t, "Requirement block nonparticipant", "block-nonparticipant-97002@example.test")
	dbfx.Member(t, testWorkspaceID, other, "member")
	for _, tc := range []struct {
		name       string
		taskID     string
		targetID   string
		revision   int64
		wantStatus int
		wantCode   string
	}{
		{name: "nonparticipant target", taskID: taskID, targetID: other, revision: f.Issue.Revision, wantStatus: http.StatusForbidden, wantCode: service.RequirementTargetNotParticipant},
		{name: "not current execute task", taskID: f.InitialTaskID, targetID: testUserID, revision: f.Issue.Revision, wantStatus: http.StatusForbidden, wantCode: service.RequirementTaskScopeCode},
		{name: "stale issue revision", taskID: taskID, targetID: testUserID, revision: f.Issue.Revision - 1, wantStatus: http.StatusConflict, wantCode: "revision_conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := testutil.Call(t, testHandler.BlockRequirement, requirementBlockRequest(f, tc.taskID, "question", tc.targetID, tc.revision, "block-invalid-"+tc.name)).Want(tc.wantStatus)
			if response.Map()["code"] != tc.wantCode {
				t.Fatalf("error = %s", response.Body)
			}
			if got := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1`, f.Issue.ID); got != 0 {
				t.Fatalf("invalid block wrote comments = %d", got)
			}
		})
	}
}

func TestBlockRequirementCommitRollback(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementBlockFixture(t, "97003")
	taskID := newRequirementBlockTask(t, f)
	previous := testHandler.TxStarter
	testHandler.TxStarter = requirementCommitStarter{pool: testPool, injectedErr: fmt.Errorf("block commit failure")}
	t.Cleanup(func() { testHandler.TxStarter = previous })
	testutil.Call(t, testHandler.BlockRequirement, requirementBlockRequest(f, taskID, "rollback question", testUserID, f.Issue.Revision, "block-rollback-97003")).Want(http.StatusInternalServerError)
	testHandler.TxStarter = previous
	issue, err := testHandler.Queries.GetIssue(context.Background(), f.Issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Status != "in_progress" {
		t.Fatalf("rollback status = %s", issue.Status)
	}
	for table, query := range map[string]string{
		"comments":  `SELECT count(*) FROM comment WHERE issue_id = $1`,
		"wakeups":   `SELECT count(*) FROM issue_wakeup WHERE requirement_id = $1`,
		"outbox":    `SELECT count(*) FROM popo_bridge_command WHERE requirement_id = $1 AND requirement_kind = 'requirement_blocked'`,
		"actions":   `SELECT count(*) FROM requirement_action WHERE requirement_id = $1 AND action = 'block'`,
		"bottom":    `SELECT count(*) FROM issue_wakeup WHERE issue_id = $1`,
		"actionsDB": `SELECT count(*) FROM requirement_event WHERE requirement_id = $1 AND action = 'blocked'`,
	} {
		arg := any(f.Issue.ID)
		if table == "wakeups" || table == "outbox" || table == "actions" || table == "actionsDB" {
			arg = f.Detail.ID
		}
		if got := dbfx.Count(t, query, arg); got != 0 {
			t.Fatalf("%s remained after rollback: %d", table, got)
		}
	}
}
