package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/popo"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type PopoInstallationResponse struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	AgentID         string `json:"agent_id"`
	RobotID         string `json:"robot_id"`
	RobotName       string `json:"robot_name"`
	BridgeID        string `json:"bridge_id"`
	WebhookURL      string `json:"webhook_url"`
	InstallerUserID string `json:"installer_user_id"`
	Status          string `json:"status"`
	InstalledAt     string `json:"installed_at"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

func popoInstallationToResponse(row db.ChannelInstallation) PopoInstallationResponse {
	info := popo.DecodePublicConfig(row.Config)
	return PopoInstallationResponse{
		ID:              uuidToString(row.ID),
		WorkspaceID:     uuidToString(row.WorkspaceID),
		AgentID:         uuidToString(row.AgentID),
		RobotID:         info.RobotID,
		RobotName:       info.RobotName,
		BridgeID:        info.BridgeID,
		WebhookURL:      info.WebhookURL,
		InstallerUserID: uuidToString(row.InstallerUserID),
		Status:          row.Status,
		InstalledAt:     row.InstalledAt.Time.UTC().Format(time.RFC3339),
		CreatedAt:       row.CreatedAt.Time.UTC().Format(time.RFC3339),
		UpdatedAt:       row.UpdatedAt.Time.UTC().Format(time.RFC3339),
	}
}

func (h *Handler) ListPopoInstallations(w http.ResponseWriter, r *http.Request) {
	if h.PopoInstall == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"installations":     []PopoInstallationResponse{},
			"configured":        false,
			"install_supported": false,
		})
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	rows, err := h.PopoInstall.ListByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list popo installations")
		return
	}
	out := make([]PopoInstallationResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, popoInstallationToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installations":     out,
		"configured":        true,
		"install_supported": true,
	})
}

type RegisterPopoRequest struct {
	BridgeID  string `json:"bridge_id"`
	RobotID   string `json:"robot_id"`
	RobotName string `json:"robot_name"`
}

func (h *Handler) RegisterPopoBot(w http.ResponseWriter, r *http.Request) {
	if h.PopoInstall == nil {
		writeFeatureDisabled(w, "popo_not_configured", "popo integration not enabled")
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
	agentIDStr := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if agentIDStr == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, agentIDStr, "agent_id")
	if !ok {
		return
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found in this workspace")
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}
	initiatorUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	var body RegisterPopoRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	bridgeUUID, ok := parseUUIDOrBadRequest(w, body.BridgeID, "bridge_id")
	if !ok {
		return
	}
	row, err := h.PopoInstall.Register(r.Context(), popo.RegisterParams{
		WorkspaceID: wsUUID,
		AgentID:     agentUUID,
		InitiatorID: initiatorUUID,
		BridgeID:    bridgeUUID,
		RobotID:     body.RobotID,
		RobotName:   body.RobotName,
	})
	if err != nil {
		switch {
		case errors.Is(err, popo.ErrInvalidRobotID), errors.Is(err, popo.ErrInvalidBridgeID):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, popo.ErrBridgeNotFound):
			writeError(w, http.StatusBadRequest, "bridge not found in this workspace")
		case errors.Is(err, popo.ErrBridgeRevoked):
			writeError(w, http.StatusBadRequest, "bridge has been revoked")
		case errors.Is(err, popo.ErrRobotNotIdle):
			writeError(w, http.StatusConflict, "this POPO robot is not idle on a recent heartbeat")
		case errors.Is(err, popo.ErrRobotOccupied):
			writeError(w, http.StatusConflict, "this POPO robot is occupied")
		case errors.Is(err, popo.ErrBotOwnedBySameWorkspace):
			writeError(w, http.StatusConflict, "this POPO robot is already connected to another agent in this workspace — disconnect it there first")
		case errors.Is(err, popo.ErrBotOwnedByArchivedAgent):
			writeError(w, http.StatusConflict, "this POPO robot is connected to an archived agent — restore that agent or disconnect its bot first")
		case errors.Is(err, popo.ErrBotOwnedByAnotherWorkspace):
			writeError(w, http.StatusConflict, "this POPO robot is already connected to a different Multica workspace")
		default:
			writeError(w, http.StatusInternalServerError, "could not save this POPO bot")
		}
		return
	}
	h.publish(protocol.EventPopoInstallationCreated, uuidToString(row.WorkspaceID), "user", userID, map[string]any{
		"id": uuidToString(row.ID),
	})
	writeJSON(w, http.StatusOK, popoInstallationToResponse(row))
}

func (h *Handler) RevokePopoInstallation(w http.ResponseWriter, r *http.Request) {
	if h.PopoInstall == nil {
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
	instUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "installationId"), "installation id")
	if !ok {
		return
	}
	inst, err := h.PopoInstall.GetInWorkspace(r.Context(), instUUID, wsUUID)
	if err != nil {
		if errors.Is(err, popo.ErrInstallationNotFound) {
			writeError(w, http.StatusNotFound, "popo installation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load installation")
		return
	}
	agent, agentErr := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          inst.AgentID,
		WorkspaceID: wsUUID,
	})
	if agentErr != nil {
		if _, ok := h.requireWorkspaceRole(w, r, uuidToString(wsUUID), "popo installation not found", "owner", "admin"); !ok {
			return
		}
	} else if !h.canManageAgent(w, r, agent) {
		return
	}
	if err := h.PopoInstall.Revoke(r.Context(), instUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke installation")
		return
	}
	h.publish(protocol.EventPopoInstallationRevoked, uuidToString(wsUUID), "user", userID, map[string]any{
		"id": uuidToString(instUUID),
	})
	w.WriteHeader(http.StatusNoContent)
}

type RedeemPopoBindingTokenRequest struct {
	Token string `json:"token"`
}

func (h *Handler) RedeemPopoBindingToken(w http.ResponseWriter, r *http.Request) {
	if h.PopoBindingTokens == nil {
		writeFeatureDisabled(w, "popo_not_configured", "popo integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req RedeemPopoBindingTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	redeemed, err := h.PopoBindingTokens.RedeemAndBind(r.Context(), req.Token, userUUID)
	if err != nil {
		switch {
		case errors.Is(err, popo.ErrBindingTokenInvalid):
			writeError(w, http.StatusGone, "binding token invalid or expired")
		case errors.Is(err, popo.ErrBindingAlreadyAssigned):
			writeError(w, http.StatusConflict, "this POPO account is already bound to a different Multica user")
		case errors.Is(err, popo.ErrBindingNotWorkspaceMember):
			writeError(w, http.StatusForbidden, "binding refused (are you a workspace member?)")
		default:
			writeError(w, http.StatusInternalServerError, "failed to redeem token")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"workspace_id":    uuidToString(redeemed.WorkspaceID),
		"installation_id": uuidToString(redeemed.InstallationID),
		"popo_user_id":    redeemed.PopoUserID,
	})
}

type PopoRegistrationResponse struct {
	ID                  string `json:"id"`
	Status              string `json:"status"`
	QRURL               string `json:"qr_url"`
	RobotID             string `json:"robot_id"`
	InstallationID      string `json:"installation_id"`
	ErrorReason         string `json:"error_reason"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
}

