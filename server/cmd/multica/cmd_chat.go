package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var chatCmd = &cobra.Command{
	Use:   "chat",
	Short: "Start a chat or read the current conversation",
}

var chatCreateCmd = &cobra.Command{
	Use:     "create",
	Aliases: []string{"new"},
	Short:   "Start a new chat with an agent",
	Long: `Create a chat session with an agent, then send the opening prompt when one is given.

--agent accepts an agent name (case-insensitive substring) or a full UUID.
The prompt is sent as the session's first user turn, which queues an agent run
immediately; the command returns once the run is queued, not when the agent
replies. Omit --prompt to create an empty session. --title names the chat and
is kept. With a prompt, that name is written after the opening message is
accepted, so automatic titling does not replace it. On an empty session the
name is written after the session is created, and a later first message keeps
it. Omit --title and the server titles the chat from the first message. The
command prints the title at that moment: the derived title, unless automatic
titling has already finished.

"multica chat history" and "multica chat thread" do not read the session this
command creates. An agent uses them inside a chat task to read the conversation
that task is handling.`,
	Example: `  # Start a chat and send the opening message
  multica chat create --agent "Backend Agent" --prompt "Investigate the failing deploy"

  # Create an empty chat with an agent ID
  multica chat create --agent 7b37ce6a-b06b-43ed-b4ae-6bb7eacc67a4`,
	Args: cobra.NoArgs,
	RunE: runChatCreate,
}

var chatHistoryCmd = &cobra.Command{
	Use:   "history",
	Short: "Overview of the channel this conversation is in (messages + thread list)",
	Long: `Show the overview of the chat channel (e.g. Slack) this conversation is in: the
recent top-level messages, and for each thread its thread_id, reply_count, and
latest_reply. It does NOT expand thread contents — it is the table of contents.

To read a specific thread's messages, take a thread_id from here and run
"multica chat thread <thread_id>".

It is the SAME command regardless of which channel the conversation came from,
and it reads only the conversation you are currently running for — it cannot
read any other session or channel.`,
	Args: cobra.NoArgs,
	RunE: runChatHistory,
}

var chatThreadCmd = &cobra.Command{
	Use:   "thread [id]",
	Short: "Read one thread's messages (the current thread, or a specific id)",
	Long: `Read the messages of a single thread.

With no id, read the thread you are currently in (the one you were @mentioned in).
With an id — a thread_id from "multica chat history" — read that specific thread.
Either way the thread is within the channel you are in; you cannot read another
channel.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runChatThread,
}

func init() {
	for _, c := range []*cobra.Command{chatHistoryCmd, chatThreadCmd} {
		c.Flags().Int("limit", 0, "Maximum number of messages to return (the server clamps the range)")
		c.Flags().String("before", "", "Opaque cursor (a next_cursor from a prior page) to read older messages")
		c.Flags().String("output", "json", "Output format: table or json")
	}

	// chat create
	chatCreateCmd.Flags().String("agent", "", "Agent name or ID (required)")
	chatCreateCmd.Flags().String("prompt", "", "Opening message (decodes \\n, \\r, \\t, \\\\; use --prompt-file for multi-line bodies or to preserve literal backslashes)")
	chatCreateCmd.Flags().Bool("prompt-stdin", false, "Read the opening message from stdin (preserves multi-line content verbatim)")
	chatCreateCmd.Flags().String("prompt-file", "", "Read the opening message from a UTF-8 file (preserves multi-line content verbatim). The path must be inside the current working directory unless --allow-external-file is set")
	chatCreateCmd.Flags().Bool("allow-external-file", false, "Allow --prompt-file to read a path outside the current working directory")
	chatCreateCmd.Flags().String("title", "", "Chat title, kept as given. With a prompt, applied after the opening message is accepted")
	chatCreateCmd.Flags().String("output", "json", "Output format: table or json")

	chatCmd.AddCommand(chatCreateCmd)
	chatCmd.AddCommand(chatHistoryCmd)
	chatCmd.AddCommand(chatThreadCmd)
}

func runChatHistory(cmd *cobra.Command, _ []string) error {
	resp, err := fetchChatRead(cmd, "/api/chat/history", "")
	if err != nil {
		return err
	}
	return renderChatRead(cmd, resp, true)
}

func runChatThread(cmd *cobra.Command, args []string) error {
	threadID := ""
	if len(args) == 1 {
		threadID = args[0]
	}
	resp, err := fetchChatRead(cmd, "/api/chat/thread", threadID)
	if err != nil {
		return err
	}
	return renderChatRead(cmd, resp, false)
}

// chatCreateResult is the CLI-owned shape for `chat create`. It stays flat so
// scripts can read the session and the opening turn without walking a nested
// server object; message_id/task_id appear only when a prompt was sent.
type chatCreateResult struct {
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	Title     string `json:"title,omitempty"`
	Status    string `json:"status,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	// Queued is nil when no prompt was sent. After a prompt, true means this
	// turn is waiting behind another turn in the session; false means it is
	// not. The task stays queued until a runtime claims it either way.
	Queued *bool `json:"queued,omitempty"`
}

