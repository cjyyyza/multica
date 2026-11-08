package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/p4depot"
	"github.com/spf13/cobra"
)

var (
	p4SwarmURL    string
	p4ReviewID    string
	p4CommentBody string
	p4Reviewers   []string
)

var p4SwarmCmd = &cobra.Command{
	Use:   "swarm",
	Short: "Work with Helix Swarm reviews",
}

var p4SwarmReviewsCmd = &cobra.Command{
	Use:   "reviews",
	Short: "List Helix Swarm reviews",
	Args:  cobra.NoArgs,
	RunE:  runP4Swarm("reviews"),
}

var p4SwarmCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a Helix Swarm review from a changelist",
	Long:  "Creates a Swarm review without changing the changelist. Shelve or submit it first with native p4 commands when required.",
	Args:  cobra.NoArgs,
	RunE:  runP4Swarm("create"),
}

var p4SwarmShowCmd = &cobra.Command{
	Use:   "show [review-id]",
	Short: "Show one Helix Swarm review",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runP4Swarm("show"),
}

var p4SwarmCommentCmd = &cobra.Command{
	Use:   "comment [review-id]",
	Short: "Comment on a Helix Swarm review",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runP4Swarm("comment"),
}

var p4SwarmLinkCmd = &cobra.Command{
	Use:   "link [review-id]",
	Short: "Attach a Helix Swarm review to the current issue",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runP4Swarm("link"),
}

func configureP4SwarmCommands() {
	for _, cmd := range []*cobra.Command{p4SwarmReviewsCmd, p4SwarmCreateCmd, p4SwarmShowCmd, p4SwarmCommentCmd, p4SwarmLinkCmd} {
		addP4TaskFlags(cmd)
	}
	p4SwarmCreateCmd.Flags().StringArrayVar(&p4Reviewers, "reviewer", nil, "Reviewer P4USER (repeatable)")
	p4SwarmShowCmd.Flags().StringVar(&p4ReviewID, "review", "", "Swarm review id")
	p4SwarmCommentCmd.Flags().StringVar(&p4ReviewID, "review", "", "Swarm review id")
	p4SwarmCommentCmd.Flags().StringVar(&p4CommentBody, "body", "", "Comment body")
	p4SwarmLinkCmd.Flags().StringVar(&p4ReviewID, "review", "", "Swarm review id")
	p4SwarmCmd.AddCommand(p4SwarmReviewsCmd, p4SwarmCreateCmd, p4SwarmShowCmd, p4SwarmCommentCmd, p4SwarmLinkCmd)
}

func addP4TaskFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&p4Port, "port", "", "P4PORT")
	cmd.Flags().StringVar(&p4DepotPath, "depot", "", "Perforce depot path")
	cmd.Flags().StringVar(&p4Stream, "stream", "", "Optional P4 stream")
	cmd.Flags().StringVar(&p4User, "user", "", "Optional P4USER hint")
	cmd.Flags().StringVar(&p4Charset, "charset", "", "Optional P4CHARSET")
	cmd.Flags().StringVarP(&p4Changelist, "changelist", "c", "", "Changelist number")
	cmd.Flags().StringVar(&p4Desc, "description", "", "Review description")
	cmd.Flags().StringVar(&p4SwarmURL, "swarm-url", "", "Configured Helix Swarm origin")
	cmd.Flags().String("output", "json", "Output format: json or text")
}

func p4DaemonTimeout() time.Duration {
	timeout := 10 * time.Minute
	if value := strings.TrimSpace(os.Getenv("MULTICA_P4_TIMEOUT")); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			timeout = parsed
		}
	}
	return timeout + time.Minute
}

type p4DaemonRejection struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Files   []string `json:"files,omitempty"`
	Holders []string `json:"holders,omitempty"`
}

func (e *p4DaemonRejection) Error() string { return e.Message }

func p4DaemonPost(parent context.Context, path string, body map[string]any, timeout time.Duration) ([]byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	return p4DaemonCall(parent, http.MethodPost, path, data, timeout)
}

