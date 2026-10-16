package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{Use: "pm-sync", Short: "Run one read-only 易协作 PM sync and deduplicated triage cycle", RunE: runYixiezuoPMSync}
	cmd.Flags().String("config-file", "", "UTF-8 JSON PM scope configuration (no credentials)")
	cmd.Flags().String("cli", "popo-cli", "Local read-only source CLI executable")
	cmd.Flags().String("output", "markdown", "Report format: markdown or json")
	cmd.Flags().Duration("timeout", 15*time.Minute, "Maximum duration of one PM cycle")
	yixiezuoCmd.AddCommand(cmd)
}

type pmSourceReader interface {
	Prepare(context.Context) error
	Discover(context.Context) ([]yixiezuo.Source, error)
	ReadIssue(context.Context, yixiezuo.Source) (yixiezuo.PMIssue, error)
}

func runYixiezuoPMSync(cmd *cobra.Command, _ []string) error {
	report := yixiezuo.PMReport{ScannedAt: time.Now().UTC(), Results: []yixiezuo.PMResult{}, Triage: []yixiezuo.PMTriageResult{}, Errors: []yixiezuo.PMFailure{}}
	path, _ := cmd.Flags().GetString("config-file")
	output, _ := cmd.Flags().GetString("output")
	err := readPMScope(path, &report.Scope)
	if err == nil {
		err = report.Scope.Normalize()
	}
	if output != "markdown" && output != "json" {
		if err == nil {
			err = fmt.Errorf("output must be markdown or json")
		}
		output = "markdown"
	}
	if err == nil {
		ids := map[string]string{"project_id": report.Scope.ProjectID}
		agent, _ := report.Scope.TriageAgentID()
		ids["triage_mention agent id"] = agent
		if report.Scope.CategoryPropertyID != "" {
			ids["category_property_id"] = report.Scope.CategoryPropertyID
		}
		for email, id := range report.Scope.AssigneeMap {
			ids["assignee_map "+email] = id
		}
		for field, id := range ids {
			if _, parseErr := util.ParseUUID(id); parseErr != nil {
				err = fmt.Errorf("%s must be a UUID", field)
				break
			}
		}
	}
	var client *cli.APIClient
	if err == nil {
		client, err = newAPIClient(cmd)
	}
	if err == nil && client.WorkspaceID == "" {
		err = fmt.Errorf("workspace-id is required")
	}
	if err != nil {
		report.Errors = append(report.Errors, yixiezuo.PMFailure{Stage: "配置与身份", Error: err.Error()})
	} else {
		bin, _ := cmd.Flags().GetString("cli")
		if override := strings.TrimSpace(os.Getenv("MULTICA_YIXIEZUO_CLI")); override != "" {
			bin = override
		}
		timeout, _ := cmd.Flags().GetDuration("timeout")
		if timeout <= 0 {
			report.Errors = append(report.Errors, yixiezuo.PMFailure{Stage: "配置", Error: "timeout must be positive"})
		} else {
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			report = runPMCycle(ctx, client, yixiezuo.NewPMReader(yixiezuo.ExecOptions{Bin: bin}, report.Scope), report)
			cancel()
		}
	}
	switch output {
	case "markdown":
		_, err = fmt.Fprint(cmd.OutOrStdout(), report.Markdown())
	case "json":
		err = json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	default:
		return fmt.Errorf("output must be markdown or json")
	}
	if err != nil {
		return err
	}
	if report.HasFailures() {
		return errSilent
	}
	return nil
}

