package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type requirementChatMessageForTest struct {
	Role    string
	Kind    string
	Content string
}

func requirementChatMessagesForTest(t *testing.T, sessionID string) []requirementChatMessageForTest {
	t.Helper()
	rows, err := testPool.Query(context.Background(),
		`SELECT role, message_kind, content FROM chat_message WHERE chat_session_id = $1`, sessionID)
	if err != nil {
		t.Fatalf("query requirement chat messages: %v", err)
	}
	defer rows.Close()
	messages := make([]requirementChatMessageForTest, 0, 2)
	for rows.Next() {
		var message requirementChatMessageForTest
		if err := rows.Scan(&message.Role, &message.Kind, &message.Content); err != nil {
			t.Fatalf("scan requirement chat message: %v", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate requirement chat messages: %v", err)
	}
	return messages
}

// The opening is the first thing a member reads in a requirement chat, so it
// follows user.language rather than a server-wide default, and the externally
// authored title is escaped before it enters markdown-rendered assistant
// content. The hidden source-context frame keeps its own kind and JSON shape.
func TestOpenRequirementOpeningFollowsUserLanguageAndEscapesTitle(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	_, agentID := newRequirementClaudePublicAgent(t, "Requirement opening locale agent")

	// One title carrying every inline metacharacter the shared escaper covers.
	title := "交付 *v2* _草稿_ `prod` [beta] <ops> ~done~ C:\\temp"
	wantEscapedTitle := "交付 \\*v2\\* \\_草稿\\_ \\`prod\\` \\[beta\\] \\<ops> \\~done\\~ C:\\\\temp"
	hiddenDescription := "source-description-that-must-not-leak"

	type openingLanguageCase struct {
		name       string
		language   string // empty leaves user.language NULL
		wantLabel  string
		wantPhrase string
	}
	cases := []openingLanguageCase{
		{name: "en", language: "en", wantLabel: "Requirement: ", wantPhrase: "continue clarifying"},
		{name: "zh-Hans", language: "zh-Hans", wantLabel: "需求：", wantPhrase: "请继续澄清"},
		{name: "legacy zh", language: "zh", wantLabel: "需求：", wantPhrase: "请继续澄清"},
		{name: "ja", language: "ja", wantLabel: "要件：", wantPhrase: "引き続き明確"},
		{name: "ko", language: "ko", wantLabel: "요구사항: ", wantPhrase: "계속 명확히"},
		{name: "fr", language: "fr", wantLabel: "Exigence : ", wantPhrase: "Continuez à clarifier"},
		// Absence is not a choice and an unknown value is not evidence of a
		// language: both render the English copy.
		{name: "empty falls back to en", wantLabel: "Requirement: ", wantPhrase: "continue clarifying"},
		{name: "unknown falls back to en", language: "de", wantLabel: "Requirement: ", wantPhrase: "continue clarifying"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			userID := dbfx.User(t, "Requirement opening "+tc.name, fmt.Sprintf("requirement-opening-%s@multica.test", uuid.NewString()))
			dbfx.Member(t, testWorkspaceID, userID, "member")
			if tc.language != "" {
				dbfx.Exec(t, `UPDATE "user" SET language = $1 WHERE id = $2`, tc.language, userID)
			}

			externalID := fmt.Sprintf("%d", time.Now().UnixNano())
			fixture := newRequirementSourceFixture(externalID)
			fixture.Snapshot.Title = title
			fixture.Snapshot.Description = hiddenDescription
			fixture.Snapshot.Digest = fixture.Snapshot.Fingerprint()
			fixture.Digest = fixture.Snapshot.Digest
			fixture.Raw = requirementJSON(fixture.Snapshot)

			detail := createRequirementForTest(t, userID, fixture.body(agentID), "opening-create-"+externalID)
			opened := openRequirementForTest(t, userID, detail.ID, 1, "opening-open-"+externalID)

			messages := requirementChatMessagesForTest(t, opened.Session.ID)
			if len(messages) != 2 {
				t.Fatalf("requirement chat has %d messages, want context + opening: %+v", len(messages), messages)
			}
			var contextMessage, openingMessage *requirementChatMessageForTest
			for i := range messages {
				switch messages[i].Kind {
				case protocol.ChatMessageKindRequirementContext:
					contextMessage = &messages[i]
				case protocol.ChatMessageKindRequirementOpening:
					openingMessage = &messages[i]
				default:
					t.Fatalf("unexpected product message kind %q in requirement chat", messages[i].Kind)
				}
			}
			if contextMessage == nil || openingMessage == nil {
				t.Fatalf("requirement chat is missing its context or opening message: %+v", messages)
			}

			// The hidden context keeps its user role and exact JSON shape, and
			// keeps carrying the source material the runtime needs.
			if contextMessage.Role != "user" {
				t.Errorf("requirement context role = %q, want user", contextMessage.Role)
			}
			var contextBody map[string]json.RawMessage
			if err := json.Unmarshal([]byte(contextMessage.Content), &contextBody); err != nil {
				t.Fatalf("requirement context is not JSON: %v\n%s", err, contextMessage.Content)
			}
			for _, key := range []string{"schema_version", "requirement_id", "source_snapshot", "source_digest"} {
				if _, ok := contextBody[key]; !ok {
					t.Fatalf("requirement context lost key %q: %s", key, contextMessage.Content)
				}
			}
			if len(contextBody) != 4 {
				t.Fatalf("requirement context structure changed, got keys %v", contextBody)
			}
			if got, want := string(contextBody["schema_version"]), "1"; got != want {
				t.Errorf("requirement context schema_version = %s, want %s", got, want)
			}
			if got, want := string(contextBody["requirement_id"]), `"`+detail.ID+`"`; got != want {
				t.Errorf("requirement context requirement_id = %s, want %s", got, want)
			}
			if got, want := string(contextBody["source_digest"]), `"`+fixture.Digest+`"`; got != want {
				t.Errorf("requirement context source_digest = %s, want %s", got, want)
			}
			if !strings.Contains(string(contextBody["source_snapshot"]), hiddenDescription) {
				t.Errorf("requirement context lost the source description: %s", contextBody["source_snapshot"])
			}

			// The visible opening is the localized product copy, escaped.
			if openingMessage.Role != "assistant" {
				t.Errorf("requirement opening role = %q, want assistant", openingMessage.Role)
			}
			if !strings.Contains(openingMessage.Content, tc.wantLabel) {
				t.Errorf("opening does not start the %q copy:\n%s", tc.name, openingMessage.Content)
			}
			if !strings.Contains(openingMessage.Content, tc.wantPhrase) {
				t.Errorf("opening is not written in the user's language (%s):\n%s", tc.name, openingMessage.Content)
			}
			if !strings.Contains(openingMessage.Content, wantEscapedTitle) {
				t.Errorf("opening does not contain the escaped title %q:\n%s", wantEscapedTitle, openingMessage.Content)
			}
			for _, rawMarkdown := range []string{"*v2*", "_草稿_", "`prod`", "[beta]", "~done~"} {
				if strings.Contains(openingMessage.Content, rawMarkdown) {
					t.Errorf("title markdown %q survived unescaped in the opening:\n%s", rawMarkdown, openingMessage.Content)
				}
			}
			// The hidden context is an input frame, not something the member
			// reads: nothing from it may appear in the visible opening.
			for _, hidden := range []string{hiddenDescription, "schema_version", "source_snapshot", fixture.Digest} {
				if strings.Contains(openingMessage.Content, hidden) {
					t.Errorf("opening leaks hidden source context %q:\n%s", hidden, openingMessage.Content)
				}
			}
			if strings.Contains(openingMessage.Content, detail.ID) {
				t.Errorf("opening leaks the requirement id:\n%s", openingMessage.Content)
			}
		})
	}
}
