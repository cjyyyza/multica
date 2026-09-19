package popo

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

// OutboundItem is one pending robot reply for the Windows bridge to send.
type OutboundItem struct {
	WorkspaceID    pgtype.UUID
	InstallationID pgtype.UUID
	BridgeID       pgtype.UUID
	ChatID         string
	ChatType       string
	RobotID        string
	Content        string
	ReplyTo        string
}

// Enqueuer stores a send command. The API never delivers it to POPO.
type Enqueuer interface {
	Enqueue(ctx context.Context, item OutboundItem) error
}
