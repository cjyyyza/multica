package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/popo"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

func wirePopo(t *testing.T) {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	install, err := popo.NewInstallService(testHandler.Queries, testPool)
	if err != nil {
		t.Fatalf("NewInstallService: %v", err)
	}
	bridge := popo.NewBridgeService(testHandler.Queries, testPool)
	binding := popo.NewBindingTokenService(testHandler.Queries, testPool)
	prevInstall, prevBridge, prevBind := testHandler.PopoInstall, testHandler.PopoBridge, testHandler.PopoBindingTokens
	testHandler.PopoInstall = install
	testHandler.PopoBridge = bridge
	testHandler.PopoBindingTokens = binding
	t.Cleanup(func() {
		testHandler.PopoInstall = prevInstall
		testHandler.PopoBridge = prevBridge
		testHandler.PopoBindingTokens = prevBind
	})
}

func popoCleanupBridge(t *testing.T, bridgeID string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM popo_inbound_event WHERE bridge_id = $1`, bridgeID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM popo_bridge_command WHERE bridge_id = $1`, bridgeID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM popo_bridge_pairing WHERE bridge_id = $1`, bridgeID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM popo_bridge WHERE id = $1`, bridgeID)
	})
}

func popoRegisterBridge(t *testing.T, hostname string) (bridgeID, token string) {
	t.Helper()
	wirePopo(t)
	req := withURLParam(newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/popo/bridge-pairings", map[string]string{
		"hostname": hostname,
	}), "id", testWorkspaceID)
	var pairing struct {
		ID          string `json:"id"`
		PairingCode string `json:"pairing_code"`
	}
	testutil.Call(t, testHandler.CreatePopoBridgePairing, req).Want(http.StatusOK).JSON(&pairing)
	if pairing.PairingCode == "" {
		t.Fatal("expected pairing_code once")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM popo_bridge_pairing WHERE id = $1`, pairing.ID)
	})

	regReq := testutil.JSONRequest(http.MethodPost, "/api/popo/bridge/register", map[string]any{
		"protocol_version": 1,
		"pairing_code":     pairing.PairingCode,
		"hostname":         hostname,
		"capabilities":     []string{"inbound", "send"},
	})
	var registered struct {
		BridgeID    string `json:"bridge_id"`
		Token       string `json:"token"`
		WorkspaceID string `json:"workspace_id"`
	}
	testutil.Call(t, testHandler.RegisterPopoBridge, regReq).Want(http.StatusOK).JSON(&registered)
	if registered.Token == "" || registered.BridgeID == "" || registered.WorkspaceID != testWorkspaceID {
		t.Fatalf("register = %+v", registered)
	}
	popoCleanupBridge(t, registered.BridgeID)
	return registered.BridgeID, registered.Token
}

func popoBearer(req *http.Request, token string) *http.Request {
	return testutil.WithHeaders(req, "Authorization", "Bearer "+token)
}

func popoHeartbeat(t *testing.T, token string, robots []map[string]any) {
	t.Helper()
	req := popoBearer(testutil.JSONRequest(http.MethodPost, "/api/popo/bridge/heartbeat", map[string]any{
		"protocol_version": 1,
		"robots":           robots,
	}), token)
	testutil.Call(t, testHandler.PopoBridgeHeartbeat, req).Want(http.StatusOK)
}

func popoIdleRobot(robotID string) []map[string]any {
	return []map[string]any{{
		"robot_id":     robotID,
		"display_name": "Support Bot",
		"connected":    true,
		"occupied_by":  nil,
	}}
}

func TestPopoPairingMintRedeemExpiryReuse(t *testing.T) {
	bridgeID, _ := popoRegisterBridge(t, "WIN-PAIR")
	if _, err := util.ParseUUID(bridgeID); err != nil {
		t.Fatalf("bridge id: %v", err)
	}

	req := withURLParam(newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/popo/bridge-pairings", map[string]string{}), "id", testWorkspaceID)
	var pairing struct {
		PairingCode string `json:"pairing_code"`
		ID          string `json:"id"`
	}
	testutil.Call(t, testHandler.CreatePopoBridgePairing, req).Want(http.StatusOK).JSON(&pairing)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM popo_bridge_pairing WHERE id = $1`, pairing.ID)
	})

	reuse := testutil.JSONRequest(http.MethodPost, "/api/popo/bridge/register", map[string]any{
		"protocol_version": 1,
		"pairing_code":     pairing.PairingCode,
		"hostname":         "WIN-REUSE",
	})
	var first struct {
		BridgeID string `json:"bridge_id"`
	}
	testutil.Call(t, testHandler.RegisterPopoBridge, reuse).Want(http.StatusOK).JSON(&first)
	popoCleanupBridge(t, first.BridgeID)

	testutil.Call(t, testHandler.RegisterPopoBridge, testutil.JSONRequest(http.MethodPost, "/api/popo/bridge/register", map[string]any{
		"protocol_version": 1,
		"pairing_code":     pairing.PairingCode,
		"hostname":         "WIN-REUSE",
	})).Want(http.StatusGone)

	expiredReq := withURLParam(newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/popo/bridge-pairings", map[string]string{}), "id", testWorkspaceID)
	var expired struct {
		ID          string `json:"id"`
		PairingCode string `json:"pairing_code"`
	}
	testutil.Call(t, testHandler.CreatePopoBridgePairing, expiredReq).Want(http.StatusOK).JSON(&expired)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM popo_bridge_pairing WHERE id = $1`, expired.ID)
	})
	dbfx.Exec(t, `UPDATE popo_bridge_pairing SET expires_at = now() - interval '1 minute' WHERE id = $1`, expired.ID)
	testutil.Call(t, testHandler.RegisterPopoBridge, testutil.JSONRequest(http.MethodPost, "/api/popo/bridge/register", map[string]any{
		"protocol_version": 1,
		"pairing_code":     expired.PairingCode,
	})).Want(http.StatusGone)

	testutil.Call(t, testHandler.RegisterPopoBridge, testutil.JSONRequest(http.MethodPost, "/api/popo/bridge/register", map[string]any{
		"protocol_version": 2,
		"pairing_code":     "ignored",
	})).Want(http.StatusBadRequest)
}

func TestPopoRevokedBridgeToken401sInbound(t *testing.T) {
	bridgeID, token := popoRegisterBridge(t, "WIN-REVOKED")
	req := withURLParams(newRequest(http.MethodDelete, "/api/workspaces/"+testWorkspaceID+"/popo/bridges/"+bridgeID, nil),
		"id", testWorkspaceID, "bridgeId", bridgeID)
	testutil.Call(t, testHandler.RevokePopoBridge, req).Want(http.StatusNoContent)

	inbound := popoBearer(testutil.JSONRequest(http.MethodPost, "/api/popo/bridge/inbound", map[string]any{
		"protocol_version": 1,
		"event_id":         "evt-revoked",
		"robot_id":         "default",
		"sender":           map[string]string{"id": "alice@corp.netease.com"},
		"chat":             map[string]string{"id": "alice@corp.netease.com", "type": "p2p"},
		"addressed_to_bot": true,
		"text":             "hello",
	}), token)
	testutil.Call(t, testHandler.IngestPopoBridgeInbound, inbound).Want(http.StatusUnauthorized)
}

func TestPopoInboundDuplicateAndWorkspaceIsolation(t *testing.T) {
	bridgeID, token := popoRegisterBridge(t, "WIN-INBOUND")
	robotID := "inbound-bot-" + bridgeID[:8]
	popoHeartbeat(t, token, popoIdleRobot(robotID))

	agentID := createHandlerTestAgent(t, "popo-inbound-agent", []byte(`{}`))
	installReq := withURLParam(newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/popo/install?agent_id="+agentID, map[string]string{
		"bridge_id": bridgeID,
		"robot_id":  robotID,
	}), "id", testWorkspaceID)
	testutil.Call(t, testHandler.RegisterPopoBot, installReq).Want(http.StatusOK)

	body := map[string]any{
		"protocol_version": 1,
		"event_id":         "evt-dup-" + robotID,
		"robot_id":         robotID,
		"sender":           map[string]string{"id": "alice@corp.netease.com", "name": "Alice"},
		"chat":             map[string]string{"id": "alice@corp.netease.com", "type": "p2p"},
		"addressed_to_bot": true,
		"text":             "hello",
		"command_text":     "hello",
	}
	first := testutil.Call(t, testHandler.IngestPopoBridgeInbound, popoBearer(testutil.JSONRequest(http.MethodPost, "/api/popo/bridge/inbound", body), token)).Want(http.StatusOK).Map()
	if first["accepted"] != true {
		t.Fatalf("first inbound = %+v", first)
	}
	second := testutil.Call(t, testHandler.IngestPopoBridgeInbound, popoBearer(testutil.JSONRequest(http.MethodPost, "/api/popo/bridge/inbound", body), token)).Want(http.StatusOK).Map()
	if second["accepted"] != true || second["duplicate"] != true {
		t.Fatalf("duplicate inbound = %+v", second)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM popo_inbound_event WHERE event_id = $1`, body["event_id"]); n != 1 {
		t.Fatalf("inbound rows = %d, want 1", n)
	}

	otherWS := dbfx.Workspace(t, "popo-other", "popo-other-"+bridgeID[:8])
	otherAgent := dbfx.Agent(t, "popo-other-agent", handlerTestRuntimeID(t), testutil.Cols{
		"workspace_id": otherWS,
		"visibility":   "workspace",
	})
	foreignRobot := "foreign-bot-" + bridgeID[:8]
	dbfx.Insert(t, "channel_installation", testutil.Cols{
		"workspace_id":      otherWS,
		"agent_id":          otherAgent,
		"channel_type":      "popo",
		"config":            []byte(`{"app_id":"` + foreignRobot + `"}`),
		"installer_user_id": testUserID,
		"status":            "active",
	})
	spoof := popoBearer(testutil.JSONRequest(http.MethodPost, "/api/popo/bridge/inbound", map[string]any{
		"protocol_version": 1,
		"event_id":         "evt-spoof",
		"robot_id":         foreignRobot,
		"sender":           map[string]string{"id": "alice@corp.netease.com"},
		"chat":             map[string]string{"id": "alice@corp.netease.com", "type": "p2p"},
		"addressed_to_bot": true,
		"text":             "hello",
	}), token)
	testutil.Call(t, testHandler.IngestPopoBridgeInbound, spoof).Want(http.StatusNotFound)

	group := popoBearer(testutil.JSONRequest(http.MethodPost, "/api/popo/bridge/inbound", map[string]any{
		"protocol_version": 1,
		"event_id":         "evt-group",
		"robot_id":         robotID,
		"sender":           map[string]string{"id": "alice@corp.netease.com"},
		"chat":             map[string]string{"id": "group-1", "type": "group"},
		"addressed_to_bot": true,
		"text":             "hello",
	}), token)
	dropped := testutil.Call(t, testHandler.IngestPopoBridgeInbound, group).Want(http.StatusOK).Map()
	if dropped["accepted"] != false {
		t.Fatalf("group inbound = %+v", dropped)
	}
}

