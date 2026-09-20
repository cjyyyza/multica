package popo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func replyID(value string) pgtype.UUID {
	id, err := util.ParseUUID(value)
	if err != nil {
		panic(err)
	}
	return id
}

func TestReplySnapshotUsesPersistedSequenceWithoutPrivateToolData(t *testing.T) {
	id := replyID("11111111-1111-4111-8111-111111111111")
	task := db.AgentTaskQueue{ID: id, Attempt: 2}
	text, snapshot := taskReplyProgress(task, []db.TaskMessage{
		{TaskID: id, Seq: 0, Type: "text", Content: pgtype.Text{String: "public answer", Valid: true}},
		{TaskID: id, Seq: 5, Type: "tool_use", Tool: pgtype.Text{String: "read", Valid: true}, Input: []byte(`{"secret":"private-input"}`)},
		{TaskID: id, Seq: 6, Type: "tool_result", Tool: pgtype.Text{String: "read", Valid: true}, Output: pgtype.Text{String: "private-output", Valid: true}},
		{TaskID: id, Seq: 7, Type: "thinking", Content: pgtype.Text{String: "private-reasoning", Valid: true}},
		{TaskID: replyID("22222222-2222-4222-8222-222222222222"), Seq: 99, Type: "text", Content: pgtype.Text{String: "other task", Valid: true}},
	})
	raw, err := json.Marshal(SendPayload{Text: text, Reply: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Sequence != 9 || snapshot.Status != "running" || !strings.HasSuffix(snapshot.ID, ":attempt:2") {
		t.Fatalf("snapshot: %+v", snapshot)
	}
	if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "other task") || text != "public answer" {
		t.Fatalf("private or unrelated data crossed the display boundary: %s", raw)
	}
	if len(snapshot.Steps) != 3 || snapshot.Steps[1].ID == snapshot.Steps[2].ID {
		t.Fatalf("steps: %+v", snapshot.Steps)
	}
	final := taskReplySnapshot(task, replyTerminalSequence, "completed", snapshot.Steps)
	if final.ID != snapshot.ID || final.Sequence <= snapshot.Sequence {
		t.Fatalf("final snapshot does not close the same run")
	}
}

type replyQueue struct{ items []OutboundItem }

func (q *replyQueue) Enqueue(_ context.Context, item OutboundItem) error {
	q.items = append(q.items, item)
	return nil
}

type replyQueries struct {
	*db.Queries
	task         db.AgentTaskQueue
	installation db.ChannelInstallation
	delivery     db.ChannelTaskDelivery
}

func (q *replyQueries) GetAgentTask(context.Context, pgtype.UUID) (db.AgentTaskQueue, error) {
	return q.task, nil
}
func (q *replyQueries) GetChannelTaskDelivery(context.Context, pgtype.UUID) (db.ChannelTaskDelivery, error) {
	return q.delivery, nil
}
func (q *replyQueries) GetChannelInstallation(context.Context, db.GetChannelInstallationParams) (db.ChannelInstallation, error) {
	return q.installation, nil
}
func (q *replyQueries) ListTaskMessages(context.Context, pgtype.UUID) ([]db.TaskMessage, error) {
	return nil, nil
}

func TestTaskLifecycleUsesExactRouteAndDoesNotFinishChatBeforeItsAnswer(t *testing.T) {
	id := replyID("11111111-1111-4111-8111-111111111111")
	workspace := replyID("22222222-2222-4222-8222-222222222222")
	installation := replyID("33333333-3333-4333-8333-333333333333")
	q := &replyQueries{
		task:         db.AgentTaskQueue{ID: id, Status: "running", ChatSessionID: id},
		installation: db.ChannelInstallation{ID: installation, WorkspaceID: workspace, Status: "active", Config: []byte(`{"bridge_id":"44444444-4444-4444-8444-444444444444","robot_id":"robot"}`)},
		delivery:     db.ChannelTaskDelivery{InstallationID: installation, ChannelType: "popo", ChannelChatID: "chat", ChatType: "p2p"},
	}
	queue := &replyQueue{}
	o := &Outbound{q: q, queue: queue}
	event := events.Event{Type: protocol.EventTaskRunning, TaskID: uuidString(id), WorkspaceID: uuidString(workspace)}
	o.enqueueTaskReply(context.Background(), event)
	if len(queue.items) != 1 || queue.items[0].Reply.Status != "running" || queue.items[0].ChatID != "chat" {
		t.Fatalf("running: %+v", queue.items)
	}
	q.task.Status = "completed"
	event.Type = protocol.EventTaskCompleted
	o.enqueueTaskReply(context.Background(), event)
	if len(queue.items) != 1 {
		t.Fatal("chat must wait for ChatDone")
	}
	q.task.Status, event.Type = "failed", protocol.EventTaskFailed
	event.Payload = map[string]any{"retry_pending": true}
	o.enqueueTaskReply(context.Background(), event)
	if len(queue.items) != 1 {
		t.Fatal("automatic retry must not close the card")
	}
	event.Payload = nil
	o.enqueueTaskReply(context.Background(), event)
	if len(queue.items) != 2 || queue.items[1].Reply.Status != "failed" || queue.items[1].Reply.ID != queue.items[0].Reply.ID {
		t.Fatalf("failed: %+v", queue.items)
	}
	event.WorkspaceID = "other-workspace"
	o.enqueueTaskReply(context.Background(), event)
	if len(queue.items) != 2 {
		t.Fatal("cross-workspace event reached a robot")
	}
}
