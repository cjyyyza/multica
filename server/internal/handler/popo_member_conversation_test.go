package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/popo"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func popoMemberConversationFixture(t *testing.T) (popoP2Env, popoP2Env, string) {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Fatal("a migrated test database is required")
	}
	enableRequirementFlowForTest(t)
	withReconcileFlags(t, true)
	alice := popoP2Setup(t)
	runtimeID, _ := newRequirementClaudeAgent(t, "Multi-user conversation")
	dbfx.Exec(t, `UPDATE agent SET runtime_id=$2, permission_mode='public_to', visibility='workspace' WHERE id=$1`, alice.agentID, runtimeID)
	bobID := dbfx.User(t, "POPO Bob", "bob-"+alice.bridgeID+"@example.test")
	dbfx.Member(t, testWorkspaceID, bobID, "member")
	bob := alice
	bob.sender = "bob-" + alice.bridgeID + "@corp.netease.com"
	dbfx.Insert(t, "channel_user_binding", testutil.Cols{"workspace_id": testWorkspaceID, "multica_user_id": bobID, "installation_id": alice.install, "channel_type": "popo", "channel_user_id": bob.sender})
	dbfx.Exec(t, `UPDATE channel_installation SET config=jsonb_set(config,'{requirement_template_url}','"https://dj01.pm.netease.com/issues/108402"') WHERE id=$1`, alice.install)
	dbfx.Cleanup(t, `DELETE FROM yixiezuo_operation WHERE workspace_id=$1 AND payload->'channel'->>'installation_id'=$2`, testWorkspaceID, alice.install)
	return alice, bob, bobID
}

func popoConversationForTest(t *testing.T, e popoP2Env, chatID string) popo.MemberConversation {
	t.Helper()
	binding, err := testHandler.Queries.GetChannelUserBindingByUserID(context.Background(), db.GetChannelUserBindingByUserIDParams{InstallationID: parseUUID(e.install), ChannelUserID: e.sender})
	if err != nil {
		t.Fatal(err)
	}
	chatType := channel.ChatTypeGroup
	if chatID == e.sender {
		chatType = channel.ChatTypeP2P
	}
	return popo.ReadMemberConversation(binding.Config, popo.ConversationScopeKey(chatID, chatType))
}

