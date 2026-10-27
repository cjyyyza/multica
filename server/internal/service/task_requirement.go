package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

const (
	RequirementChatIssueExistsCode = "requirement_issue_already_exists"
	RequirementCancelledCode       = "requirement_cancelled"
	RequirementDoneCode            = "requirement_done"
)

type RequirementChatExecutionError struct {
	Code    string
	IssueID string
}

func (e *RequirementChatExecutionError) Error() string {
	switch e.Code {
	case RequirementChatIssueExistsCode:
		return "requirement chat is read-only after its Issue was created"
	case RequirementApprovalRequiredCode:
		return "requirement approval is required"
	case RequirementCancelledCode:
		return "requirement is cancelled"
	case RequirementDoneCode:
		return "requirement is done"
	case RequirementStatusTransitionForbiddenCode:
		return "requirement issue status can only change through its requirement workflow"
	case RequirementIssueNotCancelledCode:
		return "requirement issue must be cancelled before deletion"
	default:
		return "requirement chat execution is not allowed"
	}
}

func RequirementChatErrorIssueID(err error) string {
	var gateErr *RequirementChatExecutionError
	if errors.As(err, &gateErr) {
		return gateErr.IssueID
	}
	return ""
}

func requirementChatGateError(requirement db.Requirement) error {
	if requirement.CancelledAt.Valid {
		return &RequirementChatExecutionError{Code: RequirementCancelledCode, IssueID: util.UUIDToString(requirement.IssueID)}
	}
	if requirement.IssueID.Valid {
		return &RequirementChatExecutionError{Code: RequirementChatIssueExistsCode, IssueID: util.UUIDToString(requirement.IssueID)}
	}
	return nil
}

func (s *TaskService) gateRequirementChatTask(ctx context.Context, qtx *db.Queries, chatSession db.ChatSession, lock bool) error {
	var requirement db.Requirement
	var err error
	params := db.LockRequirementByChatSessionParams{WorkspaceID: chatSession.WorkspaceID, ChatSessionID: chatSession.ID}
	if lock {
		requirement, err = qtx.LockRequirementByChatSession(ctx, params)
	} else {
		requirement, err = qtx.GetRequirementByChatSession(ctx, db.GetRequirementByChatSessionParams(params))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load requirement chat binding: %w", err)
	}
	if requirement.AgentID != chatSession.AgentID {
		return taskRequirementBindingErrorf("requirement and chat agents do not match")
	}
	return requirementChatGateError(requirement)
}

func (s *TaskService) CheckRequirementChatExecution(ctx context.Context, chatSession db.ChatSession) error {
	return s.gateRequirementChatTask(ctx, s.Queries, chatSession, false)
}

func (s *TaskService) CheckRequirementIssueExecution(ctx context.Context, workspaceID, issueID pgtype.UUID) error {
	requirement, err := s.Queries.GetRequirementByIssue(ctx, db.GetRequirementByIssueParams{WorkspaceID: workspaceID, IssueID: issueID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load requirement issue binding: %w", err)
	}
	projection, err := requirementProjection(ctx, s.Queries, requirement, false)
	if err != nil {
		return fmt.Errorf("load requirement issue execution state: %w", err)
	}
	switch projection.State {
	case "done":
		return &RequirementChatExecutionError{Code: RequirementDoneCode, IssueID: util.UUIDToString(issueID)}
	case "cancelled":
		return &RequirementChatExecutionError{Code: RequirementCancelledCode, IssueID: util.UUIDToString(issueID)}
	default:
		return nil
	}
}

func (s *TaskService) EnqueuePreparedRequirementChatTaskInTx(
	ctx context.Context,
	tx pgx.Tx,
	chatSession db.ChatSession,
	initiatorUserID pgtype.UUID,
	prepared PreparedChatTaskEnqueue,
) (db.AgentTaskQueue, error) {
	if tx == nil {
		return db.AgentTaskQueue{}, errors.New("requirement chat task: transaction is required")
	}
	requirement, err := s.Queries.WithTx(tx).GetRequirementByChatSession(ctx, db.GetRequirementByChatSessionParams{
		WorkspaceID: chatSession.WorkspaceID, ChatSessionID: chatSession.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.AgentTaskQueue{}, errors.New("requirement chat task: chat is not bound to a requirement")
	}
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("requirement chat task: load binding: %w", err)
	}
	if err := requirementChatGateError(requirement); err != nil {
		return db.AgentTaskQueue{}, err
	}
	return s.enqueueChatTaskTx(ctx, s.Queries.WithTx(tx), chatSession, initiatorUserID, false, 0, false, pgtype.UUID{}, 0, prepared)
}

var ErrTaskRequirementBinding = errors.New("task requirement binding is invalid")

func taskRequirementBindingErrorf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrTaskRequirementBinding, fmt.Sprintf(format, args...))
}

