package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/popo"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

func TestPopoStatusCountsPendingUnknownAndRuntime(t *testing.T) {
	bridgeID, token := popoRegisterBridge(t, "WIN-P4-STATUS")
	robotID := "p4-bot-" + bridgeID[:8]
	popoHeartbeat(t, token, popoIdleRobot(robotID))

	agentID := createHandlerTestAgent(t, "popo-p4-status-agent", []byte(`{}`))
	installReq := withURLParam(newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/popo/install?agent_id="+agentID, map[string]string{
		"bridge_id": bridgeID,
		"robot_id":  robotID,
	}), "id", testWorkspaceID)
	var installRow struct {
		ID string `json:"id"`
	}
	testutil.Call(t, testHandler.RegisterPopoBot, installReq).Want(http.StatusOK).JSON(&installRow)

	if err := testHandler.PopoBridge.Enqueue(context.Background(), popo.OutboundItem{
		WorkspaceID:    util.MustParseUUID(testWorkspaceID),
		InstallationID: util.MustParseUUID(installRow.ID),
		BridgeID:       util.MustParseUUID(bridgeID),
		ChatID:         "alice@corp.netease.com",
		RobotID:        robotID,
		Content:        "pending-one",
	}); err != nil {
		t.Fatalf("enqueue pending: %v", err)
	}
	if err := testHandler.PopoBridge.Enqueue(context.Background(), popo.OutboundItem{
		WorkspaceID:    util.MustParseUUID(testWorkspaceID),
		InstallationID: util.MustParseUUID(installRow.ID),
		BridgeID:       util.MustParseUUID(bridgeID),
		ChatID:         "alice@corp.netease.com",
		RobotID:        robotID,
		Content:        "to-unknown",
	}); err != nil {
		t.Fatalf("enqueue unknown candidate: %v", err)
	}

	listReq := popoBearer(httptest.NewRequest(http.MethodGet, "/api/popo/bridge/commands?wait_ms=0", nil), token)
	var listed struct {
		Commands []struct {
			ID string `json:"id"`
		} `json:"commands"`
	}
	testutil.Call(t, testHandler.ListPopoBridgeCommands, listReq).Want(http.StatusOK).JSON(&listed)
	if len(listed.Commands) != 2 {
		t.Fatalf("leased commands = %+v", listed)
	}
	unknownReq := popoBearer(withURLParams(newRequest(http.MethodPost, "/api/popo/bridge/commands/"+listed.Commands[0].ID+"/receipt", map[string]string{
		"status": "unknown",
	}), "id", listed.Commands[0].ID), token)
	testutil.Call(t, testHandler.AckPopoBridgeCommand, unknownReq).Want(http.StatusOK)

	if err := testHandler.PopoBridge.Enqueue(context.Background(), popo.OutboundItem{
		WorkspaceID:    util.MustParseUUID(testWorkspaceID),
		InstallationID: util.MustParseUUID(installRow.ID),
		BridgeID:       util.MustParseUUID(bridgeID),
		ChatID:         "alice@corp.netease.com",
		RobotID:        robotID,
		Content:        "still-pending",
	}); err != nil {
		t.Fatalf("enqueue leftover pending: %v", err)
	}

	create := popoBearer(testutil.JSONRequest(http.MethodPost, "/api/popo/bridge/media/sessions", map[string]any{
		"event_id":   "evt-p4-media",
		"index":      0,
		"filename":   "a.png",
		"mime_type":  "image/png",
		"size_bytes": 4,
		"kind":       "image",
		"robot_id":   robotID,
	}), token)
	testutil.Call(t, testHandler.CreatePopoBridgeMediaSession, create).Want(http.StatusOK)

	statusReq := withURLParam(newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/popo/status", nil), "id", testWorkspaceID)
	var status struct {
		Configured      bool `json:"configured"`
		ProtocolVersion int  `json:"protocol_version"`
		RuntimeOnline   bool `json:"runtime_online"`
		Bridges         []struct {
			ID                string `json:"id"`
			Online            bool   `json:"online"`
			PopoConnected     bool   `json:"popo_connected"`
			InboundBacklog    int64  `json:"inbound_backlog"`
			OutboundBacklog   int64  `json:"outbound_backlog"`
			UnknownDeliveries int64  `json:"unknown_deliveries"`
		} `json:"bridges"`
	}
	testutil.Call(t, testHandler.GetPopoStatus, statusReq).Want(http.StatusOK).JSON(&status)
	if !status.Configured || status.ProtocolVersion != 1 {
		t.Fatalf("status envelope = %+v", status)
	}
	if !status.RuntimeOnline {
		t.Fatal("expected runtime_online for a POPO-bound agent on an online runtime")
	}
	found := false
	for _, b := range status.Bridges {
		if b.ID != bridgeID {
			continue
		}
		found = true
		if !b.Online || !b.PopoConnected {
			t.Fatalf("bridge liveness = %+v", b)
		}
		if b.InboundBacklog != 1 {
			t.Fatalf("inbound_backlog = %d, want 1 pending media session", b.InboundBacklog)
		}
		if b.OutboundBacklog != 2 {
			t.Fatalf("outbound_backlog = %d, want pending+leased send commands", b.OutboundBacklog)
		}
		if b.UnknownDeliveries != 1 {
			t.Fatalf("unknown_deliveries = %d, want 1", b.UnknownDeliveries)
		}
	}
	if !found {
		t.Fatalf("bridge %s missing from %+v", bridgeID, status.Bridges)
	}
}

