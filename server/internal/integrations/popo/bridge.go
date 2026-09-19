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

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrUnknownProtocol          = errors.New("popo: unknown protocol version")
	ErrPairingInvalid           = errors.New("popo: pairing code invalid, expired, or already used")
	ErrBridgeNotFound           = errors.New("popo: bridge not found")
	ErrBridgeRevoked            = errors.New("popo: bridge token revoked")
	ErrInvalidBridgeID          = errors.New("popo: bridge_id is required")
	ErrRobotNotIdle             = errors.New("popo: robot is not idle on a recent heartbeat")
	ErrRobotOccupied            = errors.New("popo: robot is occupied")
	ErrCommandNotFound          = errors.New("popo: command not found")
	ErrReceiptConflict          = errors.New("popo: command receipt conflicts with a previous result")
	ErrInvalidReceipt           = errors.New("popo: invalid command receipt")
	ErrMissingEventID           = errors.New("popo: event_id is required")
	ErrMissingSender            = errors.New("popo: sender.id is required")
	ErrMissingChat              = errors.New("popo: chat.id is required")
	ErrInstallationWrong        = errors.New("popo: robot is not installed in this workspace")
	ErrMediaSessionNotFound     = errors.New("popo: media session not found")
	ErrMediaSessionExpired      = errors.New("popo: media session expired")
	ErrMediaTooLarge            = errors.New("popo: media exceeds 20 MiB")
	ErrMediaStorageUnavailable  = errors.New("popo: media storage is not configured")
	ErrMediaInvalid             = errors.New("popo: invalid media session request")
	ErrOutboundMediaDenied      = errors.New("popo: outbound media is not granted to this bridge")
	ErrMediaContentTypeMismatch = errors.New("popo: media content type does not match the session")
	ErrNoOnlineBridge           = errors.New("popo: pair a Windows host first")
	ErrRegistrationNotFound     = errors.New("popo: registration not found")
	ErrInvalidQRURL             = errors.New("popo: qr_url must be an http(s) URL")
	ErrInvalidProgress          = errors.New("popo: qr_url, robot_id, or error_reason is required")
)

type RobotReport struct {
	RobotID     string  `json:"robot_id"`
	DisplayName string  `json:"display_name"`
	Connected   bool    `json:"connected"`
	OccupiedBy  *string `json:"occupied_by"`
}

type SendPayload struct {
	RobotID          string           `json:"robot_id"`
	ChatID           string           `json:"chat_id"`
	ChatType         string           `json:"chat_type"`
	Text             string           `json:"text"`
	ReplyToMessageID *string          `json:"reply_to_message_id"`
	IssueID          string           `json:"issue_id,omitempty"`
	CommentID        string           `json:"comment_id,omitempty"`
	TaskID           string           `json:"task_id,omitempty"`
	BindingID        string           `json:"binding_id,omitempty"`
	RouteRevision    int64            `json:"route_revision,omitempty"`
	OutboundKind     string           `json:"outbound_kind,omitempty"`
	Attachments      []SendAttachment `json:"attachments,omitempty"`
}

// SendAttachment is one file the Windows bridge should fetch and deliver.
// download_path is a bridge-token URL, never a Windows local path.
type SendAttachment struct {
	AttachmentID string `json:"attachment_id"`
	Filename     string `json:"filename"`
	MimeType     string `json:"mime_type"`
	DownloadPath string `json:"download_path"`
}

type mediaStorage interface {
	Upload(ctx context.Context, key string, data []byte, contentType string, filename string) (string, error)
	ObjectURL(key string) string
}

type BridgeService struct {
	q       *db.Queries
	tx      engine.TxStarter
	now     func() time.Time
	storage mediaStorage
	ledger  engine.MediaIntentLedger
}

func NewBridgeService(q *db.Queries, tx engine.TxStarter) *BridgeService {
	return &BridgeService{q: q, tx: tx, now: time.Now}
}

