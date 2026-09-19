package popo

import "log/slog"

// logTrace writes correlation fields for enqueue/receive. Never include
// token, pairing_code, message body, or other secrets.
func logTrace(msg, eventID, installationID, chatID, issueID, taskID, deliveryID, remoteMessageID string) {
	slog.Info(msg,
		"event_id", eventID,
		"installation_id", installationID,
		"chat_id", chatID,
		"issue_id", issueID,
		"task_id", taskID,
		"delivery_id", deliveryID,
		"remote_message_id", remoteMessageID,
	)
}
