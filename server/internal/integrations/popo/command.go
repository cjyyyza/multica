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

	"github.com/multica-ai/multica/server/internal/service"
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
	passes := 0
	if err := s.q.ExpireRequirementPopoCommandLeases(ctx); err != nil {
		return nil, fmt.Errorf("expire requirement command leases: %w", err)
	}
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
			kept := rows[:0]
			for _, command := range rows {
				if !command.RequirementID.Valid {
					kept = append(kept, command)
					continue
				}
				allowed, err := s.CanDeliver(ctx, command.ID, bridgeID)
				if err != nil {
					return nil, err
				}
				if allowed {
					kept = append(kept, command)
				}
			}
			passes++
			if len(kept) > 0 {
				return kept, nil
			}
			if passes >= 4 {
				return []db.PopoBridgeCommand{}, nil
			}
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
	ProtocolVersion   int    `json:"protocol_version,omitempty"`
	Status            string `json:"status"`
	CommandID         string `json:"command_id,omitempty"`
	RemoteMessageID   string `json:"remote_message_id,omitempty"`
	Error             string `json:"error,omitempty"`
	ErrorCode         string `json:"error_code,omitempty"`
	Attempt           *int32 `json:"attempt,omitempty"`
	DeliveryID        string `json:"delivery_id,omitempty"`
	LeaseID           string `json:"lease_id,omitempty"`
	PayloadHash       string `json:"payload_hash,omitempty"`
	NotSent           *bool  `json:"not_sent,omitempty"`
	Retryable         *bool  `json:"retryable,omitempty"`
	EvidenceFinalized *bool  `json:"evidence_finalized,omitempty"`
	PartialDelivery   *bool  `json:"partial_delivery,omitempty"`
}

