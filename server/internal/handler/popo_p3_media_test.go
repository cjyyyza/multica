package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/popo"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

func popoMediaSetup(t *testing.T) (popoP2Env, *mockStorage) {
	t.Helper()
	store := &mockStorage{}
	orig := testHandler.Storage
	testHandler.Storage = store
	t.Cleanup(func() { testHandler.Storage = orig })
	return popoP2Setup(t), store
}

func popoStageFile(t *testing.T, e popoP2Env, eventID string, index int, kind, filename, mime string, body []byte) string {
	t.Helper()
	create := popoBearer(testutil.JSONRequest(http.MethodPost, "/api/popo/bridge/media/sessions", map[string]any{
		"event_id":   eventID,
		"index":      index,
		"filename":   filename,
		"mime_type":  mime,
		"size_bytes": len(body),
		"kind":       kind,
		"robot_id":   e.robotID,
	}), e.token)
	var created struct {
		SessionID string `json:"session_id"`
		MaxBytes  int    `json:"max_bytes"`
	}
	testutil.Call(t, testHandler.CreatePopoBridgeMediaSession, create).Want(http.StatusOK).JSON(&created)
	if created.SessionID == "" || created.MaxBytes != popo.MaxInboundMediaBytes {
		t.Fatalf("session = %+v", created)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/popo/bridge/media/sessions/"+created.SessionID, bytes.NewReader(body))
	req.Header.Set("Content-Type", mime)
	req = popoBearer(withURLParams(req, "id", created.SessionID), e.token)
	testutil.Call(t, testHandler.PutPopoBridgeMediaSession, req).Want(http.StatusOK)
	return created.SessionID
}

func (e popoP2Env) inboundWithMedia(t *testing.T, eventID, text, chatID, chatType string, addressed bool, media []map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{
		"protocol_version": 1,
		"event_id":         eventID,
		"robot_id":         e.robotID,
		"sender":           map[string]string{"id": e.sender, "name": "Alice"},
		"chat":             map[string]string{"id": chatID, "type": chatType},
		"addressed_to_bot": addressed,
		"text":             text,
		"command_text":     text,
		"media":            media,
	}
	accepted := testutil.Call(t, testHandler.IngestPopoBridgeInbound, popoBearer(testutil.JSONRequest(http.MethodPost, "/api/popo/bridge/inbound", body), e.token)).Want(http.StatusOK).Map()
	return accepted
}

func popoWait(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for POPO media")
}

func TestPopoStagingPutThenInboundBindsAttachment(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	e, store := popoMediaSetup(t)
	eventID := "evt-stage-1"
	png := []byte("png-bytes")
	popoStageFile(t, e, eventID, 0, "image", "shot.png", "image/png", png)

	accepted := e.inboundWithMedia(t, eventID, "see this", e.sender, "p2p", true, []map[string]any{{
		"index": 0, "kind": "image", "filename": "shot.png", "mime_type": "image/png", "size_bytes": len(png),
	}})
	if accepted["accepted"] != true {
		t.Fatalf("inbound = %+v", accepted)
	}

	var n int
	popoWait(t, 3*time.Second, func() bool {
		if err := testPool.QueryRow(context.Background(), `
			SELECT count(*)
			FROM channel_chat_session_binding AS binding
			JOIN chat_message AS message ON message.chat_session_id = binding.chat_session_id
			JOIN attachment ON attachment.chat_message_id = message.id
			WHERE binding.installation_id = $1 AND binding.channel_chat_id = $2
			  AND binding.retired_at IS NULL AND message.role = 'user'
		`, e.install, e.sender).Scan(&n); err != nil {
			t.Fatalf("count attachments: %v", err)
		}
		return n == 1
	})
	if len(store.files) == 0 {
		t.Fatal("expected staged bytes in object storage")
	}
}

func TestPopoUnboundSenderDoesNotPromoteMedia(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	e, _ := popoMediaSetup(t)
	dbfx.Exec(t, `DELETE FROM channel_user_binding WHERE installation_id = $1`, e.install)
	eventID := "evt-unbound-media"
	popoStageFile(t, e, eventID, 0, "image", "shot.png", "image/png", []byte("png"))
	accepted := e.inboundWithMedia(t, eventID, "see this", e.sender, "p2p", true, []map[string]any{{
		"index": 0, "kind": "image", "filename": "shot.png", "mime_type": "image/png",
	}})
	if accepted["accepted"] != true {
		t.Fatalf("inbound = %+v", accepted)
	}
	n := dbfx.Count(t, `
		SELECT count(*) FROM attachment
		WHERE workspace_id = $1 AND filename = 'shot.png' AND chat_message_id IS NOT NULL
	`, testWorkspaceID)
	if n != 0 {
		t.Fatalf("unbound sender promoted %d attachments", n)
	}
}

func TestPopoPartialStagingLeavesFailureVisible(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	e, _ := popoMediaSetup(t)
	eventID := "evt-partial"
	popoStageFile(t, e, eventID, 0, "image", "ok.png", "image/png", []byte("ok"))
	accepted := e.inboundWithMedia(t, eventID, "two files", e.sender, "p2p", true, []map[string]any{
		{"index": 0, "kind": "image", "filename": "ok.png", "mime_type": "image/png"},
		{"index": 1, "kind": "file", "filename": "missing.pdf", "mime_type": "application/pdf"},
	})
	if accepted["accepted"] != true {
		t.Fatalf("inbound = %+v", accepted)
	}
	var content string
	var n int
	popoWait(t, 3*time.Second, func() bool {
		if err := testPool.QueryRow(context.Background(), `
			SELECT message.content, (
				SELECT count(*) FROM attachment WHERE attachment.chat_message_id = message.id
			)
			FROM channel_chat_session_binding AS binding
			JOIN chat_message AS message ON message.chat_session_id = binding.chat_session_id
			WHERE binding.installation_id = $1 AND binding.channel_chat_id = $2
			  AND binding.retired_at IS NULL AND message.role = 'user'
			ORDER BY message.created_at DESC
			LIMIT 1
		`, e.install, e.sender).Scan(&content, &n); err != nil {
			return false
		}
		return n == 1 && strings.Contains(content, "failed")
	})
	if n != 1 {
		t.Fatalf("attachments = %d, want 1; content=%q", n, content)
	}
	if !strings.Contains(content, "missing.pdf") || !strings.Contains(content, "failed") {
		t.Fatalf("partial failure not visible: %q", content)
	}
}

func TestPopoGroupAtWithImageDescriptor(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	e, _ := popoMediaSetup(t)
	groupID := "group-media-" + e.bridgeID[:8]
	eventID := "evt-group-img"
	popoStageFile(t, e, eventID, 0, "image", "g.png", "image/png", []byte("g"))
	accepted := e.inboundWithMedia(t, eventID, "", groupID, "group", true, []map[string]any{{
		"kind": "image", "filename": "g.png", "mime_type": "image/png",
	}})
	if accepted["accepted"] != true {
		t.Fatalf("group @ image = %+v", accepted)
	}
	var chatType string
	if err := testPool.QueryRow(context.Background(), `
		SELECT chat_type FROM channel_chat_session_binding
		WHERE installation_id = $1 AND channel_chat_id = $2 AND retired_at IS NULL
	`, e.install, groupID).Scan(&chatType); err != nil {
		t.Fatalf("load group binding: %v", err)
	}
	if chatType != "group" {
		t.Fatalf("chat_type=%q", chatType)
	}
}

func TestPopoOutboundAttachmentGrant(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	e, store := popoMediaSetup(t)
	key := "workspaces/" + testWorkspaceID + "/popo/outbound/grant.png"
	_, _ = store.Upload(context.Background(), key, []byte("file-bytes"), "image/png", "grant.png")
	attID := dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id":  testWorkspaceID,
		"uploader_type": "agent", "uploader_id": e.agentID,
		"filename": "grant.png", "url": store.ObjectURL(key),
		"content_type": "image/png", "size_bytes": 10,
	})
	if err := testHandler.PopoBridge.Enqueue(context.Background(), popo.OutboundItem{
		WorkspaceID:    util.MustParseUUID(testWorkspaceID),
		InstallationID: util.MustParseUUID(e.install),
		BridgeID:       util.MustParseUUID(e.bridgeID),
		ChatID:         e.sender,
		RobotID:        e.robotID,
		Content:        "with file",
		Attachments: []popo.SendAttachment{{
			AttachmentID: attID,
			Filename:     "grant.png",
			MimeType:     "image/png",
			DownloadPath: "/api/popo/bridge/media/outbound/" + attID,
		}},
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, "/api/popo/bridge/commands?wait_ms=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Commands []struct {
			Payload json.RawMessage `json:"payload"`
		} `json:"commands"`
	}
	testutil.Call(t, testHandler.ListPopoBridgeCommands, popoBearer(req, e.token)).Want(http.StatusOK).JSON(&listed)
	if len(listed.Commands) != 1 {
		t.Fatalf("commands=%d", len(listed.Commands))
	}
	var payload popo.SendPayload
	if err := json.Unmarshal(listed.Commands[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Attachments) != 1 || payload.Attachments[0].DownloadPath == "" {
		t.Fatalf("payload attachments = %+v", payload.Attachments)
	}

	get := popoBearer(withURLParams(httptest.NewRequest(http.MethodGet, payload.Attachments[0].DownloadPath, nil), "attachmentId", attID), e.token)
	rec := testutil.Call(t, testHandler.GetPopoBridgeOutboundMedia, get).Want(http.StatusOK)
	if rec.Body.String() != "file-bytes" {
		t.Fatalf("downloaded %q", rec.Body.String())
	}
}

