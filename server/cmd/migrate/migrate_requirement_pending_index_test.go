package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/migrations"
)

func TestRequirementPendingIndexReplacementNeverLosesGuard(t *testing.T) {
	for appliedCount, name := range []string{"fresh", "interrupted_after_drop", "previous_migrations_applied"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			config := f.pool.Config()
			config.ConnConfig.RuntimeParams["search_path"] = f.schema
			pool, err := pgxpool.NewWithConfig(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			if _, err := pool.Exec(ctx, `CREATE TABLE agent_task_queue (
				issue_id uuid, agent_id uuid, comment_thread_id uuid, status text, context jsonb DEFAULT '{}'
			); CREATE TABLE schema_migrations(version text PRIMARY KEY, applied_at timestamptz DEFAULT now());`); err != nil {
				t.Fatal(err)
			}
			dir, err := migrations.ResolveDir()
			if err != nil {
				t.Fatal(err)
			}
			read := func(file string) string {
				t.Helper()
				body, err := os.ReadFile(filepath.Join(dir, file))
				if err != nil {
					t.Fatal(err)
				}
				return string(body)
			}
			if _, err := pool.Exec(ctx, read("452_agent_task_pending_thread_unique.up.sql")); err != nil {
				t.Fatal(err)
			}
			versions := []string{
				"9097_agent_task_requirement_delegation_pending_unique_drop",
				"9098_agent_task_requirement_delegation_pending_unique",
				"9099_requirement_delegation_pending_guard",
				"9100_requirement_delegation_pending_index_retire",
			}
			if appliedCount > 0 {
				for _, version := range versions[:appliedCount] {
					if _, err := pool.Exec(ctx, read(version+".up.sql")); err != nil {
						t.Fatal(err)
					}
					if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES ($1)`, version); err != nil {
						t.Fatal(err)
					}
				}
			}
			if appliedCount == 1 {
				conn, err := pool.Acquire(ctx)
				if err != nil {
					t.Fatal(err)
				}
				restore, _, err := conditionsForDirection("down")[versions[0]](ctx, conn)
				conn.Release()
				if err != nil || !restore {
					t.Fatalf("rollback must restore an interrupted guard: restore=%v error=%v", restore, err)
				}
			}
			const insert = `INSERT INTO agent_task_queue(issue_id,agent_id,status)
				VALUES ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002','queued')
				ON CONFLICT (issue_id,agent_id,(COALESCE(comment_thread_id,'00000000-0000-0000-0000-000000000000'::uuid)))
				WHERE status IN ('queued','dispatched') OR (status='deferred' AND context->>'channel_issue_media_pending'='true')
				DO NOTHING`
			assertGuard := func(stage string) {
				t.Helper()
				for range 2 {
					if _, err := pool.Exec(ctx, insert); err != nil {
						t.Fatalf("%s lost the old client's ON CONFLICT arbiter: %v", stage, err)
					}
				}
				var count int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue`).Scan(&count); err != nil || count != 1 {
					t.Fatalf("%s allowed duplicate pending runs: count=%d error=%v", stage, count, err)
				}
			}
			repairingMissingGuard := appliedCount == 1
			apply := func(direction, version string) {
				t.Helper()
				if err := runMigrations(ctx, pool, runOptions{
					Direction: direction, Files: []string{filepath.Join(dir, version+"."+direction+".sql")},
					Hooks: hooksForDirection(direction), Conditions: conditionsForDirection(direction), AdvisoryLockKey: f.lockKey,
				}); err != nil {
					t.Fatal(err)
				}
				if repairingMissingGuard && direction == "up" && version == versions[0] {
					return
				}
				assertGuard(direction + " " + version)
				if direction == "up" && version == versions[1] {
					repairingMissingGuard = false
				}
			}
			for _, version := range versions {
				apply("up", version)
			}
			assertDelegationGuard := func(stage string) {
				t.Helper()
				if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status='deferred',context='{"requirement_delegation":true}'`); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `INSERT INTO agent_task_queue SELECT * FROM agent_task_queue`); err == nil {
					t.Fatalf("%s allowed duplicate deferred Requirement runs", stage)
				}
				if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status='queued',context='{}'`); err != nil {
					t.Fatal(err)
				}
			}
			assertDelegationGuard("replacement guard")
			for i := len(versions) - 1; i >= 0; i-- {
				apply("down", versions[i])
				if versions[i] == versions[2] {
					assertDelegationGuard("rollback with 9098 still applied")
				}
			}
			for _, version := range versions {
				apply("up", version)
			}
		})
	}
}
