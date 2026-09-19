package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func popoBeginRegistration(t *testing.T, agentID, bridgeID string) (id string) {
	t.Helper()
	body := map[string]string{}
	if bridgeID != "" {
		body["bridge_id"] = bridgeID
	}
	req := withURLParam(newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/popo/registrations?agent_id="+agentID, body), "id", testWorkspaceID)
	var row struct {
		ID                  string `json:"id"`
		Status              string `json:"status"`
		PollIntervalSeconds int    `json:"poll_interval_seconds"`
	}
	testutil.Call(t, testHandler.CreatePopoRegistration, req).Want(http.StatusOK).JSON(&row)
	if row.ID == "" || row.Status != "pending" || row.PollIntervalSeconds != 2 {
		t.Fatalf("begin = %+v", row)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_installation WHERE id IN (SELECT installation_id FROM popo_registration WHERE id = $1)`, row.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM popo_registration WHERE id = $1`, row.ID)
	})
	return row.ID
}

func popoGetRegistration(t *testing.T, registrationID string, want int) map[string]any {
	t.Helper()
	req := withURLParams(httptestGetPopoRegistration(registrationID), "id", testWorkspaceID, "registrationId", registrationID)
	return testutil.Call(t, testHandler.GetPopoRegistration, req).Want(want).Map()
}

func httptestGetPopoRegistration(registrationID string) *http.Request {
	return newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/popo/registrations/"+registrationID, nil)
}

func popoProgress(t *testing.T, token, registrationID string, body map[string]any, want int) map[string]any {
	t.Helper()
	req := popoBearer(testutil.JSONRequest(http.MethodPost, "/api/popo/bridge/registrations/"+registrationID+"/progress", body), token)
	req = withURLParams(req, "id", registrationID)
	return testutil.Call(t, testHandler.ProgressPopoRegistration, req).Want(want).Map()
}

func TestPopoRegistrationBeginRequiresOnlineBridge(t *testing.T) {
	bridgeID, _ := popoRegisterBridge(t, "WIN-QR-OFFLINE")
	agentID := createHandlerTestAgent(t, "popo-qr-offline", []byte(`{}`))
	req := withURLParam(newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/popo/registrations?agent_id="+agentID, map[string]string{
		"bridge_id": bridgeID,
	}), "id", testWorkspaceID)
	res := testutil.Call(t, testHandler.CreatePopoRegistration, req).Want(http.StatusConflict).Map()
	if msg, _ := res["error"].(string); msg != "pair a Windows host first" {
		t.Fatalf("error = %+v", res)
	}
}

