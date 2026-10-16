package yixiezuo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
)

func TestPMReaderUsesLiveIDsAndOnlyReadTools(t *testing.T) {
	scope := PMScope{SourceHost: "dj01.pm.netease.com", SourceProject: "dj01", IssueType: "BUG", AssigneeEmails: []string{"owner@corp.netease.com"}}
	calls := map[string]int{}
	d := NewPMReader(ExecOptions{Bin: "fake", LookPath: func(s string) (string, error) { return s, nil }, Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		name := toolNameFromArgs(args)
		calls[name]++
		var params map[string]json.RawMessage
		json.Unmarshal([]byte(argumentsFromArgs(args)), &params)
		var data any
		switch name {
		case "get_filter_values":
			if len(params["project_id"]) == 0 {
				data = map[string]any{"project_id": map[string]any{"values": []any{[]any{"我的项目", "mine"}, []any{"DJ01需求", "7", nil}, []any{"-- DJ01单机-Bug", "10", 7}}}}
			} else {
				trackers := []any{[]any{"Requirement", "1"}}
				if string(params["project_id"]) == "10" {
					trackers = []any{[]any{"BUG-崩溃", "43"}, []any{"BUG-不影响UE工程正常使用", "5"}, []any{"Gameplay UR反馈", "18"}}
				}
				data = map[string]any{"tracker_id": map[string]any{"values": trackers}, "status_id": map[string]any{"values": []any{[]any{"已关闭", "69"}, []any{"已取消", "18"}, []any{"交付测试", "8"}, []any{"新建", "1"}}}}
			}
		case "searchUsers":
			data = []any{map[string]any{"id": 376, "name": "Actual Owner", "mail": "owner@corp.netease.com"}}
		case "list_issues":
			if string(params["set_filter"]) != "1" || string(params["project_id"]) != "10" {
				t.Errorf("unscoped query: %s", argumentsFromArgs(args))
			}
			var filters map[string]struct {
				Operator string   `json:"operator"`
				Values   []string `json:"values"`
			}
			json.Unmarshal(params["filters"], &filters)
			if strings.Join(filters["assigned_to_id"].Values, ",") != "376" || strings.Join(filters["tracker_id"].Values, ",") != "43,5" || filters["status_id"].Operator != "!" {
				t.Errorf("filters=%+v", filters)
			}
			page := string(params["page"])
			data = map[string]any{"page": json.RawMessage(params["page"]), "total_page": 2, "list": []any{map[string]any{"id": json.Number(page), "project_id": 10, "subject": map[string]any{"value": "来源标题"}, "status": "新建", "priority": "高", "tracker": "BUG-崩溃", "assigned_to": map[string]any{"id": 376, "value": "Actual Owner", "mail": "owner@corp.netease.com"}, "project": map[string]any{"id": 10, "value": "DJ01单机-Bug"}, "updated_on": "2026-10-14 12:00"}}}
		default:
			return nil, fmt.Errorf("unexpected tool: %s", name)
		}
		return fabricText(mustJSON(map[string]any{"res_code": 1, "data": data})), nil
	}}, scope)
	ctx := context.Background()
	if err := d.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	sources, err := d.Discover(ctx)
	if err != nil || len(sources) != 2 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	for _, source := range sources {
		item, err := d.ReadIssue(ctx, source)
		if err != nil || item.Assignee.Email != "owner@corp.netease.com" || item.IssueType != "BUG-崩溃" || !item.UpdatedAtMinutePrecision {
			t.Fatalf("item=%+v err=%v", item, err)
		}
	}
	if calls["get_issue_base"] != 0 || calls["getIssueForm"] != 0 {
		t.Fatal("discovery rows were fetched again per issue")
	}
	before := len(calls)
	if _, err := d.read(ctx, "update_issue", map[string]any{}); err == nil || len(calls) != before {
		t.Fatal("source write reached subprocess")
	}
}

func TestPMReaderRejectsSilentlyIgnoredFilters(t *testing.T) {
	d := NewPMReader(ExecOptions{Bin: "fake", LookPath: func(s string) (string, error) { return s, nil }, Run: func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return fabricText(mustJSON(map[string]any{"res_code": 1, "data": map[string]any{"list": []any{}, "page": 1, "total_page": 1, "invalid_filter_fields": []string{"tracker_id"}}})), nil
	}}, PMScope{SourceHost: "dj01.pm.netease.com"})
	d.filters["10"] = map[string]any{}
	if _, err := d.Discover(context.Background()); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("err=%v", err)
	}
}

