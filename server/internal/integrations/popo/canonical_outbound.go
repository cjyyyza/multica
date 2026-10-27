package popo

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
)

func CanonicalOutboundCommand(raw json.RawMessage) (string, error) {
	value, err := DecodeCanonicalJSON(raw)
	if err != nil {
		return "", err
	}
	command, ok := value.(map[string]any)
	if !ok {
		return "", fmt.Errorf("%w: outbound command must be an object", ErrCanonicalJSON)
	}
	projection, err := CanonicalOutboundProjection(command)
	if err != nil {
		return "", err
	}
	return CanonicalJSON(projection)
}

func OutboundCommandHash(raw json.RawMessage) (string, string, error) {
	canonical, err := CanonicalOutboundCommand(raw)
	if err != nil {
		return "", "", err
	}
	return canonical, HashCanonicalOutbound(canonical), nil
}

func CanonicalOutboundProjection(command map[string]any) (map[string]any, error) {
	if err := checkObjectFields(command, []string{
		"protocol_version", "type", "command_id", "delivery_id", "recipient_binding_id",
		"idempotency_key", "lease_id", "attempt", "payload_hash", "created_at", "expires_at", "trace_id", "payload",
	}, []string{"type", "command_id", "delivery_id", "recipient_binding_id", "idempotency_key", "expires_at", "payload"}); err != nil {
		return nil, err
	}
	if version, ok := command["protocol_version"]; ok {
		integer, err := schemaInteger(version)
		if err != nil || integer != 2 {
			return nil, fmt.Errorf("%w: protocol_version must be 2", ErrCanonicalJSON)
		}
	}
	if value, ok := command["attempt"]; ok {
		// The attempt is the per-lease dispatch field, deliberately outside the
		// frozen canonical identity so payload_hash stays stable across retries.
		integer, err := schemaInteger(value)
		if err != nil || integer < 1 {
			return nil, fmt.Errorf("%w: invalid attempt", ErrCanonicalJSON)
		}
	}
	projection := make(map[string]any, 7)
	for _, key := range []string{"type", "command_id", "delivery_id", "recipient_binding_id", "idempotency_key", "expires_at"} {
		value, err := requiredSchemaString(command, key, true)
		if err != nil {
			return nil, err
		}
		projection[key] = value
	}
	if projection["idempotency_key"] != projection["delivery_id"] {
		return nil, fmt.Errorf("%w: idempotency_key must equal delivery_id", ErrCanonicalJSON)
	}
	for _, key := range []string{"lease_id", "created_at", "trace_id"} {
		if value, ok := command[key]; ok {
			if _, err := schemaString(value, key, true); err != nil {
				return nil, err
			}
		}
	}
	if value, ok := command["payload_hash"]; ok {
		hash, err := schemaString(value, "payload_hash", true)
		if err != nil || !validSHA256Hash(hash) {
			return nil, fmt.Errorf("%w: invalid payload_hash", ErrCanonicalJSON)
		}
	}
	payload, ok := command["payload"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: payload must be an object", ErrCanonicalJSON)
	}
	if err := validateOutboundPayload(payload); err != nil {
		return nil, err
	}
	projectedPayload := make(map[string]any, 9)
	for _, key := range []string{"robot_id", "chat_id", "chat_type", "text", "reply_to_message_id", "attachments", "notification", "card", "reply"} {
		if value, exists := payload[key]; exists {
			projectedPayload[key] = value
		}
	}
	projection["payload"] = projectedPayload
	return projection, nil
}

func validateOutboundPayload(payload map[string]any) error {
	if err := checkObjectFields(payload, []string{
		"robot_id", "chat_id", "chat_type", "text", "reply_to_message_id", "attachments", "notification", "card", "reply",
	}, []string{"robot_id", "chat_id", "chat_type", "text"}); err != nil {
		return err
	}
	for _, key := range []string{"robot_id", "chat_id", "chat_type"} {
		if _, err := requiredSchemaString(payload, key, true); err != nil {
			return err
		}
	}
	chatType, _ := schemaString(payload["chat_type"], "chat_type", true)
	if chatType != "p2p" && chatType != "group" {
		return fmt.Errorf("%w: invalid chat_type", ErrCanonicalJSON)
	}
	if _, err := requiredSchemaString(payload, "text", false); err != nil {
		return err
	}
	if value, ok := payload["reply_to_message_id"]; ok {
		if _, err := schemaString(value, "reply_to_message_id", false); err != nil {
			return err
		}
	}
	if value, ok := payload["attachments"]; ok {
		if err := validateOutboundAttachments(value); err != nil {
			return err
		}
	}
	if value, ok := payload["notification"]; ok {
		if err := validateOutboundNotification(value); err != nil {
			return err
		}
	}
	if value, ok := payload["card"]; ok {
		if err := validateOutboundCard(value); err != nil {
			return err
		}
	}
	if value, ok := payload["reply"]; ok {
		if err := validateOutboundReply(value); err != nil {
			return err
		}
	}
	return nil
}

