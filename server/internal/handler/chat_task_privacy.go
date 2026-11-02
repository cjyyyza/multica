package handler

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A chat session's content follows the same read check as its transcript:
// ordinary chats are creator-only, requirement chats are visible to current
// participants. Task lists stay workspace-wide because presence and workload
// views count every run, so they keep a foreign chat task's row and drop only
// its content.

// redactForeignChatTaskContent clears the content of every chat task in resp
// that viewerID cannot read. tasks[i] must be the row behind resp[i]. A chat
// task whose session can no longer be resolved is treated as foreign.
func (h *Handler) redactForeignChatTaskContent(ctx context.Context, viewerID string, tasks []db.AgentTaskQueue, resp []AgentTaskResponse) error {
	seen := map[pgtype.UUID]bool{}
	var sessionIDs []pgtype.UUID
	for _, t := range tasks {
		if t.ChatSessionID.Valid && !seen[t.ChatSessionID] {
			seen[t.ChatSessionID] = true
			sessionIDs = append(sessionIDs, t.ChatSessionID)
		}
	}
	visible, err := service.ResolveChatContentVisibility(ctx, h.Queries, sessionIDs, viewerID)
	if err != nil {
		return err
	}
	for i, t := range tasks {
		if !t.ChatSessionID.Valid || visible[t.ChatSessionID] {
			continue
		}
		resp[i].Result = nil
		resp[i].Error = nil
	}
	return nil
}
