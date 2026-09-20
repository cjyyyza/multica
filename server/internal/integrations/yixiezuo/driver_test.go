package yixiezuo

import (
	"encoding/json"
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
	var page struct {
		List []rawCard `json:"list"`
	}
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
