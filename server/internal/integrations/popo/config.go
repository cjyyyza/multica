// Package popo is the POPO Open integration for the channel-agnostic engine.
//
// Install is Telegram-style BYO (paste a dj01bot robot id + loopback webhook
// URL). Transport is yixiezuo-style: the Multica API never opens POPO,
// popo-cli, or dj01bot. A Windows-local `multica popo gateway` receives
// POPO Open event envelopes and POSTs replies to the inspected dj01bot
// surface `POST /outbound` with `channel=popo_open`.
package popo

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

// TypePopo is the channel discriminator. Defined here so registering the
// platform never edits the channel core, mirroring TypeTelegram.
const TypePopo channel.Type = "popo"

const (
	defaultWebhookURL = "http://127.0.0.1:28792"
	defaultRobotID    = "default"
)

// installConfig is the JSON shape stored in channel_installation.config.
//
// app_id is the dj01bot robot id (websocketRobots[].id, or "default" for
// the webhook robot). It fills the generic (channel_type, config->>'app_id')
// routing slot.
//
// webhook_token_encrypted is base64-encoded secretbox ciphertext. The API
// never uses it; only the Windows CLI reads the decrypted token to call
// loopback dj01bot.
type installConfig struct {
	AppID                 string `json:"app_id"`
	RobotName             string `json:"robot_name,omitempty"`
	WebhookURL            string `json:"webhook_url,omitempty"`
	WebhookTokenEncrypted string `json:"webhook_token_encrypted,omitempty"`
}

type credentials struct {
	RobotID      string
	RobotName    string
	WebhookURL   string
	WebhookToken string
}

// Decrypter turns stored ciphertext into plaintext. Tests inject nil
// (stored bytes are treated as plaintext).
type Decrypter func(ciphertext []byte) (plaintext []byte, err error)

func decodeCredentials(raw json.RawMessage, decrypt Decrypter) (credentials, error) {
	if len(raw) == 0 {
		return credentials{}, errors.New("popo: empty installation config")
	}
	var cfg installConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return credentials{}, fmt.Errorf("decode popo installation config: %w", err)
	}
	token, err := decryptToken(cfg.WebhookTokenEncrypted, decrypt)
	if err != nil {
		return credentials{}, fmt.Errorf("decrypt webhook token: %w", err)
	}
	webhookURL := strings.TrimSpace(cfg.WebhookURL)
	if webhookURL == "" {
		webhookURL = defaultWebhookURL
	}
	return credentials{
		RobotID:      cfg.AppID,
		RobotName:    cfg.RobotName,
		WebhookURL:   webhookURL,
		WebhookToken: token,
	}, nil
}

// PublicConfig is the non-secret subset of an installation config.
type PublicConfig struct {
	RobotID    string
	RobotName  string
	WebhookURL string
}

// DecodePublicConfig extracts display-safe fields. A decode miss yields a
// zero value so the management list still renders the row.
func DecodePublicConfig(raw json.RawMessage) PublicConfig {
	var cfg installConfig
	_ = json.Unmarshal(raw, &cfg)
	webhookURL := strings.TrimSpace(cfg.WebhookURL)
	if webhookURL == "" {
		webhookURL = defaultWebhookURL
	}
	return PublicConfig{RobotID: cfg.AppID, RobotName: cfg.RobotName, WebhookURL: webhookURL}
}

func decryptToken(enc string, decrypt Decrypter) (string, error) {
	if enc == "" {
		return "", nil
	}
	ciphertext, err := base64.StdEncoding.DecodeString(stripWhitespace(enc))
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	if decrypt == nil {
		return string(ciphertext), nil
	}
	plaintext, err := decrypt(ciphertext)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func stripWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func normalizeRobotID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return defaultRobotID, nil
	}
	return id, nil
}

func normalizeWebhookURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultWebhookURL, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", ErrInvalidWebhookURL
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", ErrInvalidWebhookURL
	}
	host := u.Hostname()
	if !isLoopbackHost(host) {
		return "", ErrWebhookNotLoopback
	}
	return strings.TrimRight(raw, "/"), nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