func TestPopoMemberModesAndDraftsAreIndependent(t *testing.T) {
	alice, bob, bobID := popoMemberConversationFixture(t)
	group := "member-mode-group-" + alice.bridgeID
	send := func(e popoP2Env, id, text string) { t.Helper(); e.inboundChat(t, id, text, group, "group", true, nil) }
	send(alice, "alice-requirement", "/mode requirement")
	send(bob, "bob-chat", "/mode chat")
	send(alice, "alice-idea", "Alice isolated requirement")
	send(bob, "bob-message", "Bob isolated ordinary chat")
	send(bob, "bob-chat-new", "/new Bob new ordinary conversation")
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE requested_by=$1 AND kind='draft' AND payload->'channel'->>'installation_id'=$2`, bobID, alice.install); got != 0 {
		t.Fatalf("Bob's chat created %d requirements", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM chat_message m JOIN chat_session s ON s.id=m.chat_session_id WHERE s.agent_id=$1 AND s.creator_id=$2 AND m.content='Bob isolated ordinary chat'`, alice.agentID, bobID); got != 1 {
		t.Fatalf("Bob's ordinary chat was not persisted separately: %d", got)
	}
	first := popoConversationForTest(t, alice, group)
	if first.Mode != "requirement" || first.DraftOperationID == "" {
		t.Fatalf("Alice preference=%+v", first)
	}
	// A private choice on the same binding must preserve the group selection.
	alice.inbound(t, "alice-private-mode", "/mode chat", nil)
	if got := popoConversationForTest(t, alice, alice.sender); got.Mode != "chat" || got.DraftOperationID != "" {
		t.Fatalf("group selection leaked into DM: %+v", got)
	}
	if got := popoConversationForTest(t, alice, group); got != first {
		t.Fatalf("private selection changed the group preference: %+v", got)
	}
	send(bob, "bob-idea", "/requirement new Bob isolated requirement")
	// A delayed ordinary /new must not become a new requirement after switching.
	send(bob, "bob-chat-new", "/new Bob new ordinary conversation")
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE requested_by=$1 AND kind='draft' AND payload->'channel'->>'installation_id'=$2`, bobID, alice.install); got != 1 {
		t.Fatalf("replay reinterpreted an ordinary /new as a requirement: %d", got)
	}
	second := popoConversationForTest(t, bob, group)
	if second.DraftOperationID == "" || second.DraftOperationID == first.DraftOperationID {
		t.Fatalf("members share a draft: %+v %+v", first, second)
	}
	send(alice, "alice-chat", "/chat")
	// Replaying an earlier mode control cannot undo the newer selection.
	send(alice, "alice-requirement", "/mode requirement")
	if got := popoConversationForTest(t, alice, group); got.Mode != "chat" {
		t.Fatalf("old command reverted a newer preference: %+v", got)
	}
	if got := popoConversationForTest(t, bob, group); got.Mode != "requirement" || got.DraftOperationID != second.DraftOperationID {
		t.Fatalf("Alice changed Bob's selection: %+v", got)
	}
	send(alice, "alice-resume-requirement", "/requirement")
	if got := popoConversationForTest(t, alice, group); got != first {
		t.Fatalf("switching back lost the selected requirement: %+v", got)
	}
	send(alice, "alice-back-to-chat", "/chat")
	send(alice, "alice-normal", "Alice ordinary after switching")
	if got := dbfx.Count(t, `SELECT count(*) FROM chat_message m JOIN chat_session s ON s.id=m.chat_session_id WHERE s.agent_id=$1 AND s.creator_id=$2 AND m.content='Alice ordinary after switching'`, alice.agentID, testUserID); got != 1 {
		t.Fatalf("Alice did not receive her own group context: %d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM chat_session s WHERE s.agent_id=$1 AND EXISTS(SELECT 1 FROM chat_message a WHERE a.chat_session_id=s.id AND a.content='Bob isolated ordinary chat') AND EXISTS(SELECT 1 FROM chat_message a WHERE a.chat_session_id=s.id AND a.content='Alice ordinary after switching')`, alice.agentID); got != 0 {
		t.Fatal("different group members share model input history")
	}
}

func TestPopoConcurrentMembersKeepIndependentSelections(t *testing.T) {
	alice, bob, bobID := popoMemberConversationFixture(t)
	group := "concurrent-group-" + alice.bridgeID
	var workers sync.WaitGroup
	for _, entry := range []struct {
		env               popoP2Env
		id, mode, message string
	}{{alice, "concurrent-a", "requirement", "Alice concurrent requirement"}, {bob, "concurrent-b", "chat", "Bob concurrent ordinary context"}} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			entry.env.inboundChat(t, entry.id+"-mode", "/mode "+entry.mode, group, "group", true, nil)
			entry.env.inboundChat(t, entry.id+"-message", entry.message, group, "group", true, nil)
		}()
	}
	workers.Wait()
	if got := popoConversationForTest(t, alice, group); got.Mode != "requirement" || got.DraftOperationID == "" {
		t.Fatalf("Alice concurrent selection=%+v", got)
	}
	if got := popoConversationForTest(t, bob, group); got.Mode != "chat" || got.DraftOperationID != "" {
		t.Fatalf("Bob concurrent selection=%+v", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE requested_by=$1 AND kind='draft' AND payload->'channel'->>'installation_id'=$2`, bobID, alice.install); got != 0 {
		t.Fatalf("Bob concurrent chat created a draft: %d", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM chat_message m JOIN chat_session s ON s.id=m.chat_session_id WHERE s.agent_id=$1 AND s.creator_id=$2 AND m.content='Bob concurrent ordinary context'`, alice.agentID, bobID); got != 1 {
		t.Fatalf("Bob concurrent history=%d", got)
	}
}

