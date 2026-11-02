package service

import (
	"context"
	"errors"
	"log/slog"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ChatContentAccess is who may read a chat session's content at the moment of
// the call. It is a snapshot, not a stored grant: callers resolve it again on
// the next read or publish.
//
// Ordinary chats are creator-only. Requirement chats reuse the participant
// read check (a current workspace member with any requirement role). The
// creator is always included when known. A failed lookup is not visible:
// participant resolution failure keeps only the known creator, and a missing
// creator discards the audience.
type ChatContentAccess struct {
	CreatorID string
	Readers   []string
}

// ResolveChatContentAccess loads the current readers of a chat session.
// Query failure is not visible. Write permission is unchanged.
func ResolveChatContentAccess(ctx context.Context, q *db.Queries, sessionID pgtype.UUID) ChatContentAccess {
	if q == nil || !sessionID.Valid {
		return ChatContentAccess{}
	}
	session, err := q.GetChatSession(ctx, sessionID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("chat content access: session lookup failed; treating as not visible",
				"session_id", util.UUIDToString(sessionID), "error", err)
		}
		return ChatContentAccess{}
	}
	creatorID := util.UUIDToString(session.CreatorID)
	requirement, err := q.GetRequirementByChatSession(ctx, db.GetRequirementByChatSessionParams{
		WorkspaceID:   session.WorkspaceID,
		ChatSessionID: session.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ChatContentAccess{CreatorID: creatorID, Readers: assembleChatContentReaders(creatorID, false, nil, false)}
	}
	if err != nil {
		slog.Warn("chat content access: requirement lookup failed; delivering only to the known creator",
			"session_id", util.UUIDToString(session.ID), "error", err)
		return ChatContentAccess{CreatorID: creatorID, Readers: assembleChatContentReaders(creatorID, true, nil, true)}
	}
	rows, err := q.ListRequirementParticipants(ctx, db.ListRequirementParticipantsParams{
		WorkspaceID:   session.WorkspaceID,
		RequirementID: requirement.ID,
	})
	if err != nil {
		slog.Warn("chat content access: participant lookup failed; delivering only to the known creator",
			"session_id", util.UUIDToString(session.ID), "error", err)
		return ChatContentAccess{CreatorID: creatorID, Readers: assembleChatContentReaders(creatorID, true, nil, true)}
	}
	participants, ok := memberParticipantIDs(ctx, q, session.WorkspaceID, rows)
	if !ok {
		slog.Warn("chat content access: membership lookup failed; delivering only to the known creator",
			"session_id", util.UUIDToString(session.ID))
		return ChatContentAccess{CreatorID: creatorID, Readers: assembleChatContentReaders(creatorID, true, nil, true)}
	}
	return ChatContentAccess{CreatorID: creatorID, Readers: assembleChatContentReaders(creatorID, true, participants, false)}
}

// ResolveChatContentVisibility resolves a page of sessions with batch queries.
// Missing sessions and viewers without access remain absent from the result.
func ResolveChatContentVisibility(ctx context.Context, q *db.Queries, sessionIDs []pgtype.UUID, viewerID string) (map[pgtype.UUID]bool, error) {
	visible := make(map[pgtype.UUID]bool, len(sessionIDs))
	if len(sessionIDs) == 0 || viewerID == "" {
		return visible, nil
	}
	userID, err := util.ParseUUID(viewerID)
	if err != nil {
		return nil, err
	}
	uniqueIDs := make([]pgtype.UUID, 0, len(sessionIDs))
	seenSessions := make(map[pgtype.UUID]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		if !sessionID.Valid {
			continue
		}
		if _, ok := seenSessions[sessionID]; ok {
			continue
		}
		seenSessions[sessionID] = struct{}{}
		uniqueIDs = append(uniqueIDs, sessionID)
	}
	if len(uniqueIDs) == 0 {
		return visible, nil
	}
	sessions, err := q.ListChatSessionCreators(ctx, uniqueIDs)
	if err != nil {
		return nil, err
	}
	owners := make(map[pgtype.UUID]string, len(sessions))
	sessionIDsByWorkspace := make(map[pgtype.UUID][]pgtype.UUID)
	for _, session := range sessions {
		owners[session.ID] = util.UUIDToString(session.CreatorID)
		sessionIDsByWorkspace[session.WorkspaceID] = append(sessionIDsByWorkspace[session.WorkspaceID], session.ID)
	}
	requirementBySession := make(map[pgtype.UUID]pgtype.UUID)
	requirementIDsByWorkspace := make(map[pgtype.UUID][]pgtype.UUID)
	for workspaceID, ids := range sessionIDsByWorkspace {
		rows, err := q.ListRequirementIDsByChatSessions(ctx, db.ListRequirementIDsByChatSessionsParams{
			WorkspaceID: workspaceID, ChatSessionIds: ids,
		})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			requirementBySession[row.ChatSessionID] = row.ID
			requirementIDsByWorkspace[workspaceID] = append(requirementIDsByWorkspace[workspaceID], row.ID)
		}
	}
	participantRequirements := make(map[pgtype.UUID]bool)
	for workspaceID, requirementIDs := range requirementIDsByWorkspace {
		rows, err := q.ListRequirementIDsForParticipantIn(ctx, db.ListRequirementIDsForParticipantInParams{
			WorkspaceID: workspaceID, RequirementIds: requirementIDs, UserID: userID,
		})
		if err != nil {
			return nil, err
		}
		for _, requirementID := range rows {
			participantRequirements[requirementID] = true
		}
	}
	for sessionID, creatorID := range owners {
		if creatorID == viewerID {
			visible[sessionID] = true
			continue
		}
		if requirementID, ok := requirementBySession[sessionID]; ok && participantRequirements[requirementID] {
			visible[sessionID] = true
		}
	}
	return visible, nil
}

