package yixiezuo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func draftFixture(t *testing.T, createErr, readbackErr bool) (*ExecDriver, Operation, *[]map[string]any) {
	t.Helper()
	op := Operation{ID: "019a1234-1234-7123-8123-123456789012", Kind: "draft", State: "running", LeaseToken: "lease"}
	if err := json.Unmarshal([]byte(`{"draft":{"template":{"host":"dj01.pm.netease.com","id":"12","url":"https://dj01.pm.netease.com/issues/12"},"title":"New <idea>","body":"Only the new idea\nNo source body","agent_id":"019a1234-1234-7123-8123-123456789013"}}`), &op.Payload); err != nil {
		t.Fatal(err)
	}
	writes := []map[string]any{}
	createdDescription := ""
	driver := NewExecDriver(ExecOptions{Bin: "fake", LookPath: func(s string) (string, error) { return s, nil }, Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		var input map[string]any
		if err := json.Unmarshal([]byte(argumentsFromArgs(args)), &input); err != nil {
			return nil, err
		}
		var payload any
		switch toolNameFromArgs(args) {
		case "getIssueForm":
			issue := input["issue"].(map[string]any)
			switch issue["id"] {
			case float64(12):
				payload = map[string]any{"issue": map[string]any{"id": 12, "project_id": 7, "tracker_id": 34, "tracker_category": "Task", "status_id": 48, "fixed_version_id": 17, "assigned_to_id": 999, "description": "DO NOT COPY", "parent_issue_id": 11, "custom_field_values": map[string]any{"19": 381, "127": "P6", "40": []int{988}}}}
			case float64(13):
				if readbackErr {
					return nil, errors.New("readback unavailable")
				}
				payload = map[string]any{"issue": map[string]any{"id": 13, "subject": "New <idea>", "description": createdDescription, "status_id": 1, "lock_version": 1}}
			default:
				payload = map[string]any{"issue": map[string]any{"project_id": 7, "tracker_id": 34, "tracker_category": "Task", "author_id": 244, "status_id": 1, "custom_field_values": map[string]any{"19": 381}}, "fields": []any{
					map[string]any{"id": "subject", "type": "CORE", "required": true},
					map[string]any{"id": "status_id", "type": "CORE", "required": true},
					map[string]any{"id": "assigned_to_id", "type": "CORE", "required": true},
					map[string]any{"id": "fixed_version_id", "type": "CORE", "required": true},
					map[string]any{"id": "19", "type": "CUSTOM", "required": true},
					map[string]any{"id": "127", "type": "CUSTOM", "required": true},
				}}
			}
		case "bulkCreateIssue":
			writes = append(writes, input)
			if createErr {
				return nil, errors.New("lost create response")
			}
			createdDescription = input["issues"].([]any)[0].(map[string]any)["description"].(string)
			payload = []any{map[string]any{"id": 13, "uuid": "+1"}}
		case "get_issue_base":
			payload = map[string]any{"base": map[string]any{"id": 13, "subject": "New <idea>", "status": "New"}}
		case "get_issue_journals", "get_issue_attachments":
			payload = map[string]any{"list": []any{}}
		case "getIssueFieldOptions":
			payload = map[string]any{"core_fields": map[string]any{"status_id": map[string]any{"options": []any{map[string]any{"id": 1, "name": "New"}}}}}
		default:
			return nil, fmt.Errorf("unexpected tool %s", toolNameFromArgs(args))
		}
		return fabricText(mustJSON(map[string]any{"res_code": 1, "data": payload})), nil
	}})
	return driver, op, &writes
}