func TestPopoRequirementControlsDoNotCreateDraftsOrAgentRuns(t *testing.T) {
	alice, _, _ := popoMemberConversationFixture(t)
	for i, command := range []string{"/help", "/mode", "/requirement help", "/requirement status", "/requirement list", "/requirement stauts", "/requirement approve", "/requirement resolve missing", "/requirement new"} {
		alice.inbound(t, "control-"+string(rune('a'+i)), command, nil)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE payload->'channel'->>'installation_id'=$1`, alice.install); got != 0 {
		t.Fatalf("read-only/invalid commands created %d operations", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, alice.agentID); got != 0 {
		t.Fatalf("control commands created %d agent runs", got)
	}
	commands := alice.commands(t)
	if !strings.Contains(strings.Join(commands, "\n"), "/mode chat") {
		t.Fatalf("help omitted mode selection: %v", commands)
	}
}

func TestPopoRequirementSelectionChecksCurrentParticipant(t *testing.T) {
	alice, bob, bobID := popoMemberConversationFixture(t)
	source := newRequirementSourceFixture("994401")
	detail := createRequirementForTest(t, testUserID, source.body(alice.agentID), "member-select-create")
	bob.inbound(t, "bob-open-denied", "/requirement open "+detail.ID, nil)
	if got := popoConversationForTest(t, bob, bob.sender); got.RequirementID != "" || got.Mode != "chat" {
		t.Fatalf("nonparticipant selected another user's requirement: %+v", got)
	}
	dbfx.Insert(t, "requirement_participant", testutil.Cols{"workspace_id": testWorkspaceID, "requirement_id": detail.ID, "user_id": bobID, "role": "viewer"})
	bob.inbound(t, "bob-open-authorized", "/requirement open "+detail.ID, nil)
	if got := popoConversationForTest(t, bob, bob.sender); got.RequirementID != detail.ID || got.Mode != "requirement" {
		t.Fatalf("authorized selection was not retained: %+v", got)
	}
	dbfx.Exec(t, `DELETE FROM requirement_participant WHERE requirement_id=$1 AND user_id=$2`, detail.ID, bobID)
	bob.inbound(t, "bob-status-revoked", "/requirement status", nil)
	for _, text := range bob.commands(t) {
		if strings.Contains(text, "当前需求已不可访问") {
			return
		}
	}
	t.Fatal("revoked selection did not recheck access")
}

func TestMulticaSameAgentChatsKeepDifferentUsersHistory(t *testing.T) {
	alice, _, bobID := popoMemberConversationFixture(t)
	create := func(userID string) ChatSessionResponse {
		t.Helper()
		req := requirementRequest(userID, http.MethodPost, "/api/chat/sessions", map[string]any{"agent_id": alice.agentID}, "")
		var session ChatSessionResponse
		testutil.Call(t, testHandler.CreateChatSession, req).Want(http.StatusCreated).JSON(&session)
		registerRequirementChatCleanup(t, session.ID)
		return session
	}
	a, b := create(testUserID), create(bobID)
	for _, entry := range []struct{ userID, sessionID, content string }{{testUserID, a.ID, "Alice private history"}, {bobID, b.ID, "Bob private history"}} {
		testutil.Call(t, testHandler.SendChatMessage, chatRequestForTest(entry.userID, http.MethodPost, "/messages", map[string]any{"content": entry.content}, entry.sessionID)).Want(http.StatusCreated)
	}
	for _, entry := range []struct{ userID, sessionID, want, reject string }{{testUserID, a.ID, "Alice private history", "Bob private history"}, {bobID, b.ID, "Bob private history", "Alice private history"}} {
		var messages []ChatMessageResponse
		testutil.Call(t, testHandler.ListChatMessages, chatRequestForTest(entry.userID, http.MethodGet, "/messages", nil, entry.sessionID)).Want(http.StatusOK).JSON(&messages)
		raw, _ := json.Marshal(messages)
		if !strings.Contains(string(raw), entry.want) || strings.Contains(string(raw), entry.reject) {
			t.Fatalf("mixed private history: %s", raw)
		}
	}
	testutil.Call(t, testHandler.ListChatMessages, chatRequestForTest(bobID, http.MethodGet, "/messages", nil, a.ID)).Want(http.StatusForbidden)
	testutil.Call(t, testHandler.ListChatMessages, chatRequestForTest(testUserID, http.MethodGet, "/messages", nil, b.ID)).Want(http.StatusForbidden)
}

func TestMulticaSameAgentRequirementsKeepDifferentUsersPlanContexts(t *testing.T) {
	alice, _, bobID := popoMemberConversationFixture(t)
	aSource, bSource := newRequirementSourceFixture("994411"), newRequirementSourceFixture("994412")
	a := createRequirementForTest(t, testUserID, aSource.body(alice.agentID), "alice-native-requirement")
	b := createRequirementForTest(t, bobID, bSource.body(alice.agentID), "bob-native-requirement")
	aChat := openRequirementForTest(t, testUserID, a.ID, 1, "alice-native-open")
	bChat := openRequirementForTest(t, bobID, b.ID, 1, "bob-native-open")
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='completed', completed_at=now() WHERE id IN ($1,$2)`, aChat.PendingTask.TaskID, bChat.PendingTask.TaskID)
	entries := []struct{ userID, requirementID, sessionID, content, reject string }{{testUserID, a.ID, aChat.Session.ID, "Alice plan constraints", "Bob plan constraints"}, {bobID, b.ID, bChat.Session.ID, "Bob plan constraints", "Alice plan constraints"}}
	for _, entry := range entries {
		var sent SendChatMessageResponse
		testutil.Call(t, testHandler.SendChatMessage, chatRequestForTest(entry.userID, http.MethodPost, "/messages", map[string]any{"content": entry.content}, entry.sessionID)).Want(http.StatusCreated).JSON(&sent)
		input, err := testHandler.Queries.ListChatInputMessages(context.Background(), parseUUID(sent.TaskID))
		if err != nil || len(input) != 1 || input[0].Content != entry.content || uuidToString(input[0].ChatSessionID) != entry.sessionID {
			t.Fatalf("mixed immutable plan task input: %+v, %v", input, err)
		}
	}
	for _, entry := range entries {
		var messages []ChatMessageResponse
		testutil.Call(t, testHandler.ListChatMessages, chatRequestForTest(entry.userID, http.MethodGet, "/messages", nil, entry.sessionID)).Want(http.StatusOK).JSON(&messages)
		raw, _ := json.Marshal(messages)
		if !strings.Contains(string(raw), entry.content) || strings.Contains(string(raw), entry.reject) {
			t.Fatalf("mixed requirement plan input: %s", raw)
		}
		if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE requirement_id=$1 AND (chat_session_id IS DISTINCT FROM $2 OR mode <> 'plan' OR agent_id <> $3)`, entry.requirementID, entry.sessionID, alice.agentID); got != 0 {
			t.Fatalf("wrong plan task binding: %d", got)
		}
	}
	testutil.Call(t, testHandler.GetRequirementByID, requirementRequest(bobID, http.MethodGet, "/requirements/"+a.ID, nil, "", "id", testWorkspaceID, "requirementId", a.ID)).Want(http.StatusForbidden)
	testutil.Call(t, testHandler.ListChatMessages, chatRequestForTest(testUserID, http.MethodGet, "/messages", nil, bChat.Session.ID)).Want(http.StatusForbidden)
}

func TestPopoRebindCannotReuseAnotherMembersSession(t *testing.T) {
	alice, bob, bobID := popoMemberConversationFixture(t)
	group := "rebind-group-" + alice.bridgeID
	alice.inboundChat(t, "before-rebind", "Alice private history", group, "group", true, nil)
	var oldSession string
	dbfx.QueryRow(t, `SELECT chat_session_id::text FROM channel_chat_session_binding WHERE installation_id=$1 AND config->>'chat_id'=$2 AND retired_at IS NULL`, alice.install, group).Scan(&oldSession)

	dbfx.Exec(t, `DELETE FROM channel_user_binding WHERE installation_id=$1 AND multica_user_id=$2`, alice.install, testUserID)
	dbfx.Exec(t, `DELETE FROM channel_user_binding WHERE installation_id=$1 AND multica_user_id=$2`, alice.install, bobID)
	dbfx.Insert(t, "channel_user_binding", testutil.Cols{"workspace_id": testWorkspaceID, "multica_user_id": bobID, "installation_id": alice.install, "channel_type": "popo", "channel_user_id": alice.sender})
	bob.sender = alice.sender
	bob.inboundChat(t, "after-rebind", "Bob must not inherit Alice", group, "group", true, nil)

	var newSession, creator string
	dbfx.QueryRow(t, `SELECT binding.chat_session_id::text FROM channel_chat_session_binding binding JOIN chat_session session ON session.id=binding.chat_session_id WHERE binding.installation_id=$1 AND binding.config->>'chat_id'=$2 AND binding.retired_at IS NULL AND session.creator_id=$3`, alice.install, group, bobID).Scan(&newSession)
	dbfx.QueryRow(t, `SELECT creator_id::text FROM chat_session WHERE id=$1`, newSession).Scan(&creator)
	if newSession == oldSession || creator != bobID {
		t.Fatalf("rebind reused another member's session: old=%s new=%s creator=%s", oldSession, newSession, creator)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM chat_message WHERE chat_session_id=$1 AND content='Bob must not inherit Alice'`, oldSession); got != 0 {
		t.Fatal("rebound member appended to the previous member's history")
	}
}

