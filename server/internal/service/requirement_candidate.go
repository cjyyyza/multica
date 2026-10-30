package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const requirementCandidatesRoute = "POST /api/workspaces/{id}/requirements/{requirementId}/candidates"

type SubmitRequirementCandidateRequest struct {
	ExpectedRevision int64           `json:"expectedRevision"`
	Snapshot         json.RawMessage `json:"snapshot"`
}

type RequirementCandidateSnapshot struct {
	Goal               string   `json:"goal"`
	NonGoals           []string `json:"nonGoals"`
	Scope              []string `json:"scope"`
	Constraints        []string `json:"constraints"`
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
	ProjectID          *string  `json:"projectId"`
	AgentID            string   `json:"agentId"`
	AttachmentIDs      []string `json:"attachmentIds,omitempty"`
}

func CanonicalRequirementCandidateSnapshot(raw json.RawMessage, requirement db.Requirement) (RequirementCandidateSnapshot, json.RawMessage, string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return RequirementCandidateSnapshot{}, nil, "", requirementBadRequest("requirement_candidate_invalid", "candidate snapshot must be a JSON object")
	}
	for _, name := range []string{"goal", "nonGoals", "scope", "constraints", "acceptanceCriteria", "projectId", "agentId"} {
		if _, ok := fields[name]; !ok {
			return RequirementCandidateSnapshot{}, nil, "", requirementBadRequest("requirement_candidate_invalid", "candidate snapshot is missing "+name)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var snapshot RequirementCandidateSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return RequirementCandidateSnapshot{}, nil, "", requirementBadRequest("requirement_candidate_invalid", "invalid candidate snapshot")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return RequirementCandidateSnapshot{}, nil, "", requirementBadRequest("requirement_candidate_invalid", "candidate snapshot must contain one JSON value")
	}
	snapshot.Goal = strings.TrimSpace(snapshot.Goal)
	if snapshot.Goal == "" {
		return RequirementCandidateSnapshot{}, nil, "", requirementBadRequest("requirement_candidate_invalid", "candidate goal must not be empty")
	}
	for name, values := range map[string][]string{
		"nonGoals": snapshot.NonGoals, "scope": snapshot.Scope,
		"constraints": snapshot.Constraints, "acceptanceCriteria": snapshot.AcceptanceCriteria,
	} {
		normalized, err := canonicalCandidateStrings(name, values)
		if err != nil {
			return RequirementCandidateSnapshot{}, nil, "", err
		}
		switch name {
		case "nonGoals":
			snapshot.NonGoals = normalized
		case "scope":
			snapshot.Scope = normalized
		case "constraints":
			snapshot.Constraints = normalized
		case "acceptanceCriteria":
			snapshot.AcceptanceCriteria = normalized
		}
	}
	agentID, err := util.ParseUUID(snapshot.AgentID)
	if err != nil || agentID != requirement.AgentID {
		return RequirementCandidateSnapshot{}, nil, "", requirementBadRequest("requirement_candidate_invalid", "candidate agentId must match the requirement agent")
	}
	snapshot.AgentID = util.UUIDToString(agentID)
	if _, _, delegationErr := requirementDelegation(requirement); delegationErr != nil {
		return RequirementCandidateSnapshot{}, nil, "", delegationErr
	}
	if requirement.ProjectID.Valid {
		if snapshot.ProjectID == nil {
			return RequirementCandidateSnapshot{}, nil, "", requirementBadRequest("requirement_candidate_invalid", "candidate projectId must match the requirement project")
		}
		projectID, err := util.ParseUUID(*snapshot.ProjectID)
		if err != nil || projectID != requirement.ProjectID {
			return RequirementCandidateSnapshot{}, nil, "", requirementBadRequest("requirement_candidate_invalid", "candidate projectId must match the requirement project")
		}
		canonical := util.UUIDToString(projectID)
		snapshot.ProjectID = &canonical
	} else {
		if snapshot.ProjectID != nil && strings.TrimSpace(*snapshot.ProjectID) != "" {
			return RequirementCandidateSnapshot{}, nil, "", requirementBadRequest("requirement_candidate_invalid", "candidate projectId must be null when the requirement has no project")
		}
		snapshot.ProjectID = nil
	}
	snapshot.AttachmentIDs, err = canonicalCandidateAttachmentIDs(snapshot.AttachmentIDs)
	if err != nil {
		return RequirementCandidateSnapshot{}, nil, "", err
	}
	normalized, err := json.Marshal(snapshot)
	if err != nil {
		return RequirementCandidateSnapshot{}, nil, "", fmt.Errorf("marshal normalized candidate: %w", err)
	}
	value, err := decodeRequirementJSONValue(normalized)
	if err != nil {
		return RequirementCandidateSnapshot{}, nil, "", fmt.Errorf("decode normalized candidate: %w", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return RequirementCandidateSnapshot{}, nil, "", fmt.Errorf("marshal canonical candidate: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return snapshot, encoded, hex.EncodeToString(digest[:]), nil
}

func canonicalCandidateAttachmentIDs(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return nil, requirementBadRequest("requirement_candidate_invalid", "attachmentIds contains an empty or duplicate item")
		}
		seen[value] = true
		out = append(out, value)
	}
	return out, nil
}

func canonicalCandidateStrings(name string, values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, requirementBadRequest("requirement_candidate_invalid", name+" must contain at least one item")
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return nil, requirementBadRequest("requirement_candidate_invalid", name+" contains an empty or duplicate item")
		}
		seen[value] = true
		out = append(out, value)
	}
	return out, nil
}

