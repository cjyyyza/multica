package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/popo"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func writePopoNotConfigured(w http.ResponseWriter) {
	writeErrorCode(w, http.StatusServiceUnavailable, "popo_not_configured", "popo integration not configured")
}

func popoBearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(header) < 7 || !strings.EqualFold(header[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(header[7:])
}

func (h *Handler) popoBridgeFromRequest(w http.ResponseWriter, r *http.Request) (db.PopoBridge, bool) {
	if h.PopoBridge == nil {
		writePopoNotConfigured(w)
		return db.PopoBridge{}, false
	}
	token := popoBearerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "invalid or revoked bridge token")
		return db.PopoBridge{}, false
	}
	bridge, err := h.PopoBridge.Authenticate(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid or revoked bridge token")
		return db.PopoBridge{}, false
	}
	return bridge, true
}

type CreatePopoBridgePairingRequest struct {
	Hostname string `json:"hostname"`
}

func (h *Handler) CreatePopoBridgePairing(w http.ResponseWriter, r *http.Request) {
	if h.PopoBridge == nil {
		writeFeatureDisabled(w, "popo_not_configured", "popo integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	createdBy, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	var body CreatePopoBridgePairingRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	pairing, err := h.PopoBridge.MintPairing(r.Context(), wsUUID, createdBy, body.Hostname)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create pairing code")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":           uuidToString(pairing.ID),
		"pairing_code": pairing.Code,
		"expires_at":   pairing.ExpiresAt.UTC().Format(time.RFC3339),
		"ttl_seconds":  int(popo.PairingTTL.Seconds()),
	})
}

type PopoBridgeResponse struct {
	ID              string             `json:"id"`
	Hostname        string             `json:"hostname"`
	Status          string             `json:"status"`
	Online          bool               `json:"online"`
	LastHeartbeatAt string             `json:"last_heartbeat_at"`
	Robots          []popo.RobotReport `json:"robots"`
	CreatedAt       string             `json:"created_at"`
}

func popoBridgeToResponse(row db.PopoBridge, now time.Time) PopoBridgeResponse {
	return PopoBridgeResponse{
		ID:              uuidToString(row.ID),
		Hostname:        row.Hostname,
		Status:          row.Status,
		Online:          popo.BridgeOnline(row, now),
		LastHeartbeatAt: formatPopoTime(row.LastHeartbeatAt),
		Robots:          popo.DecodeRobots(row.RobotsJson),
		CreatedAt:       formatPopoTime(row.CreatedAt),
	}
}

func formatPopoTime(ts pgtype.Timestamptz) string {
	if !ts.Valid {
		return ""
	}
	return ts.Time.UTC().Format(time.RFC3339)
}

func (h *Handler) ListPopoBridges(w http.ResponseWriter, r *http.Request) {
	if h.PopoBridge == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"bridges":    []PopoBridgeResponse{},
			"configured": false,
		})
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	rows, err := h.PopoBridge.List(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list popo bridges")
		return
	}
	now := time.Now()
	out := make([]PopoBridgeResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, popoBridgeToResponse(row, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bridges":    out,
		"configured": true,
	})
}

