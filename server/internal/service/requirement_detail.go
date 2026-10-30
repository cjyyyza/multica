package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type requirementView struct {
	State        string
	IssueDeleted bool
	Issue        *db.Issue
}

func (s *RequirementService) BuildDetail(ctx context.Context, q *db.Queries, actor RequirementActor, row db.Requirement, options RequirementDetailOptions) (RequirementDetail, error) {
	participantRows, err := q.ListRequirementParticipants(ctx, db.ListRequirementParticipantsParams{
		WorkspaceID: actor.WorkspaceID, RequirementID: row.ID,
	})
	if err != nil {
		return RequirementDetail{}, fmt.Errorf("list requirement participants: %w", err)
	}
	participants := aggregateRequirementParticipants(row.ID, participantRows)
	projection, err := requirementProjection(ctx, q, row, false)
	if err != nil {
		return RequirementDetail{}, err
	}
	latest, err := s.latestRequirementCandidate(ctx, q, actor.WorkspaceID, row.ID)
	if err != nil {
		return RequirementDetail{}, err
	}
	operations, err := s.requirementOperations(ctx, q, row)
	if err != nil {
		return RequirementDetail{}, err
	}
	deliveries, err := s.requirementDeliveries(ctx, q, row)
	if err != nil {
		return RequirementDetail{}, err
	}
	permissions, err := s.requirementPermissions(ctx, q, actor, row, projection.State, latest != nil, options, participantRows)
	if err != nil {
		return RequirementDetail{}, err
	}
	var issueBody json.RawMessage
	if projection.Issue != nil {
		workspace, err := q.GetWorkspace(ctx, row.WorkspaceID)
		if err != nil {
			return RequirementDetail{}, fmt.Errorf("load requirement issue workspace: %w", err)
		}
		issueBody, err = json.Marshal(IssueToMapResolved(ctx, q, *projection.Issue, workspace.IssuePrefix))
		if err != nil {
			return RequirementDetail{}, fmt.Errorf("marshal requirement issue: %w", err)
		}
	}
	var runBody json.RawMessage
	if row.IssueID.Valid || row.ChatSessionID.Valid {
		runBody, err = s.requirementRunProjection(ctx, q, row)
		if err != nil {
			return RequirementDetail{}, err
		}
	}
	var squadID *string
	blockedQuestion, err := s.requirementBlockedQuestion(ctx, q, actor, row, projection)
	if err != nil {
		return RequirementDetail{}, err
	}
	delegation, native, err := requirementDelegation(row)
	if err != nil {
		return RequirementDetail{}, err
	}
	if native && delegation.SquadID != "" {
		squadID = requirementStringPtr(delegation.SquadID)
	}
	return RequirementDetail{
		ID: util.UUIDToString(row.ID), WorkspaceID: util.UUIDToString(row.WorkspaceID), Revision: row.Revision,
		State: projection.State, IntakeState: row.IntakeState,
		SourceHost: row.SourceHost, ExternalID: row.ExternalID,
		SourceUrl: requirementStringPtr(row.SourceUrl), SourceSnapshot: append(json.RawMessage(nil), row.SourceSnapshot...),
		SourceDigest: requirementStringPtr(row.SourceDigest), SourceLockVersion: requirementInt8Ptr(row.SourceLockVersion),
		RequesterID: util.UUIDToString(row.RequesterID), AgentID: requirementUUIDPtr(row.AgentID), SquadID: squadID, ProjectID: requirementUUIDPtr(row.ProjectID),
		ChatSessionID: requirementUUIDPtr(row.ChatSessionID), IssueID: requirementUUIDPtr(row.IssueID), IntakeOperationID: requirementUUIDPtr(row.IntakeOperationID),
		CancelledAt: requirementTimePtr(row.CancelledAt), CancelReason: requirementTextPtr(row.CancelReason),
		IssueDeleted: projection.IssueDeleted, LastError: requirementTextPtr(row.LastError),
		CreatedAt: requirementTime(row.CreatedAt), UpdatedAt: requirementTime(row.UpdatedAt),
		Participants: participants, LatestCandidate: latest, Permissions: permissions,
		BlockedQuestion: blockedQuestion,
		Issue:           issueBody, Run: runBody, Operations: operations, Deliveries: deliveries,
	}, nil
}

