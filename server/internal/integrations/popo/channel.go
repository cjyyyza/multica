package popo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// popoChannel is one installation. Connect does not open POPO; inbound
// arrives via the Windows CLI → POST /popo/inbound. Send only enqueues.
type popoChannel struct {
	installationID pgtype.UUID
	workspaceID    pgtype.UUID
	robotID        string
	queue          Enqueuer
	lookup         workspaceLookup
	logger         *slog.Logger
}

type workspaceLookup interface {
	GetChannelInstallation(ctx context.Context, arg db.GetChannelInstallationParams) (db.ChannelInstallation, error)
}

func (c *popoChannel) Type() channel.Type { return TypePopo }

func (c *popoChannel) Capabilities() channel.Capability {
	return channel.CapText | channel.CapQuoteReply
}

func (c *popoChannel) Disconnect(ctx context.Context) error { return nil }

func (c *popoChannel) Connect(ctx context.Context) error {
	// Supervisor requires Connect to block until cancel. Inbound is HTTP
	// from the Windows CLI, not a cloud long-conn.
	<-ctx.Done()
	return nil
}

func (c *popoChannel) Send(ctx context.Context, out channel.OutboundMessage) (channel.SendResult, error) {
	if c.queue == nil {
		return channel.SendResult{}, errors.New("popo: outbound queue not configured")
	}
	wsID := c.workspaceID
	if !wsID.Valid && c.lookup != nil && c.installationID.Valid {
		row, err := c.lookup.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
			ID:          c.installationID,
			ChannelType: string(TypePopo),
		})
		if err != nil {
			return channel.SendResult{}, fmt.Errorf("popo: load installation: %w", err)
		}
		wsID = row.WorkspaceID
	}
	if err := c.queue.Enqueue(ctx, OutboundItem{
		WorkspaceID:    wsID,
		InstallationID: c.installationID,
		ChatID:         out.ChatID,
		RobotID:        c.robotID,
		Content:        out.Text,
	}); err != nil {
		return channel.SendResult{}, err
	}
	return channel.SendResult{}, nil
}

// ChannelDeps are closed over by the Factory. Queue may be nil in tests
// that only exercise Connect.
type ChannelDeps struct {
	Queue  Enqueuer
	Lookup workspaceLookup
	Logger *slog.Logger
}

func RegisterPopo(reg *channel.Registry, deps ChannelDeps) {
	reg.Register(TypePopo, newPopoFactory(deps))
}

func newPopoFactory(deps ChannelDeps) channel.Factory {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(cfg channel.Config) (channel.Channel, error) {
		var ic installConfig
		if err := json.Unmarshal(cfg.Raw, &ic); err != nil {
			return nil, fmt.Errorf("popo: decode installation config: %w", err)
		}
		if ic.AppID == "" {
			return nil, errors.New("popo: installation has no robot id")
		}
		return &popoChannel{
			installationID: cfg.ID,
			robotID:        ic.AppID,
			queue:          deps.Queue,
			lookup:         deps.Lookup,
			logger:         logger,
		}, nil
	}
}