func popoRegistrationToResponse(row db.PopoRegistration) PopoRegistrationResponse {
	return PopoRegistrationResponse{
		ID:                  uuidToString(row.ID),
		Status:              row.Status,
		QRURL:               row.QrUrl,
		RobotID:             row.RobotID,
		InstallationID:      uuidToString(row.InstallationID),
		ErrorReason:         row.ErrorReason,
		PollIntervalSeconds: popo.RegistrationPollIntervalSeconds,
	}
}

type createPopoRegistrationRequest struct {
	BridgeID string `json:"bridge_id"`
}

func (h *Handler) CreatePopoRegistration(w http.ResponseWriter, r *http.Request) {
	if h.PopoRegistration == nil {
		writeFeatureDisabled(w, "popo_not_configured", "popo integration not enabled")
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
	agentIDStr := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if agentIDStr == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, agentIDStr, "agent_id")
	if !ok {
		return
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found in this workspace")
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}
	initiatorUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	var body createPopoRegistrationRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var bridgeID pgtype.UUID
	if strings.TrimSpace(body.BridgeID) != "" {
		parsed, ok := parseUUIDOrBadRequest(w, body.BridgeID, "bridge_id")
		if !ok {
			return
		}
		bridgeID = parsed
	}
	row, err := h.PopoRegistration.Begin(r.Context(), popo.BeginRegistrationParams{
		WorkspaceID: wsUUID,
		AgentID:     agentUUID,
		InitiatorID: initiatorUUID,
		BridgeID:    bridgeID,
	})
	if err != nil {
		writePopoRegistrationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, popoRegistrationToResponse(row))
}

