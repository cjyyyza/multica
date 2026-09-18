package popo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// These paths and JSON fields are copied from G:\DJ01Bot\dj01bot
// nanobot/webhook/service.py. Do not invent extra commands.

const (
	dj01botHealthPath   = "/webhook/health"
	dj01botOutboundPath = "/outbound"
	dj01botChannelPopo  = "popo_open"
)

// GatewayClient talks to a running dj01bot webhook (default 127.0.0.1:28792).
// Only the Windows CLI uses this. The Multica API process must not.
type GatewayClient struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

func (c *GatewayClient) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (c *GatewayClient) url(path string) string {
	return strings.TrimRight(strings.TrimSpace(c.BaseURL), "/") + path
}

// Health is GET /webhook/health → {"status":"ok"}.
func (c *GatewayClient) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(dj01botHealthPath), nil)
	if err != nil {
		return err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("dj01bot health: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("dj01bot health: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("dj01bot health: decode: %w", err)
	}
	if out.Status != "ok" {
		return fmt.Errorf("dj01bot health: unexpected status %q", out.Status)
	}
	return nil
}

// OutboundRequest is the inspected POST /outbound body.
type OutboundRequest struct {
	Content    string `json:"content"`
	Channel    string `json:"channel"`
	ChatID     string `json:"chat_id"`
	RobotID    string `json:"robot_id,omitempty"`
	SessionKey string `json:"session_key,omitempty"`
}

// Send posts a direct robot message. channel must be popo_open.
func (c *GatewayClient) Send(ctx context.Context, req OutboundRequest) error {
	req.Content = strings.TrimSpace(req.Content)
	req.Channel = strings.TrimSpace(req.Channel)
	req.ChatID = strings.TrimSpace(req.ChatID)
	if req.Content == "" || req.Channel == "" || req.ChatID == "" {
		return fmt.Errorf("dj01bot outbound: content, channel, and chat_id are required")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(dj01botOutboundPath), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(c.Token); token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.client().Do(httpReq)
	if err != nil {
		return fmt.Errorf("dj01bot outbound: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("dj01bot outbound: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
