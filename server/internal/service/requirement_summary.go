package service

import (
	"fmt"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type RequirementSummary struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspaceId"`
	Revision      int64   `json:"revision"`
	State         string  `json:"state"`
	IntakeState   string  `json:"intakeState"`
	SourceHost    string  `json:"sourceHost"`
	SourceKind    string  `json:"sourceKind"`
	ExternalID    string  `json:"externalId"`
	Title         string  `json:"title"`
	RequesterID   string  `json:"requesterId"`
	AgentID       *string `json:"agentId"`
	SquadID       *string `json:"squadId"`
	ProjectID     *string `json:"projectId"`
	ChatSessionID *string `json:"chatSessionId"`
	IssueID       *string `json:"issueId"`
	IssueDeleted  bool    `json:"issueDeleted"`
	CreatedAt     string  `json:"createdAt"`
	UpdatedAt     string  `json:"updatedAt"`
}

func RequirementSummaryForRow(row db.ListRequirementsForParticipantRow) (RequirementSummary, error) {
	state := row.IntakeState
	if row.IssueID.Valid && !row.IssueDeleted {
		var ok bool
		state, ok = RequirementStateForIssue(row.IssueStatus, row.IssueStatusCategory)
		if !ok {
			return RequirementSummary{}, fmt.Errorf("unknown requirement issue status %q", row.IssueStatus)
		}
	}
	if row.CancelledAt.Valid || row.IssueDeleted {
		state = "cancelled"
	}
	return RequirementSummary{
		ID: util.UUIDToString(row.ID), WorkspaceID: util.UUIDToString(row.WorkspaceID), Revision: row.Revision,
		State: state, IntakeState: row.IntakeState, SourceHost: row.SourceHost, SourceKind: row.SourceKind,
		ExternalID: row.ExternalID, Title: row.Title, RequesterID: util.UUIDToString(row.RequesterID),
		AgentID: requirementUUIDPtr(row.AgentID), SquadID: requirementStringPtr(row.SquadID),
		ProjectID: requirementUUIDPtr(row.ProjectID), ChatSessionID: requirementUUIDPtr(row.ChatSessionID),
		IssueID: requirementUUIDPtr(row.IssueID), IssueDeleted: row.IssueDeleted,
		CreatedAt: requirementTime(row.CreatedAt), UpdatedAt: requirementTime(row.UpdatedAt),
	}, nil
}