func (s *TaskService) ValidateTaskRequirement(ctx context.Context, task db.AgentTaskQueue, workspaceID pgtype.UUID) error {
	return s.validateTaskRequirement(ctx, s.Queries, task, workspaceID)
}

func (s *TaskService) validateTaskRequirement(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue, workspaceID pgtype.UUID) error {
	mode, err := taskpolicy.NormalizeMode(task.Mode)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrTaskRequirementBinding, err)
	}
	if !task.RequirementID.Valid {
		if mode == taskpolicy.ModePlan {
			return taskRequirementBindingErrorf("plan task is missing requirement_id")
		}
		return nil
	}
	requirement, err := queries.GetRequirement(ctx, db.GetRequirementParams{ID: task.RequirementID, WorkspaceID: workspaceID})
	if err != nil {
		return fmt.Errorf("load task requirement: %w", err)
	}
	if requirement.AgentID != task.AgentID {
		return taskRequirementBindingErrorf("task agent does not match its requirement")
	}
	if requirement.CancelledAt.Valid {
		return &RequirementChatExecutionError{Code: RequirementCancelledCode, IssueID: util.UUIDToString(requirement.IssueID)}
	}
	if mode == taskpolicy.ModePlan {
		if !task.ChatSessionID.Valid || task.IssueID.Valid || requirement.ChatSessionID != task.ChatSessionID {
			return taskRequirementBindingErrorf("plan task does not belong to its requirement chat")
		}
		if requirement.IssueID.Valid {
			return &RequirementChatExecutionError{Code: RequirementChatIssueExistsCode, IssueID: util.UUIDToString(requirement.IssueID)}
		}
		return nil
	}
	if !requirement.IssueID.Valid || !task.IssueID.Valid || requirement.IssueID != task.IssueID {
		return taskRequirementBindingErrorf("execute task would bypass its requirement approval or binding")
	}
	return nil
}

func (s *TaskService) ValidateTaskForRequirementCandidate(ctx context.Context, taskID, requirementID, workspaceID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.validateTaskForRequirementCandidate(ctx, s.Queries, taskID, requirementID, workspaceID)
}

func (s *TaskService) ValidateTaskForRequirementCandidateInTx(ctx context.Context, tx pgx.Tx, taskID, requirementID, workspaceID pgtype.UUID) (db.AgentTaskQueue, error) {
	if tx == nil {
		return db.AgentTaskQueue{}, errors.New("candidate task validation: transaction is required")
	}
	return s.validateTaskForRequirementCandidate(ctx, s.Queries.WithTx(tx), taskID, requirementID, workspaceID)
}

func (s *TaskService) validateTaskForRequirementCandidate(ctx context.Context, queries *db.Queries, taskID, requirementID, workspaceID pgtype.UUID) (db.AgentTaskQueue, error) {
	task, err := queries.GetAgentTask(ctx, taskID)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("load candidate task: %w", err)
	}
	if task.RequirementID != requirementID || task.Mode != taskpolicy.ModePlan || task.Status != "running" {
		return db.AgentTaskQueue{}, taskRequirementBindingErrorf("candidate task is not a running plan task for this requirement")
	}
	if err := s.validateTaskRequirement(ctx, queries, task, workspaceID); err != nil {
		return db.AgentTaskQueue{}, err
	}
	return task, nil
}
