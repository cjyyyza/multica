package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func requirementSourceAttachment(t *testing.T, workspaceID, externalID, name, contentType string, mirror bool) yixiezuo.Attachment {
	t.Helper()
	attachment := yixiezuo.Attachment{
		ID: externalID, Name: name,
		URL: "https://dj01.pm.netease.com/attachments/" + externalID + "/" + name,
	}
	if mirror {
		attachment.LocalID = dbfx.Insert(t, "attachment", testutil.Cols{
			"workspace_id": workspaceID, "uploader_type": "member", "uploader_id": testUserID,
			"filename": name, "url": "https://media.invalid/" + externalID + "/" + name,
			"content_type": contentType, "size_bytes": 128,
		})
	}
	return attachment
}

func TestApproveRequirementLinksOnlySelectedSourceAttachments(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	selected := requirementSourceAttachment(t, testWorkspaceID, "101", "design.png", "image/png", true)
	unselected := requirementSourceAttachment(t, testWorkspaceID, "102", "notes.txt", "text/plain", true)
	missingButUnselected := requirementSourceAttachment(t, testWorkspaceID, "103", "missing.pdf", "application/pdf", false)
	f := newRequirementApproveFixtureWithAttachments(t, "94110",
		[]yixiezuo.Attachment{selected, unselected, missingButUnselected}, []string{selected.ID})

	response, _ := approveRequirementForTest(t, f, "approve-selected-attachments-94110")
	issue := approveIssueForTest(t, response)
	var linkedID string
	dbfx.QueryRow(t, `SELECT id::text FROM attachment WHERE issue_id = $1`, issue.ID).Scan(&linkedID)
	if linkedID != selected.LocalID {
		t.Fatalf("Issue attachment = %s, want selected mirror %s", linkedID, selected.LocalID)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM attachment WHERE issue_id = $1`, issue.ID); got != 1 {
		t.Fatalf("Issue attachment count = %d, want only the selected attachment", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM attachment WHERE id = $1 AND issue_id IS NULL`, unselected.LocalID); got != 1 {
		t.Fatal("unselected mirrored attachment was linked to the Issue")
	}
}

func TestRequirementCandidateRejectsUnavailableAttachmentSelections(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	t.Run("unknown external id", func(t *testing.T) {
		selected := requirementSourceAttachment(t, testWorkspaceID, "111", "known.png", "image/png", true)
		f := newRequirementApproveFixtureWithAttachments(t, "94111", []yixiezuo.Attachment{selected}, []string{selected.ID})
		input := validCandidateRequestWithAttachments(t, f.Opened.Requirement, 3, []string{"does-not-exist"})
		response := testutil.Call(t, testHandler.SubmitRequirementCandidate, candidateRequest(
			testUserID, f.AgentID, f.Opened.PendingTask.TaskID, f.Detail.ID, f.Opened.Session.ID, input, "candidate-unknown-94111")).Want(http.StatusConflict)
		if response.Map()["code"] != "requirement_attachment_unavailable" {
			t.Fatalf("unknown attachment response = %s", response.Body)
		}
		if got := dbfx.Count(t, `SELECT count(*) FROM requirement_candidate WHERE requirement_id = $1`, f.Detail.ID); got != 1 {
			t.Fatalf("unknown selection created another candidate: %d", got)
		}
	})
	t.Run("cross workspace mirror", func(t *testing.T) {
		selected := requirementSourceAttachment(t, testWorkspaceID, "112", "local.png", "image/png", true)
		otherWorkspace := dbfx.Workspace(t, "Requirement attachment other workspace", "requirement-attachment-other-94112")
		foreign := requirementSourceAttachment(t, otherWorkspace, "113", "foreign.png", "image/png", true)
		f := newRequirementApproveFixtureWithAttachments(t, "94112", []yixiezuo.Attachment{selected, foreign}, []string{selected.ID})
		input := validCandidateRequestWithAttachments(t, f.Opened.Requirement, 3, []string{foreign.ID})
		response := testutil.Call(t, testHandler.SubmitRequirementCandidate, candidateRequest(
			testUserID, f.AgentID, f.Opened.PendingTask.TaskID, f.Detail.ID, f.Opened.Session.ID, input, "candidate-foreign-94112")).Want(http.StatusConflict)
		if response.Map()["code"] != "requirement_attachment_unavailable" {
			t.Fatalf("cross-workspace attachment response = %s", response.Body)
		}
	})
	t.Run("private chat mirror", func(t *testing.T) {
		selected := requirementSourceAttachment(t, testWorkspaceID, "118", "local.png", "image/png", true)
		chatMirror := requirementSourceAttachment(t, testWorkspaceID, "119", "private.png", "image/png", true)
		f := newRequirementApproveFixtureWithAttachments(t, "94116", []yixiezuo.Attachment{selected, chatMirror}, []string{selected.ID})
		dbfx.Exec(t, `UPDATE attachment SET chat_session_id = $2 WHERE id = $1`, chatMirror.LocalID, f.Opened.Session.ID)
		input := validCandidateRequestWithAttachments(t, f.Opened.Requirement, 3, []string{chatMirror.ID})
		response := testutil.Call(t, testHandler.SubmitRequirementCandidate, candidateRequest(
			testUserID, f.AgentID, f.Opened.PendingTask.TaskID, f.Detail.ID, f.Opened.Session.ID, input, "candidate-chat-94116")).Want(http.StatusConflict)
		if response.Map()["code"] != "requirement_attachment_unavailable" {
			t.Fatalf("private chat attachment response = %s", response.Body)
		}
	})
	t.Run("missing local mirror", func(t *testing.T) {
		selected := requirementSourceAttachment(t, testWorkspaceID, "114", "local.png", "image/png", true)
		missing := requirementSourceAttachment(t, testWorkspaceID, "115", "missing.png", "image/png", false)
		f := newRequirementApproveFixtureWithAttachments(t, "94113", []yixiezuo.Attachment{selected, missing}, []string{selected.ID})
		input := validCandidateRequestWithAttachments(t, f.Opened.Requirement, 3, []string{missing.ID})
		response := testutil.Call(t, testHandler.SubmitRequirementCandidate, candidateRequest(
			testUserID, f.AgentID, f.Opened.PendingTask.TaskID, f.Detail.ID, f.Opened.Session.ID, input, "candidate-missing-94113")).Want(http.StatusConflict)
		if response.Map()["code"] != "requirement_attachment_unavailable" {
			t.Fatalf("missing mirror response = %s", response.Body)
		}
	})
}

