package popo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// OutboundItem is one pending robot reply for the Windows CLI.
type OutboundItem struct {
	WorkspaceID    pgtype.UUID
	InstallationID pgtype.UUID
	ChatID         string
	RobotID        string
	Content        string
}

// Enqueuer stores a reply. The API never delivers it to POPO.
type Enqueuer interface {
	Enqueue(ctx context.Context, item OutboundItem) error
}

// Queue is the sqlc-backed Enqueuer plus the CLI poll/ack surface.
type Queue struct {
	q *db.Queries
}

func NewQueue(q *db.Queries) *Queue {
	return &Queue{q: q}
}

func (q *Queue) Enqueue(ctx context.Context, item OutboundItem) error {
	if q == nil || q.q == nil {
		return errors.New("popo: outbound queue not configured")
	}
	content := strings.TrimSpace(item.Content)
	chatID := strings.TrimSpace(item.ChatID)
	if content == "" || chatID == "" {
		return errors.New("popo: outbound chat_id and content are required")
	}
	_, err := q.q.EnqueuePopoOutbound(ctx, db.EnqueuePopoOutboundParams{
		WorkspaceID:    item.WorkspaceID,
		InstallationID: item.InstallationID,
		ChatID:         chatID,
		RobotID:        strings.TrimSpace(item.RobotID),
		Content:        content,
	})
	if err != nil {
		return fmt.Errorf("enqueue popo outbound: %w", err)
	}
	return nil
}

func (q *Queue) ListPending(ctx context.Context, workspaceID pgtype.UUID, limit int32) ([]db.PopoOutboundQueue, error) {
	if limit <= 0 {
		limit = 50
	}
	return q.q.ListPendingPopoOutbound(ctx, db.ListPendingPopoOutboundParams{
		WorkspaceID: workspaceID,
		Limit:       limit,
	})
}

func (q *Queue) Ack(ctx context.Context, id, workspaceID pgtype.UUID) error {
	n, err := q.q.AckPopoOutbound(ctx, db.AckPopoOutboundParams{ID: id, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("popo outbound item not found")
	}
	return nil
}

func (q *Queue) Fail(ctx context.Context, id, workspaceID pgtype.UUID, lastError string) error {
	_, err := q.q.FailPopoOutbound(ctx, db.FailPopoOutboundParams{
		ID:          id,
		WorkspaceID: workspaceID,
		LastError:   pgtype.Text{String: lastError, Valid: lastError != ""},
	})
	return err
}