func (h *Handler) GetPopoRegistration(w http.ResponseWriter, r *http.Request) {
	row, ok := h.loadPopoRegistrationForCaller(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, popoRegistrationToResponse(row))
}

func (h *Handler) CancelPopoRegistration(w http.ResponseWriter, r *http.Request) {
	row, ok := h.loadPopoRegistrationForCaller(w, r)
	if !ok {
		return
	}
	if _, err := h.PopoRegistration.Cancel(r.Context(), row.ID, row.WorkspaceID); err != nil {
		writePopoRegistrationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) loadPopoRegistrationForCaller(w http.ResponseWriter, r *http.Request) (db.PopoRegistration, bool) {
	if h.PopoRegistration == nil {
		writeFeatureDisabled(w, "popo_not_configured", "popo integration not enabled")
		return db.PopoRegistration{}, false
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return db.PopoRegistration{}, false
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return db.PopoRegistration{}, false
	}
	regUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "registrationId"), "registration id")
	if !ok {
		return db.PopoRegistration{}, false
	}
	row, err := h.PopoRegistration.GetInWorkspace(r.Context(), regUUID, wsUUID)
	if err != nil {
		if errors.Is(err, popo.ErrRegistrationNotFound) {
			writeError(w, http.StatusNotFound, "registration not found")
			return db.PopoRegistration{}, false
		}
		writeError(w, http.StatusInternalServerError, "failed to load registration")
		return db.PopoRegistration{}, false
	}
	if uuidToString(row.InitiatorID) != userID {
		member, mErr := h.getWorkspaceMember(r.Context(), userID, uuidToString(wsUUID))
		if mErr != nil || !roleAllowed(member.Role, "owner", "admin") {
			writeError(w, http.StatusNotFound, "registration not found")
			return db.PopoRegistration{}, false
		}
	}
	return row, true
}

func writePopoRegistrationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, popo.ErrNoOnlineBridge):
		writeError(w, http.StatusConflict, "pair a Windows host first")
	case errors.Is(err, popo.ErrBridgeNotFound):
		writeError(w, http.StatusBadRequest, "bridge not found in this workspace")
	case errors.Is(err, popo.ErrBridgeRevoked):
		writeError(w, http.StatusBadRequest, "bridge has been revoked")
	case errors.Is(err, popo.ErrInvalidQRURL), errors.Is(err, popo.ErrInvalidProgress), errors.Is(err, popo.ErrInvalidRobotID):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, popo.ErrRegistrationNotFound):
		writeError(w, http.StatusNotFound, "registration not found")
	case errors.Is(err, popo.ErrBotOwnedBySameWorkspace):
		writeError(w, http.StatusConflict, "this POPO robot is already connected to another agent in this workspace — disconnect it there first")
	case errors.Is(err, popo.ErrBotOwnedByArchivedAgent):
		writeError(w, http.StatusConflict, "this POPO robot is connected to an archived agent — restore that agent or disconnect its bot first")
	case errors.Is(err, popo.ErrBotOwnedByAnotherWorkspace):
		writeError(w, http.StatusConflict, "this POPO robot is already connected to a different Multica workspace")
	default:
		writeError(w, http.StatusInternalServerError, "could not complete POPO registration")
	}
}