func validateOutboundAttachments(value any) error {
	items, ok := value.([]any)
	if !ok {
		return fmt.Errorf("%w: attachments must be an array", ErrCanonicalJSON)
	}
	for index, item := range items {
		attachment, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: attachment %d must be an object", ErrCanonicalJSON, index)
		}
		if err := checkObjectFields(attachment, []string{"download_path", "filename", "mime_type", "size_bytes", "sha256"}, []string{"download_path", "filename", "mime_type"}); err != nil {
			return err
		}
		for _, key := range []string{"download_path", "filename"} {
			if _, err := requiredSchemaString(attachment, key, true); err != nil {
				return err
			}
		}
		if _, err := requiredSchemaString(attachment, "mime_type", false); err != nil {
			return err
		}
		if size, exists := attachment["size_bytes"]; exists {
			integer, err := schemaInteger(size)
			if err != nil || integer < 0 {
				return fmt.Errorf("%w: invalid attachment size_bytes", ErrCanonicalJSON)
			}
		}
		if digest, exists := attachment["sha256"]; exists {
			text, err := schemaString(digest, "sha256", true)
			if err != nil || len(text) != 64 || !isLowerHex(text) {
				return fmt.Errorf("%w: invalid attachment sha256", ErrCanonicalJSON)
			}
		}
	}
	return nil
}

func validateOutboundNotification(value any) error {
	notification, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: notification must be an object", ErrCanonicalJSON)
	}
	if err := checkObjectFields(notification, []string{
		"kind", "source_key", "occurrence_id", "requirement_id", "issue_id", "comment_id",
		"requirement_revision", "issue_revision", "binding_id", "route_revision",
	}, []string{"kind", "source_key", "occurrence_id", "requirement_id"}); err != nil {
		return err
	}
	kind, err := requiredSchemaString(notification, "kind", true)
	if err != nil {
		return err
	}
	switch kind {
	case "requirement_draft", "requirement_status", "requirement_blocked", "requirement_review":
	default:
		return fmt.Errorf("%w: invalid notification kind", ErrCanonicalJSON)
	}
	for _, key := range []string{"source_key", "occurrence_id", "requirement_id"} {
		if _, err := requiredSchemaString(notification, key, true); err != nil {
			return err
		}
	}
	for _, key := range []string{"issue_id", "comment_id", "binding_id"} {
		if value, exists := notification[key]; exists {
			if _, err := schemaString(value, key, false); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"requirement_revision", "issue_revision", "route_revision"} {
		if value, exists := notification[key]; exists {
			integer, err := schemaInteger(value)
			if err != nil || integer < 0 {
				return fmt.Errorf("%w: invalid %s", ErrCanonicalJSON, key)
			}
		}
	}
	return nil
}

func validateOutboundCard(value any) error {
	card, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: card must be an object", ErrCanonicalJSON)
	}
	if err := checkObjectFields(card, []string{"schema_version", "type", "title", "summary", "url"}, []string{"schema_version", "type", "title", "summary", "url"}); err != nil {
		return err
	}
	if version, err := schemaInteger(card["schema_version"]); err != nil || version != 1 {
		return fmt.Errorf("%w: card schema_version must be 1", ErrCanonicalJSON)
	}
	cardType, _ := schemaString(card["type"], "type", true)
	if cardType != "requirement_link" {
		return fmt.Errorf("%w: invalid card type", ErrCanonicalJSON)
	}
	title, err := requiredSchemaString(card, "title", false)
	if err != nil || len([]rune(title)) > 500 {
		return fmt.Errorf("%w: invalid card title", ErrCanonicalJSON)
	}
	summary, err := requiredSchemaString(card, "summary", false)
	if err != nil || len([]rune(summary)) > 4000 {
		return fmt.Errorf("%w: invalid card summary", ErrCanonicalJSON)
	}
	rawURL, err := requiredSchemaString(card, "url", true)
	if err != nil || len(rawURL) > 2048 {
		return fmt.Errorf("%w: invalid card url", ErrCanonicalJSON)
	}
	parsed, parseErr := url.Parse(rawURL)
	if parseErr != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%w: invalid card url", ErrCanonicalJSON)
	}
	return nil
}

