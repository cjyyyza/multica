package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type normalizedRequirementParticipant struct {
	UserID string   `json:"userId"`
	Roles  []string `json:"roles"`
	uuid   pgtype.UUID
}

func normalizeRequirementParticipants(inputs []RequirementParticipantInput) ([]normalizedRequirementParticipant, error) {
	seen := make(map[string]struct{}, len(inputs))
	out := make([]normalizedRequirementParticipant, 0, len(inputs))
	for _, input := range inputs {
		id, err := util.ParseUUID(input.UserID)
		if err != nil {
			return nil, requirementBadRequest("requirement_participant_invalid", "participant userId must be a UUID")
		}
		canonicalID := util.UUIDToString(id)
		if _, exists := seen[canonicalID]; exists {
			return nil, requirementBadRequest("requirement_participant_invalid", "participants must not contain duplicate users")
		}
		seen[canonicalID] = struct{}{}
		if len(input.Roles) == 0 {
			return nil, requirementBadRequest("requirement_participant_invalid", "participant roles must not be empty")
		}
		roleSeen := make(map[string]struct{}, len(input.Roles))
		roles := make([]string, 0, len(input.Roles))
		for _, role := range input.Roles {
			if role != "requester" && role != "approver" && role != "viewer" {
				return nil, requirementBadRequest("requirement_participant_invalid", "unknown participant role")
			}
			if _, exists := roleSeen[role]; exists {
				return nil, requirementBadRequest("requirement_participant_invalid", "participant roles must not contain duplicates")
			}
			roleSeen[role] = struct{}{}
			roles = append(roles, role)
		}
		sort.Strings(roles)
		out = append(out, normalizedRequirementParticipant{UserID: canonicalID, Roles: roles, uuid: id})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}

func createRequirementParticipants(inputs []RequirementParticipantInput, requesterID pgtype.UUID) ([]normalizedRequirementParticipant, error) {
	participants, err := normalizeRequirementParticipants(inputs)
	if err != nil {
		return nil, err
	}
	requester := util.UUIDToString(requesterID)
	requesterIndex := -1
	approverCount := 0
	for i := range participants {
		for _, role := range participants[i].Roles {
			if role == "requester" && participants[i].UserID != requester {
				return nil, requirementBadRequest("requirement_participant_invalid", "only the current user can be the requester")
			}
			if role == "approver" {
				approverCount++
			}
		}
		if participants[i].UserID == requester {
			requesterIndex = i
		}
	}
	if requesterIndex < 0 {
		participants = append(participants, normalizedRequirementParticipant{UserID: requester, Roles: []string{"requester"}, uuid: requesterID})
		requesterIndex = len(participants) - 1
	} else if !hasRequirementRole(participants[requesterIndex].Roles, "requester") {
		participants[requesterIndex].Roles = append(participants[requesterIndex].Roles, "requester")
	}
	if approverCount == 0 {
		participants[requesterIndex].Roles = append(participants[requesterIndex].Roles, "approver")
	}
	sort.Strings(participants[requesterIndex].Roles)
	sort.Slice(participants, func(i, j int) bool { return participants[i].UserID < participants[j].UserID })
	return participants, nil
}

func updateRequirementParticipants(inputs []RequirementParticipantInput, requesterID pgtype.UUID) ([]normalizedRequirementParticipant, error) {
	if inputs == nil {
		return nil, requirementBadRequest("requirement_participant_invalid", "participants must be the complete participant list")
	}
	participants, err := normalizeRequirementParticipants(inputs)
	if err != nil {
		return nil, err
	}
	requester := util.UUIDToString(requesterID)
	requesterCount, approverCount := 0, 0
	for _, participant := range participants {
		for _, role := range participant.Roles {
			switch role {
			case "requester":
				requesterCount++
				if participant.UserID != requester {
					return nil, requirementBadRequest("requirement_participant_invalid", "the requester cannot be replaced")
				}
			case "approver":
				approverCount++
			}
		}
	}
	if requesterCount != 1 {
		return nil, requirementBadRequest("requirement_participant_invalid", "the requester must be retained exactly once")
	}
	if approverCount == 0 {
		return nil, requirementBadRequest("requirement_participant_invalid", "at least one approver is required")
	}
	return participants, nil
}

func hasRequirementRole(roles []string, wanted string) bool {
	for _, role := range roles {
		if role == wanted {
			return true
		}
	}
	return false
}

func validateRequirementParticipantMembers(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, participants []normalizedRequirementParticipant) error {
	for _, participant := range participants {
		_, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
			UserID: participant.uuid, WorkspaceID: workspaceID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return requirementBadRequest("requirement_participant_not_member", "every participant must be a current workspace member")
		}
		if err != nil {
			return fmt.Errorf("load requirement participant member: %w", err)
		}
	}
	return nil
}

func insertRequirementParticipants(ctx context.Context, q *db.Queries, workspaceID, requirementID pgtype.UUID, participants []normalizedRequirementParticipant) error {
	for _, participant := range participants {
		for _, role := range participant.Roles {
			if _, err := q.CreateRequirementParticipant(ctx, db.CreateRequirementParticipantParams{
				WorkspaceID: workspaceID, RequirementID: requirementID, UserID: participant.uuid, Role: role,
			}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("insert requirement participant: %w", err)
			}
		}
	}
	return nil
}

func requireRequirementBlockedTargetsRetained(ctx context.Context, q *db.Queries, row db.Requirement, participants []normalizedRequirementParticipant) error {
	retained := make(map[pgtype.UUID]bool, len(participants))
	for _, participant := range participants {
		retained[participant.uuid] = true
	}
	wakeups, err := q.ListRequirementIssueWakeups(ctx, db.ListRequirementIssueWakeupsParams{WorkspaceID: row.WorkspaceID, RequirementID: row.ID})
	if err != nil {
		return fmt.Errorf("list requirement blocked targets: %w", err)
	}
	for _, wakeup := range wakeups {
		if wakeup.DisabledAt.Valid {
			continue
		}
		if !retained[wakeup.FilterActorID] {
			return newRequirementError(http.StatusConflict, "requirement_blocked_target_required", "the current blocked target must remain a participant; resolve the question before removing them")
		}
	}
	return nil
}

func fenceRemovedRequirementRecipients(ctx context.Context, q *db.Queries, workspaceID, requirementID pgtype.UUID, participants []normalizedRequirementParticipant) error {
	keep := make([]pgtype.UUID, 0, len(participants))
	for _, participant := range participants {
		keep = append(keep, participant.uuid)
	}
	if err := q.SupersedeRemovedRequirementPopoCommandRecipients(ctx, db.SupersedeRemovedRequirementPopoCommandRecipientsParams{
		WorkspaceID: workspaceID, RequirementID: requirementID, KeepUserIds: keep,
	}); err != nil {
		return fmt.Errorf("supersede removed requirement recipients: %w", err)
	}
	return nil
}
