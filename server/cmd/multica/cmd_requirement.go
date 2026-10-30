package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var requirementCmd = newRequirementCommand()

func newRequirementCommand() *cobra.Command {
	root := &cobra.Command{Use: "requirement", Short: "Plan, follow and answer requirements", PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		strict, _ := cmd.Flags().GetBool("strict")
		if strict {
			if strings.TrimSpace(os.Getenv("MULTICA_TOKEN")) == "" {
				return errors.New("strict mode requires MULTICA_TOKEN; profile credentials are disabled")
			}
			if strings.TrimSpace(cli.FlagOrEnv(cmd, "workspace-id", "MULTICA_WORKSPACE_ID", "")) == "" {
				return errors.New("strict mode requires --workspace-id or MULTICA_WORKSPACE_ID; profile workspace is disabled")
			}
			if tryResolveExplicitServerURL(cmd) == "" {
				return errors.New("strict mode requires --server-url or MULTICA_SERVER_URL; profile server is disabled")
			}
		}
		output, _ := cmd.Flags().GetString("output")
		if output != "json" && output != "table" {
			return fmt.Errorf("unsupported output format %q", output)
		}
		if output != "json" && requirementJSONOnlyCommand(cmd.Name()) {
			return fmt.Errorf("requirement %s supports only --output json", cmd.Name())
		}
		return nil
	}}
	root.PersistentFlags().Bool("strict", false, "Require explicit server, token and workspace; never fall back to a profile")
	get := &cobra.Command{
		Use:   "get <requirement-id>",
		Short: "Read a requirement with member credentials or the current task token",
		Args:  exactArgs(1),
		RunE:  runRequirementGet,
	}
	get.Flags().String("output", "json", "Output format: table or json")
	candidate := &cobra.Command{
		Use:   "candidate <requirement-id>",
		Short: "Submit a candidate snapshot for the current plan task requirement",
		Args:  exactArgs(1),
		RunE:  runRequirementCandidate,
	}
	candidate.Flags().String("snapshot-file", "", "Read the seven required fields and optional attachmentIds JSON snapshot from a file inside the task workdir")
	candidate.Flags().Bool("snapshot-stdin", false, "Read the seven required fields and optional attachmentIds JSON snapshot from stdin")
	candidate.Flags().Int64("expected-revision", 0, "Current requirement revision returned by requirement get (required)")
	candidate.Flags().String("idempotency-key", "", "Override the deterministic request idempotency key")
	candidate.Flags().Bool("allow-external-file", false, "Allow --snapshot-file outside the current working directory")
	candidate.Flags().String("output", "json", "Output format: table or json")
	block := &cobra.Command{
		Use:   "block <requirement-id>",
		Short: "Block the current requirement Issue with a participant question",
		Args:  exactArgs(1),
		RunE:  runRequirementBlock,
	}
	block.Flags().String("question-file", "", "Read the question verbatim from a UTF-8 file inside the task workdir")
	block.Flags().Bool("question-stdin", false, "Read the question verbatim from stdin")
	block.Flags().String("target-user", "", "Requirement participant user UUID to answer the question (required)")
	block.Flags().Int64("expected-issue-revision", 0, "Current requirement Issue revision (required)")
	block.Flags().String("idempotency-key", "", "Override the deterministic request idempotency key")
	block.Flags().Bool("allow-external-file", false, "Allow --question-file outside the current working directory")
	block.Flags().String("output", "json", "Output format: table or json")
	root.AddCommand(get, candidate, block)
	addRequirementMemberCommands(root)
	return root
}

type requirementCLIDetail struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspaceId"`
	Revision      int64   `json:"revision"`
	State         string  `json:"state"`
	IntakeState   string  `json:"intakeState"`
	AgentID       *string `json:"agentId"`
	ProjectID     *string `json:"projectId"`
	ChatSessionID *string `json:"chatSessionId"`
	IssueID       *string `json:"issueId"`
	IssueDeleted  bool    `json:"issueDeleted"`
	Run           *struct {
		Status string `json:"status"`
	} `json:"run"`
}

func newRequirementTaskAPIClient(cmd *cobra.Command, invalidTokenMessage string) (*cli.APIClient, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(client.Token, cli.TaskTokenPrefix) {
		return nil, errors.New(invalidTokenMessage)
	}
	if client.WorkspaceID == "" {
		return nil, errors.New("workspace id is required")
	}
	return client, nil
}

func requirementCLIPath(client *cli.APIClient, requirementID, suffix string) string {
	if requirementID == "" {
		return "/api/workspaces/" + url.PathEscape(client.WorkspaceID) + "/requirements" + suffix
	}
	return "/api/workspaces/" + url.PathEscape(client.WorkspaceID) + "/requirements/" + url.PathEscape(requirementID) + suffix
}

func runRequirementGet(cmd *cobra.Command, args []string) error {
	client, err := newRequirementAPIClient(cmd, false)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	var result json.RawMessage
	if err := client.GetJSON(ctx, requirementCLIPath(client, args[0], ""), &result); err != nil {
		return fmt.Errorf("get requirement: %w", err)
	}
	detail, err := decodeRequirementCLI(result)
	if err != nil {
		return err
	}
	return printRequirementCLI(cmd, result, &detail)
}

