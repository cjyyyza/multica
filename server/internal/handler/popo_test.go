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
		run    func(*Handler, http.ResponseWriter, *http.Request)
	}{
		{
			name:   "register",
			method: http.MethodPost,
			path:   "/api/workspaces/x/popo/install?agent_id=y",
			body:   `{"robot_id":"default"}`,
			run:    (*Handler).RegisterPopoBot,
		},
		{
			name:   "revoke",
			method: http.MethodDelete,
			path:   "/api/workspaces/x/popo/installations/y",
			run:    (*Handler).RevokePopoInstallation,
		},
		{
			name:   "redeem binding",
			method: http.MethodPost,
			path:   "/api/popo/binding/redeem",
			body:   `{"token":"placeholder"}`,
			run:    (*Handler).RedeemPopoBindingToken,
		},
		{
			name:   "ingest",
			method: http.MethodPost,
			path:   "/api/workspaces/x/popo/inbound",
			body:   `{"robot_id":"default","event":{}}`,
			run:    (*Handler).IngestPopoEvent,
		},
		{
			name:   "list outbound",
			method: http.MethodGet,
			path:   "/api/workspaces/x/popo/outbound",
			run:    (*Handler).ListPopoOutbound,
		},
		{
			name:   "ack outbound",
			method: http.MethodPost,
			path:   "/api/workspaces/x/popo/outbound-ack",
			body:   `{"results":[]}`,
			run:    (*Handler).AckPopoOutbound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{}
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			w := httptest.NewRecorder()
			tt.run(h, w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d body=%s", w.Code, w.Body.String())
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
			`{"app_id":"default","robot_name":"dj01","webhook_url":"http://127.0.0.1:28792","webhook_token_encrypted":"ciphertext-sentinel"}`,
		),
		InstalledAt: pgtype.Timestamptz{Time: now, Valid: true},
		CreatedAt:   pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:   pgtype.Timestamptz{Time: now, Valid: true},
	}

	got := popoInstallationToResponse(row)
	if got.RobotID != "default" || got.WebhookURL != "http://127.0.0.1:28792" {
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
