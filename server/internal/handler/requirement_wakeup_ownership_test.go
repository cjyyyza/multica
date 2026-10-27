package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func assertRequirementWakeupManagementForbidden(t *testing.T, f requirementReviewFixture, wakeupID, questionCommentID string) {
	t.Helper()
	var requirementID string
	dbfx.QueryRow(t, `SELECT requirement_id::text FROM issue_wakeup WHERE id = $1`, wakeupID).Scan(&requirementID)
	if requirementID != f.Detail.ID {
		t.Fatalf("wakeup %s owner = %q, want %q", wakeupID, requirementID, f.Detail.ID)
	}
	wakeup, err := testHandler.Queries.GetIssueWakeup(context.Background(), db.GetIssueWakeupParams{ID: parseUUID(wakeupID), WorkspaceID: f.Issue.WorkspaceID})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		method  string
		body    any
	}{
		{name: "save", handler: testHandler.CreateIssueWakeup, method: http.MethodPut, body: service.WakeupInput{
			AgentID: f.AgentID, Instruction: "ordinary edit", Kind: "event", EventTypes: []string{"comment.created"},
			FilterActorType: "member", FilterActorID: testUserID, ParentCommentID: questionCommentID,
		}},
		{name: "enable", handler: testHandler.EnableIssueWakeup, method: http.MethodPost, body: service.WakeupEnableInput{Revision: wakeup.Revision, Rearm: true}},
		{name: "edit instruction", handler: testHandler.EditIssueWakeupInstruction, method: http.MethodPatch, body: service.WakeupInstructionInput{
			Instruction: "ordinary instruction", ExpectedInstruction: wakeup.Instruction, Revision: wakeup.Revision,
		}},
		{name: "disable", handler: testHandler.DisableIssueWakeup, method: http.MethodPost},
		{name: "trigger", handler: testHandler.TriggerIssueWakeup, method: http.MethodPost},
		{name: "delete", handler: testHandler.DeleteIssueWakeup, method: http.MethodDelete},
	} {
		t.Run(fmt.Sprintf("%s/r%d", tc.name, wakeup.Revision), func(t *testing.T) {
			req := testutil.WithURLParams(newRequest(tc.method, "/api/issues/"+uuidToString(f.Issue.ID)+"/wakeups/"+wakeupID, tc.body), "id", uuidToString(f.Issue.ID), "wakeupID", wakeupID)
			testutil.Call(t, tc.handler, req).Want(http.StatusForbidden)
		})
	}
	stored, err := testHandler.Queries.GetIssueWakeup(context.Background(), db.GetIssueWakeupParams{ID: wakeup.ID, WorkspaceID: wakeup.WorkspaceID})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Revision != wakeup.Revision || stored.Enabled != wakeup.Enabled || stored.Instruction != wakeup.Instruction || stored.RequirementID != wakeup.RequirementID {
		t.Fatalf("ordinary management changed requirement wakeup: before=%+v after=%+v", wakeup, stored)
	}
}

func TestRequirementWakeupOwnerRejectsOrdinaryManagementForEveryGeneration(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	f := newRequirementBlockFixture(t, "97301")
	blockingTaskID := newRequirementBlockTask(t, f)
	blocked := blockRequirementForTest(t, f, blockingTaskID, "Need an owner-safe answer", "owner-block-97301")
	assertRequirementWakeupManagementForbidden(t, f, blocked.Wakeup.ID, blocked.QuestionComment.ID)

	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, blockingTaskID)
	createRequirementAnswerForTest(t, f.Issue.ID, "answer")
	if err := tickRequirementRecoveryForTest(t); err != nil {
		t.Fatal(err)
	}
	runs := requirementRecoveryTasksForTest(t, f, blocked.QuestionComment.ID)
	if len(runs) != 1 {
		t.Fatalf("recovery runs = %d", len(runs))
	}
	completeRequirementRecoveryTaskForTest(t, uuidToString(runs[0].ID))
	wakeup := requirementWakeupForTest(t, f, blocked.QuestionComment.ID)
	if uuidToString(wakeup.ID) != blocked.Wakeup.ID || wakeup.Revision != 2 {
		t.Fatalf("wakeup revision = %d (id %s), want the same row at revision 2", wakeup.Revision, uuidToString(wakeup.ID))
	}
	// In-place rearm keeps one row: every generation must already be guarded.
	assertRequirementWakeupManagementForbidden(t, f, blocked.Wakeup.ID, blocked.QuestionComment.ID)

	if _, err := service.DisableRequirementWakeupsForIssue(context.Background(), testHandler.Queries, f.Issue.WorkspaceID, f.Issue.ID); err != nil {
		t.Fatalf("requirement-owned disable service was rejected: %v", err)
	}
	ordinary, err := (&service.IssueWakeupService{Tasks: testHandler.TaskService}).Create(context.Background(), f.Issue.ID, parseUUID(testUserID), pgtype.UUID{}, service.WakeupInput{
		AgentID: f.AgentID, Kind: "event", EventTypes: []string{"task.completed"}, Instruction: "ordinary wakeup",
	})
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Cleanup(t, `DELETE FROM issue_wakeup WHERE id = $1`, ordinary.ID)
	if ordinary.RequirementID.Valid {
		t.Fatal("ordinary wakeup acquired a requirement owner")
	}
	if err := (&service.IssueWakeupService{Tasks: testHandler.TaskService}).EditInstruction(context.Background(), f.Issue.ID, ordinary.ID, parseUUID(testUserID), service.WakeupInstructionInput{
		Instruction: "ordinary edit", ExpectedInstruction: ordinary.Instruction, Revision: ordinary.Revision,
	}); err != nil {
		t.Fatalf("ordinary edit was rejected: %v", err)
	}
	if _, err := (&service.IssueWakeupService{Tasks: testHandler.TaskService}).Disable(context.Background(), f.Issue.ID, ordinary.ID, parseUUID(testUserID)); err != nil {
		t.Fatalf("ordinary disable was rejected: %v", err)
	}
	if _, err := (&service.IssueWakeupService{Tasks: testHandler.TaskService}).Enable(context.Background(), f.Issue.ID, parseUUID(testUserID), pgtype.UUID{}, ordinary.ID, service.WakeupEnableInput{Revision: ordinary.Revision}); err != nil {
		t.Fatalf("ordinary enable was rejected: %v", err)
	}
	if err := (&service.IssueWakeupService{Tasks: testHandler.TaskService}).Delete(context.Background(), f.Issue.ID, ordinary.ID, parseUUID(testUserID)); err != nil {
		t.Fatalf("ordinary delete was rejected: %v", err)
	}
}
