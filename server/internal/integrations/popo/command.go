package popo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func (s *BridgeService) Enqueue(ctx context.Context, item OutboundItem) error {
	if s == nil || s.q == nil {
		return errors.New("popo: bridge command queue not configured")
	}
	content := strings.TrimSpace(item.Content)
	chatID := strings.TrimSpace(item.ChatID)
	if content == "" || chatID == "" {
		return errors.New("popo: outbound chat_id and content are required")
	}
	wsID := item.WorkspaceID
	bridgeID := item.BridgeID
	robotID := strings.TrimSpace(item.RobotID)
	if (!bridgeID.Valid || !wsID.Valid || robotID == "") && item.InstallationID.Valid {
		row, err := s.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
			ID:          item.InstallationID,
			ChannelType: string(TypePopo),
		})
		if err != nil {
			return fmt.Errorf("popo: load installation: %w", err)
		}
		if !wsID.Valid {
			wsID = row.WorkspaceID
		}
		info := DecodePublicConfig(row.Config)
		if !bridgeID.Valid {
			parsed, err := util.ParseUUID(info.BridgeID)
			if err != nil {
				return errors.New("popo: installation has no bridge_id")
			}
			bridgeID = parsed
		}
		if robotID == "" {
			robotID = info.RobotID
		}
	}
	if !bridgeID.Valid {
		return errors.New("popo: installation has no bridge_id")
	}
	chatType := strings.TrimSpace(item.ChatType)
	if chatType == "" {
		chatType = string(chatTypeP2PWire)
	}
	var replyTo *string
	if v := strings.TrimSpace(item.ReplyTo); v != "" {
		replyTo = &v
	}
	payload, err := json.Marshal(SendPayload{
		RobotID:          robotID,
		ChatID:           chatID,
		ChatType:         chatType,
		Text:             content,
		ReplyToMessageID: replyTo,
		IssueID:          uuidString(item.IssueID),
		CommentID:        uuidString(item.CommentID),
		TaskID:           uuidString(item.TaskID),
		BindingID:        uuidString(item.BindingID),
		RouteRevision:    item.RouteRevision,
		OutboundKind:     strings.TrimSpace(item.OutboundKind),
	})
	if err != nil {
		return fmt.Errorf("encode send payload: %w", err)
	}
	_, err = s.q.EnqueuePopoBridgeCommand(ctx, db.EnqueuePopoBridgeCommandParams{
		WorkspaceID:    wsID,
		BridgeID:       bridgeID,
		InstallationID: item.InstallationID,
		Type:           CommandTypeSend,
		DeliveryID:     dbid.NewV7(),
		Payload:        payload,
	})
	if err != nil {
		return fmt.Errorf("enqueue popo command: %w", err)
	}
	return nil
}

func (s *BridgeService) LeaseCommands(ctx context.Context, bridgeID pgtype.UUID, wait time.Duration) ([]db.PopoBridgeCommand, error) {
	if wait < 0 {
		wait = 0
	}
	if wait > MaxCommandWait {
		wait = MaxCommandWait
	}
	deadline := s.now().Add(wait)
	for {
		if err := s.q.ReclaimExpiredPopoBridgeCommandLeases(ctx, bridgeID); err != nil {
			return nil, fmt.Errorf("reclaim command leases: %w", err)
		}
		rows, err := s.q.LeasePopoBridgeCommands(ctx, db.LeasePopoBridgeCommandsParams{
			LeaseExpiresAt: pgtype.Timestamptz{Time: s.now().Add(CommandLease), Valid: true},
			LeaseBridgeID:  bridgeID,
			MaxN:           MaxLeaseCommands,
		})
		if err != nil {
			return nil, fmt.Errorf("lease commands: %w", err)
		}
		if len(rows) > 0 {
			return rows, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return []db.PopoBridgeCommand{}, nil
		}
		sleep := 200 * time.Millisecond
		if sleep > remaining {
			sleep = remaining
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

type CommandReceipt struct {
	Status          string
	RemoteMessageID string
	Error           string
}

func (s *BridgeService) RecordReceipt(ctx context.Context, commandID, bridgeID pgtype.UUID, receipt CommandReceipt) (db.PopoBridgeCommand, error) {
	status := strings.TrimSpace(receipt.Status)
	switch status {
	case CommandStatusDelivered, CommandStatusFailed, CommandStatusUnknown:
	default:
		return db.PopoBridgeCommand{}, ErrInvalidReceipt
	}
	remoteID := strings.TrimSpace(receipt.RemoteMessageID)
	if status == CommandStatusDelivered && remoteID == "" {
		return db.PopoBridgeCommand{}, ErrInvalidReceipt
	}
	row, err := s.q.GetPopoBridgeCommandForBridge(ctx, db.GetPopoBridgeCommandForBridgeParams{
		ID:       commandID,
		BridgeID: bridgeID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.PopoBridgeCommand{}, ErrCommandNotFound
		}
		return db.PopoBridgeCommand{}, err
	}
	switch row.Status {
	case CommandStatusDelivered, CommandStatusFailed, CommandStatusUnknown, CommandStatusCancelled:
		if receiptMatches(row, status, remoteID) {
			return row, nil
		}
		return db.PopoBridgeCommand{}, ErrReceiptConflict
	}
	updated, err := s.q.SetPopoBridgeCommandReceipt(ctx, db.SetPopoBridgeCommandReceiptParams{
		ID:              commandID,
		BridgeID:        bridgeID,
		Status:          status,
		RemoteMessageID: pgtype.Text{String: remoteID, Valid: remoteID != ""},
		LastError:       pgtype.Text{String: receipt.Error, Valid: strings.TrimSpace(receipt.Error) != ""},
	})
	if err != nil {
		return db.PopoBridgeCommand{}, err
	}
	if status == CommandStatusDelivered {
		s.recordOutboundLedger(ctx, updated, remoteID)
	}
	return updated, nil
}

func (s *BridgeService) recordOutboundLedger(ctx context.Context, row db.PopoBridgeCommand, remoteID string) {
	if s.q == nil || remoteID == "" || !row.InstallationID.Valid {
		return
	}
	var payload SendPayload
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		return
	}
	kind := strings.TrimSpace(payload.OutboundKind)
	if kind == "" {
		kind = "send"
	}
	bindingID, _ := util.ParseUUID(payload.BindingID)
	if !bindingID.Valid {
		return
	}
	issueID, _ := util.ParseUUID(payload.IssueID)
	commentID, _ := util.ParseUUID(payload.CommentID)
	taskID, _ := util.ParseUUID(payload.TaskID)
	_ = s.q.RecordChannelOutboundMessage(ctx, db.RecordChannelOutboundMessageParams{
		OutboundInstallationID: row.InstallationID,
		OutboundChannelType:    string(TypePopo),
		OutboundMessageID:      remoteID,
		OutboundBindingID:      bindingID,
		OutboundRouteRevision:  payload.RouteRevision,
		OutboundTaskID:         taskID,
		OutboundKind:           kind,
		OutboundIssueID:        issueID,
		OutboundCommentID:      commentID,
	})
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return util.UUIDToString(id)
}

func receiptMatches(row db.PopoBridgeCommand, status, remoteID string) bool {
	if row.Status != status {
		return false
	}
	if status == CommandStatusDelivered && row.RemoteMessageID.String != remoteID {
		return false
	}
	return true
}

const chatTypeP2PWire = "p2p"
