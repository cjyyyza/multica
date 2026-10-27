package service

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type requirementQuickActionsStub struct{}

func (requirementQuickActionsStub) Enabled() bool { return true }
func (requirementQuickActionsStub) GenerateJSON(context.Context, string, string, string, float64, int64) (string, error) {
	return "", nil
}

func TestChatQuickActionsEligibleClosesAfterRequirementIssueDoneOrCancel(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	workspaceID, userID, agentID, issueID := seedAttributionFixture(t, pool)
	var runtimeID string
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatal(err)
	}
	var chatID string
	if err := pool.QueryRow(ctx, `INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, status) VALUES ($1, $2, $3, 'requirement', 'active') RETURNING id`, workspaceID, agentID, userID).Scan(&chatID); err != nil {
		t.Fatal(err)
	}
	var requirementID string
	if err := pool.QueryRow(ctx, `INSERT INTO requirement (workspace_id, source_host, external_id, source_snapshot, source_digest, requester_id, agent_id, chat_session_id) VALUES ($1, 'yixiezuo', 'plan-quick-actions', '{}'::jsonb, 'digest', $2, $3, $4) RETURNING id`, workspaceID, userID, agentID, chatID).Scan(&requirementID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE chat_session_id = $1`, chatID)
		pool.Exec(context.Background(), `DELETE FROM requirement WHERE id = $1`, requirementID)
		pool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, chatID)
	})
	var taskID string
	if err := pool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id, runtime_id, chat_session_id, status, priority, mode, requirement_id) VALUES ($1, $2, $3, 'completed', 0, 'plan', $4) RETURNING id`, agentID, runtimeID, chatID, requirementID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	svc := NewTaskService(db.New(pool), pool, nil, events.New())
	svc.QuickActions = requirementQuickActionsStub{}
	task, err := svc.Queries.GetAgentTask(ctx, util.MustParseUUID(taskID))
	if err != nil {
		t.Fatal(err)
	}
	msg := &db.ChatMessage{Content: "candidate ready", MessageKind: protocol.ChatMessageKindMessage}
	if !svc.chatQuickActionsEligible(ctx, task, msg) {
		t.Fatal("pre-approval clarification should allow quick actions")
	}
	if _, err := pool.Exec(ctx, `UPDATE requirement SET issue_id = $1 WHERE id = $2`, issueID, requirementID); err != nil {
		t.Fatal(err)
	}
	if svc.chatQuickActionsEligible(ctx, task, msg) {
		t.Fatal("Issue binding must suppress automatic quick actions")
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET status = 'done' WHERE id = $1`, issueID); err != nil {
		t.Fatal(err)
	}
	if svc.chatQuickActionsEligible(ctx, task, msg) {
		t.Fatal("done requirement must suppress automatic quick actions")
	}
	if _, err := pool.Exec(ctx, `UPDATE requirement SET issue_id = NULL, cancelled_at = now() WHERE id = $1`, requirementID); err != nil {
		t.Fatal(err)
	}
	if svc.chatQuickActionsEligible(ctx, task, msg) {
		t.Fatal("cancellation must suppress automatic quick actions")
	}
}
