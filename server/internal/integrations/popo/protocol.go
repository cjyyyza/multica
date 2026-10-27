package popo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrInvalidProtocolVersions = errors.New("popo: invalid supported_protocol_versions")
	ErrInvalidProtocolHealth   = errors.New("popo: invalid protocol health")
)

type HeartbeatRequest struct {
	ProtocolVersion           int
	Robots                    []RobotReport
	SupportedProtocolVersions *[]int
	Health                    map[string]any
}

func (r *HeartbeatRequest) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, ok := fields["supported_protocol_versions"]; ok {
		var versions []int
		if err := json.Unmarshal(raw, &versions); err != nil {
			return ErrInvalidProtocolVersions
		}
	}
	var base struct {
		ProtocolVersion           int           `json:"protocol_version"`
		Robots                    []RobotReport `json:"robots"`
		SupportedProtocolVersions *[]int        `json:"supported_protocol_versions"`
	}
	if err := json.Unmarshal(data, &base); err != nil {
		return err
	}
	var health map[string]any
	if err := json.Unmarshal(data, &health); err != nil {
		return err
	}
	delete(health, "protocol_version")
	delete(health, "robots")
	delete(health, "supported_protocol_versions")
	r.ProtocolVersion = base.ProtocolVersion
	r.Robots = base.Robots
	r.SupportedProtocolVersions = base.SupportedProtocolVersions
	r.Health = health
	return nil
}

// BridgeProtocolHealth is the bridge-visible transport health snapshot. The
// single command transport only reports the counters it actually owns.
type BridgeProtocolHealth struct {
	Pending             int64
	Unknown             int64
	Quarantine          int64
	ReceiptPending      int64
	V2Pending           int64
	BinaryDowngradeSafe bool
}

type HeartbeatResult struct {
	Bridge db.PopoBridge
	Health BridgeProtocolHealth
}

func (s *BridgeService) Heartbeat(ctx context.Context, bridgeID pgtype.UUID, request HeartbeatRequest) (HeartbeatResult, error) {
	if request.ProtocolVersion != ProtocolVersion {
		return HeartbeatResult{}, ErrUnknownProtocol
	}
	bridge, err := s.q.GetPopoBridgeByID(ctx, bridgeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return HeartbeatResult{}, ErrBridgeRevoked
		}
		return HeartbeatResult{}, err
	}
	supported, selected, err := s.negotiateProtocolVersions(ctx, bridge, request.SupportedProtocolVersions)
	if err != nil {
		return HeartbeatResult{}, err
	}
	if request.Robots == nil {
		request.Robots = []RobotReport{}
	}
	robots, err := json.Marshal(request.Robots)
	if err != nil {
		return HeartbeatResult{}, fmt.Errorf("encode robots: %w", err)
	}
	supportedJSON, _ := json.Marshal(supported)
	health := BridgeProtocolHealth{BinaryDowngradeSafe: true}
	healthJSON := []byte(`{}`)
	updated, err := s.q.HeartbeatPopoBridge(ctx, db.HeartbeatPopoBridgeParams{
		ID: bridgeID, RobotsJson: robots, SupportedProtocolVersions: supportedJSON,
		SelectedProtocolVersion: int32(selected), ProtocolHealthJson: healthJSON,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return HeartbeatResult{}, ErrBridgeRevoked
		}
		return HeartbeatResult{}, err
	}
	return HeartbeatResult{Bridge: updated, Health: health}, nil
}

func (s *BridgeService) SelectBridgeProtocol(ctx context.Context, bridgeID pgtype.UUID, declared *[]int) (db.PopoBridge, error) {
	bridge, err := s.q.GetPopoBridgeByID(ctx, bridgeID)
	if err != nil {
		return db.PopoBridge{}, err
	}
	versions, selected, err := s.negotiateProtocolVersions(ctx, bridge, declared)
	if err != nil {
		return db.PopoBridge{}, err
	}
	versionsJSON, _ := json.Marshal(versions)
	updated, err := s.q.UpdatePopoBridgeProtocolSelection(ctx, db.UpdatePopoBridgeProtocolSelectionParams{
		ID: bridgeID, SupportedProtocolVersions: versionsJSON, SelectedProtocolVersion: int32(selected),
	})
	if err != nil {
		return db.PopoBridge{}, err
	}
	return updated, nil
}

// negotiateProtocolVersions keeps the declared-versions handshake for wire
// compatibility. The server owns a single command transport, so version 1 is
// always selected even when a bridge advertises a newer version.
func (s *BridgeService) negotiateProtocolVersions(_ context.Context, bridge db.PopoBridge, declared *[]int) ([]int, int, error) {
	versions := decodeSupportedProtocolVersions(bridge.SupportedProtocolVersions)
	if declared != nil {
		var err error
		versions, err = normalizeProtocolVersions(*declared)
		if err != nil {
			return nil, 0, err
		}
	}
	return versions, 1, nil
}

func normalizeProtocolVersions(raw []int) ([]int, error) {
	if len(raw) == 0 {
		return nil, ErrInvalidProtocolVersions
	}
	seen := map[int]bool{}
	versions := make([]int, 0, len(raw))
	for _, version := range raw {
		if version != 1 && version != 2 {
			return nil, ErrInvalidProtocolVersions
		}
		if seen[version] {
			continue
		}
		seen[version] = true
		versions = append(versions, version)
	}
	if !seen[1] {
		return nil, ErrInvalidProtocolVersions
	}
	sort.Ints(versions)
	return versions, nil
}

func decodeSupportedProtocolVersions(raw []byte) []int {
	var versions []int
	if json.Unmarshal(raw, &versions) != nil {
		return []int{1}
	}
	normalized, err := normalizeProtocolVersions(versions)
	if err != nil {
		return []int{1}
	}
	return normalized
}

func ParseProtocolVersionCSV(raw string) ([]int, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, ErrInvalidProtocolVersions
	}
	parts := strings.Split(raw, ",")
	versions := make([]int, 0, len(parts))
	for _, part := range parts {
		version, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, ErrInvalidProtocolVersions
		}
		versions = append(versions, version)
	}
	return normalizeProtocolVersions(versions)
}
