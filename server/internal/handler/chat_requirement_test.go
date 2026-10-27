package handler

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// chatSessionRequirementFixture opens a requirement and returns the requirement
// id together with the chat session the open created. The returned session is
// the authoritative "this chat is requirement-owned" sample for projection
// tests; cleanup comes from the shared requirement/chat fixtures.
func chatSessionRequirementFixture(t *testing.T, externalID string) (string, string) {
	t.Helper()
	enableRequirementFlowForTest(t)
	_, agentID := newRequirementClaudeAgent(t, "Chat requirement projection agent "+externalID)
	source := newRequirementSourceFixture(externalID)
	detail := createRequirementForTest(t, testUserID, source.body(agentID), "chat-requirement-create-"+externalID)
	opened := openRequirementForTest(t, testUserID, detail.ID, 1, "chat-requirement-open-"+externalID)
	return detail.ID, opened.Session.ID
}

// uniqueChatRequirementExternalID keeps fixture source ids distinct for tests
// that share the handler test workspace.
func uniqueChatRequirementExternalID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func getChatSessionBodyForTest(t *testing.T, sessionID string) map[string]any {
	t.Helper()
	req := withURLParam(withChatTestWorkspaceCtx(t, newRequest(http.MethodGet, "/api/chat/sessions/"+sessionID, nil)), "sessionId", sessionID)
	body := map[string]any{}
	testutil.Call(t, testHandler.GetChatSession, req).Want(http.StatusOK).JSON(&body)
	return body
}

func listChatSessionBodiesForTest(t *testing.T, query string) []map[string]any {
	t.Helper()
	req := withChatTestWorkspaceCtx(t, newRequest(http.MethodGet, "/api/chat/sessions"+query, nil))
	bodies := []map[string]any{}
	testutil.Call(t, testHandler.ListChatSessions, req).Want(http.StatusOK).JSON(&bodies)
	return bodies
}

func requireListedChatSession(t *testing.T, sessions []map[string]any, sessionID string) map[string]any {
	t.Helper()
	for _, session := range sessions {
		if session["id"] == sessionID {
			return session
		}
	}
	t.Fatalf("chat session %s missing from list response", sessionID)
	return nil
}

// requireExplicitRequirementID asserts the additive key is present — never
// omitted — and equals want. want == nil means the explicit JSON null that
// tells a client "definitely no requirement".
func requireExplicitRequirementID(t *testing.T, body map[string]any, want any) {
	t.Helper()
	value, ok := body["requirement_id"]
	if !ok {
		t.Fatalf("requirement_id key is missing from ChatSession response: %+v", body)
	}
	if value != want {
		t.Fatalf("requirement_id = %#v, want %#v", value, want)
	}
}

func TestChatSessionRequirementIDProjection(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	requirementID, sessionID := chatSessionRequirementFixture(t, uniqueChatRequirementExternalID())

	single := getChatSessionBodyForTest(t, sessionID)
	requireExplicitRequirementID(t, single, requirementID)

	// Both list branches (active-only and ?status=all) must hydrate the
	// projection, and their value must agree with the single-session fetch.
	for _, query := range []string{"", "?status=all"} {
		listed := requireListedChatSession(t, listChatSessionBodiesForTest(t, query), sessionID)
		requireExplicitRequirementID(t, listed, requirementID)
		if listed["requirement_id"] != single["requirement_id"] {
			t.Fatalf("list(%q) requirement_id = %#v, single fetch = %#v", query, listed["requirement_id"], single["requirement_id"])
		}
	}

	// Update returns the same projection; the requirement id must not depend on
	// title/edit state.
	updateReq := map[string]any{"title": "Requirement chat renamed"}
	req := withChatTestWorkspaceCtx(t, newRequest(http.MethodPatch, "/api/chat/sessions/"+sessionID, updateReq))
	req = withURLParam(req, "sessionId", sessionID)
	updated := map[string]any{}
	testutil.Call(t, testHandler.UpdateChatSession, req).Want(http.StatusOK).JSON(&updated)
	requireExplicitRequirementID(t, updated, requirementID)
}