func TestPopoRevokeCancelsPendingCommands(t *testing.T) {
	bridgeID, token := popoRegisterBridge(t, "WIN-P4-REVOKE")
	robotID := "p4-revoke-" + bridgeID[:8]
	popoHeartbeat(t, token, popoIdleRobot(robotID))
	agentID := createHandlerTestAgent(t, "popo-p4-revoke-agent", []byte(`{}`))
	installReq := withURLParam(newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/popo/install?agent_id="+agentID, map[string]string{
		"bridge_id": bridgeID,
		"robot_id":  robotID,
	}), "id", testWorkspaceID)
	var installRow struct {
		ID string `json:"id"`
	}
	testutil.Call(t, testHandler.RegisterPopoBot, installReq).Want(http.StatusOK).JSON(&installRow)

	if err := testHandler.PopoBridge.Enqueue(context.Background(), popo.OutboundItem{
		WorkspaceID:    util.MustParseUUID(testWorkspaceID),
		InstallationID: util.MustParseUUID(installRow.ID),
		BridgeID:       util.MustParseUUID(bridgeID),
		ChatID:         "alice@corp.netease.com",
		RobotID:        robotID,
		Content:        "do-not-lease-after-revoke",
	}); err != nil {
		t.Fatalf("enqueue pending: %v", err)
	}
	if err := testHandler.PopoBridge.Enqueue(context.Background(), popo.OutboundItem{
		WorkspaceID:    util.MustParseUUID(testWorkspaceID),
		InstallationID: util.MustParseUUID(installRow.ID),
		BridgeID:       util.MustParseUUID(bridgeID),
		ChatID:         "alice@corp.netease.com",
		RobotID:        robotID,
		Content:        "leased-then-revoked",
	}); err != nil {
		t.Fatalf("enqueue leased: %v", err)
	}
	listReq := popoBearer(httptest.NewRequest(http.MethodGet, "/api/popo/bridge/commands?wait_ms=0", nil), token)
	var listed struct {
		Commands []struct {
			ID string `json:"id"`
		} `json:"commands"`
	}
	testutil.Call(t, testHandler.ListPopoBridgeCommands, listReq).Want(http.StatusOK).JSON(&listed)
	if len(listed.Commands) != 2 {
		t.Fatalf("commands before revoke = %+v", listed)
	}

	revoke := withURLParams(newRequest(http.MethodDelete, "/api/workspaces/"+testWorkspaceID+"/popo/bridges/"+bridgeID, nil),
		"id", testWorkspaceID, "bridgeId", bridgeID)
	testutil.Call(t, testHandler.RevokePopoBridge, revoke).Want(http.StatusNoContent)

	var cancelled int
	dbfx.QueryRow(t, `SELECT count(*) FROM popo_bridge_command WHERE bridge_id = $1 AND status = 'cancelled'`, bridgeID).Scan(&cancelled)
	if cancelled != 2 {
		t.Fatalf("cancelled commands = %d, want 2", cancelled)
	}
	var open int
	dbfx.QueryRow(t, `SELECT count(*) FROM popo_bridge_command WHERE bridge_id = $1 AND status IN ('pending', 'leased')`, bridgeID).Scan(&open)
	if open != 0 {
		t.Fatalf("open commands after revoke = %d", open)
	}

	after := popoBearer(httptest.NewRequest(http.MethodGet, "/api/popo/bridge/commands?wait_ms=0", nil), token)
	testutil.Call(t, testHandler.ListPopoBridgeCommands, after).Want(http.StatusUnauthorized)
}