func TestApproveRequirementRechecksMirrorAvailability(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	selected := requirementSourceAttachment(t, testWorkspaceID, "116", "deleted.png", "image/png", true)
	f := newRequirementApproveFixtureWithAttachments(t, "94114", []yixiezuo.Attachment{selected}, []string{selected.ID})
	dbfx.Exec(t, `DELETE FROM attachment WHERE id = $1`, selected.LocalID)
	response := testutil.Call(t, testHandler.ApproveRequirement,
		approveRequirementRequest(testUserID, f.Detail.ID, f.input(), "approve-missing-mirror-94114")).Want(http.StatusConflict)
	if response.Map()["code"] != "requirement_attachment_unavailable" {
		t.Fatalf("approve missing mirror response = %s", response.Body)
	}
	assertRequirementApproveRolledBack(t, f)
}

func TestApproveRequirementAttachmentLinkFailureRollsBack(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	selected := requirementSourceAttachment(t, testWorkspaceID, "117", "linked.png", "image/png", true)
	f := newRequirementApproveFixtureWithAttachments(t, "94115", []yixiezuo.Attachment{selected}, []string{selected.ID})
	otherIssue := dbfx.Issue(t, "Attachment already linked elsewhere")
	dbfx.Exec(t, `UPDATE attachment SET issue_id = $2 WHERE id = $1`, selected.LocalID, otherIssue)
	response := testutil.Call(t, testHandler.ApproveRequirement,
		approveRequirementRequest(testUserID, f.Detail.ID, f.input(), "approve-link-conflict-94115")).Want(http.StatusConflict)
	if response.Map()["code"] != "requirement_attachment_unavailable" {
		t.Fatalf("approve link conflict response = %s", response.Body)
	}
	assertRequirementApproveRolledBack(t, f)
	if got := dbfx.Count(t, `SELECT count(*) FROM requirement_action WHERE requirement_id = $1 AND action = 'approve'`, f.Detail.ID); got != 0 {
		t.Fatalf("failed attachment link committed an approve action: %d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM attachment WHERE id = $1 AND issue_id = $2`, selected.LocalID, otherIssue); got != 1 {
		t.Fatal("rollback changed the pre-existing attachment ownership")
	}
}
