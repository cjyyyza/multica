package popo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

type RegisterQRPayload struct {
	RegistrationID string `json:"registration_id"`
	AgentID        string `json:"agent_id"`
	Env            string `json:"env"`
}

type CancelRegistrationPayload struct {
	RegistrationID string `json:"registration_id"`
}

type RegistrationProgress struct {
	QRURL       string
	RobotID     string
	RobotName   string
	ErrorReason string
}

type ProgressResult struct {
	Registration db.PopoRegistration
	Created      *db.ChannelInstallation
}

type RegistrationService struct {
	q        *db.Queries
	tx       engine.TxStarter
	installs *InstallService
	now      func() time.Time
}

func NewRegistrationService(q *db.Queries, tx engine.TxStarter, installs *InstallService) (*RegistrationService, error) {
	if q == nil {
		return nil, errors.New("popo: RegistrationService requires queries")
	}
	if tx == nil {
		return nil, errors.New("popo: RegistrationService requires a tx starter")
	}
	if installs == nil {
		return nil, errors.New("popo: RegistrationService requires InstallService")
	}
	return &RegistrationService{q: q, tx: tx, installs: installs, now: time.Now}, nil
}

type BeginRegistrationParams struct {
	WorkspaceID pgtype.UUID
	AgentID     pgtype.UUID
	InitiatorID pgtype.UUID
	BridgeID    pgtype.UUID
}

func (s *RegistrationService) Begin(ctx context.Context, p BeginRegistrationParams) (db.PopoRegistration, error) {
	now := s.now()
	bridges, err := s.q.ListPopoBridgesByWorkspace(ctx, p.WorkspaceID)
	if err != nil {
		return db.PopoRegistration{}, err
	}
	bridge, err := pickOnlineBridge(bridges, p.BridgeID, now)
	if err != nil {
		return db.PopoRegistration{}, err
	}

	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return db.PopoRegistration{}, fmt.Errorf("begin registration tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	row, err := qtx.InsertPopoRegistration(ctx, db.InsertPopoRegistrationParams{
		WorkspaceID: p.WorkspaceID,
		AgentID:     p.AgentID,
		InitiatorID: p.InitiatorID,
		BridgeID:    bridge.ID,
		ExpiresAt:   pgtype.Timestamptz{Time: now.Add(RegistrationTTL), Valid: true},
	})
	if err != nil {
		return db.PopoRegistration{}, fmt.Errorf("insert popo registration: %w", err)
	}
	payload, err := json.Marshal(RegisterQRPayload{
		RegistrationID: util.UUIDToString(row.ID),
		AgentID:        util.UUIDToString(p.AgentID),
		Env:            RegistrationEnvProduction,
	})
	if err != nil {
		return db.PopoRegistration{}, fmt.Errorf("encode register_qr payload: %w", err)
	}
	if _, err := qtx.EnqueuePopoBridgeCommand(ctx, db.EnqueuePopoBridgeCommandParams{
		WorkspaceID:    p.WorkspaceID,
		BridgeID:       bridge.ID,
		InstallationID: pgtype.UUID{},
		Type:           CommandTypeRegisterQR,
		DeliveryID:     dbid.NewV7(),
		Payload:        payload,
	}); err != nil {
		return db.PopoRegistration{}, fmt.Errorf("enqueue register_qr: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.PopoRegistration{}, fmt.Errorf("commit registration: %w", err)
	}
	return row, nil
}

