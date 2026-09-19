package popo

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

func TestInboundFromOpenEventP2P(t *testing.T) {
	raw := json.RawMessage(`{
		"eventType":"IM_P2P_TO_ROBOT_MSG",
		"uuid":"evt-1",
		"eventData":{"from":"yujian01@corp.netease.com","notify":"/issue Fix login","sessionId":"ignored"}
	}`)
	msg, ok := InboundFromOpenEvent("default", raw)
	if !ok {
		t.Fatal("expected inbound")
	}
	if msg.Source.ChatType != channel.ChatTypeP2P || msg.Source.ChatID != "yujian01@corp.netease.com" {
		t.Fatalf("chat = %+v", msg.Source)
	}
	if !msg.AddressedToBot || msg.Text != "/issue Fix login" {
		t.Fatalf("text/addressed = %q %v", msg.Text, msg.AddressedToBot)
	}
	var meta popoRawEvent
	if err := json.Unmarshal(msg.Raw, &meta); err != nil || meta.RobotID != "default" {
		t.Fatalf("raw = %+v err=%v", meta, err)
	}
}

func TestInboundFromOpenEventGroupMentionOnly(t *testing.T) {
	plain := json.RawMessage(`{
		"eventType":"IM_CHAT_TO_ROBOT_MSG",
		"eventData":{"from":"a@corp.netease.com","sessionId":"123","notify":"hello"}
	}`)
	msg, ok := InboundFromOpenEvent("bot-a", plain)
	if !ok {
		t.Fatal("plain group text should still normalize so the engine can drop it")
	}
	if msg.AddressedToBot {
		t.Fatal("un-@ group message must not be addressed")
	}

	at := json.RawMessage(`{
		"eventType":"IM_CHAT_TO_ROBOT_AT_MSG",
		"eventData":{"from":"a@corp.netease.com","sessionId":"123","content":"/new hello"}
	}`)
	msg, ok = InboundFromOpenEvent("bot-a", at)
	if !ok || !msg.AddressedToBot || msg.Source.ChatID != "123" {
		t.Fatalf("at-msg = ok=%v addressed=%v chat=%s", ok, msg.AddressedToBot, msg.Source.ChatID)
	}
}

func TestInboundFromOpenEventDropsNonIM(t *testing.T) {
	raw := json.RawMessage(`{"eventType":"ACTION","eventData":{"from":"a"}}`)
	if _, ok := InboundFromOpenEvent("default", raw); ok {
		t.Fatal("ACTION must not reach the engine")
	}
}

func TestInboundFromOpenEventStripsImgTags(t *testing.T) {
	raw := json.RawMessage(`{
		"eventType":"IM_P2P_TO_ROBOT_MSG",
		"eventData":{"from":"a@corp.netease.com","notify":"see [img]http://x/y.png[/img] later"}
	}`)
	msg, ok := InboundFromOpenEvent("default", raw)
	if !ok || msg.Text != "see  later" {
		t.Fatalf("text=%q ok=%v", msg.Text, ok)
	}
}

func TestNormalizeRobotIDDefaultsEmpty(t *testing.T) {
	got, err := normalizeRobotID("")
	if err != nil || got != defaultRobotID {
		t.Fatalf("empty robot id = %q %v", got, err)
	}
	got, err = normalizeRobotID("  ws-bot  ")
	if err != nil || got != "ws-bot" {
		t.Fatalf("trim robot id = %q %v", got, err)
	}
}

func TestInboundFromBridgeP2PTextOnly(t *testing.T) {
	msg, ok := inboundFromBridge("default", BridgeInbound{
		EventID:        "evt-1",
		RobotID:        "default",
		Sender:         BridgeSender{ID: "alice@corp.netease.com", Name: "Alice"},
		Chat:           BridgeChat{ID: "alice@corp.netease.com", Type: "p2p"},
		AddressedToBot: true,
		Text:           "hello",
		CommandText:    "hello",
	})
	if !ok || msg.Source.ChatType != channel.ChatTypeP2P || msg.Text != "hello" {
		t.Fatalf("p2p = ok=%v msg=%+v", ok, msg)
	}

	if _, ok := inboundFromBridge("default", BridgeInbound{
		EventID:        "evt-2",
		Sender:         BridgeSender{ID: "alice@corp.netease.com"},
		Chat:           BridgeChat{ID: "group-1", Type: "group"},
		AddressedToBot: true,
		Text:           "hello",
	}); ok {
		t.Fatal("group chat must be rejected in P1")
	}
	if _, ok := inboundFromBridge("default", BridgeInbound{
		EventID:        "evt-3",
		Sender:         BridgeSender{ID: "alice@corp.netease.com"},
		Chat:           BridgeChat{ID: "alice@corp.netease.com", Type: "p2p"},
		AddressedToBot: false,
		Text:           "hello",
	}); ok {
		t.Fatal("unaddressed p2p must be rejected in P1")
	}
}

func TestInboundFromBridgeAcceptsPOPOQuoteShapes(t *testing.T) {
	for _, raw := range []string{
		`{"message_id":"mid-1"}`,
		`{"msgId":"mid-1"}`,
		`{"uuid":"mid-1"}`,
		`{"messageId":"mid-1"}`,
	} {
		var quote BridgeQuote
		if err := json.Unmarshal([]byte(raw), &quote); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		if quote.MessageID != "mid-1" {
			t.Fatalf("quote %s = %+v", raw, quote)
		}
	}
	msg, ok := inboundFromBridge("default", BridgeInbound{
		EventID:        "evt-q",
		Sender:         BridgeSender{ID: "alice@corp.netease.com"},
		Chat:           BridgeChat{ID: "alice@corp.netease.com", Type: "p2p"},
		AddressedToBot: true,
		Text:           "continue",
		CommandText:    "continue",
		Quote:          &BridgeQuote{MessageID: "mid-1"},
	})
	if !ok || msg.ReplyTo == nil || msg.ReplyTo.MessageID != "mid-1" {
		t.Fatalf("quoted inbound = ok=%v reply=%+v", ok, msg.ReplyTo)
	}
}
