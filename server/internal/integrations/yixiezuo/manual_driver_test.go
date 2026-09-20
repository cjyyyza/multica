package yixiezuo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func manualFixture(t *testing.T) (*ExecDriver, *[]map[string]any) {
	t.Helper()
	writes := []map[string]any{}
	notes := ""
	status := "待开始"
	driver := NewExecDriver(ExecOptions{Bin: "fake", LookPath: func(s string) (string, error) { return s, nil }, Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		var payload any
		switch toolNameFromArgs(args) {
		case "get_issue_base":
			payload = map[string]any{"base": map[string]any{"id": 12, "subject": "Fix loot", "description": "<p>repro</p>", "status": status, "updated_on": "2026-09-19 10:00"}, "cf_fields": []any{map[string]any{"name": "Repro", "value": "Always"}}}
		case "getIssueForm":
			payload = map[string]any{"issue": map[string]any{"id": 12, "subject": "Fix loot", "description": "<img src=\"/attachments/91/screenshot.png\">", "status_id": 47, "lock_version": 1}}
		case "get_issue_journals":
			payload = map[string]any{"list": []any{map[string]any{"id": 1, "author": map[string]any{"name": "QA"}, "notes": notes}}, "has_more": false}
		case "get_issue_attachments":
			payload = map[string]any{"list": []any{map[string]any{"id": 90, "filename": "error.log"}}}
		case "getIssueFieldOptions":
			payload = map[string]any{"core_fields": map[string]any{"status_id": map[string]any{"options": []any{map[string]any{"id": 47, "name": "待开始"}, map[string]any{"id": 8, "name": "交付测试"}}}}}
		case "update_issue":
			var update map[string]any
			_ = json.Unmarshal([]byte(argumentsFromArgs(args)), &update)
			writes = append(writes, update)
			notes = update["notes"].(string)
			payload = map[string]any{}
		default:
			return nil, fmt.Errorf("unexpected tool %s", toolNameFromArgs(args))
		}
		return fabricText(mustJSON(map[string]any{"res_code": 1, "data": payload})), nil
	}})
	return driver, &writes
}

func TestManualSnapshotIncludesInlineImagesAndComments(t *testing.T) {
	d, _ := manualFixture(t)
	source, _ := ParseSource("https://dj01.pm.netease.com/issues/12")
	snapshot, err := d.ReadSnapshot(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Attachments) != 2 || snapshot.Attachments[1].ID != "91" {
		t.Fatalf("inline screenshot lost: %+v", snapshot.Attachments)
	}
	if snapshot.LockVersion == nil || *snapshot.LockVersion != 1 || len(snapshot.Statuses) != 2 {
		t.Fatalf("edit contract: %+v", snapshot)
	}
	if snapshot.UpdatedAt != "2026-09-19T02:00:00Z" {
		t.Fatal(snapshot.UpdatedAt)
	}
	if len(snapshot.Fields) != 1 {
		t.Fatal("custom field lost")
	}
}

func TestManualPublicationRequiresFreshSourceAndKnownStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		digest string
		status string
		want   string
	}{{"conflict", "old", "", "conflict"}, {"unknown status", "", "开发中", "failed"}} {
		t.Run(tc.name, func(t *testing.T) {
			d, writes := manualFixture(t)
			source, _ := ParseSource("https://dj01.pm.netease.com/issues/12")
			snapshot, err := d.ReadSnapshot(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			digest := tc.digest
			if digest == "" {
				digest = snapshot.Digest
			}
			done := d.Execute(context.Background(), Operation{ID: "op", Kind: "publish", Payload: OperationPayload{Source: source, ExpectedDigest: digest, StatusName: tc.status, Summary: "tested"}})
			if done.State != tc.want || len(*writes) != 0 {
				t.Fatalf("unsafe publication: %+v writes=%v", done, *writes)
			}
		})
	}
}

func TestManualPublicationOnlyWritesConfirmedFieldsAndReadbacks(t *testing.T) {
	d, writes := manualFixture(t)
	source, _ := ParseSource("https://dj01.pm.netease.com/issues/12")
	snapshot, _ := d.ReadSnapshot(context.Background(), source)
	op := Operation{ID: "op", Kind: "publish", Payload: OperationPayload{Source: source, ExpectedDigest: snapshot.Digest, Revision: 10, Summary: "Fix verified"}}
	result := d.Execute(context.Background(), op)
	if result.State != "succeeded" || len(*writes) != 1 {
		t.Fatalf("publication: %+v writes=%v", result, *writes)
	}
	update := (*writes)[0]
	issue := update["issue"].(map[string]any)
	if len(issue) != 1 || issue["lock_version"] != float64(1) {
		t.Fatalf("unexpected issue fields: %v", issue)
	}
	if !strings.Contains(update["notes"].(string), "[Multica result op]") {
		t.Fatal("missing durable publication marker")
	}
	result = d.Execute(context.Background(), op)
	if result.State != "succeeded" || len(*writes) != 1 {
		t.Fatal("receipt retry duplicated comment")
	}
}

func TestSourceValidationAndLocalTimezone(t *testing.T) {
	for _, bad := range []string{"https://evil.test/issues/1", "https://user@dj01.pm.netease.com/issues/1", "http://dj01.pm.netease.com/issues/1", "https://dj01.pm.netease.com/issues/0", "https://dj01.pm.netease.com/issues/1?issue_id=2"} {
		if _, err := ParseSource(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	got, _ := ParseLocalTime("2026-09-19 10:00")
	want := time.Date(2026, 9, 19, 2, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("time shifted: %s", got)
	}
}

func TestStagedAttachmentDoesNotChangeSourceFingerprint(t *testing.T) {
	d, _ := manualFixture(t)
	source, _ := ParseSource("https://dj01.pm.netease.com/issues/12")
	s, _ := d.ReadSnapshot(context.Background(), source)
	digest := s.Fingerprint()
	s.Attachments[1].LocalURL = "https://multica.test/api/file/1"
	s.Attachments[1].LocalID = "local"
	if s.Fingerprint() != digest {
		t.Fatal("staging changed source fingerprint")
	}
	if !strings.Contains(s.Markdown(), "https://multica.test/api/file/1") {
		t.Fatal("inline image still requires source login")
	}
}
