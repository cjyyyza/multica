package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/migrations"
)

// Run only against an empty database provisioned by scripts/init-worktree-env.sh
// and scripts/ensure-postgres.sh, never against an application database.
func TestRequirementMigrationAcceptance(t *testing.T) {
	if os.Getenv("MULTICA_MIGRATION_ACCEPTANCE") != "1" {
		t.Skip("requires a disposable acceptance database")
	}
	pool := openTestPool(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	var database string
	var tables int
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(database, "multica_requirement_acceptance_") {
		t.Fatalf("refusing application database %q", database)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE schemaname='public'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("acceptance requires an empty database, tables=%d: %v", tables, err)
	}
	up, err := migrations.Files("up")
	if err != nil {
		t.Fatal(err)
	}
	var baseline, feature []string
	for _, file := range up {
		if filepath.Base(file) < "9014_" {
			baseline = append(baseline, file)
		} else {
			feature = append(feature, file)
		}
	}
	apply := func(direction string, files []string) {
		t.Helper()
		if err := runMigrations(ctx, pool, runOptions{
			Direction: direction, Files: files,
			Hooks: hooksForDirection(direction), Conditions: conditionsForDirection(direction),
		}); err != nil {
			t.Fatalf("%s migration: %v", direction, err)
		}
	}
	upgrade := os.Getenv("MULTICA_MIGRATION_ACCEPTANCE_SCENARIO") == "upgrade"
	if upgrade {
		apply("up", baseline)
		if _, err := pool.Exec(ctx, `
			INSERT INTO "user" (id,name,email) VALUES ('00000000-0000-0000-0000-000000000071','Acceptance','migration-acceptance@example.invalid');
			INSERT INTO workspace (id,name,slug,issue_prefix) VALUES ('00000000-0000-0000-0000-000000000072','Acceptance','migration-acceptance','ACC');
			INSERT INTO issue (workspace_id,number,title,status,creator_type,creator_id)
			VALUES ('00000000-0000-0000-0000-000000000072',1,'Preserved ordinary issue','todo','member','00000000-0000-0000-0000-000000000071');
		`); err != nil {
			t.Fatal(err)
		}
		apply("up", feature)
	} else {
		apply("up", up)
	}
	assertSchema := func(present bool) {
		t.Helper()
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass('requirement') IS NOT NULL`).Scan(&exists); err != nil || exists != present {
			t.Fatalf("requirement exists=%v, want %v: %v", exists, present, err)
		}
		if upgrade {
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE title='Preserved ordinary issue' AND status='todo'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("ordinary issue count=%d: %v", count, err)
			}
		}
	}
	assertSchema(true)
	down, err := migrations.Files("down")
	if err != nil {
		t.Fatal(err)
	}
	var featureDown []string
	for _, file := range down {
		if filepath.Base(file) >= "9014_" {
			featureDown = append(featureDown, file)
		}
	}
	apply("down", featureDown)
	assertSchema(false)
	apply("up", feature)
	assertSchema(true)
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil || count != len(up) {
		t.Fatalf("migration ledger=%d, want %d: %v", count, len(up), err)
	}
}
