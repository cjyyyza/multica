package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/popo"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func inboundEvidenceHash(char string) string {
	return "sha256:" + strings.Repeat(char, 64)
}

func extendedInboundBody(e popoP2Env, eventID, hash string) map[string]any {
	body := map[string]any{
		"protocol_version": 1,
		"event_id":         eventID,
		"robot_id":         e.robotID,
		"sender":           map[string]any{"id": e.sender, "name": "Alice"},
		"chat":             map[string]any{"id": e.sender, "type": "p2p"},
		"addressed_to_bot": true,
		"text":             "hello extended",
		"command_text":     "hello extended",
		"raw":              map[string]any{"event_type": "IM_P2P_TO_ROBOT_MSG", "evidence": "pre-download-A"},
		"media":            []any{},
	}
	if hash != "" {
		body["payload_hash"] = hash
	}
	return body
}

func postExtendedInbound(t *testing.T, token string, body map[string]any, want int) map[string]any {
	t.Helper()
	req := popoBearer(testutil.JSONRequest(http.MethodPost, "/api/popo/bridge/inbound", body), token)
	return testutil.Call(t, testHandler.IngestPopoBridgeInbound, req).Want(want).Map()
}

func TestPopoInboundExtendedResponseMatrixAndCommitOrder(t *testing.T) {
	e := popoP2Setup(t)
	eventID := "extended-" + e.bridgeID[:8]
	hash := inboundEvidenceHash("a")
	body := extendedInboundBody(e, eventID, hash)
	first := postExtendedInbound(t, e.token, body, http.StatusOK)
	if first["accepted"] != true {
		t.Fatalf("first response = %+v", first)
	}
	stored, err := testHandler.Queries.GetPopoInboundEventByIdentity(context.Background(), db.GetPopoInboundEventByIdentityParams{
		BridgeID: parseUUID(e.bridgeID), RobotID: e.robotID, EventID: eventID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.PayloadHash.String != hash || len(stored.PayloadEvidence) == 0 {
		t.Fatalf("durable evidence missing at response: %+v", stored)
	}

	duplicate := postExtendedInbound(t, e.token, body, http.StatusConflict)
	if duplicate["accepted"] != true || duplicate["code"] != popo.InboundCodeDurableDuplicate || duplicate["payload_hash"] != hash {
		t.Fatalf("same-hash duplicate = %+v", duplicate)
	}
	changed := extendedInboundBody(e, eventID, inboundEvidenceHash("b"))
	changed["chat"] = map[string]any{"id": "changed-chat@example.test", "type": "p2p"}
	conflict := postExtendedInbound(t, e.token, changed, http.StatusConflict)
	if conflict["accepted"] != false || conflict["code"] != popo.InboundCodeIdentityConflict {
		t.Fatalf("different-hash conflict = %+v", conflict)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_inbound_event WHERE bridge_id = $1 AND robot_id = $2 AND event_id = $3`, e.bridgeID, e.robotID, eventID); got != 1 {
		t.Fatalf("identity conflict created another durable row: %d", got)
	}
}

func TestPopoInboundRouteLegacyAndMalformedResponses(t *testing.T) {
	e := popoP2Setup(t)
	routeBody := extendedInboundBody(e, "route-"+e.bridgeID[:8], inboundEvidenceHash("a"))
	routeBody["robot_id"] = "robot-not-on-this-bridge"
	route := postExtendedInbound(t, e.token, routeBody, http.StatusConflict)
	if route["accepted"] != false || route["code"] != popo.InboundCodeRouteConflict {
		t.Fatalf("route conflict = %+v", route)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_inbound_event WHERE bridge_id = $1 AND event_id = $2`, e.bridgeID, routeBody["event_id"]); got != 0 {
		t.Fatalf("route conflict persisted: %d", got)
	}

	legacyEvent := "legacy-" + e.bridgeID[:8]
	legacy := extendedInboundBody(e, legacyEvent, "")
	first := postExtendedInbound(t, e.token, legacy, http.StatusOK)
	if first["accepted"] != true {
		t.Fatalf("legacy first = %+v", first)
	}
	duplicate := postExtendedInbound(t, e.token, legacy, http.StatusOK)
	if duplicate["accepted"] != true || duplicate["duplicate"] != true {
		t.Fatalf("legacy duplicate = %+v", duplicate)
	}
	missingEvidence := postExtendedInbound(t, e.token, extendedInboundBody(e, legacyEvent, inboundEvidenceHash("c")), http.StatusConflict)
	if missingEvidence["accepted"] != false || missingEvidence["code"] != popo.InboundCodeIdentityConflict {
		t.Fatalf("legacy hash was treated as comparable: %+v", missingEvidence)
	}

	malformed := extendedInboundBody(e, "malformed-"+e.bridgeID[:8], "not-a-hash")
	response := postExtendedInbound(t, e.token, malformed, http.StatusBadRequest)
	if response["accepted"] == true {
		t.Fatalf("malformed hash was accepted: %+v", response)
	}
	missingID := extendedInboundBody(e, "", inboundEvidenceHash("a"))
	response = postExtendedInbound(t, e.token, missingID, http.StatusBadRequest)
	if response["accepted"] == true {
		t.Fatalf("missing event id was accepted: %+v", response)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_inbound_event WHERE bridge_id = $1 AND event_id = $2`, e.bridgeID, malformed["event_id"]); got != 0 {
		t.Fatalf("malformed request persisted: %d", got)
	}
}

func TestPopoInboundExecutionDedupeCannotBypassWithAnotherChat(t *testing.T) {
	e := popoP2Setup(t)
	eventID := "chat-fence-" + e.bridgeID[:8]
	body := extendedInboundBody(e, eventID, "")
	postExtendedInbound(t, e.token, body, http.StatusOK)
	before := dbfx.Count(t, `SELECT count(*) FROM channel_chat_session_binding WHERE installation_id = $1`, e.install)
	body["chat"] = map[string]any{"id": "another-chat@example.test", "type": "p2p"}
	duplicate := postExtendedInbound(t, e.token, body, http.StatusOK)
	if duplicate["accepted"] != true || duplicate["duplicate"] != true {
		t.Fatalf("chat-changing replay = %+v", duplicate)
	}
	after := dbfx.Count(t, `SELECT count(*) FROM channel_chat_session_binding WHERE installation_id = $1`, e.install)
	if after != before {
		t.Fatalf("chat-changing replay reached execution: before=%d after=%d", before, after)
	}
	stored, err := testHandler.Queries.GetPopoInboundEventByIdentity(context.Background(), db.GetPopoInboundEventByIdentityParams{
		BridgeID: parseUUID(e.bridgeID), RobotID: e.robotID, EventID: eventID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !stored.ExecutionProcessedAt.Valid {
		t.Fatal("composite execution fence was not finalized")
	}
}