func requirementInt8Ptr(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	out := value.Int64
	return &out
}

func aggregateRequirementParticipants(requirementID pgtype.UUID, rows []db.RequirementParticipant) []RequirementParticipant {
	byUser := make(map[string][]string)
	for _, row := range rows {
		userID := util.UUIDToString(row.UserID)
		byUser[userID] = append(byUser[userID], row.Role)
	}
	users := make([]string, 0, len(byUser))
	for userID := range byUser {
		users = append(users, userID)
	}
	sort.Strings(users)
	out := make([]RequirementParticipant, 0, len(users))
	for _, userID := range users {
		roles := byUser[userID]
		sort.Slice(roles, func(i, j int) bool {
			return requirementRoleRank(roles[i]) < requirementRoleRank(roles[j])
		})
		out = append(out, RequirementParticipant{
			ID: util.UUIDToString(requirementID) + ":" + userID, UserID: userID, Roles: roles,
		})
	}
	return out
}

func requirementRoleRank(role string) int {
	switch role {
	case "requester":
		return 0
	case "approver":
		return 1
	case "viewer":
		return 2
	default:
		return 3
	}
}

func (s *RequirementService) latestRequirementCandidate(ctx context.Context, q *db.Queries, workspaceID, requirementID pgtype.UUID) (*RequirementCandidate, error) {
	row, err := q.GetLatestRequirementCandidate(ctx, db.GetLatestRequirementCandidateParams{
		WorkspaceID: workspaceID, RequirementID: requirementID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load latest requirement candidate: %w", err)
	}
	return &RequirementCandidate{
		ID: util.UUIDToString(row.ID), RequirementID: util.UUIDToString(row.RequirementID), Revision: row.Revision,
		Snapshot: append(json.RawMessage(nil), row.Snapshot...), Digest: row.Digest,
		CreatedByTaskID: requirementUUIDPtr(row.CreatedByTaskID), CreatedAt: requirementTime(row.CreatedAt),
	}, nil
}

func (s *RequirementService) requirementProjection(ctx context.Context, q *db.Queries, row db.Requirement, lockIssue bool) (requirementView, error) {
	return requirementProjection(ctx, q, row, lockIssue)
}

// requirementProjection resolves the requirement state from its issue; it needs
// no service state, so callers outside the service methods can use it directly.
func requirementProjection(ctx context.Context, q *db.Queries, row db.Requirement, lockIssue bool) (requirementView, error) {
	projection := requirementView{State: row.IntakeState}
	if row.CancelledAt.Valid {
		projection.State = "cancelled"
	}
	if !row.IssueID.Valid {
		return projection, nil
	}
	var issue db.Issue
	var err error
	if lockIssue {
		issue, err = q.LockIssueForRequirement(ctx, db.LockIssueForRequirementParams{ID: row.IssueID, WorkspaceID: row.WorkspaceID})
	} else {
		issue, err = q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: row.IssueID, WorkspaceID: row.WorkspaceID})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		projection.State, projection.IssueDeleted = "cancelled", true
		return projection, nil
	}
	if err != nil {
		return requirementView{}, fmt.Errorf("load requirement issue: %w", err)
	}
	projection.Issue = &issue
	category := ""
	if !requirementBuiltinIssueStatus(issue.Status) {
		status, statusErr := q.GetIssueStatusEntryByKey(ctx, db.GetIssueStatusEntryByKeyParams{
			WorkspaceID: issue.WorkspaceID, Key: issue.Status,
		})
		if statusErr != nil {
			return requirementView{}, fmt.Errorf("load requirement issue status category: %w", statusErr)
		}
		category = status.Category
	}
	state, ok := RequirementStateForIssue(issue.Status, category)
	if !ok {
		return requirementView{}, fmt.Errorf("unknown requirement issue status %q", issue.Status)
	}
	projection.State = state
	if row.CancelledAt.Valid {
		projection.State = "cancelled"
	}
	return projection, nil
}

func requirementBuiltinIssueStatus(status string) bool {
	switch status {
	case "backlog", "todo", "in_progress", "blocked", "in_review", "done", "cancelled":
		return true
	default:
		return false
	}
}

