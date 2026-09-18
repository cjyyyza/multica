package popo

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Outbound enqueues the agent's final chat reply for the Windows CLI.
// No streaming cards — dj01bot /outbound is text.
type Outbound struct {
	q      outboundQueries
	queue  Enqueuer
	logger *slog.Logger
}

type outboundQueries interface {
	GetChannelTaskDelivery(ctx context.Context, taskID pgtype.UUID) (db.ChannelTaskDelivery, error)
	GetChannelInstallation(ctx context.Context, arg db.GetChannelInstallationParams) (db.ChannelInstallation, error)
}

func NewOutbound(q *db.Queries, queue Enqueuer, logger *slog.Logger) *Outbound {
	if logger == nil {
		logger = slog.Default()
	}
	return &Outbound{q: q, queue: queue, logger: logger}
}

func (o *Outbound) Register(bus *events.Bus) {
	bus.Subscribe(protocol.EventChatDone, o.handleChatDone)
}

func (o *Outbound) handleChatDone(e events.Event) {
	content := chatDoneContent(e.Payload)
	if content == "" {
		return
	}
	ctx := context.Background()
	taskID, ok := eventTaskID(e)
	if !ok {
		return
	}
	delivery, err := o.q.GetChannelTaskDelivery(ctx, taskID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			o.logger.WarnContext(ctx, "popo outbound: task delivery lookup failed", "error", err)
		}
		return
	}
	if delivery.ChannelType != string(TypePopo) {
		return
	}
	inst, err := o.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID:          delivery.InstallationID,
		ChannelType: string(TypePopo),
	})
	if err != nil || inst.Status != "active" {
		return
	}
	chatID := delivery.ChannelChatID
	if len(delivery.Config) > 0 {
		var cfg popoBindingConfig
		if err := json.Unmarshal(delivery.Config, &cfg); err == nil && cfg.ChatID != "" {
			chatID = cfg.ChatID
		}
	}
	if err := o.queue.Enqueue(ctx, OutboundItem{
		WorkspaceID:    inst.WorkspaceID,
		InstallationID: inst.ID,
		ChatID:         chatID,
		RobotID:        DecodePublicConfig(inst.Config).RobotID,
		Content:        content,
	}); err != nil {
		o.logger.WarnContext(ctx, "popo outbound: enqueue failed",
			"installation_id", util.UUIDToString(inst.ID), "error", err)
	}
}

func eventTaskID(e events.Event) (pgtype.UUID, bool) {
	var raw string
	switch p := e.Payload.(type) {
	case protocol.ChatDonePayload:
		raw = p.TaskID
	case map[string]any:
		raw, _ = p["task_id"].(string)
	}
	id, err := util.ParseUUID(raw)
	return id, err == nil && id.Valid
}

func chatDoneContent(payload any) string {
	switch p := payload.(type) {
	case protocol.ChatDonePayload:
		return p.Content
	case map[string]any:
		if s, ok := p["content"].(string); ok {
			return s
		}
	}
	return ""
}
