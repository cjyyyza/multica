package popo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrInstallationNotFound       = errors.New("popo installation not found")
	ErrInvalidRobotID             = errors.New("popo: robot_id is required")
	ErrInvalidWebhookURL          = errors.New("popo: webhook_url must be an http(s) URL")
	ErrWebhookNotLoopback         = errors.New("popo: webhook_url must be a loopback address; the server never calls dj01bot")
	ErrBotOwnedByAnotherWorkspace = errors.New("popo: this robot is already connected to a different Multica workspace")
	ErrBotOwnedBySameWorkspace    = errors.New("popo: this robot is already connected to another agent in this workspace")
	ErrBotOwnedByArchivedAgent    = errors.New("popo: this robot is connected to an archived agent in this workspace")
)

const pgUniqueViolation = "23505"

type installQueries interface {
	WithTx(tx pgx.Tx) installQueries
	UpsertChannelInstallation(ctx context.Context, arg db.UpsertChannelInstallationParams) (db.ChannelInstallation, error)
	ReclaimDeadChannelInstallationByAppID(ctx context.Context, arg db.ReclaimDeadChannelInstallationByAppIDParams) (pgtype.UUID, error)
	GetChannelInstallationOwnerByAppID(ctx context.Context, arg db.GetChannelInstallationOwnerByAppIDParams) (db.GetChannelInstallationOwnerByAppIDRow, error)
	ListChannelInstallationsByWorkspace(ctx context.Context, arg db.ListChannelInstallationsByWorkspaceParams) ([]db.ChannelInstallation, error)
	GetChannelInstallationInWorkspace(ctx context.Context, arg db.GetChannelInstallationInWorkspaceParams) (db.ChannelInstallation, error)
	SetChannelInstallationStatus(ctx context.Context, arg db.SetChannelInstallationStatusParams) error
}

type dbInstallQueries struct{ *db.Queries }

func (q dbInstallQueries) WithTx(tx pgx.Tx) installQueries {
	return dbInstallQueries{q.Queries.WithTx(tx)}
}

// InstallService owns at-rest encryption of the optional webhook token.
// The box MUST be non-nil. Register does not call dj01bot.
type InstallService struct {
	box *secretbox.Box
	q   installQueries
	tx  engine.TxStarter
}

func NewInstallService(q *db.Queries, tx engine.TxStarter, box *secretbox.Box) (*InstallService, error) {
	if q == nil {
		return nil, errors.New("popo: InstallService requires queries")
	}
	return newInstallService(dbInstallQueries{q}, tx, box)
}

func newInstallService(q installQueries, tx engine.TxStarter, box *secretbox.Box) (*InstallService, error) {
	if box == nil {
		return nil, errors.New("popo: InstallService requires a non-nil secretbox.Box")
	}
	if q == nil {
		return nil, errors.New("popo: InstallService requires queries")
	}
	if tx == nil {
		return nil, errors.New("popo: InstallService requires a tx starter")
	}
	return &InstallService{box: box, q: q, tx: tx}, nil
}

type RegisterParams struct {
	WorkspaceID  pgtype.UUID
	AgentID      pgtype.UUID
	InitiatorID  pgtype.UUID
	RobotID      string
	RobotName    string
	WebhookURL   string
	WebhookToken string
}

func (s *InstallService) Register(ctx context.Context, p RegisterParams) (db.ChannelInstallation, error) {
	robotID, err := normalizeRobotID(p.RobotID)
	if err != nil {
		return db.ChannelInstallation{}, err
	}
	webhookURL, err := normalizeWebhookURL(p.WebhookURL)
	if err != nil {
		return db.ChannelInstallation{}, err
	}
	cfg := installConfig{AppID: robotID, RobotName: p.RobotName, WebhookURL: webhookURL}
	if token := strings.TrimSpace(p.WebhookToken); token != "" {
		sealed, err := s.box.Seal([]byte(token))
		if err != nil {
			return db.ChannelInstallation{}, fmt.Errorf("encrypt popo webhook token: %w", err)
		}
		cfg.WebhookTokenEncrypted = base64.StdEncoding.EncodeToString(sealed)
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("encode popo installation config: %w", err)
	}
	return s.persistInstall(ctx, installPersist{
		wsID:        p.WorkspaceID,
		agentID:     p.AgentID,
		installerID: p.InitiatorID,
		appIDKey:    robotID,
		configJSON:  cfgJSON,
	})
}