func TestPopoCommandLeaseAndReceipt(t *testing.T) {
	bridgeID, token := popoRegisterBridge(t, "WIN-CMD")
	robotID := "cmd-bot-" + bridgeID[:8]
	popoHeartbeat(t, token, popoIdleRobot(robotID))
	agentID := createHandlerTestAgent(t, "popo-cmd-agent", []byte(`{}`))
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
		ChatType:       "p2p",
		RobotID:        robotID,
		Content:        "hello back",
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	listReq := popoBearer(httptest.NewRequest(http.MethodGet, "/api/popo/bridge/commands?wait_ms=0", nil), token)
	var listed struct {
		Commands []struct {
			ID         string          `json:"id"`
			Type       string          `json:"type"`
			DeliveryID string          `json:"delivery_id"`
			Payload    json.RawMessage `json:"payload"`
		} `json:"commands"`
	}
	testutil.Call(t, testHandler.ListPopoBridgeCommands, listReq).Want(http.StatusOK).JSON(&listed)
	if len(listed.Commands) != 1 || listed.Commands[0].Type != "send" {
		t.Fatalf("commands = %+v", listed.Commands)
	}
	commandID := listed.Commands[0].ID

	empty := testutil.Call(t, testHandler.ListPopoBridgeCommands, popoBearer(httptest.NewRequest(http.MethodGet, "/api/popo/bridge/commands?wait_ms=0", nil), token)).Want(http.StatusOK).Map()
	if cmds, _ := empty["commands"].([]any); len(cmds) != 0 {
		t.Fatalf("leased commands returned again: %+v", empty)
	}

	dbfx.Exec(t, `UPDATE popo_bridge_command SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, commandID)
	relisted := struct {
		Commands []struct {
			ID string `json:"id"`
		} `json:"commands"`
	}{}
	testutil.Call(t, testHandler.ListPopoBridgeCommands, popoBearer(httptest.NewRequest(http.MethodGet, "/api/popo/bridge/commands?wait_ms=0", nil), token)).Want(http.StatusOK).JSON(&relisted)
	if len(relisted.Commands) != 1 || relisted.Commands[0].ID != commandID {
		t.Fatalf("expired lease should return pending command: %+v", relisted)
	}

	receiptPath := "/api/popo/bridge/commands/" + commandID + "/receipt"
	ack := func(body map[string]string) *http.Request {
		req := popoBearer(newRequest(http.MethodPost, receiptPath, body), token)
		return withURLParams(req, "id", commandID)
	}
	testutil.Call(t, testHandler.AckPopoBridgeCommand, ack(map[string]string{
		"status":            "delivered",
		"remote_message_id": "popo-msg-1",
	})).Want(http.StatusOK)
	testutil.Call(t, testHandler.AckPopoBridgeCommand, ack(map[string]string{
		"status":            "delivered",
		"remote_message_id": "popo-msg-1",
	})).Want(http.StatusOK)
	testutil.Call(t, testHandler.AckPopoBridgeCommand, ack(map[string]string{
		"status": "failed",
		"error":  "nope",
	})).Want(http.StatusConflict)

	if err := testHandler.PopoBridge.Enqueue(context.Background(), popo.OutboundItem{
		WorkspaceID:    util.MustParseUUID(testWorkspaceID),
		InstallationID: util.MustParseUUID(installRow.ID),
		BridgeID:       util.MustParseUUID(bridgeID),
		ChatID:         "alice@corp.netease.com",
		RobotID:        robotID,
		Content:        "maybe delivered",
	}); err != nil {
		t.Fatalf("enqueue unknown: %v", err)
	}
	var unknownListed struct {
		Commands []struct {
			ID string `json:"id"`
		} `json:"commands"`
	}
	testutil.Call(t, testHandler.ListPopoBridgeCommands, popoBearer(httptest.NewRequest(http.MethodGet, "/api/popo/bridge/commands?wait_ms=0", nil), token)).Want(http.StatusOK).JSON(&unknownListed)
	if len(unknownListed.Commands) != 1 {
		t.Fatalf("unknown command list = %+v", unknownListed)
	}
	unknownID := unknownListed.Commands[0].ID
	unknownReq := popoBearer(withURLParams(newRequest(http.MethodPost, "/api/popo/bridge/commands/"+unknownID+"/receipt", map[string]string{
		"status": "unknown",
	}), "id", unknownID), token)
	testutil.Call(t, testHandler.AckPopoBridgeCommand, unknownReq).Want(http.StatusOK)
	dbfx.Exec(t, `UPDATE popo_bridge_command SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, unknownID)
	afterUnknown := testutil.Call(t, testHandler.ListPopoBridgeCommands, popoBearer(httptest.NewRequest(http.MethodGet, "/api/popo/bridge/commands?wait_ms=0", nil), token)).Want(http.StatusOK).Map()
	if cmds, _ := afterUnknown["commands"].([]any); len(cmds) != 0 {
		t.Fatalf("unknown receipt must not be auto-resent: %+v", afterUnknown)
	}
}