func TestPopoOutboundLongTextGrant(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	e, _ := popoMediaSetup(t)
	long := strings.Repeat("a", popo.MaxOutboundTextRunes+50)
	attID := dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id":  testWorkspaceID,
		"uploader_type": "agent", "uploader_id": e.agentID,
		"filename": "reply.txt", "url": "https://cdn.example.com/reply.txt",
		"content_type": "text/plain", "size_bytes": int64(len(long)),
	})
	if err := testHandler.PopoBridge.Enqueue(context.Background(), popo.OutboundItem{
		WorkspaceID:    util.MustParseUUID(testWorkspaceID),
		InstallationID: util.MustParseUUID(e.install),
		BridgeID:       util.MustParseUUID(e.bridgeID),
		ChatID:         e.sender,
		RobotID:        e.robotID,
		Content:        long[:popo.MaxOutboundTextRunes] + "…\nhttps://app/issue",
		Attachments: []popo.SendAttachment{{
			AttachmentID: attID,
			Filename:     "reply.txt",
			MimeType:     "text/plain",
			DownloadPath: "/api/popo/bridge/media/outbound/" + attID,
		}},
	}); err != nil {
		t.Fatalf("enqueue long: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, "/api/popo/bridge/commands?wait_ms=0", nil)
	var listed struct {
		Commands []struct {
			Payload json.RawMessage `json:"payload"`
		} `json:"commands"`
	}
	testutil.Call(t, testHandler.ListPopoBridgeCommands, popoBearer(req, e.token)).Want(http.StatusOK).JSON(&listed)
	var payload popo.SendPayload
	if err := json.Unmarshal(listed.Commands[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len([]rune(payload.Text)) <= popo.MaxOutboundTextRunes {
		t.Fatalf("expected clipped text plus link, got %d runes", len([]rune(payload.Text)))
	}
	if len(payload.Attachments) != 1 || payload.Attachments[0].Filename != "reply.txt" {
		t.Fatalf("long-text attachments = %+v", payload.Attachments)
	}
}
