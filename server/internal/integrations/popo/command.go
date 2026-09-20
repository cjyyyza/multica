package popo

import (
	"context"
	"crypto/md5"
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
	if chatID == "" {
		return errors.New("popo: outbound chat_id is required")
	}
	if content == "" && len(item.Attachments) == 0 {
		return errors.New("popo: outbound content or attachments are required")
	}
	wsID := item.WorkspaceID
	bridgeID := item.BridgeID
	robotID := strings.TrimSpace(item.RobotID)
	if item.InstallationID.Valid {
		row, err := s.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
			ID:          item.InstallationID,
			ChannelType: string(TypePopo),
		})
		if err != nil {
			return fmt.Errorf("popo: load installation: %w", err)
		}
		if row.Status != "active" || (wsID.Valid && wsID != row.WorkspaceID) {
			return ErrInstallationWrong
		}
		if !wsID.Valid {
			wsID = row.WorkspaceID
		}
		info := DecodePublicConfig(row.Config)
		if (bridgeID.Valid && uuidString(bridgeID) != info.BridgeID) || (robotID != "" && robotID != info.RobotID) {
			return ErrInstallationWrong
		}
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
		SourceKey:        item.SourceKey,
		IssueStatus:      item.IssueStatus,
		Attachments:      item.Attachments,
		Reply:            item.Reply,
	})
	if err != nil {
		return fmt.Errorf("encode send payload: %w", err)
	}
	deliveryID := dbid.NewV7()
	if item.SourceKey != "" {
		deliveryID = sourceDeliveryID(item.InstallationID, item.SourceKey)
		if _, err := s.q.GetPopoBridgeCommandByDeliveryID(ctx, deliveryID); err == nil {
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	if len(item.Attachments) == 0 {
		row, err := s.q.EnqueuePopoBridgeCommand(ctx, db.EnqueuePopoBridgeCommandParams{
			WorkspaceID:    wsID,
			BridgeID:       bridgeID,
			InstallationID: item.InstallationID,
			Type:           CommandTypeSend,
			DeliveryID:     deliveryID,
			Payload:        payload,
		})
		if errors.Is(err, pgx.ErrNoRows) && item.SourceKey != "" {
			return nil
		}
		if err != nil {
			return fmt.Errorf("enqueue popo command: %w", err)
		}
		logTrace("popo command enqueued",
			"",
			uuidString(row.InstallationID),
			chatID,
			uuidString(item.IssueID),
			uuidString(item.TaskID),
			uuidString(row.DeliveryID),
			"",
		)
		return nil
	}
	if s.tx == nil {
		return errors.New("popo: outbound media grants require a transaction")
	}
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin outbound media tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	row, err := qtx.EnqueuePopoBridgeCommand(ctx, db.EnqueuePopoBridgeCommandParams{
		WorkspaceID:    wsID,
		BridgeID:       bridgeID,
		InstallationID: item.InstallationID,
		Type:           CommandTypeSend,
		DeliveryID:     deliveryID,
		Payload:        payload,
	})
	if errors.Is(err, pgx.ErrNoRows) && item.SourceKey != "" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("enqueue popo command: %w", err)
	}
	expiresAt := pgtype.Timestamptz{Time: s.now().Add(OutboundMediaGrantTTL), Valid: true}
	for _, att := range item.Attachments {
		attachmentID, err := util.ParseUUID(att.AttachmentID)
		if err != nil || !attachmentID.Valid {
			return fmt.Errorf("popo: invalid outbound attachment_id")
		}
		if _, err := qtx.InsertPopoOutboundMediaGrant(ctx, db.InsertPopoOutboundMediaGrantParams{
			ID:             dbid.NewV7(),
			WorkspaceID:    wsID,
			BridgeID:       bridgeID,
			InstallationID: item.InstallationID,
			CommandID:      row.ID,
			AttachmentID:   attachmentID,
			ExpiresAt:      expiresAt,
		}); err != nil {
			return fmt.Errorf("grant outbound media: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit outbound media: %w", err)
	}
	logTrace("popo command enqueued",
		"",
		uuidString(row.InstallationID),
		chatID,
		uuidString(item.IssueID),
		uuidString(item.TaskID),
		uuidString(row.DeliveryID),
		"",
	)
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
		if err := s.q.CancelRevokedPopoBridgeCommands(ctx, bridgeID); err != nil {
			return nil, err
		}
		if err := s.q.ReclaimExpiredPopoBridgeCommandLeases(ctx, bridgeID); err != nil {
			return nil, fmt.Errorf("reclaim command leases: %w", err)
		}
		rows, err := s.q.LeasePopoBridgeCommands(ctx, db.LeasePopoBridgeCommandsParams{
			LeaseExpiresAt:             pgtype.Timestamptz{Time: s.now().Add(CommandLease), Valid: true},
			RegistrationLeaseExpiresAt: pgtype.Timestamptz{Time: s.now().Add(RegistrationTTL + 2*CommandLease), Valid: true},
			LeaseBridgeID:              bridgeID,
			MaxN:                       MaxLeaseCommands,
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
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return db.PopoBridgeCommand{}, err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)
	row, err := qtx.LockPopoBridgeCommandForReceipt(ctx, db.LockPopoBridgeCommandForReceiptParams{
		ID:       commandID,
		BridgeID: bridgeID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.PopoBridgeCommand{}, ErrCommandNotFound
		}
		return db.PopoBridgeCommand{}, err
	}
	if status == CommandStatusDelivered && row.Type == CommandTypeSend && remoteID == "" {
		return db.PopoBridgeCommand{}, ErrInvalidReceipt
	}
	switch row.Status {
	case CommandStatusDelivered, CommandStatusFailed, CommandStatusUnknown, CommandStatusCancelled:
		if receiptMatches(row, status, remoteID) {
			if status == CommandStatusDelivered {
				if err := s.recordOutboundLedger(ctx, qtx, row, remoteID); err != nil {
					return db.PopoBridgeCommand{}, err
				}
			}
			return row, tx.Commit(ctx)
		}
		return db.PopoBridgeCommand{}, ErrReceiptConflict
	}
	updated, err := qtx.SetPopoBridgeCommandReceipt(ctx, db.SetPopoBridgeCommandReceiptParams{
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
		if err := s.recordOutboundLedger(ctx, qtx, updated, remoteID); err != nil {
			return db.PopoBridgeCommand{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return db.PopoBridgeCommand{}, err
	}
	var payload SendPayload
	_ = json.Unmarshal(updated.Payload, &payload)
	logTrace("popo command receipt",
		"",
		uuidString(updated.InstallationID),
		payload.ChatID,
		payload.IssueID,
		payload.TaskID,
		uuidString(updated.DeliveryID),
		remoteID,
	)
	return updated, nil
}

func (s *BridgeService) recordOutboundLedger(ctx context.Context, q *db.Queries, row db.PopoBridgeCommand, remoteID string) error {
	if row.Type != CommandTypeSend || remoteID == "" || !row.InstallationID.Valid {
		return nil
	}
	var payload SendPayload
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		return err
	}
	kind := strings.TrimSpace(payload.OutboundKind)
	if kind == "" {
		kind = "send"
	}
	bindingID, _ := util.ParseUUID(payload.BindingID)
	if !bindingID.Valid {
		return nil
	}
	issueID, _ := util.ParseUUID(payload.IssueID)
	commentID, _ := util.ParseUUID(payload.CommentID)
	taskID, _ := util.ParseUUID(payload.TaskID)
	return q.RecordChannelOutboundMessage(ctx, db.RecordChannelOutboundMessageParams{
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

// This hash is a stable deduplication coordinate, not an authentication secret.
func sourceDeliveryID(installationID pgtype.UUID, sourceKey string) pgtype.UUID {
	return pgtype.UUID{Bytes: md5.Sum([]byte(uuidString(installationID) + ":" + sourceKey)), Valid: true}
}

func (s *BridgeService) CanDeliver(ctx context.Context, commandID, bridgeID pgtype.UUID) (bool, error) {
	return s.q.CanDeliverPopoBridgeCommand(ctx, db.CanDeliverPopoBridgeCommandParams{ID: commandID, BridgeID: bridgeID})
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
