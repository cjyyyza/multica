package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestRequirementGetAndCandidateCLISubmitsSnapshotWithStableKey(t *testing.T) {
	t.Chdir(t.TempDir())
	const (
		workspaceID   = "00000000-0000-4000-8000-000000000001"
		requirementID = "00000000-0000-4000-8000-000000000002"
		agentID       = "00000000-0000-4000-8000-000000000003"
		projectID     = "00000000-0000-4000-8000-000000000004"
	)
	rawSnapshot := `{"agentId":"` + agentID + `","goal":"  Ship the plan  ","nonGoals":[" No execution "],"scope":[" API "],"constraints":[" Keep scope "],"acceptanceCriteria":[" Canonical digest "],"projectId":"` + projectID + `","attachmentIds":["101"]}`
	if err := os.WriteFile("candidate.json", []byte(rawSnapshot), 0o600); err != nil {
		t.Fatal(err)
	}
	var getCalls, candidateCalls int
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer mat_plan_scope" {
			t.Errorf("authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/workspaces/"+workspaceID+"/requirements/"+requirementID:
			getCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": requirementID, "workspaceId": workspaceID, "revision": 7,
				"agentId": agentID, "projectId": projectID, "chatSessionId": "00000000-0000-4000-8000-000000000005",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/workspaces/"+workspaceID+"/requirements/"+requirementID+"/candidates":
			candidateCalls++
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body) != 2 {
				t.Errorf("candidate body keys = %v, want only expectedRevision and snapshot", body)
			}
			var expectedRevision int64
			if err := json.Unmarshal(body["expectedRevision"], &expectedRevision); err != nil {
				t.Error(err)
			}
			if expectedRevision != 7 || string(body["snapshot"]) != rawSnapshot {
				t.Errorf("candidate body = %+v, want revision 7 and raw snapshot %s", body, rawSnapshot)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": requirementID, "revision": 8})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", workspaceID)
	t.Setenv("MULTICA_TOKEN", "mat_plan_scope")
	t.Setenv("MULTICA_TASK_ID", "00000000-0000-4000-8000-000000000006")

	get := newRequirementCommand()
	get.SetArgs([]string{"get", requirementID})
	if err := get.Execute(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		cmd := newRequirementCommand()
		cmd.SetArgs([]string{"candidate", requirementID, "--expected-revision", "7", "--snapshot-file", "./candidate.json"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	if getCalls != 1 || candidateCalls != 2 {
		t.Fatalf("get/candidate calls = %d/%d", getCalls, candidateCalls)
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] || !strings.HasPrefix(keys[0], "requirement-candidate:") {
		t.Fatalf("idempotency keys = %+v", keys)
	}
}

func TestRequirementCandidateCLIRejectsInvalidSnapshotJSON(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("candidate.json", []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_SERVER_URL", "http://127.0.0.1:1")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "mat_plan_scope")
	t.Setenv("MULTICA_TASK_ID", "task-1")
	cmd := newRequirementCommand()
	cmd.SetArgs([]string{"candidate", "req-1", "--expected-revision", "1", "--snapshot-file", "./candidate.json"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "snapshot must be valid JSON") {
		t.Fatalf("invalid snapshot error = %v", err)
	}
}

func TestRequirementCandidateCLIReadsSnapshotStdin(t *testing.T) {
	const (
		workspaceID   = "00000000-0000-4000-8000-000000000011"
		requirementID = "00000000-0000-4000-8000-000000000012"
		agentID       = "00000000-0000-4000-8000-000000000013"
	)
	snapshot := `{"goal":"Plan","nonGoals":["None"],"scope":["CLI"],"constraints":["Strict"],"acceptanceCriteria":["Pass"],"projectId":null,"agentId":"` + agentID + `"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/workspaces/"+workspaceID+"/requirements/"+requirementID+"/candidates" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if string(body["snapshot"]) != snapshot {
			t.Errorf("candidate snapshot = %s, want %s", body["snapshot"], snapshot)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": requirementID, "revision": 3})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", workspaceID)
	t.Setenv("MULTICA_TOKEN", "mat_plan_scope")
	t.Setenv("MULTICA_TASK_ID", "00000000-0000-4000-8000-000000000014")
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = input
	t.Cleanup(func() { os.Stdin = original })
	if _, err := output.WriteString(snapshot); err != nil {
		t.Fatal(err)
	}
	output.Close()
	cmd := newRequirementCommand()
	cmd.SetArgs([]string{"candidate", requirementID, "--expected-revision", "2", "--snapshot-stdin", "--idempotency-key", "custom-candidate-key"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}