// WithMedia enables staging PUT (intent ledger then object storage).
func (s *BridgeService) WithMedia(store mediaStorage, ledger engine.MediaIntentLedger) *BridgeService {
	if s == nil {
		return s
	}
	s.storage = store
	s.ledger = ledger
	return s
}

type PairingResult struct {
	ID        pgtype.UUID
	Code      string
	ExpiresAt time.Time
}

func (s *BridgeService) MintPairing(ctx context.Context, workspaceID, createdBy pgtype.UUID, hostname string) (PairingResult, error) {
	raw, err := randomBindingToken(32)
	if err != nil {
		return PairingResult{}, fmt.Errorf("generate pairing code: %w", err)
	}
	expiresAt := s.now().Add(PairingTTL)
	row, err := s.q.CreatePopoBridgePairing(ctx, db.CreatePopoBridgePairingParams{
		WorkspaceID: workspaceID,
		CodeHash:    hashBindingToken(raw),
		CreatedBy:   createdBy,
		Hostname:    strings.TrimSpace(hostname),
		ExpiresAt:   pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return PairingResult{}, fmt.Errorf("persist pairing: %w", err)
	}
	return PairingResult{ID: row.ID, Code: raw, ExpiresAt: expiresAt}, nil
}

type RegisterBridgeParams struct {
	ProtocolVersion int
	PairingCode     string
	Hostname        string
	Capabilities    []string
}

type RegisteredBridge struct {
	Bridge      db.PopoBridge
	Token       string
	WorkspaceID pgtype.UUID
}

func (s *BridgeService) Register(ctx context.Context, p RegisterBridgeParams) (RegisteredBridge, error) {
	if p.ProtocolVersion != ProtocolVersion {
		return RegisteredBridge{}, ErrUnknownProtocol
	}
	code := strings.TrimSpace(p.PairingCode)
	if code == "" {
		return RegisteredBridge{}, ErrPairingInvalid
	}
	token, err := randomBindingToken(32)
	if err != nil {
		return RegisteredBridge{}, fmt.Errorf("generate bridge token: %w", err)
	}
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return RegisteredBridge{}, fmt.Errorf("begin register tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	pairing, err := qtx.GetPopoBridgePairingByCodeHashForUpdate(ctx, hashBindingToken(code))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RegisteredBridge{}, ErrPairingInvalid
		}
		return RegisteredBridge{}, fmt.Errorf("load pairing: %w", err)
	}
	if pairing.ConsumedAt.Valid || !pairing.ExpiresAt.Valid || !pairing.ExpiresAt.Time.After(s.now()) {
		return RegisteredBridge{}, ErrPairingInvalid
	}

	hostname := strings.TrimSpace(p.Hostname)
	if hostname == "" {
		hostname = pairing.Hostname
	}
	bridge, err := qtx.InsertPopoBridge(ctx, db.InsertPopoBridgeParams{
		WorkspaceID: pairing.WorkspaceID,
		TokenHash:   hashBindingToken(token),
		Hostname:    hostname,
	})
	if err != nil {
		return RegisteredBridge{}, fmt.Errorf("insert bridge: %w", err)
	}
	if _, err := qtx.ConsumePopoBridgePairing(ctx, db.ConsumePopoBridgePairingParams{
		ID:       pairing.ID,
		BridgeID: bridge.ID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RegisteredBridge{}, ErrPairingInvalid
		}
		return RegisteredBridge{}, fmt.Errorf("consume pairing: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RegisteredBridge{}, fmt.Errorf("commit register: %w", err)
	}
	return RegisteredBridge{Bridge: bridge, Token: token, WorkspaceID: bridge.WorkspaceID}, nil
}

func (s *BridgeService) Authenticate(ctx context.Context, rawToken string) (db.PopoBridge, error) {
	token := strings.TrimSpace(rawToken)
	if token == "" {
		return db.PopoBridge{}, ErrBridgeRevoked
	}
	bridge, err := s.q.GetPopoBridgeByTokenHash(ctx, hashBindingToken(token))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.PopoBridge{}, ErrBridgeRevoked
		}
		return db.PopoBridge{}, err
	}
	if bridge.Status != BridgeStatusActive {
		return db.PopoBridge{}, ErrBridgeRevoked
	}
	return bridge, nil
}

