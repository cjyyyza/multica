package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/util"
)

type popoSendFunc func(ctx context.Context, executable, receiver, message string) error

// maybeNotifyIssueMentionsViaPOPO mirrors explicit member mentions from an
// agent's successfully posted comment to the local POPO account. The API
// process never invokes popo-cli; the daemon-managed CLI runs beside the local
// POPO login. Delivery is best-effort and must never change the comment result.
func maybeNotifyIssueMentionsViaPOPO(client *cli.APIClient, issueID, content string) {
	if runtime.GOOS != "windows" || !inDaemonManagedExecutionContext() {
		return
	}
	executable := strings.TrimSpace(os.Getenv("MULTICA_POPO_CLI"))
	if executable == "" {
		executable = "popo-cli"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := notifyIssueMentionsViaPOPO(ctx, client, issueID, content, executable, sendPOPOMessage); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: comment was added, but POPO notification failed: %v\n", err)
	}
}

func notifyIssueMentionsViaPOPO(ctx context.Context, client *cli.APIClient, issueID, content, executable string, send popoSendFunc) error {
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

	message := content + "\n\nIssue: " + issueLink
	var sendErrors []error
	for _, recipientID := range recipientIDs {
		receiver := emails[recipientID]
		if receiver == "" {
			sendErrors = append(sendErrors, fmt.Errorf("mentioned member %s has no email", recipientID))
			continue
		}
		if err := send(ctx, executable, receiver, message); err != nil {
			// A timeout or lost response has an unknown remote result. Never
			// retry automatically and risk a duplicate POPO message.
			sendErrors = append(sendErrors, fmt.Errorf("send to %s: %w", receiver, err))
		}
	}
	return errors.Join(sendErrors...)
}

func sendPOPOMessage(ctx context.Context, executable, receiver, message string) error {
	file, err := os.CreateTemp("", "multica-popo-message-*.txt")
	if err != nil {
		return fmt.Errorf("create POPO message file: %w", err)
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.WriteString(message); err != nil {
		file.Close()
		return fmt.Errorf("write POPO message file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close POPO message file: %w", err)
	}

	command := exec.CommandContext(ctx, executable,
		"popo", "message_send_p2p",
		"receiver="+receiver,
		"source=AGENT",
		"message=@text:"+path,
	)
	raw, err := command.Output()
	if err != nil {
		return fmt.Errorf("popo-cli message_send_p2p: %w", err)
	}
	var response struct {
		OK   bool `json:"ok"`
		Data struct {
			Data struct {
				Data struct {
					Status int `json:"status"`
				} `json:"data"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return fmt.Errorf("decode popo-cli message_send_p2p response: %w", err)
	}
	if !response.OK {
		return errors.New("popo-cli message_send_p2p returned ok=false")
	}
	if response.Data.Data.Data.Status != 1 {
		return fmt.Errorf("popo-cli message_send_p2p returned status=%d", response.Data.Data.Data.Status)
	}
	return nil
}
