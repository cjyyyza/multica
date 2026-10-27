package migrations

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const requirementStatusPolicyMigrationTestSchema = "requirement_status_policy_migration_test"

// TestRequirementIssueStatusPolicyMigrations pins the SQL half of the policy:
// migration 9083 replaces the GUC-authorized 4-argument predicate with an
// explicit-action 5-argument one, validates the whole (action, current, next)
// tuple, and its down migration restores the 9081 signature.
func TestRequirementIssueStatusPolicyMigrations(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("integration test requires Postgres at DATABASE_URL")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect to Postgres: %v", err)
	}
	defer pool.Close()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire Postgres connection: %v", err)
	}
	defer conn.Release()

	cleanup := func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+requirementStatusPolicyMigrationTestSchema+" CASCADE")
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+requirementStatusPolicyMigrationTestSchema); err != nil {
		t.Fatalf("create isolated migration schema: %v", err)
	}
	if _, err := conn.Exec(ctx, `SELECT set_config('search_path', $1, false)`, requirementStatusPolicyMigrationTestSchema); err != nil {
		t.Fatalf("set isolated migration search path: %v", err)
	}

	if _, err := conn.Exec(ctx, `
		CREATE TABLE requirement (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), issue_id UUID);
		CREATE TABLE issue_status (workspace_id UUID NOT NULL, key TEXT NOT NULL, category TEXT NOT NULL);
		CREATE FUNCTION issue_status_category(p_category TEXT)
		RETURNS TEXT LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $function$
			SELECT CASE p_category
				WHEN 'backlog' THEN 'unstarted' WHEN 'todo' THEN 'unstarted'
				WHEN 'in_progress' THEN 'started' WHEN 'in_review' THEN 'started'
				WHEN 'blocked' THEN 'started' WHEN 'cancelled' THEN 'closed'
				ELSE p_category END
		$function$;
		CREATE FUNCTION issue_effective_status(p_workspace_id UUID, p_status TEXT)
		RETURNS TEXT LANGUAGE sql STABLE PARALLEL SAFE AS $function$
			SELECT CASE
				WHEN p_status IN ('backlog', 'todo', 'in_progress', 'in_review', 'done', 'blocked', 'cancelled') THEN p_status
				ELSE COALESCE((SELECT CASE s.category
					WHEN 'done' THEN 'done'
					WHEN 'cancelled' THEN 'cancelled'
					WHEN 'closed' THEN 'cancelled'
					ELSE p_status END
					FROM issue_status s WHERE s.workspace_id = p_workspace_id AND s.key = p_status), p_status)
			END
		$function$;
	`); err != nil {
		t.Fatalf("create policy prerequisites: %v", err)
	}

	applyMigrationFile(t, ctx, conn.Conn(), "9083_requirement_issue_status_explicit_action.up.sql")

	const (
		workspaceID  = "00000000-0000-4000-8000-0000000000a1"
		boundIssue   = "00000000-0000-4000-8000-0000000000a2"
		unboundIssue = "00000000-0000-4000-8000-0000000000a3"
	)
	if _, err := conn.Exec(ctx, `INSERT INTO requirement (issue_id) VALUES ($1)`, boundIssue); err != nil {
		t.Fatalf("bind requirement issue: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO issue_status (workspace_id, key, category) VALUES
			($1, 'custom_started', 'started'),
			($1, 'custom_done', 'done'),
			($1, 'custom_closed', 'closed')
	`, workspaceID); err != nil {
		t.Fatalf("insert custom statuses: %v", err)
	}

	allowed := func(issueID, current, next, action string) bool {
		var ok bool
		if err := conn.QueryRow(ctx, `SELECT requirement_issue_status_allowed($1, $2, $3, $4, $5)`,
			workspaceID, issueID, current, next, action).Scan(&ok); err != nil {
			t.Fatalf("evaluate policy (%s, %s, %s): %v", action, current, next, err)
		}
		return ok
	}

	for _, tc := range []struct {
		name    string
		issueID string
		current string
		next    string
		action  string
		want    bool
	}{
		// An issue without a requirement binding is unrestricted.
		{"unbound generic write", unboundIssue, "todo", "done", "", true},
		{"unbound block", unboundIssue, "todo", "blocked", "", true},

		// Generic edits cannot move a bound issue, but a same-status write is
		// not a transition.
		{"generic start", boundIssue, "todo", "in_progress", "", false},
		{"generic review leave", boundIssue, "in_review", "in_progress", "", false},
		{"generic same status", boundIssue, "in_review", "in_review", "", true},

		// Task start / completion.
		{"task start from todo", boundIssue, "todo", "in_progress", "task_start", true},
		{"task start alias", boundIssue, "backlog", "in_progress", "start", true},
		{"task start from blocked", boundIssue, "blocked", "in_progress", "task_start", false},
		{"task start from review", boundIssue, "in_review", "in_progress", "task_start", false},
		{"task complete from todo", boundIssue, "todo", "in_review", "task_complete", true},
		{"task complete from in_progress", boundIssue, "in_progress", "in_review", "task_complete", true},
		{"task complete alias", boundIssue, "in_progress", "in_review", "complete_task", true},
		{"task complete from blocked", boundIssue, "blocked", "in_review", "task_complete", false},
		{"task complete wrong target", boundIssue, "todo", "done", "task_complete", false},
		{"task complete custom started", boundIssue, "custom_started", "in_review", "task_complete", true},
		{"task complete custom done", boundIssue, "custom_done", "in_review", "task_complete", false},

		// Accept / reject.
		{"accept from review", boundIssue, "in_review", "done", "accept", true},
		{"accept from todo", boundIssue, "todo", "done", "accept", false},
		{"accept wrong target", boundIssue, "in_review", "in_progress", "accept", false},
		{"reject from review", boundIssue, "in_review", "in_progress", "reject", true},
		{"reject from todo", boundIssue, "todo", "in_progress", "reject", false},

		// Block / resume.
		{"block from in_progress", boundIssue, "in_progress", "blocked", "block", true},
		{"block from todo", boundIssue, "todo", "blocked", "block", true},
		{"block from review", boundIssue, "in_review", "blocked", "block", false},
		{"block from done", boundIssue, "done", "blocked", "block", false},
		{"resume from blocked", boundIssue, "blocked", "in_progress", "wakeup_resume", true},
		{"resume alias", boundIssue, "blocked", "in_progress", "resume", true},
		{"resume from todo", boundIssue, "todo", "in_progress", "wakeup_resume", false},

		// Cancel.
		{"cancel from review", boundIssue, "in_review", "cancelled", "cancel", true},
		{"cancel from todo", boundIssue, "todo", "cancelled", "cancel", true},
		{"cancel from done", boundIssue, "done", "cancelled", "cancel", false},
		{"cancel custom closed", boundIssue, "custom_closed", "cancelled", "cancel", false},

		// Anything unlisted is denied, closing the previous ELSE TRUE path.
		{"unknown action", boundIssue, "todo", "in_review", "made_up", false},
		{"empty action with action-only target", boundIssue, "todo", "in_review", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := allowed(tc.issueID, tc.current, tc.next, tc.action); got != tc.want {
				t.Fatalf("policy(%q, %s, %s) = %t, want %t", tc.action, tc.current, tc.next, got, tc.want)
			}
		})
	}

	applyMigrationFile(t, ctx, conn.Conn(), "9083_requirement_issue_status_explicit_action.down.sql")
	var fiveArgExists, fourArgExists bool
	if err := conn.QueryRow(ctx, `SELECT to_regprocedure('requirement_issue_status_allowed(uuid,uuid,text,text,text)') IS NOT NULL`).Scan(&fiveArgExists); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT to_regprocedure('requirement_issue_status_allowed(uuid,uuid,text,text)') IS NOT NULL`).Scan(&fourArgExists); err != nil {
		t.Fatal(err)
	}
	if fiveArgExists || !fourArgExists {
		t.Fatalf("rollback signatures: five-arg=%t four-arg=%t", fiveArgExists, fourArgExists)
	}
	// The restored 4-argument predicate keeps the old GUC authorization: the
	// rules 9081 enforced (review may only leave through reject, done only
	// through accept) still apply.
	var restored bool
	if err := conn.QueryRow(ctx, `SELECT requirement_issue_status_allowed($1::uuid, $2::uuid, 'in_review', 'in_progress')`, workspaceID, boundIssue).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if restored {
		t.Fatal("rolled-back policy allowed a generic review transition")
	}
	if _, err := conn.Exec(ctx, `SELECT set_config('multica.requirement_status_action', 'accept', false)`); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT requirement_issue_status_allowed($1::uuid, $2::uuid, 'in_review', 'done')`, workspaceID, boundIssue).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if !restored {
		t.Fatal("rolled-back policy ignored the accept GUC")
	}
	if _, err := conn.Exec(ctx, `RESET multica.requirement_status_action`); err != nil {
		t.Fatalf("reset policy GUC: %v", err)
	}
}
