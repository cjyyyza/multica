package popo

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

func TestPopoGroupConversationContextIsMemberScoped(t *testing.T) {
	message := channel.InboundMessage{Source: channel.Source{ChatID: "shared-group", ChatType: channel.ChatTypeGroup, SenderID: "alice"}}
	aliceID := pgtype.UUID{Bytes: [16]byte{101}, Valid: true}
	bobID := pgtype.UUID{Bytes: [16]byte{102}, Valid: true}
	alice, raw := popoSessionRouting(message, aliceID)
	message.Source.SenderID = "bob"
	bob, _ := popoSessionRouting(message, bobID)
	if alice == bob {
		t.Fatal("different group members share a model conversation")
	}
	message.Source.SenderID = "alice"
	rebound, _ := popoSessionRouting(message, bobID)
	if rebound == alice {
		t.Fatal("a POPO account rebound to another Multica user reused the old route")
	}
	reopened, _ := popoSessionRouting(message, aliceID)
	if reopened != alice {
		t.Fatal("the same member did not recover their conversation")
	}
	var config popoBindingConfig
	if err := json.Unmarshal(raw, &config); err != nil || config.ChatID != "shared-group" {
		t.Fatalf("the outbound destination must remain the real group: %s, %v", raw, err)
	}
}

func TestPopoMemberCommandsSeparateControlsFromDraftInput(t *testing.T) {
	for _, test := range []struct {
		input, action string
		invalid       bool
	}{
		{"/help", "help", false}, {"/mode chat", "mode", false}, {"/mode requirement", "mode", false},
		{"/requirement status", "status", false}, {"/requirement list", "list", false}, {"/需求 打开 req-id", "open", false},
		{"/requirement stauts", "", true}, {"/requirement approve", "", true}, {"/requirement new", "", true}, {"/requirement help extra", "", true},
		{"/requirement new https://dj01.pm.netease.com/issues/1 New idea\nconstraints", "new", false},
	} {
		t.Run(test.input, func(t *testing.T) {
			command, handled, err := ParseMemberCommand(test.input)
			if !handled || (err != nil) != test.invalid || command.Action != test.action {
				t.Fatalf("command=%+v handled=%v err=%v", command, handled, err)
			}
			if command.Action == "new" && (command.URL != "https://dj01.pm.netease.com/issues/1" || command.Body != "New idea\nconstraints") {
				t.Fatalf("draft input changed: %+v", command)
			}
		})
	}
	for _, text := range []string{"ordinary idea", "/issue task", "/reply MUL-1 answer", "/status MUL-1", "/stop MUL-1", "/yixiezuo preview https://example.test"} {
		if _, handled, _ := ParseMemberCommand(text); handled {
			t.Fatalf("member controls swallowed %q", text)
		}
	}
}

func TestReadMemberConversationPreservesLegacyDefaultUntilExplicitSelection(t *testing.T) {
	legacy := []byte(`{"popo_default_mode":"requirement"}`)
	if got := ReadMemberConversation(legacy, "group:shared"); got.Mode != ConversationRequirement {
		t.Fatalf("legacy default = %q", got.Mode)
	}
	selected := []byte(`{"popo_default_mode":"requirement","popo_conversations":{"group:shared":{"mode":"chat"}}}`)
	if got := ReadMemberConversation(selected, "group:shared"); got.Mode != ConversationChat {
		t.Fatalf("explicit chat did not override legacy default: %+v", got)
	}
	if got := ReadMemberConversation([]byte(`{}`), "group:shared"); got.Mode != ConversationChat {
		t.Fatalf("new binding default = %q", got.Mode)
	}
}
