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
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type BridgeInbound struct {
	ProtocolVersion int               `json:"protocol_version"`
	EventID         string            `json:"event_id"`
	RobotID         string            `json:"robot_id"`
	Sender          BridgeSender      `json:"sender"`
	Chat            BridgeChat        `json:"chat"`
	AddressedToBot  bool              `json:"addressed_to_bot"`
	Text            string            `json:"text"`
	CommandText     string            `json:"command_text"`
	Quote           *BridgeQuote      `json:"quote"`
	Media           []json.RawMessage `json:"media"`
	Raw             json.RawMessage   `json:"raw"`
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
	MessageID string `json:"message_id"`
}

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

	msg, p1OK := inboundFromBridge(robotID, req)
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
			return InboundDecision{Accepted: true, Duplicate: true, Message: msg}, nil
		}
		return InboundDecision{}, err
	}
	return InboundDecision{Accepted: true, Message: msg}, nil
}

func inboundFromBridge(robotID string, req BridgeInbound) (channel.InboundMessage, bool) {
	chatType := strings.TrimSpace(strings.ToLower(req.Chat.Type))
	text := strings.TrimSpace(req.Text)
	commandText := strings.TrimSpace(req.CommandText)
	if commandText == "" {
		commandText = text
	}
	if chatType != string(channel.ChatTypeP2P) || !req.AddressedToBot || text == "" || len(req.Media) > 0 {
		return channel.InboundMessage{}, false
	}
	cleaned := text
	forceFresh := false
	if control, ok := engine.ParseControlCommand(commandText); ok {
		cleaned = control.Body
		forceFresh = control.Kind == engine.ControlCommandFreshSession
	}
	eventID := strings.TrimSpace(req.EventID)
	chatID := strings.TrimSpace(req.Chat.ID)
	sender := strings.TrimSpace(req.Sender.ID)
	rawMeta, _ := json.Marshal(popoRawEvent{
		RobotID:    robotID,
		EventType:  firstNonEmpty(openEventType(req.Raw), eventP2P),
		SenderName: firstNonEmpty(req.Sender.Name, sender),
	})
	msg := channel.InboundMessage{
		EventID:        eventID,
		MessageID:      chatID + ":" + eventID,
		Type:           channel.MsgTypeText,
		Text:           cleaned,
		CommandText:    commandText,
		AddressedToBot: true,
		ForceFresh:     forceFresh,
		Source: channel.Source{
			ChannelType:    TypePopo,
			ChatID:         chatID,
			ChatType:       channel.ChatTypeP2P,
			SenderID:       sender,
			SenderStableID: sender,
		},
		Raw: rawMeta,
	}
	if req.Quote != nil && strings.TrimSpace(req.Quote.MessageID) != "" {
		msg.ReplyTo = &channel.ReplyCtx{MessageID: strings.TrimSpace(req.Quote.MessageID)}
	}
	return msg, true
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
