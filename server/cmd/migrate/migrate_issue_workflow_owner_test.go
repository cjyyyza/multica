package main

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"
)

func TestIssueWorkflowOwnerMigrationBackfillsEarliestWorkspaceOwner(t *testing.T) {
	for _, preexisting := range []bool{false, true} {
		name := "old_schema"
		if preexisting {
			name = "existing_owner_column"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			base := openTestPool(t)
			schema := createScratchSchema(t, ctx, base, "workflow_owner_")
			pool := openTestPoolWithSearchPath(t, schema)
			for _, statement := range []string{
				`CREATE TABLE issue_workflow (name text, workspace_id uuid, is_default boolean)`,
				`CREATE TABLE member (id uuid, workspace_id uuid, user_id uuid, role text, created_at timestamptz)`,
				`INSERT INTO member VALUES
				 ('00000000-0000-0000-0000-000000000002',md5('a')::uuid,md5('tie-later')::uuid,'owner','2026-01-01'),
				 ('00000000-0000-0000-0000-000000000001',md5('a')::uuid,md5('first-owner')::uuid,'owner','2026-01-01'),
				 (md5('newer')::uuid,md5('a')::uuid,md5('newer-owner')::uuid,'owner','2026-01-02'),
				 (md5('admin')::uuid,md5('a')::uuid,md5('admin')::uuid,'admin','2025-01-01'),
				 (md5('other')::uuid,md5('b')::uuid,md5('other-owner')::uuid,'owner','2025-01-01')`,
				`INSERT INTO issue_workflow VALUES
				 ('custom',md5('a')::uuid,false), ('default',md5('a')::uuid,true),
				 ('other workspace',md5('b')::uuid,false), ('without owner',md5('c')::uuid,false),
				 ('manual',md5('a')::uuid,false)`,
			} {
				if _, err := pool.Exec(ctx, statement); err != nil {
					t.Fatal(err)
				}
			}
			if preexisting {
				if _, err := pool.Exec(ctx, `ALTER TABLE issue_workflow ADD COLUMN owner_id uuid;
				 UPDATE issue_workflow SET owner_id=md5('manual-owner')::uuid WHERE name='manual'`); err != nil {
					t.Fatal(err)
				}
			}
			const version = "585_issue_workflow_owner"
			options := runOptions{Direction: "up", Files: realMigrationFiles(t, []string{version}, "up"),
				SchemaMigrationsTable: schema + ".schema_migrations", AdvisoryLockKey: int64(rand.Uint64()&0x7fffffffffffffff) | 1}
			for range 2 {
				if err := runMigrations(ctx, pool, options); err != nil {
					t.Fatal(err)
				}
			}
			manualOwner := "md5('first-owner')::uuid"
			if preexisting {
				manualOwner = "md5('manual-owner')::uuid"
			}
			for workflow, expected := range map[string]string{
				"custom": "md5('first-owner')::uuid", "default": "NULL::uuid",
				"other workspace": "md5('other-owner')::uuid", "without owner": "NULL::uuid", "manual": manualOwner,
			} {
				var matches bool
				if err := pool.QueryRow(ctx, "SELECT owner_id IS NOT DISTINCT FROM ("+expected+") FROM issue_workflow WHERE name=$1", workflow).Scan(&matches); err != nil {
					t.Fatal(err)
				}
				if !matches {
					t.Errorf("workflow %q owner does not match %s", workflow, expected)
				}
			}
		})
	}
}
