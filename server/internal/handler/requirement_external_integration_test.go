//go:build externalintegration

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRequirementPopoCLISubmissionAndUnknownReceipt(t *testing.T) {
	if os.Getenv("MULTICA_RUN_POPO_ACCEPTANCE") != "1" {
		t.Skip("explicit external-send opt-in required")
	}
	recipient := strings.TrimSpace(os.Getenv("MULTICA_ACCEPTANCE_POPO_RECIPIENT"))
	if !strings.Contains(recipient, "@") || strings.ContainsAny(recipient, "\r\n&|<>") {
		t.Fatal("an explicitly selected POPO account is required")
	}
	bin := os.Getenv("MULTICA_POPO_CLI")
	if bin == "" {
		bin = "popo-cli"
	}
	cli, err := exec.LookPath(bin)
	if err != nil {
		t.Fatal(err)
	}
	sourceID := strings.TrimSpace(os.Getenv("MULTICA_ACCEPTANCE_SOURCE_ID"))
	if sourceID == "" {
		t.Fatal("an explicitly selected source reference is required")
	}
	source := newRequirementSourceFixture(sourceID)
	source.Snapshot.Title = "【Multica链路验收】仅验证通知，不代表业务任务完成"
	source.Snapshot.Description = "Controlled acceptance notification; no business fields are changed."
	source.Snapshot.Digest = source.Snapshot.Fingerprint()
	source.Digest = source.Snapshot.Digest
	source.Raw = requirementJSON(source.Snapshot)
	f := newRequirementPopoFixtureWithSource(t, source, nil)
	dbfx.Exec(t, `UPDATE channel_user_binding SET channel_user_id=$2 WHERE installation_id=$1 AND multica_user_id=$3`, f.env.install, recipient, testUserID)
	worker := requirementOutboxWorkerForTest()
	if result, err := worker.RunOnce(context.Background(), 10); err != nil || result.Accepted != 1 {
		t.Fatalf("prepare: %+v, %v", result, err)
	}
	delivery := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	payload := decodeRequirementSendPayload(t, delivery)
	if payload.ChatID != recipient || !strings.Contains(payload.Text, "Multica链路验收") {
		t.Fatalf("unexpected delivery target or content: recipient=%q", payload.ChatID)
	}
	if got := f.env.commands(t); len(got) != 1 || got[0] != payload.Text {
		t.Fatal("bridge did not lease the prepared notification")
	}
	outputDir := os.Getenv("MULTICA_ACCEPTANCE_EVIDENCE_DIR")
	if outputDir == "" {
		outputDir = t.TempDir()
	}
	messageFile := filepath.Join(outputDir, "popo-acceptance-message.txt")
	if err := os.WriteFile(messageFile, []byte(payload.Text), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(messageFile) })
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, cli, "popo", "message_send_p2p", "receiver="+recipient, "source=AGENT", "message=@text:"+messageFile)
	raw, err := command.Output()
	if writeErr := os.WriteFile(filepath.Join(outputDir, "popo-acceptance-response.json"), raw, 0600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if err != nil {
		t.Fatalf("POPO send failed; no automatic retry: %v", err)
	}
	recordAcceptancePopoReceipt(t, f, delivery, raw)
	t.Logf("requirement=%s command=%s recipient=%s submission=accepted delivery=unknown", f.detail.ID, uuidToString(delivery.ID), recipient)
}

func recordAcceptancePopoReceipt(t *testing.T, f requirementPopoFixture, delivery db.PopoBridgeCommand, raw []byte) {
	t.Helper()
	var response struct {
		OK   bool `json:"ok"`
		Data struct {
			Data struct {
				Data struct {
					Status int `json:"status"`
				} `json:"data"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Data.Data.Data.Status != 1 {
		t.Fatal("POPO did not acknowledge the send; inspect the saved response before any retry")
	}
	// The P2P CLI acknowledges submission without exposing a message identity.
	receipt := map[string]any{
		"status": "unknown",
		"error":  "POPO accepted the submission without a remote message identity; do not resend automatically",
	}
	req := withURLParam(testutil.JSONRequest(http.MethodPost, "/api/popo/bridge/commands/"+uuidToString(delivery.ID)+"/receipt", receipt), "id", uuidToString(delivery.ID))
	testutil.Call(t, testHandler.AckPopoBridgeCommand, popoBearer(req, f.env.token)).Want(http.StatusOK)
	got := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	if got.Status != "unknown" || got.RemoteMessageID.Valid || got.EvidenceFinalized || !got.NeedsReconcile {
		t.Fatalf("receipt was not projected: status=%s", got.Status)
	}
	if result, err := requirementOutboxWorkerForTest().RunOnce(context.Background(), 10); err != nil || result.Claimed != 0 {
		t.Fatalf("submitted notification was retried: %+v, %v", result, err)
	}
}

func TestRequirementPopoAcknowledgementWithoutIdentityStaysUnknown(t *testing.T) {
	f := newRequirementPopoFixture(t, "987654321")
	if _, err := requirementOutboxWorkerForTest().RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	delivery := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	if len(f.env.commands(t)) != 1 {
		t.Fatal("bridge did not lease the notification")
	}
	raw := []byte(`{"ok":true,"data":{"code":"FABRIC_OK","data":{"code":"FABRIC_OK","data":{"status":1,"message":"success","data":{}},"trace_id":"not-a-message-id"}},"request_id":"also-not-a-message-id"}`)
	recordAcceptancePopoReceipt(t, f, delivery, raw)
}
