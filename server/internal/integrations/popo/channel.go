package popo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// popoChannel is one installation. Connect does not open POPO; inbound
// arrives via POST /api/popo/bridge/inbound. Send enqueues a bridge command.
type popoChannel struct {
	installationID pgtype.UUID
	workspaceID    pgtype.UUID
	bridgeID       pgtype.UUID
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
	return channel.CapText | channel.CapQuoteReply | channel.CapAttachment | channel.CapVoice
}

func (c *popoChannel) Disconnect(ctx context.Context) error { return nil }

func (c *popoChannel) Connect(ctx context.Context) error {
	// Supervisor requires Connect to block until cancel. Inbound is HTTP
	// from the Windows bridge, not a cloud long-conn.
	<-ctx.Done()
	return nil
}

func (c *popoChannel) Send(ctx context.Context, out channel.OutboundMessage) (channel.SendResult, error) {
	if c.queue == nil {
		return channel.SendResult{}, errors.New("popo: outbound queue not configured")
	}
	wsID := c.workspaceID
	bridgeID := c.bridgeID
	robotID := c.robotID
	if (!wsID.Valid || !bridgeID.Valid) && c.lookup != nil && c.installationID.Valid {
		row, err := c.lookup.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
			ID:          c.installationID,
			ChannelType: string(TypePopo),
		})
		if err != nil {
			return channel.SendResult{}, fmt.Errorf("popo: load installation: %w", err)
		}
		if !wsID.Valid {
			wsID = row.WorkspaceID
		}
		info := DecodePublicConfig(row.Config)
		if !bridgeID.Valid {
			if parsed, err := util.ParseUUID(info.BridgeID); err == nil {
				bridgeID = parsed
			}
		}
		if robotID == "" {
			robotID = info.RobotID
		}
	}
	if err := c.queue.Enqueue(ctx, OutboundItem{
		WorkspaceID:    wsID,
		InstallationID: c.installationID,
		BridgeID:       bridgeID,
		ChatID:         out.ChatID,
		ChatType:       string(channel.ChatTypeP2P),
		RobotID:        robotID,
		Content:        out.Text,
		ReplyTo:        out.ReplyTo,
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
		var bridgeID pgtype.UUID
		if ok, id := parseBridgeID(ic.BridgeID); ok {
			parsed, err := util.ParseUUID(id)
			if err == nil {
				bridgeID = parsed
			}
		}
		return &popoChannel{
			installationID: cfg.ID,
			bridgeID:       bridgeID,
			robotID:        ic.AppID,
			queue:          deps.Queue,
			lookup:         deps.Lookup,
			logger:         logger,
		}, nil
	}
}
