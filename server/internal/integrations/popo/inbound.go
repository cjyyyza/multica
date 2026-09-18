package popo

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
)

// Event types actually routed by dj01bot inbound_ops._handle_popo_message_event.
const (
	eventP2P     = "IM_P2P_TO_ROBOT_MSG"
	eventGroupAt = "IM_CHAT_TO_ROBOT_AT_MSG"
	eventGroup   = "IM_CHAT_TO_ROBOT_MSG"
)

var imgTag = regexp.MustCompile(`(?i)\[img\].*?\[/img\]`)

type openEnvelope struct {
	EventType string          `json:"eventType"`
	EventData json.RawMessage `json:"eventData"`
	UUID      string          `json:"uuid"`
}

type openEventData struct {
	From      string `json:"from"`
	FromNick  string `json:"fromNick"`
	SessionID string `json:"sessionId"`
	Notify    string `json:"notify"`
	Content   string `json:"content"`
	UUID      string `json:"uuid"`
	MsgID     string `json:"msgId"`
	MessageID string `json:"messageId"`
	MsgType   int    `json:"msgType"`
}

type popoRawEvent struct {
	RobotID    string `json:"robot_id"`
	EventType  string `json:"event_type"`
	SenderName string `json:"sender_name,omitempty"`
}

// InboundFromOpenEvent normalizes a POPO Open webhook/websocket envelope
// using the field names dj01bot already reads. ok=false means drop.
func InboundFromOpenEvent(robotID string, raw json.RawMessage) (channel.InboundMessage, bool) {
	var env openEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return channel.InboundMessage{}, false
	}
	eventType := strings.TrimSpace(env.EventType)
	if eventType != eventP2P && eventType != eventGroupAt && eventType != eventGroup {
		return channel.InboundMessage{}, false
	}
	data, ok := decodeEventData(env.EventData)
	if !ok {
		return channel.InboundMessage{}, false
	}
	sender := strings.TrimSpace(data.From)
	if sender == "" {
		return channel.InboundMessage{}, false
	}
	chatID := sender
	chatType := channel.ChatTypeP2P
	addressed := true
	if eventType != eventP2P {
		chatID = strings.TrimSpace(data.SessionID)
		chatType = channel.ChatTypeGroup
		addressed = eventType == eventGroupAt
	}
	if chatID == "" {
		return channel.InboundMessage{}, false
	}
	text := flattenNotify(data.Notify)
	if text == "" {
		text = flattenNotify(data.Content)
	}
	if data.MsgType != 0 && data.MsgType != 1 {
		return channel.InboundMessage{}, false
	}
	if text == "" {
		return channel.InboundMessage{}, false
	}
	cleaned := strings.TrimSpace(text)
	commandText := cleaned
	forceFresh := false
	if control, ok := engine.ParseControlCommand(cleaned); ok {
		cleaned = control.Body
		forceFresh = control.Kind == engine.ControlCommandFreshSession
	}
	messageID := firstNonEmpty(data.UUID, data.MsgID, data.MessageID, env.UUID)
	if messageID == "" {
		messageID = eventType + ":" + chatID + ":" + sender + ":" + cleaned
	}
	eventID := firstNonEmpty(env.UUID, messageID)
	rawMeta, _ := json.Marshal(popoRawEvent{
		RobotID:    robotID,
		EventType:  eventType,
		SenderName: firstNonEmpty(data.FromNick, sender),
	})
	return channel.InboundMessage{
		EventID:        eventID,
		MessageID:      chatID + ":" + messageID,
		Type:           channel.MsgTypeText,
		Text:           cleaned,
		CommandText:    commandText,
		AddressedToBot: addressed,
		ForceFresh:     forceFresh,
		Source: channel.Source{
			ChannelType:    TypePopo,
			ChatID:         chatID,
			ChatType:       chatType,
			SenderID:       sender,
			SenderStableID: sender,
		},
		Raw: rawMeta,
	}, true
}

func decodeEventData(raw json.RawMessage) (openEventData, bool) {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 {
		return openEventData{}, false
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return openEventData{}, false
		}
		raw = []byte(s)
	}
	var data openEventData
	if err := json.Unmarshal(raw, &data); err != nil {
		return openEventData{}, false
	}
	return data, true
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func flattenNotify(text string) string {
	return strings.TrimSpace(imgTag.ReplaceAllString(text, ""))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