func (h *Handler) RevokePopoBridge(w http.ResponseWriter, r *http.Request) {
	if h.PopoBridge == nil {
		writeFeatureDisabled(w, "popo_not_configured", "popo integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	bridgeUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "bridgeId"), "bridge id")
	if !ok {
		return
	}
	revokedBy, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	if _, err := h.PopoBridge.GetInWorkspace(r.Context(), bridgeUUID, wsUUID); err != nil {
		if errors.Is(err, popo.ErrBridgeNotFound) {
			writeError(w, http.StatusNotFound, "popo bridge not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load bridge")
		return
	}
	if err := h.PopoBridge.Revoke(r.Context(), bridgeUUID, wsUUID, revokedBy); err != nil && !errors.Is(err, popo.ErrBridgeNotFound) {
		writeError(w, http.StatusInternalServerError, "failed to revoke bridge")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type registerPopoBridgeRequest struct {
	ProtocolVersion int      `json:"protocol_version"`
	PairingCode     string   `json:"pairing_code"`
	Hostname        string   `json:"hostname"`
	Capabilities    []string `json:"capabilities"`
}

func (h *Handler) RegisterPopoBridge(w http.ResponseWriter, r *http.Request) {
	if h.PopoBridge == nil {
		writePopoNotConfigured(w)
		return
	}
	var body registerPopoBridgeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	registered, err := h.PopoBridge.Register(r.Context(), popo.RegisterBridgeParams{
		ProtocolVersion: body.ProtocolVersion,
		PairingCode:     body.PairingCode,
		Hostname:        body.Hostname,
		Capabilities:    body.Capabilities,
	})
	if err != nil {
		switch {
		case errors.Is(err, popo.ErrUnknownProtocol):
			writeError(w, http.StatusBadRequest, "unknown protocol version")
		case errors.Is(err, popo.ErrPairingInvalid):
			writeError(w, http.StatusGone, "pairing code invalid or expired")
		default:
			writeError(w, http.StatusInternalServerError, "failed to register bridge")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"protocol_version":           popo.ProtocolVersion,
		"bridge_id":                  uuidToString(registered.Bridge.ID),
		"workspace_id":               uuidToString(registered.WorkspaceID),
		"token":                      registered.Token,
		"heartbeat_interval_seconds": int(popo.HeartbeatInterval.Seconds()),
	})
}

type popoHeartbeatRequest struct {
	ProtocolVersion int                `json:"protocol_version"`
	Robots          []popo.RobotReport `json:"robots"`
}

func (h *Handler) PopoBridgeHeartbeat(w http.ResponseWriter, r *http.Request) {
	bridge, ok := h.popoBridgeFromRequest(w, r)
	if !ok {
		return
	}
	var body popoHeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if _, err := h.PopoBridge.Heartbeat(r.Context(), bridge.ID, body.ProtocolVersion, body.Robots); err != nil {
		switch {
		case errors.Is(err, popo.ErrUnknownProtocol):
			writeError(w, http.StatusBadRequest, "unknown protocol version")
		case errors.Is(err, popo.ErrBridgeRevoked):
			writeError(w, http.StatusUnauthorized, "invalid or revoked bridge token")
		default:
			writeError(w, http.StatusInternalServerError, "failed to record heartbeat")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": false})
}

func (h *Handler) IngestPopoBridgeInbound(w http.ResponseWriter, r *http.Request) {
	bridge, ok := h.popoBridgeFromRequest(w, r)
	if !ok {
		return
	}
	var body popo.BridgeInbound
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	decision, err := h.PopoBridge.AcceptInbound(r.Context(), bridge, body)
	if err != nil {
		switch {
		case errors.Is(err, popo.ErrUnknownProtocol):
			writeError(w, http.StatusBadRequest, "unknown protocol version")
		case errors.Is(err, popo.ErrMissingEventID), errors.Is(err, popo.ErrMissingSender), errors.Is(err, popo.ErrMissingChat):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, popo.ErrInstallationWrong):
			writeError(w, http.StatusNotFound, "popo installation not found")
		default:
			writeError(w, http.StatusInternalServerError, "failed to ingest popo event")
		}
		return
	}
	if !decision.Accepted {
		writeJSON(w, http.StatusOK, map[string]any{"accepted": false})
		return
	}
	if h.ChannelRouter != nil && decision.Message.EventID != "" {
		if err := h.ChannelRouter.Handle(r.Context(), decision.Message); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to ingest popo event")
			return
		}
	}
	out := map[string]any{"accepted": true}
	if decision.Duplicate {
		out["duplicate"] = true
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) ListPopoBridgeCommands(w http.ResponseWriter, r *http.Request) {
	bridge, ok := h.popoBridgeFromRequest(w, r)
	if !ok {
		return
	}
	wait := popo.DefaultCommandWait
	if raw := strings.TrimSpace(r.URL.Query().Get("wait_ms")); raw != "" {
		ms, err := strconv.Atoi(raw)
		if err != nil || ms < 0 {
			writeError(w, http.StatusBadRequest, "invalid wait_ms")
			return
		}
		wait = time.Duration(ms) * time.Millisecond
		if wait > popo.MaxCommandWait {
			wait = popo.MaxCommandWait
		}
	}
	rows, err := h.PopoBridge.LeaseCommands(r.Context(), bridge.ID, wait)
	if err != nil {
		if errors.Is(err, r.Context().Err()) {
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to list commands")
		return
	}
	commands := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		payload := json.RawMessage(row.Payload)
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		commands = append(commands, map[string]any{
			"id":          uuidToString(row.ID),
			"type":        row.Type,
			"delivery_id": uuidToString(row.DeliveryID),
			"payload":     payload,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"commands": commands})
}

type popoCommandReceiptRequest struct {
	Status          string `json:"status"`
	RemoteMessageID string `json:"remote_message_id"`
	Error           string `json:"error"`
}

func (h *Handler) AckPopoBridgeCommand(w http.ResponseWriter, r *http.Request) {
	bridge, ok := h.popoBridgeFromRequest(w, r)
	if !ok {
		return
	}
	commandUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "command id")
	if !ok {
		return
	}
	var body popoCommandReceiptRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if _, err := h.PopoBridge.RecordReceipt(r.Context(), commandUUID, bridge.ID, popo.CommandReceipt{
		Status:          body.Status,
		RemoteMessageID: body.RemoteMessageID,
		Error:           body.Error,
	}); err != nil {
		switch {
		case errors.Is(err, popo.ErrInvalidReceipt):
			writeError(w, http.StatusBadRequest, "invalid command receipt")
		case errors.Is(err, popo.ErrCommandNotFound):
			writeError(w, http.StatusNotFound, "command not found")
		case errors.Is(err, popo.ErrReceiptConflict):
			writeError(w, http.StatusConflict, "command receipt conflicts with a previous result")
		default:
			writeError(w, http.StatusInternalServerError, "failed to record receipt")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