func (s *RegistrationService) GetInWorkspace(ctx context.Context, id, workspaceID pgtype.UUID) (db.PopoRegistration, error) {
	if _, err := s.q.ExpirePopoRegistrationIfStale(ctx, db.ExpirePopoRegistrationIfStaleParams{
		ID:        id,
		ExpiresAt: pgtype.Timestamptz{Time: s.now(), Valid: true},
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return db.PopoRegistration{}, err
	}
	row, err := s.q.GetPopoRegistrationInWorkspace(ctx, db.GetPopoRegistrationInWorkspaceParams{
		ID:          id,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.PopoRegistration{}, ErrRegistrationNotFound
		}
		return db.PopoRegistration{}, err
	}
	return row, nil
}

func (s *RegistrationService) Cancel(ctx context.Context, id, workspaceID pgtype.UUID) (db.PopoRegistration, error) {
	row, err := s.GetInWorkspace(ctx, id, workspaceID)
	if err != nil {
		return db.PopoRegistration{}, err
	}
	if !registrationOpen(row.Status) {
		return row, nil
	}
	updated, err := s.q.UpdatePopoRegistration(ctx, db.UpdatePopoRegistrationParams{
		Status:         RegistrationStatusExpired,
		QrUrl:          row.QrUrl,
		RobotID:        row.RobotID,
		RobotName:      row.RobotName,
		InstallationID: row.InstallationID,
		ErrorReason:    row.ErrorReason,
		ID:             row.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return s.q.GetPopoRegistration(ctx, id)
		}
		return db.PopoRegistration{}, err
	}
	s.enqueueCancel(ctx, updated)
	return updated, nil
}

func (s *RegistrationService) enqueueCancel(ctx context.Context, row db.PopoRegistration) {
	payload, err := json.Marshal(CancelRegistrationPayload{
		RegistrationID: util.UUIDToString(row.ID),
	})
	if err != nil {
		return
	}
	_, _ = s.q.EnqueuePopoBridgeCommand(ctx, db.EnqueuePopoBridgeCommandParams{
		WorkspaceID:    row.WorkspaceID,
		BridgeID:       row.BridgeID,
		InstallationID: pgtype.UUID{},
		Type:           CommandTypeCancelRegistration,
		DeliveryID:     dbid.NewV7(),
		Payload:        payload,
	})
}

func (s *RegistrationService) ApplyProgress(ctx context.Context, bridge db.PopoBridge, id pgtype.UUID, progress RegistrationProgress) (ProgressResult, error) {
	progress = sanitizeProgress(progress)
	if progress.QRURL == "" && progress.RobotID == "" && progress.ErrorReason == "" {
		return ProgressResult{}, ErrInvalidProgress
	}
	if progress.QRURL != "" {
		if err := validateQRURL(progress.QRURL); err != nil {
			return ProgressResult{}, err
		}
	}

	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return ProgressResult{}, fmt.Errorf("begin progress tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	row, err := qtx.GetPopoRegistrationForUpdate(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProgressResult{}, ErrRegistrationNotFound
		}
		return ProgressResult{}, err
	}
	if !uuidEqual(row.WorkspaceID, bridge.WorkspaceID) || !uuidEqual(row.BridgeID, bridge.ID) {
		return ProgressResult{}, ErrRegistrationNotFound
	}
	now := s.now()
	if registrationOpen(row.Status) && row.ExpiresAt.Valid && !row.ExpiresAt.Time.After(now) {
		expired, err := qtx.ExpirePopoRegistrationIfStale(ctx, db.ExpirePopoRegistrationIfStaleParams{
			ID:        row.ID,
			ExpiresAt: pgtype.Timestamptz{Time: now, Valid: true},
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return ProgressResult{}, err
		}
		if err == nil {
			row = expired
		}
	}
	if !registrationOpen(row.Status) {
		if err := tx.Commit(ctx); err != nil {
			return ProgressResult{}, err
		}
		return ProgressResult{Registration: row}, nil
	}

	next := row
	var created *db.ChannelInstallation
	switch {
	case progress.ErrorReason != "":
		next.Status = RegistrationStatusError
		next.ErrorReason = progress.ErrorReason
	case progress.RobotID != "":
		inst, err := s.installs.RegisterFromScan(ctx, RegisterParams{
			WorkspaceID: row.WorkspaceID,
			AgentID:     row.AgentID,
			InitiatorID: row.InitiatorID,
			BridgeID:    row.BridgeID,
			RobotID:     progress.RobotID,
			RobotName:   progress.RobotName,
		})
		if err != nil {
			next.Status = RegistrationStatusError
			next.ErrorReason = registrationErrorReason(err)
			updated, uerr := qtx.UpdatePopoRegistration(ctx, updateFrom(next))
			if uerr != nil {
				return ProgressResult{}, uerr
			}
			if err := tx.Commit(ctx); err != nil {
				return ProgressResult{}, err
			}
			return ProgressResult{Registration: updated}, err
		}
		next.Status = RegistrationStatusSuccess
		next.RobotID = strings.TrimSpace(progress.RobotID)
		next.RobotName = strings.TrimSpace(progress.RobotName)
		next.InstallationID = inst.ID
		created = &inst
	default:
		next.Status = RegistrationStatusAwaitingScan
		next.QrUrl = progress.QRURL
	}

	updated, err := qtx.UpdatePopoRegistration(ctx, updateFrom(next))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.Commit(ctx); err != nil {
				return ProgressResult{}, err
			}
			return ProgressResult{Registration: row}, nil
		}
		return ProgressResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProgressResult{}, err
	}
	return ProgressResult{Registration: updated, Created: created}, nil
}

func updateFrom(row db.PopoRegistration) db.UpdatePopoRegistrationParams {
	return db.UpdatePopoRegistrationParams{
		Status:         row.Status,
		QrUrl:          row.QrUrl,
		RobotID:        row.RobotID,
		RobotName:      row.RobotName,
		InstallationID: row.InstallationID,
		ErrorReason:    row.ErrorReason,
		ID:             row.ID,
	}
}

func registrationOpen(status string) bool {
	return status == RegistrationStatusPending || status == RegistrationStatusAwaitingScan
}

func registrationErrorReason(err error) string {
	switch {
	case errors.Is(err, ErrBotOwnedBySameWorkspace),
		errors.Is(err, ErrBotOwnedByAnotherWorkspace),
		errors.Is(err, ErrBotOwnedByArchivedAgent):
		return RegistrationReasonConflict
	default:
		return RegistrationReasonInternal
	}
}

func sanitizeProgress(p RegistrationProgress) RegistrationProgress {
	p.QRURL = strings.TrimSpace(p.QRURL)
	p.RobotID = strings.TrimSpace(p.RobotID)
	p.RobotName = strings.TrimSpace(p.RobotName)
	p.ErrorReason = strings.TrimSpace(p.ErrorReason)
	return p
}

func validateQRURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ErrInvalidQRURL
	}
	return nil
}

func pickOnlineBridge(bridges []db.PopoBridge, preferred pgtype.UUID, now time.Time) (db.PopoBridge, error) {
	if preferred.Valid {
		var found db.PopoBridge
		exists := false
		for _, b := range bridges {
			if uuidEqual(b.ID, preferred) {
				found = b
				exists = true
				break
			}
		}
		if !exists {
			return db.PopoBridge{}, ErrBridgeNotFound
		}
		if found.Status != BridgeStatusActive {
			return db.PopoBridge{}, ErrBridgeRevoked
		}
		if !BridgeOnline(found, now) {
			return db.PopoBridge{}, ErrNoOnlineBridge
		}
		return found, nil
	}
	var best db.PopoBridge
	found := false
	for _, b := range bridges {
		if !BridgeOnline(b, now) {
			continue
		}
		if !found || b.LastHeartbeatAt.Time.After(best.LastHeartbeatAt.Time) {
			best = b
			found = true
		}
	}
	if !found {
		return db.PopoBridge{}, ErrNoOnlineBridge
	}
	return best, nil
}
