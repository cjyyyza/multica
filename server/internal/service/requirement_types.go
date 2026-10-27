package service

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

type RequirementRunProjector func(workspaceID string, task db.AgentTaskQueue) (json.RawMessage, error)

type RequirementDeliveryReconciler interface {
	ReconcileRequirementDelivery(ctx context.Context, q *db.Queries, workspaceID, deliveryID pgtype.UUID) (db.PopoBridgeCommand, error)
	RecoverConfirmedFailedRequirementDelivery(ctx context.Context, q *db.Queries, workspaceID, deliveryID pgtype.UUID) (db.PopoBridgeCommand, error)
}

type RequirementService struct {
	Queries            *db.Queries
	TxStarter          TxStarter
	Tasks              *TaskService
	Issues             *IssueService
	FeatureFlags       *featureflag.Service
	AppURL             string
	RunProjector       RequirementRunProjector
	DeliveryReconciler RequirementDeliveryReconciler
}

func NewRequirementService(queries *db.Queries, txStarter TxStarter) *RequirementService {
	return &RequirementService{Queries: queries, TxStarter: txStarter}
}

type RequirementError struct {
	Status                int
	Code                  string
	Message               string
	ExistingRequirementID string
	ResourceType          string
	ResourceID            string
	ExpectedRevision      *int64
	ActualRevision        *int64
	Cause                 error
}

func (e *RequirementError) Error() string { return e.Message }
func (e *RequirementError) Unwrap() error { return e.Cause }

func newRequirementError(status int, code, message string) *RequirementError {
	return &RequirementError{Status: status, Code: code, Message: message}
}

func requirementNotFound() *RequirementError {
	return newRequirementError(http.StatusNotFound, "requirement_not_found", "requirement not found")
}

func requirementForbidden() *RequirementError {
	return newRequirementError(http.StatusForbidden, "requirement_forbidden", "requirement access is not allowed")
}

func requirementBadRequest(code, message string) *RequirementError {
	return newRequirementError(http.StatusBadRequest, code, message)
}

func requirementRevisionConflict(id pgtype.UUID, expected, actual int64) *RequirementError {
	expectedCopy, actualCopy := expected, actual
	return &RequirementError{
		Status: http.StatusConflict, Code: "revision_conflict", Message: "resource changed since it was loaded",
		ResourceType: "requirement", ResourceID: util.UUIDToString(id),
		ExpectedRevision: &expectedCopy, ActualRevision: &actualCopy,
	}
}

type RequirementActor struct {
	WorkspaceID pgtype.UUID
	UserID      pgtype.UUID
}

type RequirementParticipantInput struct {
	UserID string   `json:"userId"`
	Roles  []string `json:"roles"`
}

type CreateRequirementRequest struct {
	SourceHost        string                        `json:"sourceHost"`
	ExternalID        string                        `json:"externalId"`
	SourceUrl         string                        `json:"sourceUrl"`
	SourceSnapshot    json.RawMessage               `json:"sourceSnapshot"`
	SourceDigest      string                        `json:"sourceDigest"`
	SourceLockVersion *int64                        `json:"sourceLockVersion"`
	AgentID           string                        `json:"agentId"`
	ProjectID         *string                       `json:"projectId"`
	Participants      []RequirementParticipantInput `json:"participants"`
	IntakeOperationID *string                       `json:"intakeOperationId"`
}

type UpdateRequirementParticipantsRequest struct {
	ExpectedRevision int64                         `json:"expectedRevision"`
	Participants     []RequirementParticipantInput `json:"participants"`
}

type RequirementParticipant struct {
	ID     string   `json:"id"`
	UserID string   `json:"userId"`
	Roles  []string `json:"roles"`
}

type RequirementCandidate struct {
	ID              string          `json:"id"`
	RequirementID   string          `json:"requirementId"`
	Revision        int64           `json:"revision"`
	Snapshot        json.RawMessage `json:"snapshot"`
	Digest          string          `json:"digest"`
	CreatedByTaskID string          `json:"createdByTaskId"`
	CreatedAt       string          `json:"createdAt"`
}

type RequirementPermissions struct {
	CanRead               bool `json:"canRead"`
	CanOpen               bool `json:"canOpen"`
	CanManageParticipants bool `json:"canManageParticipants"`
	CanSubmitCandidate    bool `json:"canSubmitCandidate"`
	CanApprove            bool `json:"canApprove"`
	CanAccept             bool `json:"canAccept"`
	CanReject             bool `json:"canReject"`
	CanCancel             bool `json:"canCancel"`
	CanReconcile          bool `json:"canReconcile"`
}

type RequirementOperation struct {
	ID                string  `json:"id"`
	RequirementID     *string `json:"requirementId"`
	Kind              string  `json:"kind"`
	State             string  `json:"state"`
	RequestHash       *string `json:"requestHash"`
	TargetOperationID *string `json:"targetOperationId,omitempty"`
	Error             *string `json:"error"`
	CreatedAt         string  `json:"createdAt"`
	UpdatedAt         string  `json:"updatedAt"`
}

