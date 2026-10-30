package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func addRequirementMemberCommands(root *cobra.Command) {
	add := func(use, short string, args cobra.PositionalArgs, run func(*cobra.Command, []string) error) *cobra.Command {
		cmd := &cobra.Command{Use: use, Short: short, Args: args, RunE: run}
		outputHelp := "Output format: table or json"
		if requirementJSONOnlyCommand(cmd.Name()) {
			outputHelp = "Output format: json"
		}
		cmd.Flags().String("output", "json", outputHelp)
		root.AddCommand(cmd)
		return cmd
	}
	textFlags := func(cmd *cobra.Command, name string) {
		cmd.Flags().String(name, "", "Text content")
		cmd.Flags().String(name+"-file", "", "Read UTF-8 content from a file inside the current workdir")
		cmd.Flags().Bool(name+"-stdin", false, "Read content from stdin")
		cmd.Flags().Bool("allow-external-file", false, "Allow input files outside the current workdir")
		cmd.Flags().String("idempotency-key", "", "Override the deterministic key; reuse only for an identical retry")
	}
	create := add("create", "Ask one agent or a squad to prepare a plan for human approval", exactArgs(0), runRequirementCreate)
	create.Aliases = []string{"plan"}
	textFlags(create, "idea")
	create.Flags().String("agent", "", "Planning and execution agent UUID (choose agent or squad)")
	create.Flags().String("squad", "", "Squad UUID whose leader prepares the plan")
	create.Flags().String("external-ref", "", "Stable caller reference for this request (required)")
	create.Flags().String("project", "", "Project UUID")
	create.Flags().String("participants-file", "", "JSON participant array with userId and roles")
	list := add("list", "List requirements visible to the current member", exactArgs(0), runRequirementList)
	list.Flags().String("external-ref", "", "Filter by exact caller reference")
	list.Flags().Int("limit", 25, "Page size (1-100)")
	list.Flags().Int("offset", 0, "Page offset")
	clarify := add("clarify <requirement-id>", "Send planning clarification before approval", exactArgs(1), runRequirementMemberMessage)
	textFlags(clarify, "content")
	clarify.Flags().Int64("expected-revision", 0, "Current requirement revision (required)")
	reply := add("reply <requirement-id>", "Answer the active execution question as its designated participant", exactArgs(1), runRequirementMemberMessage)
	textFlags(reply, "content")
	reply.Flags().String("question-comment", "", "Current blockedQuestion.commentId (required)")
	reply.Flags().Int64("expected-issue-revision", 0, "Current issue.revision (required)")
	add("targets", "Discover workspace agents, squads and runtimes", exactArgs(0), runRequirementTargets)
	messages := add("messages <requirement-id>", "Read a page of planning messages", exactArgs(1), runRequirementReadRelated)
	messages.Flags().Int("limit", 50, "Page size (1-100)")
	messages.Flags().String("before-created-at", "", "next_cursor.created_at from the previous page")
	messages.Flags().String("before-id", "", "next_cursor.id from the previous page")
	add("result <requirement-id>", "Read the Issue, task runs and comment timeline", exactArgs(1), runRequirementReadRelated)
	wait := add("wait <requirement-id>", "Wait for a state or the next human decision; never approve or accept", exactArgs(1), runRequirementWait)
	wait.Flags().StringSlice("until", []string{"awaiting_approval", "blocked", "in_review", "done", "cancelled"}, "States to stop at (comma separated)")
	wait.Flags().Duration("timeout", 10*time.Minute, "Maximum total wait")
	wait.Flags().Duration("interval", 2*time.Second, "Polling interval")
}

func requirementJSONOnlyCommand(name string) bool {
	switch name {
	case "list", "targets", "messages", "result":
		return true
	default:
		return false
	}
}

func newRequirementAPIClient(cmd *cobra.Command, memberOnly bool) (*cli.APIClient, error) {
	if memberOnly {
		if err := requireHumanLocalCommand("requirement " + cmd.Name()); err != nil {
			return nil, err
		}
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, err
	}
	if client.WorkspaceID == "" {
		return nil, errors.New("workspace id is required")
	}
	if memberOnly && strings.HasPrefix(client.Token, cli.TaskTokenPrefix) {
		return nil, errors.New("this requirement command requires member credentials")
	}
	return client, nil
}