func TestChatSessionRequirementIDExplicitNull(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "Chat requirement null projection agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)

	requireExplicitRequirementID(t, getChatSessionBodyForTest(t, sessionID), nil)
	for _, query := range []string{"", "?status=all"} {
		requireExplicitRequirementID(t, requireListedChatSession(t, listChatSessionBodiesForTest(t, query), sessionID), nil)
	}

	// Every single-session endpoint returning ChatSessionResponse must emit the
	// additive key too, not only the read endpoints.
	titleReq := withChatTestWorkspaceCtx(t, newRequest(http.MethodPatch, "/api/chat/sessions/"+sessionID, map[string]any{"title": "ordinary renamed"}))
	titleReq = withURLParam(titleReq, "sessionId", sessionID)
	titleBody := map[string]any{}
	testutil.Call(t, testHandler.UpdateChatSession, titleReq).Want(http.StatusOK).JSON(&titleBody)
	requireExplicitRequirementID(t, titleBody, nil)

	pinReq := withChatTestWorkspaceCtx(t, newRequest(http.MethodPatch, "/api/chat/sessions/"+sessionID+"/pin", map[string]any{"pinned": true}))
	pinReq = withURLParam(pinReq, "sessionId", sessionID)
	pinBody := map[string]any{}
	testutil.Call(t, testHandler.SetChatSessionPinned, pinReq).Want(http.StatusOK).JSON(&pinBody)
	requireExplicitRequirementID(t, pinBody, nil)

	archiveReq := withChatTestWorkspaceCtx(t, newRequest(http.MethodPatch, "/api/chat/sessions/"+sessionID+"/archive", map[string]any{"archived": true}))
	archiveReq = withURLParam(archiveReq, "sessionId", sessionID)
	archiveBody := map[string]any{}
	testutil.Call(t, testHandler.SetChatSessionArchived, archiveReq).Want(http.StatusOK).JSON(&archiveBody)
	requireExplicitRequirementID(t, archiveBody, nil)

	createReq := withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions", map[string]any{
		"agent_id": agentID,
		"title":    "ordinary projection create",
	}))
	createBody := map[string]any{}
	testutil.Call(t, testHandler.CreateChatSession, createReq).Want(http.StatusCreated).JSON(&createBody)
	dbfx.Cleanup(t, `DELETE FROM chat_session WHERE id = $1`, createBody["id"])
	requireExplicitRequirementID(t, createBody, nil)
}

func TestChatSessionRequirementIDInOpenResponse(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	enableRequirementFlowForTest(t)
	externalID := uniqueChatRequirementExternalID()
	_, agentID := newRequirementClaudeAgent(t, "Chat requirement open projection agent "+externalID)
	detail := createRequirementForTest(t, testUserID, newRequirementSourceFixture(externalID).body(agentID), "chat-requirement-open-projection-"+externalID)

	var envelope struct {
		Session map[string]any `json:"session"`
	}
	testutil.Call(t, testHandler.OpenRequirement, requirementRequest(
		testUserID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID+"/open",
		map[string]any{"expectedRevision": 1}, "chat-requirement-open-projection-"+externalID,
		"id", testWorkspaceID, "requirementId", detail.ID,
	)).Want(http.StatusOK).JSON(&envelope)
	sessionID, _ := envelope.Session["id"].(string)
	if sessionID == "" {
		t.Fatalf("open response has no session id: %+v", envelope.Session)
	}
	registerRequirementChatCleanup(t, sessionID)
	requireExplicitRequirementID(t, envelope.Session, detail.ID)
}

func TestChatSessionRequirementIDWorkspaceIsolation(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "Chat requirement isolation agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	otherWorkspaceID := dbfx.Workspace(t, "Chat requirement isolation workspace", fmt.Sprintf("chat-requirement-isolation-%d", time.Now().UnixNano()))

	// A requirement row in another workspace that points at this session is
	// corrupt data the application cannot create — there is no foreign key and
	// the only writer binds a session it just created in the same workspace.
	// It is exactly what the workspace-scoped batch lookup must not surface: a
	// projection keyed on session id alone would leak another workspace's
	// requirement id into this response.
	dbfx.Insert(t, "requirement", testutil.Cols{
		"workspace_id":    otherWorkspaceID,
		"source_host":     "yixiezuo",
		"external_id":     uniqueChatRequirementExternalID(),
		"source_snapshot": testutil.Raw("'{}'::jsonb"),
		"source_digest":   "sha256:isolation",
		"requester_id":    testUserID,
		"agent_id":        agentID,
		"chat_session_id": sessionID,
	})

	requireExplicitRequirementID(t, getChatSessionBodyForTest(t, sessionID), nil)
	requireExplicitRequirementID(t, requireListedChatSession(t, listChatSessionBodiesForTest(t, ""), sessionID), nil)
}