func (s *BridgeService) Heartbeat(ctx context.Context, bridgeID pgtype.UUID, protocolVersion int, robots []RobotReport) (db.PopoBridge, error) {
	if protocolVersion != ProtocolVersion {
		return db.PopoBridge{}, ErrUnknownProtocol
	}
	if robots == nil {
		robots = []RobotReport{}
	}
	payload, err := json.Marshal(robots)
	if err != nil {
		return db.PopoBridge{}, fmt.Errorf("encode robots: %w", err)
	}
	row, err := s.q.HeartbeatPopoBridge(ctx, db.HeartbeatPopoBridgeParams{
		ID:         bridgeID,
		RobotsJson: payload,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.PopoBridge{}, ErrBridgeRevoked
		}
		return db.PopoBridge{}, err
	}
	return row, nil
}

func (s *BridgeService) List(ctx context.Context, workspaceID pgtype.UUID) ([]db.PopoBridge, error) {
	return s.q.ListPopoBridgesByWorkspace(ctx, workspaceID)
}

func (s *BridgeService) GetInWorkspace(ctx context.Context, id, workspaceID pgtype.UUID) (db.PopoBridge, error) {
	row, err := s.q.GetPopoBridgeInWorkspace(ctx, db.GetPopoBridgeInWorkspaceParams{
		ID:          id,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.PopoBridge{}, ErrBridgeNotFound
		}
		return db.PopoBridge{}, err
	}
	return row, nil
}

func (s *BridgeService) Revoke(ctx context.Context, id, workspaceID, revokedBy pgtype.UUID) error {
	_, err := s.q.RevokePopoBridge(ctx, db.RevokePopoBridgeParams{
		ID:          id,
		WorkspaceID: workspaceID,
		RevokedBy:   revokedBy,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBridgeNotFound
		}
		return err
	}
	return nil
}

func BridgeOnline(bridge db.PopoBridge, now time.Time) bool {
	if bridge.Status != BridgeStatusActive || !bridge.LastHeartbeatAt.Valid {
		return false
	}
	return now.Sub(bridge.LastHeartbeatAt.Time) <= BridgeOfflineAfter
}

func DecodeRobots(raw []byte) []RobotReport {
	if len(raw) == 0 {
		return []RobotReport{}
	}
	var robots []RobotReport
	if err := json.Unmarshal(raw, &robots); err != nil || robots == nil {
		return []RobotReport{}
	}
	return robots
}

func robotIdleOnHeartbeat(bridge db.PopoBridge, robotID string, agentID pgtype.UUID, liveOwnerAgent pgtype.UUID, now time.Time) error {
	if !BridgeOnline(bridge, now) {
		return ErrRobotNotIdle
	}
	for _, robot := range DecodeRobots(bridge.RobotsJson) {
		if strings.TrimSpace(robot.RobotID) != robotID {
			continue
		}
		if !robot.Connected {
			return ErrRobotNotIdle
		}
		occupied := strings.TrimSpace(occupiedByValue(robot.OccupiedBy))
		if occupied == "" || occupied == OccupiedByMultica {
			// This Multica bridge holds the websocket. That is not an
			// agent binding; uniqueness still rejects another live owner.
			if occupied == OccupiedByMultica && liveOwnerAgent.Valid && !uuidEqual(liveOwnerAgent, agentID) {
				return ErrRobotOccupied
			}
			return nil
		}
		return ErrRobotOccupied
	}
	return ErrRobotNotIdle
}

func occupiedByValue(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func uuidEqual(a, b pgtype.UUID) bool {
	return a.Valid && b.Valid && a.Bytes == b.Bytes
}