func decodeRequirementCLI(raw json.RawMessage) (requirementCLIDetail, error) {
	var detail requirementCLIDetail
	if err := json.Unmarshal(raw, &detail); err != nil {
		return detail, fmt.Errorf("decode requirement: %w", err)
	}
	if detail.ID == "" || detail.WorkspaceID == "" || detail.Revision < 1 {
		return detail, errors.New("malformed requirement response: id, workspaceId and revision are required")
	}
	return detail, nil
}

func printRequirementCLI(cmd *cobra.Command, raw json.RawMessage, detail *requirementCLIDetail) error {
	output, _ := cmd.Flags().GetString("output")
	switch output {
	case "json":
		return cli.PrintJSON(cmd.OutOrStdout(), raw)
	case "table":
		if detail != nil {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\trevision %d\n", detail.ID, detail.State, detail.Revision)
			return err
		}
		return fmt.Errorf("requirement %s supports only --output json", cmd.Name())
	default:
		return fmt.Errorf("unsupported output format %q", output)
	}
}

func requirementMemberKey(cmd *cobra.Command, client *cli.APIClient, path string, body any) (string, error) {
	if cmd.Flags().Changed("idempotency-key") {
		key, _ := cmd.Flags().GetString("idempotency-key")
		if strings.TrimSpace(key) == "" || len(key) > 512 {
			return "", errors.New("--idempotency-key must contain 1-512 bytes")
		}
		return key, nil
	}
	seed, err := json.Marshal(map[string]any{"workspace": client.WorkspaceID, "path": path, "body": body})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(seed)
	return "requirement-" + cmd.Name() + ":" + hex.EncodeToString(digest[:]), nil
}