func runRequirementCandidate(cmd *cobra.Command, args []string) error {
	raw, provided, err := resolveTextFlag(cmd, "snapshot")
	if err != nil {
		return err
	}
	if !provided {
		return fmt.Errorf("one of --snapshot-file or --snapshot-stdin is required")
	}
	snapshot := json.RawMessage(raw)
	if !json.Valid(snapshot) {
		return errors.New("snapshot must be valid JSON")
	}
	revision, _ := cmd.Flags().GetInt64("expected-revision")
	if revision <= 0 {
		return fmt.Errorf("--expected-revision must be positive")
	}
	client, err := newRequirementTaskAPIClient(cmd, "requirement candidate requires a daemon-managed plan task token")
	if err != nil {
		return err
	}
	if strings.TrimSpace(client.TaskID) == "" {
		return fmt.Errorf("requirement candidate requires MULTICA_TASK_ID for its deterministic idempotency key")
	}
	key, err := requirementCandidateIdempotencyKey(cmd, client.TaskID, args[0], revision, snapshot)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	body := map[string]any{"expectedRevision": revision, "snapshot": snapshot}
	var result json.RawMessage
	if err := client.PostJSONWithHeaders(ctx, requirementCLIPath(client, args[0], "/candidates"), body, &result, map[string]string{"Idempotency-Key": key}); err != nil {
		return fmt.Errorf("submit requirement candidate: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(cmd.OutOrStdout(), result)
	}
	var submitted requirementCLIDetail
	if err := json.Unmarshal(result, &submitted); err != nil {
		return fmt.Errorf("decode requirement candidate response: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Requirement candidate submitted: %s revision %d\n", submitted.ID, submitted.Revision)
	return nil
}

func requirementCandidateIdempotencyKey(cmd *cobra.Command, taskID, requirementID string, revision int64, snapshot json.RawMessage) (string, error) {
	if configured, _ := cmd.Flags().GetString("idempotency-key"); configured != "" {
		if strings.TrimSpace(configured) == "" {
			return "", fmt.Errorf("--idempotency-key cannot be blank")
		}
		return configured, nil
	}
	seed, err := json.Marshal(map[string]any{
		"task_id": taskID, "requirement_id": requirementID, "expected_revision": revision, "snapshot": snapshot,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(seed)
	return "requirement-candidate:" + hex.EncodeToString(digest[:]), nil
}

type requirementBlockCLIResponse struct {
	QuestionComment struct {
		ID string `json:"id"`
	} `json:"question_comment"`
	Wakeup struct {
		ID         string `json:"id"`
		Generation int64  `json:"generation"`
	} `json:"wakeup"`
}

func runRequirementBlock(cmd *cobra.Command, args []string) error {
	question, provided, err := resolveTextFlag(cmd, "question")
	if err != nil {
		return err
	}
	if !provided {
		return fmt.Errorf("one of --question-file or --question-stdin is required")
	}
	target, _ := cmd.Flags().GetString("target-user")
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("--target-user is required")
	}
	revision, _ := cmd.Flags().GetInt64("expected-issue-revision")
	if revision <= 0 {
		return fmt.Errorf("--expected-issue-revision must be positive")
	}
	client, err := newRequirementTaskAPIClient(cmd, "requirement block requires a daemon-managed execute task token")
	if err != nil {
		return err
	}
	if strings.TrimSpace(client.TaskID) == "" {
		return errors.New("multica requirement block requires MULTICA_TASK_ID from a running task")
	}
	key, err := requirementBlockIdempotencyKey(cmd, client.TaskID, args[0], question, target, revision)
	if err != nil {
		return err
	}
	body := map[string]any{"question": question, "targetUserId": target, "expectedIssueRevision": revision}
	path := "/api/workspaces/" + url.PathEscape(client.WorkspaceID) + "/requirements/" + url.PathEscape(args[0]) + "/block"
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var result requirementBlockCLIResponse
	if err := client.PostJSONWithHeaders(ctx, path, body, &result, map[string]string{"Idempotency-Key": key}); err != nil {
		return fmt.Errorf("block requirement: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(cmd.OutOrStdout(), result)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Requirement blocked: comment %s, wakeup %s generation %d\n", result.QuestionComment.ID, result.Wakeup.ID, result.Wakeup.Generation)
	return nil
}

func requirementBlockIdempotencyKey(cmd *cobra.Command, taskID, requirementID, question, targetUserID string, expectedIssueRevision int64) (string, error) {
	if configured, _ := cmd.Flags().GetString("idempotency-key"); configured != "" {
		if strings.TrimSpace(configured) == "" {
			return "", fmt.Errorf("--idempotency-key cannot be blank")
		}
		return configured, nil
	}
	seed, err := json.Marshal(map[string]any{
		"task_id": taskID, "requirement_id": requirementID,
		"question": question, "target_user_id": targetUserID, "expected_issue_revision": expectedIssueRevision,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(seed)
	return "requirement-block:" + hex.EncodeToString(digest[:]), nil
}