func readPMScope(path string, target *yixiezuo.PMScope) error {
	if path == "" {
		return fmt.Errorf("config-file is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read PM configuration: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid PM configuration: %w", err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fmt.Errorf("PM configuration must contain exactly one JSON object")
	}
	return nil
}

func runPMCycle(ctx context.Context, client *cli.APIClient, reader pmSourceReader, report yixiezuo.PMReport) yixiezuo.PMReport {
	base := yixiezuoAPIPath(client, "/pm")
	failure := func(stage, id string, err error) {
		report.Errors = append(report.Errors, yixiezuo.PMFailure{Stage: stage, ExternalID: id, Error: err.Error()})
	}
	sources := []yixiezuo.Source{}
	if err := reader.Prepare(ctx); err != nil {
		failure("读取易协作查询目录", "", err)
	} else {
		discovered, err := reader.Discover(ctx)
		report.Discovered = len(discovered)
		sources = append(sources, discovered...)
		if err != nil {
			failure("查询易协作未完成 BUG", "", err)
		}
	}
	// Collect the complete reconciliation list before any status write can
	// remove a row and shift subsequent offset pages.
	for offset := int32(0); ; {
		var page struct {
			Sources    []yixiezuo.Source   `json:"sources"`
			Errors     []yixiezuo.PMResult `json:"errors"`
			HasMore    bool                `json:"has_more"`
			NextOffset int32               `json:"next_offset"`
		}
		if err := client.PostJSON(ctx, base+"/reconcile", map[string]any{"scope": report.Scope, "offset": offset}, &page); err != nil {
			failure("读取关闭对账清单", "", err)
			break
		}
		if page.Sources == nil || page.Errors == nil {
			failure("读取关闭对账清单", "", fmt.Errorf("malformed reconciliation response"))
			break
		}
		report.Reconciled += len(page.Sources)
		sources = append(sources, page.Sources...)
		report.Results = append(report.Results, page.Errors...)
		if !page.HasMore {
			break
		}
		if page.NextOffset <= offset {
			failure("读取关闭对账清单", "", fmt.Errorf("pagination did not advance"))
			break
		}
		offset = page.NextOffset
	}
	seen := map[string]bool{}
	for _, source := range sources {
		key := source.Host + "/" + source.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		item, err := reader.ReadIssue(ctx, source)
		if err != nil {
			failure("读取来源工单", source.ID, err)
			continue
		}
		var result yixiezuo.PMResult
		if err := client.PostJSON(ctx, base+"/issues", yixiezuo.PMSourceRequest{Scope: report.Scope, Issue: item}, &result); err != nil {
			failure("Multica 同步写入（结果可能不确定，下轮按来源键查重）", source.ID, err)
			continue
		}
		validIdentity := true
		if result.State == "created" || result.State == "updated" || result.State == "unchanged" {
			_, parseErr := util.ParseUUID(result.IssueID)
			validIdentity = parseErr == nil
		}
		if !validIdentity || result.ExternalID != source.ID || (result.State != "created" && result.State != "updated" && result.State != "unchanged" && result.State != "skipped" && result.State != "failed") {
			failure("Multica 同步回执", source.ID, fmt.Errorf("malformed PM synchronization receipt"))
			continue
		}
		report.Results = append(report.Results, result)
	}
	// The candidate scan is also completed before comments can start a triager
	// that changes candidate membership while later pages are being read.
	candidates := []struct {
		ID         string `json:"id"`
		Identifier string `json:"identifier"`
	}{}
	for offset := int32(0); ; {
		var page struct {
			Issues []struct {
				ID         string `json:"id"`
				Identifier string `json:"identifier"`
			} `json:"issues"`
			HasMore    bool  `json:"has_more"`
			NextOffset int32 `json:"next_offset"`
		}
		if err := client.PostJSON(ctx, base+"/triage/candidates", map[string]any{"scope": report.Scope, "offset": offset}, &page); err != nil {
			failure("扫描未分类任务单", "", err)
			break
		}
		if page.Issues == nil {
			failure("扫描未分类任务单", "", fmt.Errorf("malformed triage candidate response"))
			break
		}
		candidates = append(candidates, page.Issues...)
		if !page.HasMore {
			break
		}
		if page.NextOffset <= offset {
			failure("扫描未分类任务单", "", fmt.Errorf("pagination did not advance"))
			break
		}
		offset = page.NextOffset
	}
	report.TriageScanned = len(candidates)
	seenTriage := map[string]bool{}
	for _, candidate := range candidates {
		if _, err := util.ParseUUID(candidate.ID); err != nil {
			failure("未分类任务单身份", candidate.Identifier, fmt.Errorf("invalid candidate issue id: %s", candidate.ID))
			continue
		}
		if seenTriage[candidate.ID] {
			continue
		}
		seenTriage[candidate.ID] = true
		var result yixiezuo.PMTriageResult
		if err := client.PostJSON(ctx, base+"/triage/"+candidate.ID, map[string]any{"scope": report.Scope}, &result); err != nil {
			result = yixiezuo.PMTriageResult{IssueID: candidate.ID, Identifier: candidate.Identifier, State: "failed", Error: err.Error()}
		} else if result.IssueID != candidate.ID || (result.State != "notified" && result.State != "already_notified" && result.State != "skipped" && result.State != "failed") {
			result = yixiezuo.PMTriageResult{IssueID: candidate.ID, Identifier: candidate.Identifier, State: "failed", Error: "malformed triage trigger receipt"}
		}
		if result.State == "notified" || result.State == "already_notified" {
			if _, err := util.ParseUUID(result.CommentID); err != nil {
				result.State, result.Error = "failed", "triage receipt has no valid comment identity"
			}
		}
		report.Triage = append(report.Triage, result)
	}
	return report
}
