package popo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (o *Outbound) sourceQueued(ctx context.Context, installationID pgtype.UUID, key string) bool {
	if o.durable == nil {
		return false
	}
	_, err := o.durable.GetPopoBridgeCommandByDeliveryID(ctx, sourceDeliveryID(installationID, key))
	return err == nil
}

// Run recovers notifications after commit-before-publish crashes. It never
// executes an agent or retries a delivery with an unknown remote outcome.
func (o *Outbound) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if _, err := o.Reconcile(ctx); err != nil && ctx.Err() == nil {
			o.logger.WarnContext(ctx, "popo outbound recovery failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (o *Outbound) Reconcile(ctx context.Context) (int, error) {
	o.recoveryMu.Lock()
	defer o.recoveryMu.Unlock()
	if o.durable == nil {
		return 0, nil
	}
	rows, err := o.durable.ListPopoRecoveryCandidates(ctx, 100)
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return queued, err
		}
		if err := o.recoverSource(ctx, row.Kind, row.EntityID); err != nil {
			return queued, err
		}
		if o.sourceQueued(ctx, row.InstallationID, row.SourceKey) {
			queued++
		}
	}
	return queued, nil
}

func (o *Outbound) recoverSource(ctx context.Context, kind string, id pgtype.UUID) error {
	switch kind {
	case "chat_done":
		message, err := o.durable.GetChatMessageByTaskAssistant(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		o.enqueueChatDone(ctx, events.Event{Payload: protocol.ChatDonePayload{
			TaskID: uuidString(id), MessageID: uuidString(message.ID), Content: message.Content,
		}})
	case "issue_comment":
		comment, err := o.durable.GetComment(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if comment.DeletedAt.Valid {
			return nil
		}
		o.enqueueComment(ctx, events.Event{Payload: map[string]any{"comment": map[string]any{
			"id": uuidString(comment.ID), "issue_id": uuidString(comment.IssueID),
			"content": comment.Content, "author_type": comment.AuthorType,
		}}})
	case "task_failed", "task_cancelled", "task_progress", "task_completed":
		eventType := protocol.EventTaskFailed
		if kind == "task_cancelled" {
			eventType = protocol.EventTaskCancelled
		} else if kind == "task_progress" {
			eventType = protocol.EventTaskRunning
		} else if kind == "task_completed" {
			eventType = protocol.EventTaskCompleted
		}
		o.enqueueTaskTerminal(ctx, events.Event{Type: eventType, Payload: map[string]any{"task_id": uuidString(id)}})
	case "issue_created", "issue_status":
		issue, err := o.durable.GetIssue(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		source, err := o.durable.GetChannelIssueSourceByIssue(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		text := ""
		if kind == "issue_created" {
			text = issueCreatedText(engine.Result{IssueIdentifier: o.issueIdentifier(ctx, id), IssueTitle: issue.Title}, "")
		}
		if kind == "issue_status" {
			text = fmt.Sprintf("Issue status: %s", issue.Status)
		}
		o.enqueueSource(ctx, source, issue.ID, pgtype.UUID{}, pgtype.UUID{}, text, kind)
	}
	return nil
}