func TestDraftCreatesOnlyNewIdeaAndRequiredMetadata(t *testing.T) {
	d, op, writes := draftFixture(t, false, false)
	done := d.Execute(context.Background(), op)
	if done.State != "succeeded" || done.Snapshot == nil || done.Snapshot.Source.ID != "13" || len(*writes) != 1 {
		t.Fatalf("draft result=%+v writes=%d", done, len(*writes))
	}
	issue := (*writes)[0]["issues"].([]any)[0].(map[string]any)
	if issue["status_id"] != float64(1) || issue["assigned_to_id"] != float64(244) || issue["fixed_version_id"] != float64(17) {
		t.Fatalf("draft defaults=%v", issue)
	}
	for _, key := range []string{"id", "parent_issue_id", "attachments", "watcher_user_ids"} {
		if _, ok := issue[key]; ok {
			t.Fatalf("copied unrelated template field %s", key)
		}
	}
	custom := issue["custom_field_values"].(map[string]any)
	if len(custom) != 2 || custom["127"] != "P6" {
		t.Fatalf("draft custom fields=%v", custom)
	}
	body := issue["description"].(string)
	if !strings.Contains(body, "待澄清") || !strings.Contains(body, "[Multica draft "+op.ID+"]") || strings.Contains(body, "DO NOT COPY") {
		t.Fatalf("draft body=%s", body)
	}
	if done.Snapshot.Digest != done.Snapshot.Fingerprint() {
		t.Fatal("draft snapshot is not verifiable")
	}
}

func TestDraftRecognizesActualSourceValidationRejection(t *testing.T) {
	d, op, _ := draftFixture(t, false, false)
	originalRun := d.opts.Run
	writes := 0
	d.opts.Run = func(ctx context.Context, bin string, args []string) ([]byte, error) {
		if toolNameFromArgs(args) == "bulkCreateIssue" {
			writes++
			return fabricText([]byte(`{"res_code":1,"data":[{"errors":[{"message":"不包含于列表中","fields":[{"id":"assigned_to_id","key":"assignedToId","name":"指派给"}]}],"uuid":"+1","error":true}]}`)), nil
		}
		return originalRun(ctx, bin, args)
	}
	done := d.Execute(context.Background(), op)
	if done.State != "failed" || writes != 1 || !strings.Contains(done.Error, "assigned_to_id") {
		t.Fatalf("validation failure was reported as unconfirmed or retried: %+v writes=%d", done, writes)
	}
}

func TestDraftCreateOrReadbackFailureIsUnknownAndNeverRetried(t *testing.T) {
	for _, tc := range []struct {
		name             string
		create, readback bool
	}{{"create response lost", true, false}, {"readback unavailable", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			d, op, writes := draftFixture(t, tc.create, tc.readback)
			done := d.Execute(context.Background(), op)
			if done.State != "unknown" || len(*writes) != 1 {
				t.Fatalf("result=%+v writes=%d", done, len(*writes))
			}
		})
	}
}

func TestDraftDoesNotCreateOutsideRunningLease(t *testing.T) {
	d, op, writes := draftFixture(t, false, false)
	op.State = "unknown"
	done := d.Execute(context.Background(), op)
	if done.State != "failed" || len(*writes) != 0 {
		t.Fatalf("unknown operation repeated create: %+v writes=%d", done, len(*writes))
	}
}

func TestDraftRequiresNewFormDefaultsAndEscapesIdeaHTML(t *testing.T) {
	_, op, _ := draftFixture(t, false, false)
	op.Payload.Draft.Body = "<script>alert(1)</script>\nNew idea"
	template := draftForm{Issue: map[string]any{"project_id": 7, "tracker_id": 34, "tracker_category": "Task", "status_id": 48}}
	var initial draftForm
	if err := json.Unmarshal([]byte(`{"issue":{"status_id":1},"fields":[{"id":"status_id","type":"CORE","required":true}]}`), &initial); err != nil {
		t.Fatal(err)
	}
	fields, err := buildDraftFields(op, template, initial)
	if err != nil || strings.Contains(fields["description"].(string), "<script>") || !strings.Contains(fields["description"].(string), "&lt;script&gt;") {
		t.Fatalf("unsafe draft text: fields=%v err=%v", fields, err)
	}
	delete(initial.Issue, "status_id")
	if _, err := buildDraftFields(op, template, initial); err == nil {
		t.Fatal("copied the template's non-initial status")
	}
	initial.Issue["status_id"] = 1
	if err := json.Unmarshal([]byte(`{"issue":{"status_id":1},"fields":[{"id":"127","type":"CUSTOM","required":true}]}`), &initial); err != nil {
		t.Fatal(err)
	}
	if _, err := buildDraftFields(op, template, initial); err == nil || !strings.Contains(err.Error(), "127") {
		t.Fatalf("missing required source metadata was silently omitted: %v", err)
	}
}
