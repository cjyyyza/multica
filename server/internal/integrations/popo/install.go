package popo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrInstallationNotFound       = errors.New("popo installation not found")
	ErrInvalidRobotID             = errors.New("popo: robot_id is required")
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
	GetChannelInstallationByAppID(ctx context.Context, arg db.GetChannelInstallationByAppIDParams) (db.ChannelInstallation, error)
	ListChannelInstallationsByWorkspace(ctx context.Context, arg db.ListChannelInstallationsByWorkspaceParams) ([]db.ChannelInstallation, error)
	GetChannelInstallationInWorkspace(ctx context.Context, arg db.GetChannelInstallationInWorkspaceParams) (db.ChannelInstallation, error)
	SetChannelInstallationStatus(ctx context.Context, arg db.SetChannelInstallationStatusParams) error
	GetPopoBridgeInWorkspace(ctx context.Context, arg db.GetPopoBridgeInWorkspaceParams) (db.PopoBridge, error)
}

type dbInstallQueries struct{ *db.Queries }

func (q dbInstallQueries) WithTx(tx pgx.Tx) installQueries {
	return dbInstallQueries{q.Queries.WithTx(tx)}
}

type InstallService struct {
	q   installQueries
	tx  engine.TxStarter
	now func() time.Time
}

func NewInstallService(q *db.Queries, tx engine.TxStarter) (*InstallService, error) {
	if q == nil {
		return nil, errors.New("popo: InstallService requires queries")
	}
	return newInstallService(dbInstallQueries{q}, tx)
}

func newInstallService(q installQueries, tx engine.TxStarter) (*InstallService, error) {
	if q == nil {
		return nil, errors.New("popo: InstallService requires queries")
	}
	if tx == nil {
		return nil, errors.New("popo: InstallService requires a tx starter")
	}
	return &InstallService{q: q, tx: tx, now: time.Now}, nil
}

type RegisterParams struct {
	WorkspaceID pgtype.UUID
	AgentID     pgtype.UUID
	InitiatorID pgtype.UUID
	BridgeID    pgtype.UUID
	RobotID     string
	RobotName   string
}

func (s *InstallService) Register(ctx context.Context, p RegisterParams) (db.ChannelInstallation, error) {
	if !p.BridgeID.Valid {
		return db.ChannelInstallation{}, ErrInvalidBridgeID
	}
	robotID, err := normalizeRobotID(p.RobotID)
	if err != nil {
		return db.ChannelInstallation{}, err
	}
	bridge, err := s.q.GetPopoBridgeInWorkspace(ctx, db.GetPopoBridgeInWorkspaceParams{
		ID:          p.BridgeID,
		WorkspaceID: p.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.ChannelInstallation{}, ErrBridgeNotFound
		}
		return db.ChannelInstallation{}, err
	}
	if bridge.Status != BridgeStatusActive {
		return db.ChannelInstallation{}, ErrBridgeRevoked
	}
	liveOwner, _ := s.q.GetChannelInstallationByAppID(ctx, db.GetChannelInstallationByAppIDParams{
		ChannelType: string(TypePopo),
		AppID:       robotID,
	})
	var liveOwnerAgent pgtype.UUID
	if liveOwner.Status == "active" && liveOwner.WorkspaceID == p.WorkspaceID {
		liveOwnerAgent = liveOwner.AgentID
	}
	if err := robotIdleOnHeartbeat(bridge, robotID, p.AgentID, liveOwnerAgent, s.now()); err != nil {
		return db.ChannelInstallation{}, err
	}
	cfg := installConfig{
		AppID:     robotID,
		RobotName: strings.TrimSpace(p.RobotName),
		BridgeID:  util.UUIDToString(p.BridgeID),
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
