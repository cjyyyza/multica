package yixiezuo

import (
	"strings"
	"testing"
)

func TestPMScopeAndStatusBoundaries(t *testing.T) {
	scope := PMScope{SourceHost: "dj01.pm.netease.com", SourceProject: "dj01", AssigneeEmails: []string{"ZENGRUI01@corp.netease.com"}, TriageMention: "[@任务单分诊员](mention://agent/ee6f11fb-44c2-4226-a01d-1b0ba9921651)"}
	if err := scope.Normalize(); err != nil {
		t.Fatal(err)
	}
	item := PMIssue{Source: Source{Host: scope.SourceHost}, Project: PMProject{ID: "10"}, IssueType: "BUG-崩溃", Assignee: PMPerson{Email: "zengrui01@corp.netease.com"}}
	if !scope.Includes(item) {
		t.Fatal("instance scope lost BUG subtype")
	}
	item.IssueType = "Gameplay UR反馈"
	if scope.Includes(item) {
		t.Fatal("non-BUG included")
	}
	item.IssueType, item.Assignee.Email = "BUG", "someone@corp.netease.com"
	if scope.Includes(item) {
		t.Fatal("wrong assignee included")
	}
	for _, status := range []string{"已关闭", "已取消", "已解决", "交付测试"} {
		if !PMExcludedStatus(status) {
			t.Errorf("not excluded: %s", status)
		}
	}
	if PMTerminalStatus("已解决") != "" || PMTerminalStatus("交付测试") != "" {
		t.Fatal("invented closure decision")
	}
	if PMTerminalStatus("已关闭") != "done" || PMTerminalStatus("已取消") != "cancelled" {
		t.Fatal("closure mapping lost")
	}
}

func TestPMAppendMarkerPreservesBodyAndRefusesConflicts(t *testing.T) {
	body := "人工正文\r\n\r\n- 保留代码 `C:\\test`\n来源信息"
	appended, err := PMAppendMarker(body, "42")
	if err != nil || !strings.HasPrefix(appended, body) {
		t.Fatalf("body replaced: %q %v", appended, err)
	}
	again, err := PMAppendMarker(appended, "42")
	if err != nil || again != appended {
		t.Fatalf("repeat appended another marker: %q %v", again, err)
	}
	if _, err := PMAppendMarker(appended, "43"); err == nil {
		t.Fatal("conflicting source marker accepted")
	}
	if id, err := PMExternalID("外部单号：42\r\n"); err != nil || id != "42" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if _, err := PMExternalID("外部单号：42\n外部单号：43"); err == nil {
		t.Fatal("ambiguous markers accepted")
	}
}

func TestPMExternalIDRequiresStandaloneLine(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"plain", "外部单号：42", "42"},
		{"CRLF", "正文\r\n \t外部单号: \t42 \t\r\n下一行", "42"},
		{"inline prefix", "参考外部单号：42", ""},
		{"inline suffix", "外部单号：42 是参考资料", ""},
		{"split value", "外部单号：\n42", ""},
		{"split label", "外部单号\n：42", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := PMExternalID(tc.body); err != nil || got != tc.want {
				t.Fatalf("PMExternalID(%q) = %q, %v; want %q", tc.body, got, err, tc.want)
			}
		})
	}
}