// RequirementStateForIssue maps an issue status and its custom status category
// to the requirement state. It is exported so callers that only hold a task or
// requirement row can project state without constructing a service.
func RequirementStateForIssue(status, category string) (string, bool) {
	switch status {
	case "backlog", "todo", "in_progress":
		return "executing", true
	case "blocked":
		return "blocked", true
	case "in_review":
		return "in_review", true
	case "done":
		return "done", true
	case "cancelled":
		return "cancelled", true
	}
	switch category {
	case "unstarted", "started":
		return "executing", true
	case "done":
		return "done", true
	case "closed":
		return "cancelled", true
	default:
		return "", false
	}
}

func requirementStateForIssue(status, category string) (string, bool) {
	return RequirementStateForIssue(status, category)
}

func requirementTerminalState(state string) bool {
	return state == "done" || state == "cancelled"
}

func requirementActorRoles(participantRows []db.RequirementParticipant, userID pgtype.UUID) map[string]bool {
	roles := make(map[string]bool)
	for _, participant := range participantRows {
		if participant.UserID == userID {
			roles[participant.Role] = true
		}
	}
	return roles
}

func requirementActorIsMember(ctx context.Context, q *db.Queries, actor RequirementActor) (bool, error) {
	_, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID: actor.UserID, WorkspaceID: actor.WorkspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load requirement actor member: %w", err)
	}
	return true, nil
}

func (s *RequirementService) requirementMemberAndRoles(ctx context.Context, q *db.Queries, actor RequirementActor, requirementID pgtype.UUID) (bool, map[string]bool, error) {
	member, err := requirementActorIsMember(ctx, q, actor)
	if err != nil || !member {
		return member, map[string]bool{}, err
	}
	rows, err := q.ListRequirementParticipantRoles(ctx, db.ListRequirementParticipantRolesParams{
		WorkspaceID: actor.WorkspaceID, RequirementID: requirementID, UserID: actor.UserID,
	})
	if err != nil {
		return false, nil, fmt.Errorf("list requirement actor roles: %w", err)
	}
	return true, requirementActorRoles(rows, actor.UserID), nil
}

func (s *RequirementService) requirementReadAuthorized(ctx context.Context, q *db.Queries, actor RequirementActor, row db.Requirement) (bool, error) {
	member, roles, err := s.requirementMemberAndRoles(ctx, q, actor, row.ID)
	return member && len(roles) > 0, err
}

func (s *RequirementService) requirementPermissions(ctx context.Context, q *db.Queries, actor RequirementActor, row db.Requirement, state string, hasCandidate bool, options RequirementDetailOptions, participantRows []db.RequirementParticipant) (RequirementPermissions, error) {
	permissions := RequirementPermissions{}
	// Callers that already resolved workspace membership for this actor mark the
	// read as authorized, so re-querying the member row only repeats a check
	// that already passed on the same transaction.
	member := true
	if !options.ReadAuthorized {
		var err error
		member, err = requirementActorIsMember(ctx, q, actor)
		if err != nil {
			return permissions, err
		}
	}
	roles := map[string]bool{}
	if member {
		roles = requirementActorRoles(participantRows, actor.UserID)
	}
	permissions.CanRead = options.ReadAuthorized || (member && len(roles) > 0)
	terminal := requirementTerminalState(state)
	requesterOrApprover := roles["requester"] || roles["approver"]
	permissions.CanManageParticipants = permissions.CanRead && roles["requester"] && !terminal
	permissions.CanSubmitCandidate = permissions.CanRead && options.CandidateAuthorized && !row.IssueID.Valid && !row.CancelledAt.Valid
	permissions.CanAccept = permissions.CanRead && requesterOrApprover && state == "in_review"
	permissions.CanReject = permissions.CanAccept
	permissions.CanCancel = permissions.CanRead && requesterOrApprover && !terminal
	permissions.CanReconcile = permissions.CanRead && roles["approver"]

	// Open and approve are impossible once the issue exists or the requirement
	// is cancelled, so skip their agent, project and source checks entirely.
	if permissions.CanRead && !row.IssueID.Valid && !row.CancelledAt.Valid {
		canInvoke, projectOK := false, true
		agent, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: row.AgentID, WorkspaceID: actor.WorkspaceID})
		if err == nil && !agent.ArchivedAt.Valid {
			canInvoke = CanMemberInvokeAgent(ctx, q, agent, actor.UserID, actor.WorkspaceID)
		}
		if row.ProjectID.Valid {
			_, projectErr := q.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: row.ProjectID, WorkspaceID: actor.WorkspaceID})
			projectOK = projectErr == nil
		}
		sourceOK := validateStoredRequirementSource(row)
		source, native, nativeErr := requirementDelegation(row)
		if nativeErr != nil {
			return permissions, nativeErr
		}
		permissions.CanOpen = (!native || source.hasPlan()) && canInvoke && projectOK && sourceOK && validateRequirementPlanRuntime(ctx, q, agent) == nil
		if native {
			permissions.CanReconcile = false
		}
		permissions.CanApprove = roles["approver"] && canInvoke && projectOK && sourceOK && hasCandidate &&
			row.IntakeState == "awaiting_approval"
	}
	return permissions, nil
}

