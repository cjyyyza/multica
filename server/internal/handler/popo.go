package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

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