type RequirementDelivery struct {
	ID                 string  `json:"id"`
	RequirementID      *string `json:"requirementId"`
	Kind               string  `json:"kind"`
	State              string  `json:"state"`
	SourceKey          *string `json:"sourceKey"`
	OccurrenceID       *string `json:"occurrenceId"`
	RecipientUserID    *string `json:"recipientUserId,omitempty"`
	RecipientBindingID *string `json:"recipientBindingId"`
	TransportCommandID *string `json:"transportCommandId,omitempty"`
	TransportAttempt   int32   `json:"transportAttempt,omitempty"`
	AcceptedAt         *string `json:"acceptedAt,omitempty"`
	RequestHash        *string `json:"requestHash,omitempty"`
	AttemptCount       int32   `json:"attemptCount"`
	LastError          *string `json:"lastError"`
	DeliveredAt        *string `json:"deliveredAt"`
	CreatedAt          string  `json:"createdAt"`
	UpdatedAt          string  `json:"updatedAt"`
}

type RequirementDetail struct {
	ID                string                   `json:"id"`
	WorkspaceID       string                   `json:"workspaceId"`
	Revision          int64                    `json:"revision"`
	State             string                   `json:"state"`
	IntakeState       string                   `json:"intakeState"`
	SourceHost        string                   `json:"sourceHost"`
	ExternalID        string                   `json:"externalId"`
	SourceUrl         *string                  `json:"sourceUrl"`
	SourceSnapshot    json.RawMessage          `json:"sourceSnapshot"`
	SourceDigest      *string                  `json:"sourceDigest"`
	SourceLockVersion *int64                   `json:"sourceLockVersion"`
	RequesterID       string                   `json:"requesterId"`
	AgentID           *string                  `json:"agentId"`
	ProjectID         *string                  `json:"projectId"`
	ChatSessionID     *string                  `json:"chatSessionId"`
	IssueID           *string                  `json:"issueId"`
	IntakeOperationID *string                  `json:"intakeOperationId"`
	CancelledAt       *string                  `json:"cancelledAt"`
	CancelReason      *string                  `json:"cancelReason"`
	IssueDeleted      bool                     `json:"issueDeleted"`
	LastError         *string                  `json:"lastError"`
	CreatedAt         string                   `json:"createdAt"`
	UpdatedAt         string                   `json:"updatedAt"`
	Participants      []RequirementParticipant `json:"participants"`
	LatestCandidate   *RequirementCandidate    `json:"latestCandidate"`
	Permissions       RequirementPermissions   `json:"permissions"`
	Issue             json.RawMessage          `json:"issue"`
	Run               json.RawMessage          `json:"run"`
	Operations        []RequirementOperation   `json:"operations"`
	Deliveries        []RequirementDelivery    `json:"deliveries"`
}

type RequirementDetailOptions struct {
	ReadAuthorized      bool
	CandidateAuthorized bool
}

// RequirementRealtimePointer is the readback identity a realtime
// requirement:updated event carries; clients re-read the requirement at this
// revision instead of consuming an inline detail snapshot.
type RequirementRealtimePointer struct {
	ID            string
	WorkspaceID   string
	Revision      int64
	ChatSessionID *string
}

func (d RequirementDetail) RealtimePointer() *RequirementRealtimePointer {
	return &RequirementRealtimePointer{
		ID:            d.ID,
		WorkspaceID:   d.WorkspaceID,
		Revision:      d.Revision,
		ChatSessionID: d.ChatSessionID,
	}
}

type RequirementActionResult struct {
	Status         int
	Body           json.RawMessage
	Replay         bool
	EventType      string
	Realtime       *RequirementRealtimePointer
	FinalizeTasks  []db.AgentTaskQueue
	PromoteTasks   []db.AgentTaskQueue
	IssueUpdates   []RequirementIssueUpdate
	Comments       []RequirementCommentEvent
	CancelledTasks []db.AgentTaskQueue
	PostCommit     func()
	WorkspaceID    string
	ActorType      string
	ActorID        string
}

func requirementTime(value pgtype.Timestamptz) string {
	if !value.Valid {
		return ""
	}
	return value.Time.UTC().Format(time.RFC3339Nano)
}

func requirementTimePtr(value pgtype.Timestamptz) *string {
	if !value.Valid {
		return nil
	}
	formatted := requirementTime(value)
	return &formatted
}

func requirementUUIDPtr(value pgtype.UUID) *string {
	if !value.Valid {
		return nil
	}
	formatted := util.UUIDToString(value)
	return &formatted
}

func requirementTextPtr(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	text := value.String
	return &text
}

func requirementStringPtr(value string) *string {
	return &value
}

func requirementNonEmptyTextPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