type submitCandidateHashBody struct {
	ExpectedRevision int64 `json:"expectedRevision"`
	Snapshot         any   `json:"snapshot"`
}

func (s *RequirementService) SubmitRequirementCandidate(ctx context.Context, actor RequirementActor, taskID, requirementID pgtype.UUID, input SubmitRequirementCandidateRequest, idempotencyKey, requestID string) (RequirementActionResult, error) {
	if input.ExpectedRevision <= 0 {
		return RequirementActionResult{}, requirementBadRequest("expected_revision_invalid", "expectedRevision must be positive")
	}
	if s.Tasks == nil {
		return RequirementActionResult{}, errors.New("requirement candidate: task service is required")
	}
	snapshotValue, err := decodeRequirementJSONValue(input.Snapshot)
	if err != nil {
		return RequirementActionResult{}, requirementBadRequest("requirement_candidate_invalid", "candidate snapshot must be a JSON object")
	}
	return s.ExecuteAction(ctx, RequirementActionInput{
		WorkspaceID: actor.WorkspaceID, RequirementID: requirementID, ActorType: "agent", ActorID: actor.UserID,
		Action: "candidate", IdempotencyKey: idempotencyKey,
		Method: http.MethodPost, Route: requirementCandidatesRoute,
		ExpectedRevision: &input.ExpectedRevision,
		Body:             submitCandidateHashBody{ExpectedRevision: input.ExpectedRevision, Snapshot: snapshotValue},
		AuthorizeReplay: func(ctx context.Context, q *db.Queries, requirementID pgtype.UUID) error {
			return authorizeCandidateReplay(ctx, q, actor, taskID, requirementID)
		},
	}, func(ctx context.Context, tx pgx.Tx, q *db.Queries) (RequirementActionResponse, error) {
		if err := s.requireFlow(ctx); err != nil {
			return RequirementActionResponse{}, err
		}
		row, err := q.LockRequirement(ctx, db.LockRequirementParams{ID: requirementID, WorkspaceID: actor.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return RequirementActionResponse{}, requirementNotFound()
		}
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("lock requirement candidate: %w", err)
		}
		if row.Revision != input.ExpectedRevision {
			return RequirementActionResponse{}, requirementRevisionConflict(row.ID, input.ExpectedRevision, row.Revision)
		}
		if row.CancelledAt.Valid {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, RequirementCancelledCode, "requirement is cancelled")
		}
		if row.IssueID.Valid {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, RequirementChatIssueExistsCode, "candidates are closed after issue creation")
		}
		task, err := s.Tasks.ValidateTaskForRequirementCandidateInTx(ctx, tx, taskID, requirementID, actor.WorkspaceID)
		if err != nil {
			return RequirementActionResponse{}, requirementCandidateTaskError(err)
		}
		if task.AgentID != actor.UserID {
			return RequirementActionResponse{}, newRequirementError(http.StatusForbidden, "requirement_candidate_forbidden", "task actor does not match the candidate task")
		}
		candidateSnapshot, canonical, digest, err := CanonicalRequirementCandidateSnapshot(input.Snapshot, row)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if len(candidateSnapshot.AttachmentIDs) > 0 {
			sourcePreview, _, err := loadVerifiableRequirementSourcePreview(ctx, q, row)
			if err != nil {
				return RequirementActionResponse{}, requirementAttachmentUnavailable("")
			}
			if _, err := resolveRequirementCandidateAttachments(ctx, q, row, candidateSnapshot.AttachmentIDs, sourcePreview.Attachments); err != nil {
				return RequirementActionResponse{}, err
			}
		}
		candidate, err := q.CreateRequirementCandidate(ctx, db.CreateRequirementCandidateParams{
			WorkspaceID: actor.WorkspaceID, RequirementID: row.ID, Revision: row.Revision + 1,
			Snapshot: canonical, Digest: digest, CreatedByTaskID: task.ID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return RequirementActionResponse{}, newRequirementError(http.StatusConflict, "requirement_candidate_exists", "a candidate already exists at this revision")
		}
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("create requirement candidate: %w", err)
		}
		updated, err := q.UpdateRequirementIntakeState(ctx, db.UpdateRequirementIntakeStateParams{
			ID: row.ID, WorkspaceID: actor.WorkspaceID, ExpectedRevision: input.ExpectedRevision, IntakeState: "awaiting_approval",
		})
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("advance requirement candidate state: %w", err)
		}
		event, err := appendRequirementEvent(ctx, q, actor, "agent", updated, "candidate_submitted", row.IntakeState, updated.IntakeState, map[string]any{
			"requirement_id": util.UUIDToString(row.ID), "candidate_id": util.UUIDToString(candidate.ID), "task_id": util.UUIDToString(task.ID),
		}, requestID)
		if err != nil {
			return RequirementActionResponse{}, err
		}
		if err := s.appendRequirementOutbox(ctx, q, updated, event, "requirement_status"); err != nil {
			return RequirementActionResponse{}, err
		}
		detail, err := s.BuildDetail(ctx, q, actor, updated, RequirementDetailOptions{ReadAuthorized: true, CandidateAuthorized: true})
		if err != nil {
			return RequirementActionResponse{}, err
		}
		body, err := json.Marshal(detail)
		if err != nil {
			return RequirementActionResponse{}, fmt.Errorf("marshal requirement candidate response: %w", err)
		}
		return RequirementActionResponse{
			RequirementID: row.ID, Status: http.StatusOK, Body: body,
			EventType: protocol.EventRequirementUpdated, Realtime: detail.RealtimePointer(),
		}, nil
	})
}

func requirementCandidateTaskError(err error) error {
	var gateErr *RequirementChatExecutionError
	if errors.Is(err, ErrTaskRequirementBinding) || errors.As(err, &gateErr) {
		return newRequirementError(http.StatusForbidden, "requirement_candidate_forbidden", err.Error())
	}
	return fmt.Errorf("validate candidate task: %w", err)
}

func authorizeCandidateReplay(ctx context.Context, q *db.Queries, actor RequirementActor, taskID, requirementID pgtype.UUID) error {
	task, err := q.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: taskID, WorkspaceID: actor.WorkspaceID})
	if err != nil || task.AgentID != actor.UserID || task.RequirementID != requirementID || task.Mode != "plan" {
		return requirementForbidden()
	}
	requirement, err := q.GetRequirement(ctx, db.GetRequirementParams{ID: requirementID, WorkspaceID: actor.WorkspaceID})
	if err != nil || !task.ChatSessionID.Valid || requirement.ChatSessionID != task.ChatSessionID {
		return requirementForbidden()
	}
	return nil
}
