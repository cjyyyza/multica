package popo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const replyTerminalSequence int64 = 9007199254740991

// ReplySnapshot is a full projection, never a token delta or an execution command.
type ReplySnapshot struct {
	Schema   int         `json:"schema"`
	ID       string      `json:"id"`
	Sequence int64       `json:"sequence"`
	Status   string      `json:"status"`
	Prompt   string      `json:"prompt,omitempty"`
	Steps    []ReplyStep `json:"steps,omitempty"`
}

type ReplyStep struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

func replyClip(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func taskReplySnapshot(task db.AgentTaskQueue, sequence int64, status string, steps []ReplyStep) *ReplySnapshot {
	if !task.ID.Valid {
		return nil
	}
	return &ReplySnapshot{Schema: 1,
		ID:       fmt.Sprintf("task:%s:attempt:%d", uuidString(task.ID), task.Attempt),
		Sequence: sequence, Status: status, Prompt: replyClip(task.TriggerSummary.String, 500), Steps: steps}
}

type taskReplyReader interface {
	ListTaskMessages(context.Context, pgtype.UUID) ([]db.TaskMessage, error)
}

// Project only persisted display events. Tool inputs/outputs and reasoning never
// cross the bridge. Sequence comes from task_message, so replay survives restart.
func taskReplyProgress(task db.AgentTaskQueue, messages []db.TaskMessage) (string, *ReplySnapshot) {
	sequence := int64(1)
	parts := []string{}
	steps := []ReplyStep{{ID: "run", Name: "Multica", Title: "执行任务", Status: "running"}}
	for _, message := range messages {
		if message.TaskID != task.ID {
			continue
		}
		if value := int64(message.Seq) + 2; value > sequence {
			sequence = value
		}
		switch message.Type {
		case "text":
			if text := strings.TrimSpace(message.Content.String); text != "" {
				parts = append(parts, text)
			}
		case "tool_use", "tool_result":
			label := "已发起工具调用："
			if message.Type == "tool_result" {
				label = "已收到工具结果："
			}
			steps = append(steps, ReplyStep{ID: fmt.Sprintf("message:%d", message.Seq),
				Name: replyClip(message.Tool.String, 80), Title: replyClip(label+message.Tool.String, 240), Status: "completed"})
		}
	}
	if len(steps) > 500 {
		steps = append(steps[:1:1], steps[len(steps)-499:]...)
	}
	text := strings.Join(parts, "\n\n")
	if text == "" {
		text = "任务正在执行。"
	}
	return replyClip(text, MaxOutboundTextRunes), taskReplySnapshot(task, sequence, "running", steps)
}

func (o *Outbound) handleTaskReplyProgress(e events.Event) {
	o.enqueueTaskReply(context.Background(), e)
}

func (o *Outbound) enqueueTaskReply(ctx context.Context, e events.Event) {
	if o.queue == nil {
		return
	}
	if fields, ok := e.Payload.(map[string]any); ok && fields["retry_pending"] == true {
		return
	}
	id, ok := eventTaskID(e)
	if !ok {
		return
	}
	task, err := o.q.GetAgentTask(ctx, id)
	if err != nil || !task.ID.Valid {
		return
	}
	status, kind := "running", "task_progress"
	switch e.Type {
	case protocol.EventTaskCompleted:
		// ChatDone owns the final chat answer; do not close its card early.
		if task.ChatSessionID.Valid || task.Status != "completed" {
			return
		}
		status, kind = "completed", "task_completed"
	case protocol.EventTaskFailed:
		if task.Status != "failed" {
			return
		}
		status, kind = "failed", "task_failed"
	case protocol.EventTaskCancelled:
		if task.Status != "cancelled" {
			return
		}
		status, kind = "cancelled", "task_cancelled"
	default:
		if task.Status != "running" {
			return
		}
	}
	var messages []db.TaskMessage
	if reader, ok := o.q.(taskReplyReader); ok {
		messages, err = reader.ListTaskMessages(ctx, id)
		if err != nil {
			return
		}
	}
	text, snapshot := taskReplyProgress(task, messages)
	if status != "running" {
		snapshot.Sequence, snapshot.Status = replyTerminalSequence, status
		snapshot.Steps[0].Status = status
		text = map[string]string{"completed": "本次执行已完成。", "failed": "本次执行失败。", "cancelled": "本次执行已取消。"}[status]
		if status == "completed" {
			var result struct {
				Output string `json:"output"`
			}
			if json.Unmarshal(task.Result, &result) == nil && strings.TrimSpace(result.Output) != "" {
				text += "\n\n" + result.Output
			}
		}
	}
	item := OutboundItem{TaskID: id, IssueID: task.IssueID, Content: text, OutboundKind: kind, Reply: snapshot}
	delivery, err := o.q.GetChannelTaskDelivery(ctx, id)
	if err == nil {
		if delivery.ChannelType != string(TypePopo) {
			return
		}
		item.InstallationID = delivery.InstallationID
		item.ChatID, item.ChatType = delivery.ChannelChatID, delivery.ChatType
		var binding popoBindingConfig
		if json.Unmarshal(delivery.Config, &binding) == nil && binding.ChatID != "" {
			item.ChatID = binding.ChatID
		}
		item.BindingID, item.RouteRevision = delivery.BindingID, delivery.RouteRevision
		var config popoBindingConfig
		if json.Unmarshal(delivery.Config, &config) == nil && config.ChatID != "" {
			item.ChatID = config.ChatID
		}
	} else if errors.Is(err, pgx.ErrNoRows) && task.IssueID.Valid {
		source, sourceErr := o.q.GetChannelIssueSourceByIssue(ctx, task.IssueID)
		if sourceErr != nil || source.ChannelType != string(TypePopo) {
			return
		}
		item.WorkspaceID, item.InstallationID = source.WorkspaceID, source.InstallationID
		item.ChatID, item.ChatType = source.ChannelChatID, source.ChatType
		item.BindingID, item.RouteRevision = source.BindingID, source.RouteRevision
	} else {
		return
	}
	inst, err := o.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{ID: item.InstallationID, ChannelType: string(TypePopo)})
	if err != nil || inst.Status != "active" || (item.WorkspaceID.Valid && inst.WorkspaceID != item.WorkspaceID) {
		return
	}
	if e.WorkspaceID != "" && e.WorkspaceID != uuidString(inst.WorkspaceID) {
		return
	}
	item.WorkspaceID = inst.WorkspaceID
	info := DecodePublicConfig(inst.Config)
	item.BridgeID, err = util.ParseUUID(info.BridgeID)
	if err != nil {
		return
	}
	item.RobotID = info.RobotID
	item.SourceKey = fmt.Sprintf("task_card:%s:%d", snapshot.ID, snapshot.Sequence)
	if status == "failed" || status == "cancelled" {
		item.SourceKey = kind + ":" + uuidString(id)
	}
	if o.sourceQueued(ctx, item.InstallationID, item.SourceKey) {
		return
	}
	if item.ChatType == "" {
		item.ChatType = "p2p"
	}
	if task.IssueID.Valid {
		item.Content, _ = clipOutboundText(item.Content, o.issueWebLink(ctx, task.IssueID))
	}
	if err := o.queue.Enqueue(ctx, item); err != nil {
		o.logger.WarnContext(ctx, "popo reply snapshot enqueue failed", "task_id", uuidString(id), "error", err)
	}
}
