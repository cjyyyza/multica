package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/testenv"
)

func newSquadMemberSetRoleTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "set-role"}
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("member-id", "", "")
	cmd.Flags().String("member-type", "agent", "")
	cmd.Flags().String("role", "", "")
	cmd.Flags().String("output", "json", "")
	return cmd
}

func TestRunSquadMemberSetRolePatchesRole(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace-123")

	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if r.Header.Get("X-Workspace-ID") != "workspace-123" {
			t.Fatalf("X-Workspace-ID = %q, want workspace-123", r.Header.Get("X-Workspace-ID"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"squad_id":    "squad-123",
			"member_id":   "member-456",
			"member_type": "agent",
			"role":        "reviewer",
		})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	cmd := newSquadMemberSetRoleTestCmd()
	_ = cmd.Flags().Set("member-id", "member-456")
	_ = cmd.Flags().Set("member-type", "agent")
	_ = cmd.Flags().Set("role", "reviewer")
	_ = cmd.Flags().Set("output", "json")

	if err := runSquadMemberSetRole(cmd, []string{"squad-123"}); err != nil {
		t.Fatalf("runSquadMemberSetRole: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Fatalf("method = %s, want PATCH", gotMethod)
	}
	if gotPath != "/api/squads/squad-123/members/role" {
		t.Fatalf("path = %q, want /api/squads/squad-123/members/role", gotPath)
	}
	wantBody := map[string]any{"member_id": "member-456", "member_type": "agent", "role": "reviewer"}
	for k, want := range wantBody {
		if gotBody[k] != want {
			t.Fatalf("body[%s] = %v, want %v (full body: %#v)", k, gotBody[k], want, gotBody)
		}
	}
}

func TestRunSquadMemberSetRoleValidatesRequiredFlags(t *testing.T) {
	cmd := newSquadMemberSetRoleTestCmd()
	if err := runSquadMemberSetRole(cmd, []string{"squad-123"}); err == nil {
		t.Fatal("expected missing --member-id error")
	}

	cmd = newSquadMemberSetRoleTestCmd()
	_ = cmd.Flags().Set("member-id", "member-456")
	_ = cmd.Flags().Set("member-type", "invalid")
	if err := runSquadMemberSetRole(cmd, []string{"squad-123"}); err == nil {
		t.Fatal("expected invalid --member-type error")
	}

	cmd = newSquadMemberSetRoleTestCmd()
	_ = cmd.Flags().Set("member-id", "member-456")
	if err := runSquadMemberSetRole(cmd, []string{"squad-123"}); err == nil {
		t.Fatal("expected missing --role error")
	}
}

func newSquadCreateTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "create"}
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("name", "", "")
	cmd.Flags().String("description", "", "")
	cmd.Flags().String("leader", "", "")
	cmd.Flags().Int("max-concurrent-tasks", 0, "")
	cmd.Flags().String("output", "json", "")
	return cmd
}

func newSquadUpdateTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "update"}
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("name", "", "")
	cmd.Flags().String("description", "", "")
	cmd.Flags().String("instructions", "", "")
	cmd.Flags().String("leader", "", "")
	cmd.Flags().String("avatar-url", "", "")
	cmd.Flags().Int("max-concurrent-tasks", 0, "")
	cmd.Flags().String("output", "json", "")
	return cmd
}