func requirementMemberPost(cmd *cobra.Command, pathFor func(*cli.APIClient) string, body any, reply bool) error {
	client, err := newRequirementAPIClient(cmd, true)
	if err != nil {
		return err
	}
	path := pathFor(client)
	key, err := requirementMemberKey(cmd, client, path, body)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	var raw json.RawMessage
	if err := client.PostJSONWithHeaders(ctx, path, body, &raw, map[string]string{"Idempotency-Key": key}); err != nil {
		return err
	}
	detailRaw := raw
	if reply {
		var envelope struct {
			Requirement json.RawMessage `json:"requirement"`
			Reply       struct {
				ID                string `json:"id"`
				IssueID           string `json:"issueId"`
				QuestionCommentID string `json:"questionCommentId"`
				IssueRevision     int64  `json:"issueRevision"`
			} `json:"reply"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return fmt.Errorf("decode requirement reply: %w", err)
		}
		if envelope.Reply.ID == "" || envelope.Reply.IssueID == "" || envelope.Reply.QuestionCommentID == "" || envelope.Reply.IssueRevision < 1 {
			return errors.New("malformed requirement reply response")
		}
		detailRaw = envelope.Requirement
	}
	detail, err := decodeRequirementCLI(detailRaw)
	if err != nil {
		return err
	}
	return printRequirementCLI(cmd, raw, &detail)
}

func requirementText(cmd *cobra.Command, name string) (string, error) {
	value, provided, err := resolveTextFlag(cmd, name)
	if err != nil {
		return "", err
	}
	if !provided || strings.TrimSpace(value) == "" || len(value) > 20000 {
		return "", fmt.Errorf("--%s, --%s-file or --%s-stdin must contain 1-20000 UTF-8 bytes", name, name, name)
	}
	return value, nil
}

func runRequirementCreate(cmd *cobra.Command, _ []string) error {
	agent, _ := cmd.Flags().GetString("agent")
	squad, _ := cmd.Flags().GetString("squad")
	ref, _ := cmd.Flags().GetString("external-ref")
	if (strings.TrimSpace(agent) == "") == (strings.TrimSpace(squad) == "") || strings.TrimSpace(ref) == "" {
		return errors.New("provide --external-ref and exactly one of --agent or --squad")
	}
	idea, err := requirementText(cmd, "idea")
	if err != nil {
		return err
	}
	body := map[string]any{"externalRef": ref, "idea": idea}
	if agent != "" {
		body["agentId"] = agent
	} else {
		body["squadId"] = squad
	}
	if project, _ := cmd.Flags().GetString("project"); project != "" {
		body["projectId"] = project
	}
	if file, _ := cmd.Flags().GetString("participants-file"); file != "" {
		raw, _, err := resolveTextFlag(cmd, "participants")
		if err != nil {
			return err
		}
		var participants []struct {
			UserID string   `json:"userId"`
			Roles  []string `json:"roles"`
		}
		if err := json.Unmarshal([]byte(raw), &participants); err != nil || participants == nil {
			return errors.New("--participants-file must contain a JSON array")
		}
		for _, participant := range participants {
			if participant.UserID == "" || len(participant.Roles) == 0 {
				return errors.New("each participant requires userId and roles")
			}
			for _, role := range participant.Roles {
				if role != "requester" && role != "approver" && role != "viewer" {
					return fmt.Errorf("invalid participant role %q", role)
				}
			}
		}
		body["participants"] = participants
	}
	return requirementMemberPost(cmd, func(c *cli.APIClient) string {
		return "/api/workspaces/" + url.PathEscape(c.WorkspaceID) + "/requirement-requests"
	}, body, false)
}

func runRequirementMemberMessage(cmd *cobra.Command, args []string) error {
	content, err := requirementText(cmd, "content")
	if err != nil {
		return err
	}
	body := map[string]any{"content": content}
	suffix := "/messages"
	reply := cmd.Name() == "reply"
	if reply {
		question, _ := cmd.Flags().GetString("question-comment")
		revision, _ := cmd.Flags().GetInt64("expected-issue-revision")
		if strings.TrimSpace(question) == "" || revision < 1 {
			return errors.New("--question-comment and a positive --expected-issue-revision are required")
		}
		body["questionCommentId"], body["expectedIssueRevision"] = question, revision
		suffix = "/replies"
	} else {
		revision, _ := cmd.Flags().GetInt64("expected-revision")
		if revision < 1 {
			return errors.New("a positive --expected-revision is required")
		}
		body["expectedRevision"] = revision
	}
	return requirementMemberPost(cmd, func(c *cli.APIClient) string { return requirementCLIPath(c, args[0], suffix) }, body, reply)
}

func requirementGetRaw(ctx context.Context, client *cli.APIClient, path string) (json.RawMessage, error) {
	requestCtx, cancel := cli.APIContext(ctx)
	defer cancel()
	var raw json.RawMessage
	err := client.GetJSON(requestCtx, path, &raw)
	return raw, err
}

func runRequirementList(cmd *cobra.Command, _ []string) error {
	limit, _ := cmd.Flags().GetInt("limit")
	offset, _ := cmd.Flags().GetInt("offset")
	if limit < 1 || limit > 100 || offset < 0 || offset > 2147483646 {
		return errors.New("limit must be 1-100 and offset 0-2147483646")
	}
	client, err := newRequirementAPIClient(cmd, true)
	if err != nil {
		return err
	}
	query := url.Values{"limit": {fmt.Sprint(limit)}, "offset": {fmt.Sprint(offset)}}
	if ref, _ := cmd.Flags().GetString("external-ref"); ref != "" {
		query.Set("externalRef", ref)
	}
	raw, err := requirementGetRaw(cmd.Context(), client, requirementCLIPath(client, "", "")+"?"+query.Encode())
	if err != nil {
		return err
	}
	var page struct {
		Requirements []json.RawMessage `json:"requirements"`
		HasMore      *bool             `json:"hasMore"`
		NextOffset   *int              `json:"nextOffset"`
	}
	if err := json.Unmarshal(raw, &page); err != nil || page.Requirements == nil || page.HasMore == nil || (*page.HasMore && (page.NextOffset == nil || *page.NextOffset <= offset)) {
		return errors.New("malformed requirement list response")
	}
	for _, row := range page.Requirements {
		if _, err := decodeRequirementCLI(row); err != nil {
			return err
		}
	}
	return printRequirementCLI(cmd, raw, nil)
}

func runRequirementTargets(cmd *cobra.Command, _ []string) error {
	client, err := newRequirementAPIClient(cmd, true)
	if err != nil {
		return err
	}
	result := map[string]json.RawMessage{}
	for _, target := range []struct{ name, path string }{{"workspace", "/api/workspaces/" + url.PathEscape(client.WorkspaceID)}, {"agents", "/api/agents"}, {"squads", "/api/squads"}, {"runtimes", "/api/runtimes"}} {
		raw, err := requirementGetRaw(cmd.Context(), client, target.path)
		if err != nil {
			return err
		}
		result[target.name] = raw
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return printRequirementCLI(cmd, raw, nil)
}

func runRequirementReadRelated(cmd *cobra.Command, args []string) error {
	query := url.Values{}
	if cmd.Name() == "messages" {
		limit, _ := cmd.Flags().GetInt("limit")
		beforeAt, _ := cmd.Flags().GetString("before-created-at")
		beforeID, _ := cmd.Flags().GetString("before-id")
		if limit < 1 || limit > 100 || (beforeAt == "") != (beforeID == "") {
			return errors.New("limit must be 1-100; provide both --before-created-at and --before-id for older messages")
		}
		query.Set("limit", fmt.Sprint(limit))
		if beforeAt != "" {
			if _, err := time.Parse(time.RFC3339Nano, beforeAt); err != nil {
				return fmt.Errorf("invalid --before-created-at: %w", err)
			}
			query.Set("before_created_at", beforeAt)
			query.Set("before_id", beforeID)
		}
	}
	client, err := newRequirementAPIClient(cmd, true)
	if err != nil {
		return err
	}
	raw, err := requirementGetRaw(cmd.Context(), client, requirementCLIPath(client, args[0], ""))
	if err != nil {
		return err
	}
	detail, err := decodeRequirementCLI(raw)
	if err != nil {
		return err
	}
	result := map[string]json.RawMessage{"requirement": raw}
	if cmd.Name() == "messages" {
		result["messages"] = json.RawMessage(`{"messages":[],"has_more":false}`)
		if detail.ChatSessionID != nil {
			result["messages"], err = requirementGetRaw(cmd.Context(), client, "/api/chat/sessions/"+url.PathEscape(*detail.ChatSessionID)+"/messages/page?"+query.Encode())
			if err != nil {
				return err
			}
		}
	} else {
		result["issue"], result["runs"], result["comments"] = json.RawMessage("null"), json.RawMessage("[]"), json.RawMessage("[]")
		if detail.IssueID != nil && !detail.IssueDeleted {
			base := "/api/issues/" + url.PathEscape(*detail.IssueID)
			for _, part := range []struct{ name, suffix string }{{"issue", ""}, {"runs", "/task-runs"}, {"comments", "/comments"}} {
				result[part.name], err = requirementGetRaw(cmd.Context(), client, base+part.suffix)
				if err != nil {
					return err
				}
			}
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return printRequirementCLI(cmd, encoded, &detail)
}

func runRequirementWait(cmd *cobra.Command, args []string) error {
	until, _ := cmd.Flags().GetStringSlice("until")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	interval, _ := cmd.Flags().GetDuration("interval")
	valid := map[string]bool{"intake_pending": true, "clarifying": true, "awaiting_approval": true, "executing": true, "blocked": true, "in_review": true, "done": true, "cancelled": true}
	wanted := map[string]bool{}
	for _, state := range until {
		if !valid[state] {
			return fmt.Errorf("unsupported requirement state %q", state)
		}
		wanted[state] = true
	}
	if len(wanted) == 0 || timeout <= 0 || interval <= 0 {
		return errors.New("provide states and positive timeout/interval")
	}
	client, err := newRequirementAPIClient(cmd, true)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for requirement: %w", err)
		}
		raw, err := requirementGetRaw(ctx, client, requirementCLIPath(client, args[0], ""))
		if err != nil {
			return err
		}
		detail, err := decodeRequirementCLI(raw)
		if err != nil {
			return err
		}
		if !valid[detail.State] {
			return fmt.Errorf("unknown requirement state %q", detail.State)
		}
		if wanted[detail.State] {
			return printRequirementCLI(cmd, raw, &detail)
		}
		if detail.State == "done" || detail.State == "cancelled" {
			return fmt.Errorf("requirement reached %s before the requested states", detail.State)
		}
		if detail.Run != nil && (detail.Run.Status == "failed" || detail.Run.Status == "cancelled") {
			return fmt.Errorf("requirement run %s; inspect requirement get/result before retrying", detail.Run.Status)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("wait for requirement: %w", ctx.Err())
		case <-timer.C:
		}
	}
}