func p4DaemonCall(parent context.Context, method, path string, body []byte, timeout time.Duration) ([]byte, error) {
	daemonPort := os.Getenv("MULTICA_DAEMON_PORT")
	if daemonPort == "" {
		return nil, fmt.Errorf("MULTICA_DAEMON_PORT not set (this command runs inside a daemon task)")
	}
	taskToken := os.Getenv("MULTICA_TOKEN")
	if taskToken == "" {
		return nil, fmt.Errorf("MULTICA_TOKEN not set (p4 commands require the active task credential)")
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://127.0.0.1:%s%s", daemonPort, path), reader)
	if err != nil {
		return nil, fmt.Errorf("create daemon request: %w", err)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+taskToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read daemon response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var rejection p4DaemonRejection
		if json.Unmarshal(payload, &rejection) == nil && rejection.Code != "" {
			return nil, &rejection
		}
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(payload)))
	}
	return payload, nil
}

func reportP4Failure(op string, err error) error {
	var rejection *p4DaemonRejection
	if !errors.As(err, &rejection) {
		return fmt.Errorf("%s failed: %w", op, err)
	}
	fmt.Fprintln(os.Stderr, rejection.Message)
	for _, file := range rejection.Files {
		fmt.Fprintln(os.Stderr, file)
	}
	for _, holder := range rejection.Holders {
		fmt.Fprintln(os.Stderr, holder)
	}
	return fmt.Errorf("%s was rejected (%s)", op, rejection.Code)
}

func p4TaskEnv(ref p4depot.Ref) (map[string]any, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	return map[string]any{
		"port": ref.Port, "depot": ref.Depot, "stream": ref.Stream,
		"user": ref.User, "charset": ref.Charset, "swarm_url": ref.SwarmURL,
		"workspace_id": os.Getenv("MULTICA_WORKSPACE_ID"),
		"workdir":      workDir, "task_id": os.Getenv("MULTICA_TASK_ID"),
	}, nil
}

func printP4JSONOrText(cmd *cobra.Command, payload []byte, text string) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		var parsed any
		if err := json.Unmarshal(payload, &parsed); err != nil {
			return cli.PrintJSON(os.Stdout, map[string]any{"output": string(payload)})
		}
		return cli.PrintJSON(os.Stdout, parsed)
	}
	if strings.TrimSpace(text) == "" {
		text = strings.TrimSpace(string(payload))
	}
	fmt.Fprintln(os.Stdout, text)
	return nil
}

func runP4ChangelistLinkCommand(cmd *cobra.Command, _ []string) error {
	payload, err := postP4ChangelistLink(cmd, p4Changelist)
	if err != nil {
		return err
	}
	text := "Linked Perforce changelist " + strings.TrimSpace(p4Changelist) + " to " + strings.TrimSpace(os.Getenv("MULTICA_ISSUE_ID"))
	return printP4JSONOrText(cmd, payload, text)
}

func postP4ChangelistLink(cmd *cobra.Command, changelist string) ([]byte, error) {
	changelist = strings.TrimSpace(changelist)
	if number, err := strconv.Atoi(changelist); err != nil || number <= 0 {
		return nil, fmt.Errorf("changelist must be a positive number")
	}
	if strings.TrimSpace(os.Getenv("MULTICA_ISSUE_ID")) == "" {
		return nil, fmt.Errorf("changelist link requires MULTICA_ISSUE_ID (run it inside an issue task)")
	}
	body, err := p4TaskEnv(p4depot.Ref{})
	if err != nil {
		return nil, err
	}
	body["changelist"] = changelist
	payload, err := p4DaemonPost(cmd.Context(), "/p4/changelist/link", body, p4DaemonTimeout())
	if err != nil {
		return nil, reportP4Failure("p4 changelist link", err)
	}
	return payload, nil
}