func TestPMReaderReconciliationRejectsChangingObservations(t *testing.T) {
	for _, change := range []string{"stable", "status", "form_title", "deleted"} {
		t.Run(change, func(t *testing.T) {
			baseReads := 0
			d := NewPMReader(ExecOptions{Bin: "fake", LookPath: func(s string) (string, error) { return s, nil }, Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
				var data any
				switch toolNameFromArgs(args) {
				case "get_issue_base":
					baseReads++
					if baseReads == 2 && change == "deleted" {
						return nil, fmt.Errorf("source unavailable")
					}
					status := "已关闭"
					if baseReads == 2 && change == "status" {
						status = "新建"
					}
					data = map[string]any{"base": map[string]any{"id": 1, "project_id": 10, "tracker_id": 43, "subject": "Source", "description": "Repro", "status": status,
						"assigned_to": "Moved Owner", "project_name": "DJ01单机-Bug", "tracker": "BUG-崩溃", "updated_on": "2026-10-14T12:00:00+08:00"}, "core_fields": []any{}}
				case "getIssueForm":
					title := "Source"
					if change == "form_title" {
						title = "Edited during read"
					}
					data = map[string]any{"issue": map[string]any{"id": 1, "project_id": 10, "tracker_id": 43, "assigned_to_id": 949, "subject": title, "description": "Repro"}}
				default:
					t.Fatal("unexpected source tool")
				}
				return fabricText(mustJSON(map[string]any{"res_code": 1, "data": data})), nil
			}}, PMScope{SourceHost: "dj01.pm.netease.com"})
			source, err := ParseSource("https://dj01.pm.netease.com/issues/1")
			if err != nil {
				t.Fatal(err)
			}
			item, err := d.ReadIssue(context.Background(), source)
			if change == "stable" {
				if err != nil || item.Status != "已关闭" || item.Assignee.Name != "Moved Owner" || item.Project.Name != "DJ01单机-Bug" {
					t.Fatalf("item=%+v err=%v", item, err)
				}
			} else if err == nil {
				t.Fatalf("changing observation accepted: %+v", item)
			}
		})
	}
}

func TestPMReaderRejectsIncompletePagination(t *testing.T) {
	for _, raw := range []string{`{"list":[]}`, `{"list":[],"total_page":1}`, `{"list":[],"page":2,"total_page":1}`} {
		d := NewPMReader(ExecOptions{Bin: "fake", LookPath: func(s string) (string, error) { return s, nil }, Run: func(_ context.Context, _ string, _ []string) ([]byte, error) {
			return fabricText(mustJSON(map[string]any{"res_code": 1, "data": json.RawMessage(raw)})), nil
		}}, PMScope{SourceHost: "dj01.pm.netease.com"})
		d.filters["10"] = map[string]any{}
		if _, err := d.Discover(context.Background()); err == nil {
			t.Fatalf("malformed page accepted: %s", raw)
		}
	}
}

func TestPMReportShowsEveryFailureAndNoChange(t *testing.T) {
	r := PMReport{Results: []PMResult{{ExternalID: "1", State: "failed", Error: "write rejected"}}, Triage: []PMTriageResult{{Identifier: "MUL-2", State: "failed", ReasonCode: "invocation_not_allowed", Error: "invocation_not_allowed"}}, Errors: []PMFailure{{Stage: "读取来源工单", ExternalID: "3", Error: "not found"}}}
	text := r.Markdown()
	for _, want := range []string{"扫描时间", "查询范围", "本轮单数", "新增同步", "更新", "未分类转交", "异常与待人工确认", "write rejected", "invocation_not_allowed", "not found"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(text, "本轮无变化") || !r.HasFailures() {
		t.Fatal("failure reported as no change")
	}
	if text := (PMReport{}).Markdown(); !strings.Contains(text, "本轮无变化") {
		t.Fatalf("no-change report=%s", text)
	}
}

func TestPMReportSourceFieldsCannotCreateMentionsOrSections(t *testing.T) {
	mention := "[@unrelated](mention://agent/ee6f11fb-44c2-4226-a01d-1b0ba9921651)"
	r := PMReport{Results: []PMResult{{ExternalID: "1", State: "updated", Changes: []PMChange{{Field: "标题", New: "Source title\n## Extra section\n" + mention}}, Warnings: []string{mention}}}}
	text := r.Markdown()
	if strings.Count(text, "\n## ") != 4 || len(util.ParseMentions(text)) != 0 {
		t.Fatalf("source text escaped the fixed report: %s", text)
	}
	if r.Results[0].Changes[0].New != "Source title\n## Extra section\n"+mention {
		t.Fatal("structured receipt was rewritten")
	}
}
