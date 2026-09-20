package popo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// BridgeStatus is one Windows host's live diagnostics. Online, POPO
// connected, and the three counters are independent; a single healthy
// flag must not stand in for this set.
type BridgeStatus struct {
	Bridge            db.PopoBridge
	Online            bool
	PopoConnected     bool
	InboundBacklog    int64
	OutboundBacklog   int64
	UnknownDeliveries int64
}

// WorkspaceStatus is the member-visible POPO operations snapshot.
type WorkspaceStatus struct {
	Bridges       []BridgeStatus
	RuntimeOnline bool
}

func (s *BridgeService) Status(ctx context.Context, workspaceID pgtype.UUID) (WorkspaceStatus, error) {
	rows, err := s.q.ListPopoBridgesByWorkspace(ctx, workspaceID)
	if err != nil {
		return WorkspaceStatus{}, fmt.Errorf("list popo bridges: %w", err)
	}
	cmdCounts, err := s.q.CountPopoBridgeCommandStatuses(ctx, workspaceID)
	if err != nil {
		return WorkspaceStatus{}, fmt.Errorf("count popo commands: %w", err)
	}
	mediaCounts, err := s.q.CountPendingPopoMediaStagingByBridge(ctx, workspaceID)
	if err != nil {
		return WorkspaceStatus{}, fmt.Errorf("count pending popo media: %w", err)
	}
	runtimeOnline, err := s.q.PopoBoundAgentRuntimeOnline(ctx, workspaceID)
	if err != nil {
		return WorkspaceStatus{}, fmt.Errorf("popo runtime presence: %w", err)
	}

	outbound := make(map[[16]byte]int64, len(cmdCounts))
	unknown := make(map[[16]byte]int64, len(cmdCounts))
	for _, row := range cmdCounts {
		if !row.BridgeID.Valid {
			continue
		}
		key := row.BridgeID.Bytes
		switch row.Status {
		case CommandStatusPending, CommandStatusLeased:
			outbound[key] += row.N
		case CommandStatusUnknown:
			unknown[key] += row.N
		}
	}
	inbound := make(map[[16]byte]int64, len(mediaCounts))
	for _, row := range mediaCounts {
		if row.BridgeID.Valid {
			inbound[row.BridgeID.Bytes] = row.N
		}
	}

	now := s.now()
	out := make([]BridgeStatus, 0, len(rows))
	for _, row := range rows {
		out = append(out, BridgeStatus{
			Bridge:            row,
			Online:            BridgeOnline(row, now),
			PopoConnected:     BridgePopoConnected(row),
			InboundBacklog:    inbound[row.ID.Bytes],
			OutboundBacklog:   outbound[row.ID.Bytes],
			UnknownDeliveries: unknown[row.ID.Bytes],
		})
	}
	return WorkspaceStatus{Bridges: out, RuntimeOnline: runtimeOnline}, nil
}

func BridgePopoConnected(bridge db.PopoBridge) bool {
	for _, robot := range DecodeRobots(bridge.RobotsJson) {
		if robot.Connected {
			return true
		}
	}
	return false
}
