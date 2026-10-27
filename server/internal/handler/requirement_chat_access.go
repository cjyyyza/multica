package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) requirementChatParticipant(ctx context.Context, workspaceID, sessionID, userID pgtype.UUID) (bool, bool, map[string]bool, error) {
	requirement, err := h.Queries.GetRequirementByChatSession(ctx, db.GetRequirementByChatSessionParams{
		WorkspaceID: workspaceID, ChatSessionID: sessionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil, nil
	}
	if err != nil {
		return false, false, nil, err
	}
	if _, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID: userID, WorkspaceID: workspaceID,
	}); errors.Is(err, pgx.ErrNoRows) {
		return true, false, map[string]bool{}, nil
	} else if err != nil {
		return true, false, nil, err
	}
	rows, err := h.Queries.ListRequirementParticipantRoles(ctx, db.ListRequirementParticipantRolesParams{
		WorkspaceID: workspaceID, RequirementID: requirement.ID, UserID: userID,
	})
	if err != nil {
		return true, false, nil, err
	}
	roles := make(map[string]bool, len(rows))
	for _, row := range rows {
		roles[row.Role] = true
	}
	return true, len(roles) > 0, roles, nil
}

func (h *Handler) requirementChatWriteAllowed(ctx context.Context, workspaceID, sessionID, userID pgtype.UUID) (bool, error) {
	found, participant, roles, err := h.requirementChatParticipant(ctx, workspaceID, sessionID, userID)
	if err != nil || !found {
		return !found, err
	}
	return participant && (roles["requester"] || roles["approver"]), nil
}

func (h *Handler) addAccessibleRequirementAgents(ctx context.Context, workspaceID, userID pgtype.UUID, allowed map[string]struct{}) error {
	ids, err := h.Queries.ListRequirementAgentIDsForUser(ctx, db.ListRequirementAgentIDsForUserParams{
		WorkspaceID: workspaceID, UserID: userID,
	})
	if err != nil {
		return err
	}
	for _, id := range ids {
		allowed[uuidToString(id)] = struct{}{}
	}
	return nil
}

func requireChatCreator(w http.ResponseWriter, session db.ChatSession, userID string) bool {
	if uuidToString(session.CreatorID) == userID {
		return true
	}
	writeError(w, http.StatusForbidden, "only the chat creator can perform this action")
	return false
}

func chatPersonalProjection(creatorID, userID string, unreadCount int32, pinned bool) (bool, int, bool) {
	if creatorID != userID {
		return false, 0, false
	}
	return unreadCount > 0, int(unreadCount), pinned
}