type installPersist struct {
	wsID        pgtype.UUID
	agentID     pgtype.UUID
	installerID pgtype.UUID
	appIDKey    string
	configJSON  []byte
}

func (s *InstallService) persistInstall(ctx context.Context, p installPersist) (db.ChannelInstallation, error) {
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("begin install tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	if _, err := qtx.ReclaimDeadChannelInstallationByAppID(ctx, db.ReclaimDeadChannelInstallationByAppIDParams{
		ChannelType: string(TypePopo),
		AppID:       p.appIDKey,
		WorkspaceID: p.wsID,
		AgentID:     p.agentID,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return db.ChannelInstallation{}, fmt.Errorf("reclaim dead popo installation: %w", err)
	}

	inst, err := qtx.UpsertChannelInstallation(ctx, db.UpsertChannelInstallationParams{
		WorkspaceID:     p.wsID,
		AgentID:         p.agentID,
		ChannelType:     string(TypePopo),
		Config:          p.configJSON,
		InstallerUserID: p.installerID,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return db.ChannelInstallation{}, s.liveOwnerConflictErr(ctx, p.wsID, p.appIDKey)
		}
		return db.ChannelInstallation{}, fmt.Errorf("upsert popo installation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("commit popo install: %w", err)
	}
	return inst, nil
}

func (s *InstallService) liveOwnerConflictErr(ctx context.Context, requestingWorkspaceID pgtype.UUID, appID string) error {
	owner, err := s.q.GetChannelInstallationOwnerByAppID(ctx, db.GetChannelInstallationOwnerByAppIDParams{
		ChannelType: string(TypePopo),
		AppID:       appID,
	})
	if err != nil {
		return ErrBotOwnedByAnotherWorkspace
	}
	switch {
	case owner.WorkspaceID != requestingWorkspaceID:
		return ErrBotOwnedByAnotherWorkspace
	case owner.AgentArchivedAt.Valid:
		return ErrBotOwnedByArchivedAgent
	default:
		return ErrBotOwnedBySameWorkspace
	}
}

func (s *InstallService) ListByWorkspace(ctx context.Context, wsID pgtype.UUID) ([]db.ChannelInstallation, error) {
	return s.q.ListChannelInstallationsByWorkspace(ctx, db.ListChannelInstallationsByWorkspaceParams{
		WorkspaceID: wsID,
		ChannelType: string(TypePopo),
	})
}

func (s *InstallService) GetInWorkspace(ctx context.Context, id, wsID pgtype.UUID) (db.ChannelInstallation, error) {
	inst, err := s.q.GetChannelInstallationInWorkspace(ctx, db.GetChannelInstallationInWorkspaceParams{
		ID:          id,
		WorkspaceID: wsID,
		ChannelType: string(TypePopo),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.ChannelInstallation{}, ErrInstallationNotFound
		}
		return db.ChannelInstallation{}, err
	}
	return inst, nil
}

func (s *InstallService) Revoke(ctx context.Context, id pgtype.UUID) error {
	return s.q.SetChannelInstallationStatus(ctx, db.SetChannelInstallationStatusParams{
		ID:     id,
		Status: "revoked",
	})
}

func (s *InstallService) ActiveInWorkspaceByRobot(ctx context.Context, wsID pgtype.UUID, robotID string) (db.ChannelInstallation, error) {
	robotID, err := normalizeRobotID(robotID)
	if err != nil {
		return db.ChannelInstallation{}, err
	}
	rows, err := s.ListByWorkspace(ctx, wsID)
	if err != nil {
		return db.ChannelInstallation{}, err
	}
	for _, row := range rows {
		if row.Status == "active" && DecodePublicConfig(row.Config).RobotID == robotID {
			return row, nil
		}
	}
	return db.ChannelInstallation{}, ErrInstallationNotFound
}