func validateOutboundReply(value any) error {
	reply, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: reply must be an object", ErrCanonicalJSON)
	}
	if err := checkObjectFields(reply, []string{"schema", "id", "sequence", "status", "prompt", "steps"}, []string{"schema", "id", "sequence", "status", "prompt", "steps"}); err != nil {
		return err
	}
	if version, err := schemaInteger(reply["schema"]); err != nil || version != 1 {
		return fmt.Errorf("%w: reply schema must be 1", ErrCanonicalJSON)
	}
	if _, err := requiredSchemaString(reply, "id", true); err != nil {
		return err
	}
	if sequence, err := schemaInteger(reply["sequence"]); err != nil || sequence < 1 {
		return fmt.Errorf("%w: invalid reply sequence", ErrCanonicalJSON)
	}
	status, _ := schemaString(reply["status"], "status", true)
	switch status {
	case "running", "waiting", "completed", "failed", "cancelled":
	default:
		return fmt.Errorf("%w: invalid reply status", ErrCanonicalJSON)
	}
	if _, err := requiredSchemaString(reply, "prompt", false); err != nil {
		return err
	}
	steps, ok := reply["steps"].([]any)
	if !ok || len(steps) > 500 {
		return fmt.Errorf("%w: invalid reply steps", ErrCanonicalJSON)
	}
	for _, item := range steps {
		step, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: reply step must be an object", ErrCanonicalJSON)
		}
		if err := checkObjectFields(step, []string{"id", "name", "title", "status"}, []string{"id", "name", "title", "status"}); err != nil {
			return err
		}
		for _, key := range []string{"id", "name", "title"} {
			if _, err := requiredSchemaString(step, key, key == "id"); err != nil {
				return err
			}
		}
		stepStatus, _ := schemaString(step["status"], "status", true)
		switch stepStatus {
		case "pending", "running", "completed", "failed", "cancelled", "unknown":
		default:
			return fmt.Errorf("%w: invalid reply step status", ErrCanonicalJSON)
		}
	}
	return nil
}

func checkObjectFields(object map[string]any, allowed, required []string) error {
	allowedSet := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = true
	}
	for key := range object {
		if !allowedSet[key] {
			return fmt.Errorf("%w: unsupported field %s", ErrCanonicalJSON, key)
		}
	}
	for _, key := range required {
		if _, ok := object[key]; !ok {
			return fmt.Errorf("%w: missing field %s", ErrCanonicalJSON, key)
		}
	}
	return nil
}

func requiredSchemaString(object map[string]any, key string, nonEmpty bool) (string, error) {
	value, ok := object[key]
	if !ok {
		return "", fmt.Errorf("%w: missing field %s", ErrCanonicalJSON, key)
	}
	return schemaString(value, key, nonEmpty)
}

func schemaString(value any, key string, nonEmpty bool) (string, error) {
	text, ok := value.(string)
	if !ok || (nonEmpty && (strings.TrimSpace(text) == "" || strings.TrimSpace(text) != text)) {
		return "", fmt.Errorf("%w: invalid string field %s", ErrCanonicalJSON, key)
	}
	return text, nil
}

func schemaInteger(value any) (int64, error) {
	var number float64
	switch typed := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseFloat(string(typed), 64)
		if err != nil {
			return 0, err
		}
		number = parsed
	case float64:
		number = typed
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	default:
		return 0, fmt.Errorf("not a number")
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number < math.MinInt64 || number > math.MaxInt64 {
		return 0, fmt.Errorf("not an integer")
	}
	return int64(number), nil
}

func validSHA256Hash(value string) bool {
	return strings.HasPrefix(value, "sha256:") && len(value) == 71 && isLowerHex(value[7:])
}

func isLowerHex(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