func (s *RequirementService) requirementRunProjection(ctx context.Context, q *db.Queries, row db.Requirement) (json.RawMessage, error) {
	var task db.AgentTaskQueue
	var err error
	if row.IssueID.Valid {
		task, err = q.GetLatestRequirementTaskForIssue(ctx, db.GetLatestRequirementTaskForIssueParams{RequirementID: row.ID, IssueID: row.IssueID})
	} else {
		task, err = q.GetLatestRequirementPlanTask(ctx, db.GetLatestRequirementPlanTaskParams{RequirementID: row.ID, ChatSessionID: row.ChatSessionID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		approval, approvalErr := q.GetRequirementApproval(ctx, db.GetRequirementApprovalParams{WorkspaceID: row.WorkspaceID, RequirementID: row.ID})
		if errors.Is(approvalErr, pgx.ErrNoRows) {
			return nil, nil
		}
		if approvalErr != nil {
			return nil, fmt.Errorf("load requirement approval run: %w", approvalErr)
		}
		task, err = q.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: approval.InitialTaskID, WorkspaceID: row.WorkspaceID})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load requirement run: %w", err)
	}
	if s.RunProjector != nil {
		body, err := s.RunProjector(util.UUIDToString(row.WorkspaceID), task)
		if err != nil {
			return nil, fmt.Errorf("project requirement run: %w", err)
		}
		return body, nil
	}
	return json.Marshal(map[string]any{
		"id": util.UUIDToString(task.ID), "agent_id": util.UUIDToString(task.AgentID),
		"runtime_id": util.UUIDToString(task.RuntimeID), "issue_id": util.UUIDToString(task.IssueID),
		"workspace_id": util.UUIDToString(row.WorkspaceID), "mode": task.Mode,
		"requirement_id": util.UUIDToString(task.RequirementID), "status": task.Status, "priority": task.Priority,
		"dispatched_at": requirementTimePtr(task.DispatchedAt), "started_at": requirementTimePtr(task.StartedAt),
		"completed_at": requirementTimePtr(task.CompletedAt), "result": json.RawMessage(task.Result),
		"error": requirementTextPtr(task.Error), "failure_reason": requirementTextPtr(task.FailureReason),
		"created_at": requirementTime(task.CreatedAt), "chat_session_id": requirementUUIDPtr(task.ChatSessionID),
		"autopilot_run_id": requirementUUIDPtr(task.AutopilotRunID), "parent_task_id": requirementUUIDPtr(task.ParentTaskID),
		"trigger_comment_id": requirementUUIDPtr(task.TriggerCommentID),
	})
}

