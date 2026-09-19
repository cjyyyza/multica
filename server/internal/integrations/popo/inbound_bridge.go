package popo

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type BridgeInbound struct {
	ProtocolVersion int             `json:"protocol_version"`
	EventID         string          `json:"event_id"`
	RobotID         string          `json:"robot_id"`
	Sender          BridgeSender    `json:"sender"`
	Chat            BridgeChat      `json:"chat"`
	AddressedToBot  bool            `json:"addressed_to_bot"`
	Text            string          `json:"text"`
	CommandText     string          `json:"command_text"`
	Quote           *BridgeQuote    `json:"quote"`
	Media           []BridgeMedia   `json:"media"`
	Raw             json.RawMessage `json:"raw"`
}

// BridgeMedia is a descriptor only. Bytes arrive later via staging PUT.
type BridgeMedia struct {
	Index      int    `json:"index"`
	Kind       string `json:"kind"`
	Filename   string `json:"filename"`
	MimeType   string `json:"mime_type"`
	SizeBytes  int64  `json:"size_bytes"`
	PopoFileID string `json:"popo_file_id,omitempty"`
}

type BridgeSender struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type BridgeChat struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type BridgeQuote struct {
	MessageID  string `json:"message_id"`
	Text       string `json:"text"`
	SenderID   string `json:"sender_id"`
	SenderName string `json:"sender_name"`
}

const quotedContentUnavailable = "[quoted content unavailable]"

func (q *BridgeQuote) UnmarshalJSON(data []byte) error {
	if q == nil {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	q.MessageID = firstNonEmpty(
		jsonString(raw["message_id"]),
		jsonString(raw["msgId"]),
		jsonString(raw["msg_id"]),
		jsonString(raw["uuid"]),
		jsonString(raw["messageId"]),
	)
	q.Text = firstNonEmpty(
		jsonString(raw["text"]),
		jsonString(raw["notify"]),
	)
	q.SenderID = firstNonEmpty(
		jsonString(raw["sender_id"]),
		jsonString(raw["from"]),
		jsonString(raw["senderId"]),
	)
	q.SenderName = firstNonEmpty(
		jsonString(raw["sender_name"]),
		jsonString(raw["from_name"]),
		jsonString(raw["fromUserName"]),
		jsonString(raw["senderName"]),
	)
	return nil
}

func jsonString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strings.TrimSpace(strconv.FormatFloat(t, 'f', -1, 64))
	default:
		return ""
	}
}

type InboundDecision struct {
	Accepted  bool
	Duplicate bool
	Message   channel.InboundMessage
}

func (s *BridgeService) AcceptInbound(ctx context.Context, bridge db.PopoBridge, req BridgeInbound) (InboundDecision, error) {
	if req.ProtocolVersion != ProtocolVersion {
		return InboundDecision{}, ErrUnknownProtocol
	}
	eventID := strings.TrimSpace(req.EventID)
	if eventID == "" {
		return InboundDecision{}, ErrMissingEventID
	}
	senderID := strings.TrimSpace(req.Sender.ID)
	if senderID == "" {
		return InboundDecision{}, ErrMissingSender
	}
	chatID := strings.TrimSpace(req.Chat.ID)
	if chatID == "" {
		return InboundDecision{}, ErrMissingChat
	}
	robotID, err := normalizeRobotID(req.RobotID)
	if err != nil {
		return InboundDecision{}, err
	}
	inst, err := s.q.GetChannelInstallationByAppID(ctx, db.GetChannelInstallationByAppIDParams{
		ChannelType: string(TypePopo),
		AppID:       robotID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return InboundDecision{}, ErrInstallationWrong
		}
		return InboundDecision{}, err
	}
	if inst.WorkspaceID != bridge.WorkspaceID || inst.Status != "active" {
		return InboundDecision{}, ErrInstallationWrong
	}

	msg, p1OK := inboundFromBridge(robotID, util.UUIDToString(bridge.ID), req)
	if !p1OK {
		return InboundDecision{Accepted: false}, nil
	}

	_, err = s.q.InsertPopoInboundEvent(ctx, db.InsertPopoInboundEventParams{
		WorkspaceID:    bridge.WorkspaceID,
		BridgeID:       bridge.ID,
		InstallationID: inst.ID,
		EventID:        eventID,
		RobotID:        robotID,
		Accepted:       true,
		Duplicate:      false,
	})
	if err != nil {
		if isUniqueViolation(err) {
			// Bridge receipt is not engine processing. Replays still enter
			// Handle; channel_inbound_message_dedup is the execution fence.
			logTrace("popo inbound received", eventID, util.UUIDToString(inst.ID), chatID, "", "", "", "")
			return InboundDecision{Accepted: true, Duplicate: true, Message: msg}, nil
		}
		return InboundDecision{}, err
	}
	logTrace("popo inbound received", eventID, util.UUIDToString(inst.ID), chatID, "", "", "", "")
	return InboundDecision{Accepted: true, Message: msg}, nil
}