func TestPopoInstallRejectsOccupiedAndDuplicateOwner(t *testing.T) {
	bridgeID, token := popoRegisterBridge(t, "WIN-INSTALL")
	robotID := "install-bot-" + bridgeID[:8]
	popoHeartbeat(t, token, []map[string]any{{
		"robot_id":     robotID,
		"display_name": "Busy",
		"connected":    true,
		"occupied_by":  "dj01bot",
	}})
	agentA := createHandlerTestAgent(t, "popo-install-a", []byte(`{}`))
	installBody := map[string]string{"bridge_id": bridgeID, "robot_id": robotID}
	occupied := withURLParam(newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/popo/install?agent_id="+agentA, installBody), "id", testWorkspaceID)
	testutil.Call(t, testHandler.RegisterPopoBot, occupied).Want(http.StatusConflict)

	popoHeartbeat(t, token, []map[string]any{{
		"robot_id":     robotID,
		"display_name": "Held",
		"connected":    true,
		"occupied_by":  "multica",
	}})
	idleInstall := withURLParam(newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/popo/install?agent_id="+agentA, installBody), "id", testWorkspaceID)
	testutil.Call(t, testHandler.RegisterPopoBot, idleInstall).Want(http.StatusOK)

	agentB := createHandlerTestAgent(t, "popo-install-b", []byte(`{}`))
	dup := withURLParam(newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/popo/install?agent_id="+agentB, map[string]string{
		"bridge_id": bridgeID,
		"robot_id":  robotID,
	}), "id", testWorkspaceID)
	testutil.Call(t, testHandler.RegisterPopoBot, dup).Want(http.StatusConflict)
}

func TestListPopoBridgesHeartbeatOnline(t *testing.T) {
	bridgeID, token := popoRegisterBridge(t, "WIN-LIST")
	popoHeartbeat(t, token, popoIdleRobot("default"))
	req := withURLParam(newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/popo/bridges", nil), "id", testWorkspaceID)
	var listed struct {
		Configured bool `json:"configured"`
		Bridges    []struct {
			ID     string `json:"id"`
			Online bool   `json:"online"`
			Robots []struct {
				RobotID string `json:"robot_id"`
			} `json:"robots"`
		} `json:"bridges"`
	}
	testutil.Call(t, testHandler.ListPopoBridges, req).Want(http.StatusOK).JSON(&listed)
	if !listed.Configured {
		t.Fatal("expected configured")
	}
	found := false
	for _, b := range listed.Bridges {
		if b.ID == bridgeID {
			found = true
			if !b.Online || len(b.Robots) != 1 || b.Robots[0].RobotID != "default" {
				t.Fatalf("bridge = %+v", b)
			}
		}
	}
	if !found {
		t.Fatalf("bridge %s missing from %+v", bridgeID, listed.Bridges)
	}
}