// chatCreateTitleMaxRunes matches the cap UpdateChatSession enforces.
const chatCreateTitleMaxRunes = 200

func runChatCreate(cmd *cobra.Command, _ []string) error {
	agentRef, _ := cmd.Flags().GetString("agent")
	if strings.TrimSpace(agentRef) == "" {
		return fmt.Errorf("--agent is required (agent name or ID)")
	}
	title, err := chatCreateTitle(cmd)
	if err != nil {
		return err
	}
	prompt, hasPrompt, err := resolveTextFlag(cmd, "prompt")
	if err != nil {
		return err
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	agentID, err := resolveAgent(ctx, client, strings.TrimSpace(agentRef))
	if err != nil {
		return fmt.Errorf("resolve agent: %w", err)
	}

	// A title on create is the first-party seed the opening message may
	// replace. Write the caller's name afterwards: after the send when there
	// is a prompt, otherwise once the empty session exists.
	body := map[string]any{"agent_id": agentID}
	var session map[string]any
	if err := client.PostJSON(ctx, "/api/chat/sessions", body, &session); err != nil {
		return fmt.Errorf("create chat session: %w", err)
	}
	sessionID := strVal(session, "id")
	if sessionID == "" {
		return fmt.Errorf("create chat session: response is missing the session id")
	}

	var sent map[string]any
	if hasPrompt {
		path := "/api/chat/sessions/" + url.PathEscape(sessionID) + "/messages"
		if err := client.PostJSON(ctx, path, map[string]any{"content": prompt}, &sent); err != nil {
			// The session exists at this point. WithUserMessage keeps the id in
			// the default error line; a plain wrap is dropped by FormatError
			// whenever the cause is an HTTP or network error.
			return chatPromptSendError(sessionID, err)
		}
		if title != "" {
			if err := applyChatCreateTitle(ctx, client, session, sessionID, title); err != nil {
				return chatTitleSetError(sessionID, true, err)
			}
		} else {
			// Send initializes an empty title from the first message, but that
			// title is not on the send response. Re-read it. A failure here must
			// not fail the command: the run is already queued.
			refreshChatCreateTitle(ctx, client, session, sessionID)
		}
	} else if title != "" {
		if err := applyChatCreateTitle(ctx, client, session, sessionID, title); err != nil {
			return chatTitleSetError(sessionID, false, err)
		}
	}

	return renderChatCreate(cmd, session, sent)
}

func chatCreateTitle(cmd *cobra.Command) (string, error) {
	if !cmd.Flags().Changed("title") {
		return "", nil
	}
	title, _ := cmd.Flags().GetString("title")
	title = strings.TrimSpace(title)
	if title == "" {
		return "", fmt.Errorf("--title is empty")
	}
	if len([]rune(title)) > chatCreateTitleMaxRunes {
		return "", fmt.Errorf("--title must be at most %d characters", chatCreateTitleMaxRunes)
	}
	return title, nil
}

func chatPromptSendError(sessionID string, cause error) error {
	return chatPartialError(sessionID, cause,
		"Chat session %s was created, but sending the prompt failed.",
		"对话 %s 已创建，但发送提示词失败。")
}

func chatTitleSetError(sessionID string, promptSent bool, cause error) error {
	en := "Chat session %s was created, but setting the title failed."
	zh := "对话 %s 已创建，但设置标题失败。"
	if promptSent {
		en = "Chat session %s was created and the prompt was sent, but setting the title failed."
		zh = "对话 %s 已创建，提示词已发送，但设置标题失败。"
	}
	return chatPartialError(sessionID, cause, en, zh)
}

func chatPartialError(sessionID string, cause error, en, zh string) error {
	reason := strings.TrimSpace(cli.UserMessage(cause))
	msg := fmt.Sprintf(en, sessionID)
	follow := " Open that session instead of creating another one."
	if cli.DetectLanguage() == cli.LangZH {
		msg = fmt.Sprintf(zh, sessionID)
		follow = " 请打开该对话，不要再新建一个。"
	}
	if reason != "" {
		msg += " " + reason
		if !strings.HasSuffix(reason, ".") && !strings.HasSuffix(reason, "。") {
			msg += "."
		}
	}
	return cli.WithUserMessage(msg+follow, cause)
}

func applyChatCreateTitle(ctx context.Context, client *cli.APIClient, session map[string]any, sessionID, title string) error {
	var updated map[string]any
	path := "/api/chat/sessions/" + url.PathEscape(sessionID)
	if err := client.PatchJSON(ctx, path, map[string]any{"title": title}, &updated); err != nil {
		return err
	}
	if got := strVal(updated, "title"); got != "" {
		session["title"] = got
		return nil
	}
	session["title"] = title
	return nil
}

func refreshChatCreateTitle(ctx context.Context, client *cli.APIClient, session map[string]any, sessionID string) {
	if strVal(session, "title") != "" {
		return
	}
	var refreshed map[string]any
	path := "/api/chat/sessions/" + url.PathEscape(sessionID)
	if err := client.GetJSON(ctx, path, &refreshed); err != nil {
		return
	}
	if title := strVal(refreshed, "title"); title != "" {
		session["title"] = title
	}
}

func renderChatCreate(cmd *cobra.Command, session, sent map[string]any) error {
	sessionID := strVal(session, "id")
	agentID := strVal(session, "agent_id")
	title := strVal(session, "title")
	status := strVal(session, "status")
	createdAt := strVal(session, "created_at")
	messageID := strVal(sent, "message_id")
	taskID := strVal(sent, "task_id")
	var queued *bool
	if v, ok := sent["queued"].(bool); ok {
		queued = &v
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		headers := []string{"SESSION", "AGENT", "TITLE", "STATUS", "TASK"}
		rows := [][]string{{
			sessionID,
			agentID,
			chatCell(title),
			chatCell(status),
			chatCell(taskID),
		}}
		cli.PrintTable(os.Stdout, headers, rows)
		return nil
	}

	return cli.PrintJSON(os.Stdout, chatCreateResult{
		SessionID: sessionID,
		AgentID:   agentID,
		Title:     title,
		Status:    status,
		CreatedAt: createdAt,
		MessageID: messageID,
		TaskID:    taskID,
		Queued:    queued,
	})
}

// chatCell keeps empty optional columns readable in the one-row table.
func chatCell(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

// fetchChatRead builds the request (shared --limit/--before paging, plus the
// optional thread id) and decodes the response.
func fetchChatRead(cmd *cobra.Command, basePath, threadID string) (map[string]any, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	limit, _ := cmd.Flags().GetInt("limit")
	before, _ := cmd.Flags().GetString("before")

	q := url.Values{}
	if threadID != "" {
		q.Set("id", threadID)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if before != "" {
		q.Set("before", before)
	}
	path := basePath
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var resp map[string]any
	if err := client.GetJSON(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("read chat: %w", err)
	}
	return resp, nil
}

// renderChatRead prints the response as JSON (default) or a table. The overview
// table adds the thread columns so the agent can pick a thread_id to drill into.
func renderChatRead(cmd *cobra.Command, resp map[string]any, overview bool) error {
	output, _ := cmd.Flags().GetString("output")
	if output != "table" {
		return cli.PrintJSON(os.Stdout, resp)
	}
	if note := strVal(resp, "note"); note != "" {
		fmt.Fprintln(os.Stdout, note)
		return nil
	}
	msgs, _ := resp["messages"].([]any)
	var headers []string
	if overview {
		headers = []string{"TS", "ROLE", "AUTHOR", "THREAD_ID", "REPLIES", "TEXT"}
	} else {
		headers = []string{"TS", "ROLE", "AUTHOR", "TEXT"}
	}
	rows := make([][]string, 0, len(msgs))
	for _, mi := range msgs {
		m, ok := mi.(map[string]any)
		if !ok {
			continue
		}
		if overview {
			rows = append(rows, []string{strVal(m, "ts"), strVal(m, "role"), strVal(m, "author"), strVal(m, "thread_id"), numVal(m, "reply_count"), strVal(m, "text")})
		} else {
			rows = append(rows, []string{strVal(m, "ts"), strVal(m, "role"), strVal(m, "author"), strVal(m, "text")})
		}
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// numVal renders a numeric JSON field as a string, blank when zero/absent.
func numVal(m map[string]any, key string) string {
	if v, ok := m[key].(float64); ok && v != 0 {
		return strconv.Itoa(int(v))
	}
	return ""
}