func inboundFromBridge(robotID, bridgeID string, req BridgeInbound) (channel.InboundMessage, bool) {
	chatType, ok := popoBridgeChatType(req.Chat.Type)
	if !ok {
		return channel.InboundMessage{}, false
	}
	// P2P inbound is always an interaction with the bot; groups only ingest
	// an explicit @. Unaddressed group chatter is dropped with no persist.
	if !req.AddressedToBot {
		return channel.InboundMessage{}, false
	}
	media := normalizeBridgeMedia(req.Media)
	text := strings.TrimSpace(req.Text)
	commandText := strings.TrimSpace(req.CommandText)
	if commandText == "" {
		commandText = text
	}
	if commandText == "" && len(media) == 0 {
		return channel.InboundMessage{}, false
	}
	cleaned := commandText
	forceFresh := false
	if control, ok := engine.ParseControlCommand(commandText); ok {
		cleaned = control.Body
		forceFresh = control.Kind == engine.ControlCommandFreshSession
	}
	eventID := strings.TrimSpace(req.EventID)
	chatID := strings.TrimSpace(req.Chat.ID)
	sender := strings.TrimSpace(req.Sender.ID)
	defaultEvent := eventP2P
	if chatType == channel.ChatTypeGroup {
		defaultEvent = eventGroupAt
	}
	msg := channel.InboundMessage{
		EventID:        eventID,
		MessageID:      chatID + ":" + eventID,
		Type:           inboundMsgType(cleaned, media),
		Text:           cleaned,
		CommandText:    commandText,
		AddressedToBot: true,
		ForceFresh:     forceFresh,
		Source: channel.Source{
			ChannelType:    TypePopo,
			ChatID:         chatID,
			ChatType:       chatType,
			SenderID:       sender,
			SenderStableID: sender,
		},
	}
	applyBridgeQuote(&msg, req.Quote, cleaned)
	body := msg.Text
	if len(media) > 0 {
		placeholders := make([]string, 0, len(media))
		for _, item := range media {
			placeholders = append(placeholders, mediaPlaceholder(parseMediaKind(item.Kind)))
		}
		joined := strings.Join(placeholders, "\n")
		if strings.TrimSpace(msg.Text) == "" {
			msg.Text = joined
		} else {
			msg.Text = msg.Text + "\n\n" + joined
		}
		if strings.TrimSpace(msg.CommandText) == "" {
			msg.CommandText = joined
		}
	}
	rawMeta, _ := json.Marshal(popoRawEvent{
		RobotID:    robotID,
		EventType:  firstNonEmpty(openEventType(req.Raw), defaultEvent),
		SenderName: firstNonEmpty(req.Sender.Name, sender),
		BridgeID:   strings.TrimSpace(bridgeID),
		Body:       body,
		Media:      media,
	})
	msg.Raw = rawMeta
	return msg, true
}

func inboundMsgType(text string, media []BridgeMedia) channel.MsgType {
	if strings.TrimSpace(text) != "" || len(media) == 0 {
		return channel.MsgTypeText
	}
	return parseMediaKind(media[0].Kind)
}

func normalizeBridgeMedia(in []BridgeMedia) []BridgeMedia {
	if len(in) == 0 {
		return nil
	}
	if len(in) > MaxInboundMedia {
		in = in[:MaxInboundMedia]
	}
	allZero := true
	for _, item := range in {
		if item.Index != 0 {
			allZero = false
			break
		}
	}
	out := make([]BridgeMedia, 0, len(in))
	for i, item := range in {
		item.Kind = strings.ToLower(strings.TrimSpace(item.Kind))
		if item.Kind == "" {
			item.Kind = MediaKindFile
		}
		item.Filename = cleanMediaFilename(item.Filename)
		item.MimeType = strings.TrimSpace(item.MimeType)
		item.PopoFileID = strings.TrimSpace(item.PopoFileID)
		if allZero {
			item.Index = i
		}
		if item.Index < 0 {
			continue
		}
		out = append(out, item)
	}
	return out
}

func parseMediaKind(kind string) channel.MsgType {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case MediaKindImage:
		return channel.MsgTypeImage
	case MediaKindAudio:
		return channel.MsgTypeAudio
	case MediaKindVideo:
		return channel.MsgTypeVideo
	default:
		return channel.MsgTypeFile
	}
}

func mediaPlaceholder(kind channel.MsgType) string {
	switch kind {
	case channel.MsgTypeImage:
		return "[Image]"
	case channel.MsgTypeAudio:
		return "[Audio]"
	case channel.MsgTypeVideo:
		return "[Video]"
	default:
		return "[File]"
	}
}

func mediaFailurePlaceholder(item BridgeMedia) string {
	kind := parseMediaKind(item.Kind)
	name := strings.TrimSpace(item.Filename)
	label := "file"
	if kind == channel.MsgTypeImage {
		label = "image"
	}
	if name == "" {
		return "[" + label + " — failed]"
	}
	return "[" + label + ": " + name + " — failed]"
}

func cleanMediaFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if strings.Trim(name, ".") == "" {
		return ""
	}
	return name
}

func popoBridgeChatType(raw string) (channel.ChatType, bool) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case string(channel.ChatTypeP2P):
		return channel.ChatTypeP2P, true
	case string(channel.ChatTypeGroup):
		return channel.ChatTypeGroup, true
	default:
		return "", false
	}
}

func applyBridgeQuote(msg *channel.InboundMessage, quote *BridgeQuote, instruction string) {
	if msg == nil || quote == nil {
		return
	}
	messageID := strings.TrimSpace(quote.MessageID)
	quoteText := strings.TrimSpace(quote.Text)
	if messageID == "" && quoteText == "" {
		return
	}
	if messageID != "" {
		msg.ReplyTo = &channel.ReplyCtx{MessageID: messageID}
	}
	body := quoteText
	if body == "" {
		body = quotedContentUnavailable
	}
	block := channel.FormatQuotedMessage(quote.SenderName, body)
	if block == "" {
		return
	}
	msg.HasSelectedContext = true
	if strings.TrimSpace(instruction) == "" {
		msg.Text = block
		return
	}
	msg.Text = block + "\n\n" + instruction
}

func openEventType(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var env openEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return ""
	}
	return strings.TrimSpace(env.EventType)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}
