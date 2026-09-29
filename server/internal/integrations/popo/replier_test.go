package popo

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
)

func TestInvokeDeniedRepliesOnlyToTheSender(t *testing.T) {
	for _, chatType := range []channel.ChatType{channel.ChatTypeP2P, channel.ChatTypeGroup} {
		t.Run(string(chatType), func(t *testing.T) {
			queue := &replyQueue{}
			replier := NewOutboundReplier(OutboundReplierConfig{Queue: queue})
			msg := channel.InboundMessage{}
			msg.Source.ChatType = chatType
			msg.Source.ChatID = "sender@example.test"
			msg.Source.SenderID = "sender@example.test"
			if chatType == channel.ChatTypeGroup {
				msg.Source.ChatID = "private-agent-group"
			}
			replier.Reply(context.Background(), engine.ResolvedInstallation{}, msg, engine.Result{
				Outcome: engine.OutcomeInvokeDenied,
			})
			if len(queue.items) != 1 {
				t.Fatalf("queued %d replies, want one refusal", len(queue.items))
			}
			item := queue.items[0]
			if item.ChatType != "p2p" || item.ChatID != msg.Source.SenderID || item.Content != msgInvokeDenied {
				t.Fatalf("refusal must reach only the sender: %+v", item)
			}
		})
	}
}