func TestPopoPrivateRebindRotatesSessionOwnership(t *testing.T) {
	alice, bob, bobID := popoMemberConversationFixture(t)
	alice.inbound(t, "private-before-rebind", "Alice private history", nil)
	var oldSession string
	dbfx.QueryRow(t, `SELECT chat_session_id::text FROM channel_chat_session_binding WHERE installation_id=$1 AND channel_chat_id=$2 AND retired_at IS NULL`, alice.install, alice.sender).Scan(&oldSession)

	dbfx.Exec(t, `DELETE FROM channel_user_binding WHERE installation_id=$1 AND multica_user_id=$2`, alice.install, testUserID)
	dbfx.Exec(t, `DELETE FROM channel_user_binding WHERE installation_id=$1 AND multica_user_id=$2`, alice.install, bobID)
	dbfx.Insert(t, "channel_user_binding", testutil.Cols{"workspace_id": testWorkspaceID, "multica_user_id": bobID, "installation_id": alice.install, "channel_type": "popo", "channel_user_id": alice.sender})
	bob.sender = alice.sender
	bob.inbound(t, "private-after-rebind", "Bob private history", nil)

	var newSession, creator string
	dbfx.QueryRow(t, `SELECT chat_session_id::text FROM channel_chat_session_binding WHERE installation_id=$1 AND channel_chat_id=$2 AND retired_at IS NULL`, alice.install, alice.sender).Scan(&newSession)
	dbfx.QueryRow(t, `SELECT creator_id::text FROM chat_session WHERE id=$1`, newSession).Scan(&creator)
	if newSession == oldSession || creator != bobID {
		t.Fatalf("private rebind reused another member's session: old=%s new=%s creator=%s", oldSession, newSession, creator)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM chat_message WHERE chat_session_id=$1 AND content='Bob private history'`, oldSession); got != 0 {
		t.Fatal("private rebind appended to the previous member's history")
	}
}

func TestPopoDraftReplayRepairsOnlyMissingConversationPointer(t *testing.T) {
	alice, _, _ := popoMemberConversationFixture(t)
	alice.inbound(t, "replay-mode", "/mode requirement", nil)
	alice.inbound(t, "replay-draft", "Recover this draft", nil)
	first := popoConversationForTest(t, alice, alice.sender)
	dbfx.Exec(t, `UPDATE channel_user_binding SET config='{}' WHERE installation_id=$1 AND multica_user_id=$2`, alice.install, testUserID)
	dbfx.Exec(t, `DELETE FROM channel_inbound_write WHERE installation_id=$1 AND kind='popo_conversation'`, alice.install)

	alice.inbound(t, "replay-draft", "Recover this draft", nil)
	recovered := popoConversationForTest(t, alice, alice.sender)
	if recovered != first {
		t.Fatalf("replay did not restore the missing pointer: first=%+v recovered=%+v", first, recovered)
	}

	alice.inbound(t, "newer-draft", "/requirement new A newer selection", nil)
	newer := popoConversationForTest(t, alice, alice.sender)
	alice.inbound(t, "replay-draft", "Recover this draft", nil)
	if got := popoConversationForTest(t, alice, alice.sender); got != newer {
		t.Fatalf("old replay replaced the newer selection: got=%+v want=%+v", got, newer)
	}
	alice.inbound(t, "after-recovery", "Do not create another requirement", nil)
	if got := dbfx.Count(t, `SELECT count(*) FROM yixiezuo_operation WHERE kind='draft' AND requested_by=$1 AND payload->'channel'->>'installation_id'=$2`, testUserID, alice.install); got != 2 {
		t.Fatalf("replay recovery created duplicate drafts: %d", got)
	}
}

func TestPopoAccountBindingManagementIsMemberScoped(t *testing.T) {
	if ids := popoInstallationToResponse(db.ChannelInstallation{}).BoundPopoUserIDs; ids == nil || len(ids) != 0 {
		t.Fatalf("single installation response must use an empty binding array: %#v", ids)
	}
	alice, bob, bobID := popoMemberConversationFixture(t)
	list := func(userID string) []PopoInstallationResponse {
		t.Helper()
		var response struct{ Installations []PopoInstallationResponse }
		testutil.Call(t, testHandler.ListPopoInstallations, requirementRequest(userID, http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/popo/installations", nil, "", "id", testWorkspaceID)).Want(http.StatusOK).JSON(&response)
		return response.Installations
	}
	for _, installation := range list(testUserID) {
		if installation.ID == alice.install && (len(installation.BoundPopoUserIDs) != 1 || installation.BoundPopoUserIDs[0] != alice.sender) {
			t.Fatalf("current member binding missing or leaked: %+v", installation.BoundPopoUserIDs)
		}
	}
	for _, installation := range list(bobID) {
		if installation.ID == alice.install && (len(installation.BoundPopoUserIDs) != 1 || installation.BoundPopoUserIDs[0] != bob.sender) {
			t.Fatalf("other member received the wrong binding: %+v", installation.BoundPopoUserIDs)
		}
	}

	remove := func(userID string, status int) {
		t.Helper()
		testutil.Call(t, testHandler.DeletePopoBinding, requirementRequest(userID, http.MethodDelete, "/api/workspaces/"+testWorkspaceID+"/popo/installations/"+alice.install+"/binding", nil, "", "id", testWorkspaceID, "installationId", alice.install)).Want(status)
	}
	remove(bobID, http.StatusNoContent)
	if got := dbfx.Count(t, `SELECT count(*) FROM channel_user_binding WHERE installation_id=$1 AND multica_user_id=$2`, alice.install, testUserID); got != 1 {
		t.Fatal("deleting Bob's binding removed Alice's binding")
	}
	remove(testUserID, http.StatusNoContent)
	remove(testUserID, http.StatusNotFound)
}
