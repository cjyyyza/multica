package yixiezuo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestUnwrapPMMCPListIssues(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"ok":true,"code":"FABRIC_OK","data":{"code":"FABRIC_OK","data":{"code":"FABRIC_OK","hint":"Forwarded.","data":[{"type":"text","text":"{\"res_code\":1,\"res_msg\":\"请求成功\",\"data\":{\"list\":[{\"id\":108137,\"subject\":{\"value\":\"修登录\",\"type\":\"issues_link\"},\"status\":\"新建\",\"priority\":\"普通\",\"description\":\"desc\",\"start_date\":\"2026-09-18\",\"due_date\":\"2026-09-25\",\"updated_on\":\"2026-09-18 17:53\",\"project_id\":7}],\"page\":1,\"total_page\":1,\"total_count\":1}}"}]}}}`)
	inner, err := unwrapPMMCP(raw)
	if err != nil {
		t.Fatal(err)
	}
	var page listIssuesPage
	if err := json.Unmarshal(inner, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.List) != 1 {
		t.Fatalf("list %+v", page)
	}
	card, err := cardFromListRow(page.List[0])
	if err != nil {
		t.Fatal(err)
	}
	if card.ExternalID != "108137" || card.Title != "修登录" || card.StatusName != "新建" || card.Priority != "普通" {
		t.Fatalf("card %+v", card)
	}
	if card.UpdatedAt.IsZero() {
		t.Fatal("expected updated_on to parse")
	}
}

func TestUnwrapPMMCPGetIssueBase(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"ok":true,"data":{"data":{"data":[{"type":"text","text":"{\"res_code\":1,\"data\":{\"base\":{\"id\":12,\"subject\":\"详情\",\"description\":\"<p>d</p>\",\"status\":\"进行中\",\"updated_on\":\"2026-09-18T17:42:28+08:00\"},\"core_fields\":[[{\"key\":\"start_date\",\"value\":\"2026-09-18\"},{\"key\":\"due_date\",\"value\":\"2026-09-25\"}]]}}"}]}}}`)
	inner, err := unwrapPMMCP(raw)
	if err != nil {
		t.Fatal(err)
	}
	card, err := parseIssueBase(inner)
	if err != nil {
		t.Fatal(err)
	}
	if card.ExternalID != "12" || card.StatusName != "进行中" || card.StartDate != "2026-09-18" || card.DueDate != "2026-09-25" {
		t.Fatalf("card %+v", card)
	}
}

func TestUnwrapPMMCPListInstances(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"ok":true,"code":"FABRIC_OK","data":{"code":"FABRIC_OK","data":{"code":"FABRIC_OK","data":[{"domain":"dj01.pm.netease.com","name":"DJ01"}]}}}`)
	inner, err := unwrapPMMCP(raw)
	if err != nil {
		t.Fatal(err)
	}
	var instances []struct {
		Domain string `json:"domain"`
	}
	if err := json.Unmarshal(inner, &instances); err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 || instances[0].Domain != "dj01.pm.netease.com" {
		t.Fatalf("instances %+v", instances)
	}
}

func TestExecDriverListAndUpdate(t *testing.T) {
	t.Parallel()
	drv := NewExecDriver(ExecOptions{
		Bin:               "popo-cli",
		GCPHost:           "dj01.pm.netease.com",
		ExternalProjectID: "7",
		ListQueryID:       "9",
		LookPath:          func(file string) (string, error) { return file, nil },
		Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
			tool := toolNameFromArgs(args)
			switch tool {
			case "list_issues":
				if !containsArg(args, "context=kanban") || !containsArgPrefix(args, `arguments=`) {
					return nil, fmt.Errorf("expected pmmcp list_issues, got %v", args)
				}
				payload := mustJSON(map[string]any{
					"res_code": 1,
					"data": map[string]any{
						"list": []any{map[string]any{
							"id": 7, "project_id": 7, "subject": "卡片", "status": "新建",
							"updated_on": "2026-01-01T00:00:00Z",
						}},
						"page": 1, "total_page": 1, "total_count": 1,
					},
				})
				return fabricText(payload), nil
			case "listIssueStatuses":
				return fabricText(mustJSON(map[string]any{
					"res_code": 1,
					"data":     map[string]any{"list": []any{map[string]any{"id": 7, "name": "开发中"}}, "total_page": 1},
				})), nil
			case "update_issue":
				argJSON := argumentsFromArgs(args)
				if !strings.Contains(argJSON, `"status_id":7`) || !strings.Contains(argJSON, `"subject":"新标题"`) {
					return nil, fmt.Errorf("update args %s", argJSON)
				}
				return fabricText(mustJSON(map[string]any{
					"res_code": 1,
					"data": map[string]any{"base": map[string]any{
						"id": 7, "subject": "新标题", "status": "开发中", "updated_on": "2026-01-03T00:00:00Z",
					}},
				})), nil
			default:
				return nil, fmt.Errorf("unexpected tool %q args %v", tool, args)
			}
		},
	})
	cards, err := drv.ListCards(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].ExternalID != "7" {
		t.Fatalf("list %+v", cards)
	}
	updated, err := drv.UpdateCard(context.Background(), "7", IssueFields{Title: "新标题", Description: "d"}, "开发中")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "新标题" || updated.StatusName != "开发中" {
		t.Fatalf("update %+v", updated)
	}
}

func TestExecDriverRefusesUnscopedKanban(t *testing.T) {
	t.Parallel()
	drv := NewExecDriver(ExecOptions{
		Bin:      "popo-cli",
		GCPHost:  "dj01.pm.netease.com",
		LookPath: func(file string) (string, error) { return file, nil },
		Run: func(context.Context, string, []string) ([]byte, error) {
			t.Fatal("should not call CLI without a scope")
			return nil, nil
		},
	})
	if _, err := drv.ListCards(context.Background()); err == nil {
		t.Fatal("expected unscoped list to fail")
	}
}

func TestExecDriverRefusesUnfilteredProjectScope(t *testing.T) {
	t.Parallel()
	drv := NewExecDriver(ExecOptions{
		Bin:               "popo-cli",
		GCPHost:           "dj01.pm.netease.com",
		ExternalProjectID: "7",
		LookPath:          func(file string) (string, error) { return file, nil },
		Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
			if toolNameFromArgs(args) != "list_issues" {
				return nil, fmt.Errorf("unexpected %v", args)
			}
			return fabricText(mustJSON(map[string]any{
				"res_code": 1,
				"data": map[string]any{
					"list": []any{
						map[string]any{"id": 1, "project_id": 10, "subject": "其他项目", "status": "新建"},
						map[string]any{"id": 2, "project_id": 7, "subject": "本项目", "status": "新建"},
					},
					"page": 1, "total_page": 100, "total_count": 16943,
				},
			})), nil
		},
	})
	if _, err := drv.ListCards(context.Background()); err == nil {
		t.Fatal("expected mixed unfiltered kanban to fail without query_id")
	}
}

func fabricText(inner []byte) []byte {
	block := map[string]any{"type": "text", "text": string(inner)}
	return mustJSON(map[string]any{
		"ok": true,
		"data": map[string]any{
			"data": map[string]any{
				"data": []any{block},
			},
		},
	})
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

func toolNameFromArgs(args []string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, "toolName=") {
			return strings.TrimPrefix(arg, "toolName=")
		}
	}
	return ""
}

func argumentsFromArgs(args []string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, "arguments=") {
			return strings.TrimPrefix(arg, "arguments=")
		}
	}
	return ""
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func containsArgPrefix(args []string, prefix string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	return false
}