// ChatContentVisible reports whether viewerID may read the session's content.
// A failed lookup is not visible. An empty viewer is not visible.
func ChatContentVisible(ctx context.Context, q *db.Queries, sessionID pgtype.UUID, viewerID string) bool {
	if viewerID == "" {
		return false
	}
	for _, id := range ResolveChatContentAccess(ctx, q, sessionID).Readers {
		if id == viewerID {
			return true
		}
	}
	return false
}

// assembleChatContentReaders is the shared visibility rule, independent of the
// database. requirementBound false is an ordinary chat. participantsFailed
// keeps only the known creator. The creator is included whenever known.
func assembleChatContentReaders(creatorID string, requirementBound bool, participantIDs []string, participantsFailed bool) []string {
	if !requirementBound || participantsFailed {
		if creatorID == "" {
			return nil
		}
		return []string{creatorID}
	}
	seen := map[string]struct{}{}
	extras := make([]string, 0, len(participantIDs))
	for _, id := range participantIDs {
		if id == "" || id == creatorID {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		extras = append(extras, id)
	}
	sort.Strings(extras)
	if creatorID == "" {
		if len(extras) == 0 {
			return nil
		}
		return extras
	}
	readers := make([]string, 0, 1+len(extras))
	readers = append(readers, creatorID)
	return append(readers, extras...)
}

// memberParticipantIDs returns participant user IDs who are still members of
// the workspace. A membership lookup failure is not visible (ok=false).
func memberParticipantIDs(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, rows []db.RequirementParticipant) ([]string, bool) {
	if len(rows) == 0 {
		return nil, true
	}
	seen := map[string]pgtype.UUID{}
	userIDs := make([]pgtype.UUID, 0, len(rows))
	workspaceIDs := make([]pgtype.UUID, 0, len(rows))
	for _, row := range rows {
		id := util.UUIDToString(row.UserID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = row.UserID
		userIDs = append(userIDs, row.UserID)
		workspaceIDs = append(workspaceIDs, workspaceID)
	}
	if len(userIDs) == 0 {
		return nil, true
	}
	memberships, err := q.GetRealtimeMemberships(ctx, db.GetRealtimeMembershipsParams{
		UserIds:      userIDs,
		WorkspaceIds: workspaceIDs,
	})
	if err != nil {
		return nil, false
	}
	ids := make([]string, 0, len(memberships))
	for _, membership := range memberships {
		if id := util.UUIDToString(membership.UserID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, true
}