func TestRunSquadCreateSendsMaxConcurrentTasks(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace-123")

	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":   "squad-123",
			"name": "Platform Squad",
		})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	cmd := newSquadCreateTestCmd()
	_ = cmd.Flags().Set("name", "Platform Squad")
	_ = cmd.Flags().Set("leader", "11111111-1111-1111-1111-111111111111")
	_ = cmd.Flags().Set("max-concurrent-tasks", "7")

	if err := runSquadCreate(cmd, nil); err != nil {
		t.Fatalf("runSquadCreate: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/squads" {
		t.Fatalf("request = %s %s, want POST /api/squads", gotMethod, gotPath)
	}
	if gotBody["max_concurrent_tasks"] != float64(7) {
		t.Fatalf("max_concurrent_tasks = %v, want 7 (full body: %#v)", gotBody["max_concurrent_tasks"], gotBody)
	}
}

func TestRunSquadCreateOmitsUnlimitedMaxConcurrentTasks(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace-123")

	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "squad-123", "name": "Platform Squad"})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	cmd := newSquadCreateTestCmd()
	_ = cmd.Flags().Set("name", "Platform Squad")
	_ = cmd.Flags().Set("leader", "11111111-1111-1111-1111-111111111111")

	if err := runSquadCreate(cmd, nil); err != nil {
		t.Fatalf("runSquadCreate: %v", err)
	}
	if _, ok := gotBody["max_concurrent_tasks"]; ok {
		t.Fatalf("max_concurrent_tasks should be omitted when unlimited, body: %#v", gotBody)
	}
}

func TestRunSquadMaxConcurrentTasksRejectsOutOfRange(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")

	for _, value := range []string{"-1", "51"} {
		createCmd := newSquadCreateTestCmd()
		_ = createCmd.Flags().Set("name", "Platform Squad")
		_ = createCmd.Flags().Set("leader", "11111111-1111-1111-1111-111111111111")
		_ = createCmd.Flags().Set("max-concurrent-tasks", value)
		if err := runSquadCreate(createCmd, nil); err == nil {
			t.Fatalf("create: expected out-of-range error for %s", value)
		}

		updateCmd := newSquadUpdateTestCmd()
		_ = updateCmd.Flags().Set("max-concurrent-tasks", value)
		if err := runSquadUpdate(updateCmd, []string{"squad-123"}); err == nil {
			t.Fatalf("update: expected out-of-range error for %s", value)
		}
	}
}

func TestRunSquadUpdateMaxConcurrentTasks(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace-123")

	cases := []struct {
		name     string
		value    string
		wantBody any
	}{
		{name: "sets a limit", value: "12", wantBody: float64(12)},
		{name: "zero clears the limit", value: "0", wantBody: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
					t.Fatalf("decode request body: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "squad-123", "name": "Platform Squad"})
			}))
			defer srv.Close()
			t.Setenv("MULTICA_SERVER_URL", srv.URL)

			cmd := newSquadUpdateTestCmd()
			_ = cmd.Flags().Set("max-concurrent-tasks", tc.value)
			if err := runSquadUpdate(cmd, []string{"squad-123"}); err != nil {
				t.Fatalf("runSquadUpdate: %v", err)
			}
			raw, ok := gotBody["max_concurrent_tasks"]
			if !ok {
				t.Fatalf("max_concurrent_tasks missing from body: %#v", gotBody)
			}
			if raw != tc.wantBody {
				t.Fatalf("max_concurrent_tasks = %#v, want %#v", raw, tc.wantBody)
			}
		})
	}
}

func TestRunSquadUpdateOmitsMaxConcurrentTasksWhenUnchanged(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace-123")

	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "squad-123", "name": "Renamed"})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	cmd := newSquadUpdateTestCmd()
	_ = cmd.Flags().Set("name", "Renamed")
	if err := runSquadUpdate(cmd, []string{"squad-123"}); err != nil {
		t.Fatalf("runSquadUpdate: %v", err)
	}
	if _, ok := gotBody["max_concurrent_tasks"]; ok {
		t.Fatalf("max_concurrent_tasks should be omitted when unchanged, body: %#v", gotBody)
	}
}

func TestMaxConcurrentTasksDisplay(t *testing.T) {
	if got := maxConcurrentTasksDisplay(map[string]any{"max_concurrent_tasks": float64(5)}); got != "5" {
		t.Fatalf("display = %q, want 5", got)
	}
	if got := maxConcurrentTasksDisplay(map[string]any{"max_concurrent_tasks": nil}); got != "unlimited" {
		t.Fatalf("display = %q, want unlimited", got)
	}
	if got := maxConcurrentTasksDisplay(map[string]any{}); got != "unlimited" {
		t.Fatalf("display = %q, want unlimited", got)
	}
}
