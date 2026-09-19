package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestListPopoInstallationsNotConfiguredReturnsEmpty(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/x/popo/installations", nil)
	w := httptest.NewRecorder()

	h.ListPopoInstallations(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Installations    []any `json:"installations"`
		Configured       bool  `json:"configured"`
		InstallSupported bool  `json:"install_supported"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Configured || resp.InstallSupported || len(resp.Installations) != 0 {
		t.Fatalf("unexpected unconfigured response: %+v", resp)
	}
}

func TestPopoMutationHandlersRejectUnconfiguredDeployment(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
		run    func(*Handler, http.ResponseWriter, *http.Request)
	}{
		{
			name:   "register install",
			method: http.MethodPost,
			path:   "/api/workspaces/x/popo/install?agent_id=y",
			body:   `{"bridge_id":"11111111-1111-1111-1111-111111111111","robot_id":"default"}`,
			status: http.StatusForbidden,
			run:    (*Handler).RegisterPopoBot,
		},
		{
			name:   "revoke install",
			method: http.MethodDelete,
			path:   "/api/workspaces/x/popo/installations/y",
			status: http.StatusForbidden,
			run:    (*Handler).RevokePopoInstallation,
		},
		{
			name:   "redeem binding",
			method: http.MethodPost,
			path:   "/api/popo/binding/redeem",
			body:   `{"token":"placeholder"}`,
			status: http.StatusForbidden,
			run:    (*Handler).RedeemPopoBindingToken,
		},
		{
			name:   "pairing",
			method: http.MethodPost,
			path:   "/api/workspaces/x/popo/bridge-pairings",
			body:   `{}`,
			status: http.StatusForbidden,
			run:    (*Handler).CreatePopoBridgePairing,
		},
		{
			name:   "bridge register",
			method: http.MethodPost,
			path:   "/api/popo/bridge/register",
			body:   `{"protocol_version":1,"pairing_code":"x"}`,
			status: http.StatusServiceUnavailable,
			run:    (*Handler).RegisterPopoBridge,
		},
		{
			name:   "bridge inbound",
			method: http.MethodPost,
			path:   "/api/popo/bridge/inbound",
			body:   `{"protocol_version":1,"event_id":"e"}`,
			status: http.StatusServiceUnavailable,
			run:    (*Handler).IngestPopoBridgeInbound,
		},
		{
			name:   "bridge commands",
			method: http.MethodGet,
			path:   "/api/popo/bridge/commands",
			status: http.StatusServiceUnavailable,
			run:    (*Handler).ListPopoBridgeCommands,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{}
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			w := httptest.NewRecorder()
			tt.run(h, w, req)
			if w.Code != tt.status {
				t.Fatalf("expected %d, got %d body=%s", tt.status, w.Code, w.Body.String())
			}
			if tt.status == http.StatusServiceUnavailable && !strings.Contains(w.Body.String(), "popo_not_configured") {
				t.Fatalf("expected popo_not_configured: %s", w.Body.String())
			}
		})
	}
}

func TestPopoInstallationResponseNeverExposesStoredCredential(t *testing.T) {
	now := time.Date(2026, 9, 18, 16, 0, 0, 0, time.UTC)
	row := db.ChannelInstallation{
		ID:              parseUUID("11111111-1111-1111-1111-111111111111"),
		WorkspaceID:     parseUUID("22222222-2222-2222-2222-222222222222"),
		AgentID:         parseUUID("33333333-3333-3333-3333-333333333333"),
		InstallerUserID: parseUUID("44444444-4444-4444-4444-444444444444"),
		Status:          "active",
		Config: json.RawMessage(
			`{"app_id":"default","robot_name":"dj01","bridge_id":"55555555-5555-5555-5555-555555555555","webhook_token_encrypted":"ciphertext-sentinel"}`,
		),
		InstalledAt: pgtype.Timestamptz{Time: now, Valid: true},
		CreatedAt:   pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:   pgtype.Timestamptz{Time: now, Valid: true},
	}

	got := popoInstallationToResponse(row)
	if got.RobotID != "default" || got.BridgeID != "55555555-5555-5555-5555-555555555555" {
		t.Fatalf("public identity = %+v", got)
	}
	payload, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if strings.Contains(string(payload), "ciphertext-sentinel") ||
		strings.Contains(string(payload), "webhook_token") {
		t.Fatalf("management response exposed stored credential: %s", payload)
	}
}