func TestPopoRegistrationQRThenRobotCreatesInstallation(t *testing.T) {
	bridgeID, token := popoRegisterBridge(t, "WIN-QR-OK")
	popoHeartbeat(t, token, popoIdleRobot("unrelated"))
	agentID := createHandlerTestAgent(t, "popo-qr-ok", []byte(`{}`))
	regID := popoBeginRegistration(t, agentID, bridgeID)

	listed := testutil.Call(t, testHandler.ListPopoBridgeCommands, popoBearer(newRequest(http.MethodGet, "/api/popo/bridge/commands?wait_ms=0", nil), token)).Want(http.StatusOK).Map()
	cmds, _ := listed["commands"].([]any)
	if len(cmds) != 1 {
		t.Fatalf("commands = %+v", listed)
	}
	cmd, _ := cmds[0].(map[string]any)
	if cmd["type"] != "register_qr" {
		t.Fatalf("command type = %+v", cmd)
	}
	payload, _ := cmd["payload"].(map[string]any)
	if payload["registration_id"] != regID || payload["agent_id"] != agentID || payload["env"] != "production" {
		t.Fatalf("payload = %+v", payload)
	}

	qr := "https://popo.example/qr/" + regID
	progress := popoProgress(t, token, regID, map[string]any{"qr_url": qr}, http.StatusOK)
	if progress["status"] != "awaiting_scan" || progress["qr_url"] != qr {
		t.Fatalf("qr progress = %+v", progress)
	}
	got := popoGetRegistration(t, regID, http.StatusOK)
	if got["status"] != "awaiting_scan" || got["qr_url"] != qr {
		t.Fatalf("get after qr = %+v", got)
	}

	robotID := "scan-bot-" + bridgeID[:8]
	done := popoProgress(t, token, regID, map[string]any{
		"robot_id":   robotID,
		"robot_name": "Scan Bot",
		"appSecret":  "must-not-store",
		"aesKey":     "must-not-store",
		"app_secret": "must-not-store",
		"aes_key":    "must-not-store",
	}, http.StatusOK)
	if done["status"] != "success" || done["robot_id"] != robotID {
		t.Fatalf("robot progress = %+v", done)
	}
	instID, _ := done["installation_id"].(string)
	if instID == "" {
		t.Fatal("expected installation_id")
	}

	final := popoGetRegistration(t, regID, http.StatusOK)
	if final["status"] != "success" || final["installation_id"] != instID {
		t.Fatalf("get after success = %+v", final)
	}

	var cfgRaw []byte
	if err := testPool.QueryRow(context.Background(), `SELECT config FROM channel_installation WHERE id = $1`, instID).Scan(&cfgRaw); err != nil {
		t.Fatalf("load config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg["app_id"] != robotID || cfg["robot_name"] != "Scan Bot" || cfg["bridge_id"] != bridgeID {
		t.Fatalf("config = %s", cfgRaw)
	}
	for _, secret := range []string{"appSecret", "aesKey", "app_secret", "aes_key", "webhook_token_encrypted"} {
		if _, ok := cfg[secret]; ok {
			t.Fatalf("secret field %s stored: %s", secret, cfgRaw)
		}
	}

	listReq := withURLParam(newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/popo/installations", nil), "id", testWorkspaceID)
	var listing struct {
		Installations []struct {
			ID      string `json:"id"`
			RobotID string `json:"robot_id"`
			Status  string `json:"status"`
		} `json:"installations"`
	}
	testutil.Call(t, testHandler.ListPopoInstallations, listReq).Want(http.StatusOK).JSON(&listing)
	found := false
	for _, inst := range listing.Installations {
		if inst.ID == instID {
			found = true
			if inst.RobotID != robotID || inst.Status != "active" {
				t.Fatalf("installation = %+v", inst)
			}
		}
	}
	if !found {
		t.Fatalf("installation %s missing from %+v", instID, listing)
	}
}

func TestPopoRegistrationDuplicateRobotConflict(t *testing.T) {
	bridgeID, token := popoRegisterBridge(t, "WIN-QR-DUP")
	robotID := "dup-bot-" + bridgeID[:8]
	popoHeartbeat(t, token, popoIdleRobot(robotID))
	owner := createHandlerTestAgent(t, "popo-qr-dup-owner", []byte(`{}`))
	installReq := withURLParam(newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/popo/install?agent_id="+owner, map[string]string{
		"bridge_id": bridgeID,
		"robot_id":  robotID,
	}), "id", testWorkspaceID)
	var existing struct {
		ID string `json:"id"`
	}
	testutil.Call(t, testHandler.RegisterPopoBot, installReq).Want(http.StatusOK).JSON(&existing)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_installation WHERE id = $1`, existing.ID)
	})

	other := createHandlerTestAgent(t, "popo-qr-dup-other", []byte(`{}`))
	regID := popoBeginRegistration(t, other, bridgeID)
	res := popoProgress(t, token, regID, map[string]any{"robot_id": robotID}, http.StatusConflict)
	if _, ok := res["error"].(string); !ok {
		t.Fatalf("conflict body = %+v", res)
	}
	got := popoGetRegistration(t, regID, http.StatusOK)
	if got["status"] != "error" || got["error_reason"] != "installation_conflict" {
		t.Fatalf("get after conflict = %+v", got)
	}
}

func TestPopoRegistrationCancelIgnoresLaterProgress(t *testing.T) {
	bridgeID, token := popoRegisterBridge(t, "WIN-QR-CANCEL")
	popoHeartbeat(t, token, popoIdleRobot("unrelated"))
	agentID := createHandlerTestAgent(t, "popo-qr-cancel", []byte(`{}`))
	regID := popoBeginRegistration(t, agentID, bridgeID)

	del := withURLParams(newRequest(http.MethodDelete, "/api/workspaces/"+testWorkspaceID+"/popo/registrations/"+regID, nil),
		"id", testWorkspaceID, "registrationId", regID)
	testutil.Call(t, testHandler.CancelPopoRegistration, del).Want(http.StatusNoContent)

	got := popoGetRegistration(t, regID, http.StatusOK)
	if got["status"] != "expired" {
		t.Fatalf("cancelled = %+v", got)
	}
	ignored := popoProgress(t, token, regID, map[string]any{
		"robot_id":  "late-bot",
		"qr_url":    "https://popo.example/late",
		"appSecret": "nope",
	}, http.StatusOK)
	if ignored["status"] != "expired" {
		t.Fatalf("progress after cancel = %+v", ignored)
	}
	var n int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM channel_installation WHERE agent_id = $1 AND channel_type = 'popo' AND status = 'active'`, agentID).Scan(&n); err != nil {
		t.Fatalf("count installs: %v", err)
	}
	if n != 0 {
		t.Fatalf("cancel must not create an installation, got %d", n)
	}
}
