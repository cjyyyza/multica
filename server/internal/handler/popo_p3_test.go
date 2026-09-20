package handler

import (
	"context"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

func TestPopoGroupMentionCreatesGroupSession(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	e := popoP2Setup(t)
	groupID := "group-" + e.bridgeID[:8]
	dropped := e.inboundChat(t, "evt-group-plain", "noise in the room", groupID, "group", false, nil)
	if dropped["accepted"] != false {
		t.Fatalf("unaddressed group = %+v", dropped)
	}

	accepted := e.inboundChat(t, "evt-group-at", "hello group", groupID, "group", true, nil)
	if accepted["accepted"] != true {
		t.Fatalf("addressed group = %+v", accepted)
	}
	e.inbound(t, "evt-p2p-hello", "hello p2p", nil)

	var groupType, groupSession, p2pType, p2pSession string
	if err := testPool.QueryRow(context.Background(), `
		SELECT chat_type, chat_session_id::text
		FROM channel_chat_session_binding
		WHERE installation_id = $1 AND channel_chat_id = $2 AND retired_at IS NULL
	`, e.install, groupID).Scan(&groupType, &groupSession); err != nil {
		t.Fatalf("load group binding: %v", err)
	}
	if groupType != string(channel.ChatTypeGroup) || groupSession == "" {
		t.Fatalf("group binding type=%q session=%q", groupType, groupSession)
	}
	if err := testPool.QueryRow(context.Background(), `
		SELECT chat_type, chat_session_id::text
		FROM channel_chat_session_binding
		WHERE installation_id = $1 AND channel_chat_id = $2 AND retired_at IS NULL
	`, e.install, e.sender).Scan(&p2pType, &p2pSession); err != nil {
		t.Fatalf("load p2p binding: %v", err)
	}
	if p2pType != string(channel.ChatTypeP2P) || p2pSession == "" || p2pSession == groupSession {
		t.Fatalf("p2p binding type=%q session=%q group=%q", p2pType, p2pSession, groupSession)
	}
}

func TestPopoQuotedIssueCommandIsNotExecuted(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	e := popoP2Setup(t)
	quotedTitle := "Quoted-from-history-" + e.bridgeID[:8]
	e.inbound(t, "evt-quoted-issue", "please continue", map[string]string{
		"message_id":  "hist-" + e.bridgeID[:8],
		"text":        "/issue " + quotedTitle,
		"sender_name": "Bob",
	})

	if n := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, quotedTitle); n != 0 {
		t.Fatalf("quoted /issue created %d issues", n)
	}
	var content string
	if err := testPool.QueryRow(context.Background(), `
		SELECT message.content
		FROM channel_chat_session_binding AS binding
		JOIN chat_message AS message ON message.chat_session_id = binding.chat_session_id
		WHERE binding.installation_id = $1 AND binding.channel_chat_id = $2
		  AND binding.retired_at IS NULL AND message.role = 'user'
		ORDER BY message.created_at DESC
		LIMIT 1
	`, e.install, e.sender).Scan(&content); err != nil {
		t.Fatalf("load quoted chat message: %v", err)
	}
	wantQuote := channel.FormatQuotedMessage("Bob", "/issue "+quotedTitle)
	if !strings.Contains(content, wantQuote) || !strings.Contains(content, "please continue") {
		t.Fatalf("stored text missing quote or user body: %q", content)
	}
}

func TestPopoNewStartsANewChat(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	e := popoP2Setup(t)
	e.inbound(t, "evt-new-first", "first topic", nil)

	var firstSession string
	var firstRevision int64
	if err := testPool.QueryRow(context.Background(), `
		SELECT chat_session_id::text, route_revision
		FROM channel_chat_session_binding
		WHERE installation_id = $1 AND channel_chat_id = $2 AND retired_at IS NULL
	`, e.install, e.sender).Scan(&firstSession, &firstRevision); err != nil {
		t.Fatalf("load first route: %v", err)
	}

	e.inbound(t, "evt-new-second", "/new second topic", nil)

	var secondSession string
	var secondRevision int64
	if err := testPool.QueryRow(context.Background(), `
		SELECT chat_session_id::text, route_revision
		FROM channel_chat_session_binding
		WHERE installation_id = $1 AND channel_chat_id = $2 AND retired_at IS NULL
	`, e.install, e.sender).Scan(&secondSession, &secondRevision); err != nil {
		t.Fatalf("load /new route: %v", err)
	}
	if secondSession == firstSession {
		t.Fatal("/new kept the old chat_session")
	}
	if secondRevision != firstRevision+1 {
		t.Fatalf("route revision %d -> %d", firstRevision, secondRevision)
	}
	if n := dbfx.Count(t, `
		SELECT count(*) FROM channel_chat_session_binding
		WHERE installation_id = $1 AND channel_chat_id = $2 AND chat_session_id = $3 AND retired_at IS NOT NULL
	`, e.install, e.sender, firstSession); n != 1 {
		t.Fatalf("old /new route not retired, rows=%d", n)
	}
	var stored string
	if err := testPool.QueryRow(context.Background(), `
		SELECT content FROM chat_message
		WHERE chat_session_id = $1 AND role = 'user'
		ORDER BY created_at DESC LIMIT 1
	`, secondSession).Scan(&stored); err != nil {
		t.Fatalf("load /new message: %v", err)
	}
	if stored != "second topic" {
		t.Fatalf("/new stored %q, want the command body without the directive", stored)
	}
}

func TestPopoClearSetsFreshContext(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	e := popoP2Setup(t)
	e.inbound(t, "evt-clear-first", "hello", nil)

	var sessionID string
	var firstContext int64
	if err := testPool.QueryRow(context.Background(), `
		SELECT chat_session_id::text, context_revision
		FROM channel_chat_session_binding
		WHERE installation_id = $1 AND channel_chat_id = $2 AND retired_at IS NULL
	`, e.install, e.sender).Scan(&sessionID, &firstContext); err != nil {
		t.Fatalf("load first context: %v", err)
	}

	e.inbound(t, "evt-clear-second", "/clear next question", nil)

	var sameSession string
	var nextContext int64
	if err := testPool.QueryRow(context.Background(), `
		SELECT chat_session_id::text, context_revision
		FROM channel_chat_session_binding
		WHERE installation_id = $1 AND channel_chat_id = $2 AND retired_at IS NULL
	`, e.install, e.sender).Scan(&sameSession, &nextContext); err != nil {
		t.Fatalf("load /clear context: %v", err)
	}
	if sameSession != sessionID {
		t.Fatalf("/clear changed chat_session %s -> %s", sessionID, sameSession)
	}
	if nextContext <= firstContext {
		t.Fatalf("context revision %d -> %d, want a fresh generation", firstContext, nextContext)
	}
	var stored string
	var forceFresh bool
	if err := testPool.QueryRow(context.Background(), `
		SELECT message.content, task.force_fresh_session
		FROM chat_message AS message
		JOIN agent_task_queue AS task ON task.chat_session_id = message.chat_session_id
		WHERE message.chat_session_id = $1 AND message.role = 'user' AND message.content = 'next question'
		ORDER BY task.created_at DESC
		LIMIT 1
	`, sessionID).Scan(&stored, &forceFresh); err != nil {
		t.Fatalf("load /clear turn: %v", err)
	}
	if stored != "next question" || !forceFresh {
		t.Fatalf("/clear turn content=%q fresh=%v", stored, forceFresh)
	}
}