func (s *RequirementService) requirementOperations(ctx context.Context, q *db.Queries, row db.Requirement) ([]RequirementOperation, error) {
	operations := make([]RequirementOperation, 0)
	if row.IntakeOperationID.Valid {
		operation, err := q.GetYixiezuoOperation(ctx, db.GetYixiezuoOperationParams{
			WorkspaceID: row.WorkspaceID, RequestedBy: row.RequesterID, ID: row.IntakeOperationID,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("load requirement intake operation: %w", err)
		}
		if err == nil {
			operations = append(operations, requirementOperationFromRow(row, operation))
		}
	}
	if row.IssueID.Valid {
		rows, err := q.ListYixiezuoOperationsByIssue(ctx, db.ListYixiezuoOperationsByIssueParams{WorkspaceID: row.WorkspaceID, IssueID: row.IssueID})
		if err != nil {
			return nil, fmt.Errorf("list requirement issue operations: %w", err)
		}
		for _, operation := range rows {
			if row.IntakeOperationID.Valid && operation.ID == row.IntakeOperationID {
				continue
			}
			operations = append(operations, requirementOperationFromRow(row, operation))
		}
	}
	return operations, nil
}

func requirementOperationFromRow(row db.Requirement, operation db.YixiezuoOperation) RequirementOperation {
	updatedAt := operation.CreatedAt
	if operation.StartedAt.Valid {
		updatedAt = operation.StartedAt
	}
	if operation.CompletedAt.Valid {
		updatedAt = operation.CompletedAt
	}
	return RequirementOperation{
		ID: util.UUIDToString(operation.ID), RequirementID: requirementUUIDPtr(row.ID), Kind: operation.Kind, State: operation.State,
		RequestHash: requirementTextPtr(operation.RequestHash), TargetOperationID: requirementUUIDPtr(operation.TargetOperationID),
		Error:     requirementNonEmptyTextPtr(operation.Error),
		CreatedAt: requirementTime(operation.CreatedAt), UpdatedAt: requirementTime(updatedAt),
	}
}

func (s *RequirementService) requirementDeliveries(ctx context.Context, q *db.Queries, row db.Requirement) ([]RequirementDelivery, error) {
	const deliveryPageSize int32 = 200
	rows := make([]db.PopoBridgeCommand, 0, deliveryPageSize)
	for offset := int32(0); ; offset += deliveryPageSize {
		page, err := q.ListRequirementPopoCommands(ctx, db.ListRequirementPopoCommandsParams{
			WorkspaceID: row.WorkspaceID, RequirementID: row.ID, PageLimit: deliveryPageSize, PageOffset: offset,
		})
		if err != nil {
			return nil, fmt.Errorf("list requirement deliveries: %w", err)
		}
		rows = append(rows, page...)
		if int32(len(page)) < deliveryPageSize {
			break
		}
	}
	deliveries := make([]RequirementDelivery, 0, len(rows))
	for _, delivery := range rows {
		sourceKey := fmt.Sprintf("requirement:%s:%s:%s", util.UUIDToString(row.ID), delivery.RequirementKind.String, util.UUIDToString(delivery.OccurrenceID))
		var transportCommandID *string
		if delivery.BridgeID.Valid {
			transportCommandID = requirementUUIDPtr(delivery.ID)
		}
		deliveries = append(deliveries, RequirementDelivery{
			ID: util.UUIDToString(delivery.DeliveryID), RequirementID: requirementUUIDPtr(row.ID), Kind: delivery.RequirementKind.String,
			State: requirementDeliveryState(delivery), SourceKey: &sourceKey,
			OccurrenceID: requirementUUIDPtr(delivery.OccurrenceID), RecipientUserID: requirementUUIDPtr(delivery.RecipientUserID),
			RecipientBindingID: requirementUUIDPtr(delivery.RecipientBindingID), TransportCommandID: transportCommandID,
			TransportAttempt: delivery.Attempt, AcceptedAt: requirementTimePtr(delivery.AcceptedAt), RequestHash: requirementTextPtr(delivery.RequestHash),
			AttemptCount: delivery.Attempt, LastError: requirementTextPtr(delivery.LastError), DeliveredAt: requirementTimePtr(delivery.DeliveredAt),
			CreatedAt: requirementTime(delivery.CreatedAt), UpdatedAt: requirementTime(delivery.UpdatedAt),
		})
	}
	return deliveries, nil
}

func requirementDeliveryState(delivery db.PopoBridgeCommand) string {
	switch delivery.Status {
	case "conflict", "failed", "cancelled", "expired":
		return "failed"
	case "unknown":
		return "unknown"
	case "superseded":
		return "superseded"
	case "delivered":
		return "delivered"
	case "leased":
		return "sending"
	case "pending":
		if !delivery.BridgeID.Valid && delivery.LastError.Valid {
			return "backlog"
		}
		if delivery.AcceptedAt.Valid {
			return "accepted"
		}
		return "pending"
	default:
		return "unknown"
	}
}
