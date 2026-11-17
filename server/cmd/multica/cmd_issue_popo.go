package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/notify"
	"github.com/multica-ai/multica/server/internal/util"
)

// maybeNotifyIssueMentionsViaPOPO mirrors explicit member mentions from an
// agent's successfully posted comment as the configured work partner. The
// daemon-managed CLI runs beside the local POPO login. Delivery is best-effort
// and must never change the comment result.
func maybeNotifyIssueMentionsViaPOPO(client *cli.APIClient, issueID, content string) {
	if runtime.GOOS != "windows" || !inDaemonManagedExecutionContext() {
		return
	}
	sender := notify.Popo{Bin: strings.TrimSpace(os.Getenv("MULTICA_POPO_CLI"))}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := notifyIssueMentionsViaPOPO(ctx, client, issueID, content, sender); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: comment was added, but POPO notification failed: %v\n", err)
	}
}

func notifyIssueMentionsViaPOPO(ctx context.Context, client *cli.APIClient, issueID, content string, sender notify.Sender) error {
	var recipientIDs []string
	for _, mention := range util.ParseMentions(content) {
		if mention.Type == "member" {
			recipientIDs = append(recipientIDs, mention.ID)
		}
	}
	if len(recipientIDs) == 0 {
		return nil
	}

	var issue struct {
		ID                      string  `json:"id"`
		Identifier              string  `json:"identifier"`
		WorkspaceID             string  `json:"workspace_id"`
		RequirementID           *string `json:"requirement_id"`
		RequirementBindingKnown bool    `json:"requirement_binding_known"`
	}
	if err := client.GetJSON(ctx, "/api/issues/"+url.PathEscape(issueID), &issue); err != nil {
		return fmt.Errorf("read issue for POPO notification: %w", err)
	}
	// Requirement blockers already have a durable, dedicated POPO outbox.
	if issue.RequirementID != nil && *issue.RequirementID != "" {
		return nil
	}
	if !issue.RequirementBindingKnown {
		return errors.New("server did not confirm the issue requirement binding")
	}

	notificationConfig, _, err := notify.LoadJSONConfig(strings.TrimSpace(os.Getenv("MULTICA_NOTIFY_CONFIG")))
	if err != nil {
		return fmt.Errorf("load work partner from MULTICA_NOTIFY_CONFIG: %w", err)
	}
	partner, err := notificationConfig.Target("")
	if err != nil {
		return fmt.Errorf("select work partner for POPO notification: %w", err)
	}

	var config struct {
		DaemonAppURL string `json:"daemon_app_url"`
	}
	if err := client.GetJSON(ctx, "/api/config", &config); err != nil {
		return fmt.Errorf("read app URL for POPO notification: %w", err)
	}
	var workspace struct {
		Slug string `json:"slug"`
	}
	if err := client.GetJSON(ctx, "/api/workspaces/"+url.PathEscape(issue.WorkspaceID), &workspace); err != nil {
		return fmt.Errorf("read workspace for POPO notification: %w", err)
	}
	issueLink := channel.IssueWebLink(config.DaemonAppURL, workspace.Slug, issue.Identifier)
	if issueLink == "" {
		return errors.New("server did not provide enough information to build the issue link")
	}

	var members []struct {
		UserID string `json:"user_id"`
		Email  string `json:"email"`
	}
	if err := client.GetJSON(ctx, "/api/workspaces/"+url.PathEscape(issue.WorkspaceID)+"/members", &members); err != nil {
		return fmt.Errorf("read member emails for POPO notification: %w", err)
	}
	emails := make(map[string]string, len(members))
	for _, member := range members {
		emails[member.UserID] = strings.TrimSpace(member.Email)
	}

	notification := notify.Notification{Message: content + "\n\nIssue: " + issueLink}
	var sendErrors []error
	for _, recipientID := range recipientIDs {
		receiver := emails[recipientID]
		if receiver == "" {
			sendErrors = append(sendErrors, fmt.Errorf("mentioned member %s has no email", recipientID))
			continue
		}
		// Only the sender comes from deployment routing; mentions determine the
		// private recipients, never the configured group or its mention list.
		target := notify.Target{RobotName: partner.RobotName, Receiver: receiver}
		if _, err := sender.Send(ctx, target, notification); err != nil {
			// A timeout or lost response has an unknown remote result. Never
			// retry automatically and risk a duplicate POPO message.
			sendErrors = append(sendErrors, fmt.Errorf("send to %s: %w", receiver, err))
		}
	}
	return errors.Join(sendErrors...)
}
