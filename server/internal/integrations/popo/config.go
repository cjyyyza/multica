// Package popo is the POPO Open integration for the channel-agnostic engine.
//
// Transport is a workspace-scoped Windows bridge: Windows pairs with Multica,
// then POSTs inbound and long-polls send commands. The API process never
// opens POPO, popo-cli, or dj01bot.
package popo

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/util"
)

// TypePopo is the channel discriminator. Defined here so registering the
// platform never edits the channel core, mirroring TypeTelegram.
const TypePopo channel.Type = "popo"

const (
	defaultRobotID = "default"

	ProtocolVersion = 1

	PairingTTL               = 15 * time.Minute
	HeartbeatInterval        = 15 * time.Second
	BridgeOfflineAfter       = 45 * time.Second
	CommandLease             = 60 * time.Second
	DefaultCommandWait       = 25 * time.Second
	MaxCommandWait           = 30 * time.Second
	MaxLeaseCommands   int32 = 20

	BridgeStatusActive  = "active"
	BridgeStatusRevoked = "revoked"

	CommandTypeSend = "send"

	CommandStatusPending   = "pending"
	CommandStatusLeased    = "leased"
	CommandStatusDelivered = "delivered"
	CommandStatusFailed    = "failed"
	CommandStatusUnknown   = "unknown"
	CommandStatusCancelled = "cancelled"

	OccupiedByDJ01Bot = "dj01bot"
	OccupiedBySparse  = "sparse"
	OccupiedByMultica = "multica"
)

// installConfig is the JSON shape stored in channel_installation.config.
//
// app_id is the dj01bot robot id (websocketRobots[].id, or "default").
// bridge_id is the Windows bridge that owns the robot connection.
type installConfig struct {
	AppID     string `json:"app_id"`
	RobotName string `json:"robot_name,omitempty"`
	BridgeID  string `json:"bridge_id,omitempty"`
	// Legacy fields from the unpublished loopback-gateway install. Kept so
	// DecodePublicConfig can still render old rows; new installs omit them.
	WebhookURL            string `json:"webhook_url,omitempty"`
	WebhookTokenEncrypted string `json:"webhook_token_encrypted,omitempty"`
}

// PublicConfig is the non-secret subset of an installation config.
type PublicConfig struct {
	RobotID    string
	RobotName  string
	BridgeID   string
	WebhookURL string
}

// DecodePublicConfig extracts display-safe fields. A decode miss yields a
// zero value so the management list still renders the row.
func DecodePublicConfig(raw json.RawMessage) PublicConfig {
	var cfg installConfig
	_ = json.Unmarshal(raw, &cfg)
	return PublicConfig{
		RobotID:    cfg.AppID,
		RobotName:  cfg.RobotName,
		BridgeID:   strings.TrimSpace(cfg.BridgeID),
		WebhookURL: strings.TrimSpace(cfg.WebhookURL),
	}
}

func normalizeRobotID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return defaultRobotID, nil
	}
	return id, nil
}

func parseBridgeID(raw string) (ok bool, id string) {
	id = strings.TrimSpace(raw)
	if id == "" {
		return false, ""
	}
	if _, err := util.ParseUUID(id); err != nil {
		return false, ""
	}
	return true, id
}