func (s *BridgeService) RecordReceipt(ctx context.Context, commandID, bridgeID pgtype.UUID, receipt CommandReceipt) (db.PopoBridgeCommand, error) {
	status := strings.TrimSpace(receipt.Status)
	switch status {
	case CommandStatusDelivered, CommandStatusFailed, CommandStatusUnknown,
		CommandStatusCancelled, CommandStatusExpired, CommandStatusSuperseded:
	default:
		return db.PopoBridgeCommand{}, ErrInvalidReceipt
	}
	remoteID := strings.TrimSpace(receipt.RemoteMessageID)
	receiptError := receipt.Error
	if strings.TrimSpace(receiptError) == "" {
		receiptError = receipt.ErrorCode
	}
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
	if errors.Is(err, pgx.ErrNoRows) && strings.TrimSpace(receipt.DeliveryID) != "" {
		deliveryID, parseErr := util.ParseUUID(strings.TrimSpace(receipt.DeliveryID))
		if parseErr != nil {
			return db.PopoBridgeCommand{}, ErrReceiptConflict
		}
		row, err = qtx.LockRequirementPopoCommandForReceiptByDelivery(ctx, db.LockRequirementPopoCommandForReceiptByDeliveryParams{
			BridgeID: bridgeID, DeliveryID: deliveryID,
		})
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.PopoBridgeCommand{}, ErrCommandNotFound
		}
		return db.PopoBridgeCommand{}, err
	}
	if raw := strings.TrimSpace(receipt.DeliveryID); raw != "" {
		deliveryID, parseErr := util.ParseUUID(raw)
		if parseErr != nil || (deliveryID != row.DeliveryID && deliveryID != row.ID) {
			return db.PopoBridgeCommand{}, ErrReceiptConflict
		}
	}
	if receipt.Attempt != nil && *receipt.Attempt != row.Attempt {
		return db.PopoBridgeCommand{}, ErrReceiptConflict
	}
	if row.Attempt > 1 && receipt.Attempt == nil {
		return db.PopoBridgeCommand{}, ErrInvalidReceipt
	}
	if status == CommandStatusDelivered && row.Type == CommandTypeSend && remoteID == "" {
		return db.PopoBridgeCommand{}, ErrInvalidReceipt
	}
	if receipt.PartialDelivery != nil && *receipt.PartialDelivery && status == CommandStatusDelivered {
		return db.PopoBridgeCommand{}, ErrInvalidReceipt
	}
	if status == CommandStatusDelivered && receiptError != "" {
		// A delivered receipt may still carry a warning; the outcome stays delivered.
		receiptError = ""
	}
	if row.RequirementID.Valid {
		updated, err := s.recordRequirementReceipt(ctx, qtx, row, receipt, status, remoteID, receiptError)
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
		return updated, nil
	}
	updated, err := s.recordLegacyReceipt(ctx, qtx, row, status, remoteID, receiptError)
	if err != nil {
		return db.PopoBridgeCommand{}, err
	}
	if err := s.applyReceiptEffects(ctx, qtx, updated, status, remoteID, receiptError); err != nil {
		return db.PopoBridgeCommand{}, err
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

func (s *BridgeService) recordLegacyReceipt(ctx context.Context, q *db.Queries, row db.PopoBridgeCommand, status, remoteID, receiptError string) (db.PopoBridgeCommand, error) {
	switch row.Status {
	case CommandStatusDelivered, CommandStatusFailed, CommandStatusUnknown,
		CommandStatusCancelled, CommandStatusExpired, CommandStatusSuperseded:
		if receiptMatches(row, status, remoteID) {
			return row, nil
		}
		lateDelivered := status == CommandStatusDelivered &&
			(row.Status == CommandStatusCancelled || row.Status == CommandStatusExpired || row.Status == CommandStatusSuperseded)
		if !lateDelivered {
			return db.PopoBridgeCommand{}, ErrReceiptConflict
		}
	}
	return q.SetPopoBridgeCommandReceipt(ctx, db.SetPopoBridgeCommandReceiptParams{
		ID: row.ID, BridgeID: row.BridgeID, Status: status,
		RemoteMessageID: pgtype.Text{String: remoteID, Valid: remoteID != ""},
		LastError:       pgtype.Text{String: receiptError, Valid: strings.TrimSpace(receiptError) != ""},
	})
}

func (s *BridgeService) recordRequirementReceipt(
	ctx context.Context,
	q *db.Queries,
	row db.PopoBridgeCommand,
	receipt CommandReceipt,
	status, remoteID, receiptError string,
) (db.PopoBridgeCommand, error) {
	if raw := strings.TrimSpace(receipt.LeaseID); raw != "" {
		leaseID, err := util.ParseUUID(raw)
		if err != nil || !row.LeaseID.Valid || row.LeaseID != leaseID {
			return db.PopoBridgeCommand{}, ErrReceiptConflict
		}
	}
	if raw := strings.TrimSpace(receipt.PayloadHash); raw != "" {
		if !row.PayloadHash.Valid || row.PayloadHash.String != raw {
			return db.PopoBridgeCommand{}, ErrReceiptConflict
		}
	}
	if row.Status == status {
		if status == CommandStatusDelivered && row.RemoteMessageID.String != remoteID {
			return db.PopoBridgeCommand{}, ErrReceiptConflict
		}
		return row, nil
	}
	switch row.Status {
	case CommandStatusDelivered:
		return db.PopoBridgeCommand{}, ErrReceiptConflict
	case CommandStatusFailed, CommandStatusUnknown, CommandStatusCancelled, CommandStatusExpired, CommandStatusSuperseded:
		if status != CommandStatusDelivered {
			if status == CommandStatusCancelled || status == CommandStatusExpired {
				// A late transport close for an already-fenced business intent
				// keeps the stronger local terminal state.
				return row, nil
			}
			return db.PopoBridgeCommand{}, ErrReceiptConflict
		}
	}

	params := db.RecordRequirementPopoCommandReceiptParams{
		ID: row.ID, BridgeID: row.BridgeID, Attempt: row.Attempt,
		Status:            status,
		RemoteMessageID:   pgtype.Text{String: remoteID, Valid: remoteID != ""},
		LastError:         pgtype.Text{String: receiptError, Valid: strings.TrimSpace(receiptError) != ""},
		NotSent:           false,
		Retryable:         false,
		PartialDelivery:   receipt.PartialDelivery != nil && *receipt.PartialDelivery,
		EvidenceFinalized: receipt.EvidenceFinalized != nil && *receipt.EvidenceFinalized,
		NeedsReconcile:    false,
	}
	if raw := strings.TrimSpace(receipt.LeaseID); raw != "" {
		params.LeaseID, _ = util.ParseUUID(raw)
	}
	if raw := strings.TrimSpace(receipt.PayloadHash); raw != "" {
		params.PayloadHash = pgtype.Text{String: raw, Valid: true}
	}

	switch status {
	case CommandStatusDelivered:
		params.NotSent = false
		params.Retryable = false
		params.PartialDelivery = false
		params.EvidenceFinalized = true
		params.NeedsReconcile = false
	case CommandStatusUnknown:
		params.NotSent = false
		params.Retryable = false
		params.NeedsReconcile = true
	case CommandStatusCancelled, CommandStatusExpired, CommandStatusSuperseded:
		params.NotSent = true
		params.Retryable = false
		params.PartialDelivery = false
		params.EvidenceFinalized = true
		params.NeedsReconcile = false
	case CommandStatusFailed:
		confirmedNotSent := confirmedNotSentFailure(row, receipt, remoteID)
		if !confirmedNotSent {
			// The bridge may have sent the message before failing. Record the
			// outcome as unknown so no automatic path can resend it.
			params.Status = CommandStatusUnknown
			params.NotSent = false
			params.Retryable = false
			params.EvidenceFinalized = false
			params.NeedsReconcile = true
			if params.LastError.Valid {
				params.LastError.String = "POPO delivery outcome is uncertain: " + params.LastError.String
			} else {
				params.LastError = pgtype.Text{String: "POPO delivery outcome is uncertain", Valid: true}
			}
			break
		}
		params.NotSent = true
		params.Retryable = receipt.Retryable != nil && *receipt.Retryable
		params.PartialDelivery = false
		params.EvidenceFinalized = true
		params.NeedsReconcile = !params.Retryable
	}

	updated, err := q.RecordRequirementPopoCommandReceipt(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.PopoBridgeCommand{}, ErrReceiptConflict
	}
	if err != nil {
		return db.PopoBridgeCommand{}, err
	}
	if updated.Status == CommandStatusFailed && updated.Retryable && updated.NotSent && updated.EvidenceFinalized {
		if requeued, ok, err := s.requeueConfirmedFailedRequirement(ctx, q, updated); err != nil {
			return db.PopoBridgeCommand{}, err
		} else if ok {
			return requeued, nil
		}
	}
	return updated, nil
}

// confirmedNotSentFailure requires positive proof that nothing reached POPO. A
// command that may have left the bridge cannot be retried without a receipt
// that explicitly finalizes the not-sent evidence.
func confirmedNotSentFailure(row db.PopoBridgeCommand, receipt CommandReceipt, remoteID string) bool {
	if remoteID != "" || (receipt.PartialDelivery != nil && *receipt.PartialDelivery) {
		return false
	}
	if !row.DispatchedAt.Valid {
		return true
	}
	return receipt.NotSent != nil && *receipt.NotSent &&
		receipt.EvidenceFinalized != nil && *receipt.EvidenceFinalized
}

func (s *BridgeService) requeueConfirmedFailedRequirement(ctx context.Context, q *db.Queries, row db.PopoBridgeCommand) (db.PopoBridgeCommand, bool, error) {
	if !row.BridgeID.Valid || !row.PayloadHash.Valid || row.Attempt >= requirementOutboxMaxTransportTries {
		return db.PopoBridgeCommand{}, false, nil
	}
	expectedRequestHash := service.RequirementDeliveryRequestHash(service.RequirementDeliveryIdentityFromCommand(row))
	if !row.RequestHash.Valid || row.RequestHash.String != expectedRequestHash {
		return db.PopoBridgeCommand{}, false, nil
	}
	bridge, err := q.GetPopoBridgeInWorkspace(ctx, db.GetPopoBridgeInWorkspaceParams{ID: row.BridgeID, WorkspaceID: row.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.PopoBridgeCommand{}, false, nil
	}
	if err != nil {
		return db.PopoBridgeCommand{}, false, err
	}
	if !BridgeSupportsReceiptAttempt(bridge) {
		return db.PopoBridgeCommand{}, false, nil
	}
	requeued, err := q.RequeueConfirmedFailedRequirementPopoCommand(ctx, db.RequeueConfirmedFailedRequirementPopoCommandParams{
		ID: row.ID, WorkspaceID: row.WorkspaceID, Attempt: row.Attempt, PayloadHash: row.PayloadHash,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.PopoBridgeCommand{}, false, nil
	}
	if err != nil {
		return db.PopoBridgeCommand{}, false, err
	}
	return requeued, true, nil
}

func (s *BridgeService) applyReceiptEffects(ctx context.Context, q *db.Queries, row db.PopoBridgeCommand, status, remoteID, _ string) error {
	if status == CommandStatusDelivered {
		return s.recordOutboundLedger(ctx, q, row, remoteID)
	}
	return nil
}

func (s *BridgeService) recordOutboundLedger(ctx context.Context, q *db.Queries, row db.PopoBridgeCommand, remoteID string) error {
	if row.Type != CommandTypeSend || remoteID == "" || !row.InstallationID.Valid {
		return nil
	}
	var payload SendPayload
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		return err
	}
	if payload.BindingID == "" {
		return nil
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
	command, err := s.q.GetPopoBridgeCommandForBridge(ctx, db.GetPopoBridgeCommandForBridgeParams{ID: commandID, BridgeID: bridgeID})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !command.RequirementID.Valid {
		return s.q.CanDeliverPopoBridgeCommand(ctx, db.CanDeliverPopoBridgeCommandParams{ID: commandID, BridgeID: bridgeID})
	}
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	qtx := s.q.WithTx(tx)
	allowed, err := qtx.CanDeliverPopoBridgeCommand(ctx, db.CanDeliverPopoBridgeCommandParams{ID: commandID, BridgeID: bridgeID})
	if err != nil || !allowed {
		return allowed, err
	}
	locked, err := qtx.LockRequirementPopoCommandForDelivery(ctx, db.LockRequirementPopoCommandForDeliveryParams{ID: commandID, BridgeID: bridgeID})
	if err != nil {
		return false, err
	}
	if !locked.PayloadHash.Valid {
		// Prepared rows from the pre-single-ledger upgrade carry no content
		// hash. Adopt the payload the bridge would deliver once, then validate.
		adopted, adoptErr := qtx.SetRequirementPopoCommandPayloadHash(ctx, db.SetRequirementPopoCommandPayloadHashParams{
			ID: locked.ID, WorkspaceID: locked.WorkspaceID,
			PayloadHash: pgtype.Text{String: requirementPayloadHash(locked.Payload), Valid: true},
		})
		if adoptErr == nil {
			locked = adopted
		} else if !errors.Is(adoptErr, pgx.ErrNoRows) {
			return false, adoptErr
		}
	}
	bridge, err := qtx.GetPopoBridgeInWorkspace(ctx, db.GetPopoBridgeInWorkspaceParams{ID: locked.BridgeID, WorkspaceID: locked.WorkspaceID})
	if err != nil {
		return false, err
	}
	if locked.Attempt > 1 && !BridgeSupportsReceiptAttempt(bridge) {
		if _, fenceErr := fenceRequirementCommand(ctx, qtx, locked, errRequirementIdentity); fenceErr != nil {
			return false, fenceErr
		}
		return false, tx.Commit(ctx)
	}
	worker := NewRequirementOutboxWorker(s.q, s.tx, "", nil)
	if err := worker.validateCommand(ctx, qtx, locked); err != nil {
		if !errors.Is(err, errRequirementStale) && !errors.Is(err, errRequirementBacklog) &&
			!errors.Is(err, errRequirementTransport) && !errors.Is(err, errRequirementIdentity) {
			return false, err
		}
		if _, fenceErr := fenceRequirementCommand(ctx, qtx, locked, err); fenceErr != nil {
			return false, fenceErr
		}
		return false, tx.Commit(ctx)
	}
	return true, tx.Commit(ctx)
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