func runP4Swarm(action string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if action == "link" && strings.TrimSpace(os.Getenv("MULTICA_ISSUE_ID")) == "" {
			return fmt.Errorf("swarm link requires MULTICA_ISSUE_ID (run it inside an issue task)")
		}
		ref, err := optionalP4PortDepot()
		if err != nil {
			return err
		}
		body, err := p4TaskEnv(ref)
		if err != nil {
			return err
		}
		body["action"] = action
		if p4Changelist != "" {
			body["changelist"] = p4Changelist
		}
		if p4Desc != "" {
			body["description"] = p4Desc
		}
		reviewID := p4ReviewID
		if len(args) > 0 {
			reviewID = args[0]
		}
		if reviewID != "" {
			body["review_id"] = reviewID
		}
		if p4CommentBody != "" {
			body["body"] = p4CommentBody
		}
		if len(p4Reviewers) > 0 {
			body["reviewers"] = p4Reviewers
		}
		daemonAction := action
		if action == "link" {
			daemonAction = "show"
			body["action"] = "show"
		}
		payload, err := p4DaemonPost(cmd.Context(), "/p4/swarm", body, p4DaemonTimeout())
		if err != nil {
			return reportP4Failure("p4 swarm "+action, err)
		}
		if err := printP4JSONOrText(cmd, payload, ""); err != nil {
			return err
		}
		if daemonAction == "create" || daemonAction == "show" {
			if err := reportSwarmReviewToIssue(cmd, payload); err != nil {
				return err
			}
		}
		if (action == "create" || action == "link") && strings.TrimSpace(os.Getenv("MULTICA_ISSUE_ID")) != "" {
			changelist, err := p4SwarmChangelist(payload, p4Changelist)
			if err != nil {
				return err
			}
			if changelist != "" {
				if _, err := postP4ChangelistLink(cmd, changelist); err != nil {
					return fmt.Errorf("Swarm review was linked, but linking changelist %s failed: %w; retry with `multica p4 changelist link --changelist %s`", changelist, err, changelist)
				}
			}
		}
		return nil
	}
}

func p4SwarmChangelist(payload []byte, fallback string) (string, error) {
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback), nil
	}
	var parsed struct {
		Review *struct {
			Changes []int `json:"changes"`
		} `json:"review"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return "", fmt.Errorf("parse Swarm review for changelist link: %w", err)
	}
	if parsed.Review == nil || len(parsed.Review.Changes) == 0 || parsed.Review.Changes[0] <= 0 {
		return "", nil
	}
	return strconv.Itoa(parsed.Review.Changes[0]), nil
}

func reportSwarmReviewToIssue(cmd *cobra.Command, payload []byte) error {
	issueID := strings.TrimSpace(os.Getenv("MULTICA_ISSUE_ID"))
	if issueID == "" {
		return nil
	}
	var parsed struct {
		SwarmURL string `json:"swarm_url"`
		Review   *struct {
			ID          int    `json:"id"`
			Author      string `json:"author"`
			State       string `json:"state"`
			Description string `json:"description"`
			Changes     []int  `json:"changes"`
			URL         string `json:"url"`
		} `json:"review"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return fmt.Errorf("parse Swarm review for issue link: %w", err)
	}
	if parsed.Review == nil || parsed.Review.ID <= 0 {
		return fmt.Errorf("daemon response did not include a Swarm review to link")
	}
	linkError := func(err error) error {
		return fmt.Errorf("Swarm review %d (%s) is available, but linking it to %s failed: %w; retry with `multica p4 swarm link --review %d` and the same depot flags", parsed.Review.ID, parsed.Review.URL, issueID, err, parsed.Review.ID)
	}
	if !p4depot.ValidSwarmURL(parsed.SwarmURL) {
		return linkError(fmt.Errorf("daemon response is missing a valid swarm_url"))
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return linkError(err)
	}
	changelist := p4Changelist
	if changelist == "" && len(parsed.Review.Changes) > 0 {
		changelist = strconv.Itoa(parsed.Review.Changes[0])
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	var out map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+url.PathEscape(issueID)+"/swarm-reviews", map[string]any{
		"review_id": parsed.Review.ID, "swarm_url": parsed.SwarmURL, "changelist": changelist,
		"description": parsed.Review.Description, "author": parsed.Review.Author,
		"state": parsed.Review.State, "html_url": parsed.Review.URL,
	}, &out); err != nil {
		return linkError(err)
	}
	fmt.Fprintf(os.Stderr, "Linked Swarm review %d to %s\n", parsed.Review.ID, issueID)
	return nil
}
