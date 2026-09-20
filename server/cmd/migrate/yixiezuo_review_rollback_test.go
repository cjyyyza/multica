package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestYixiezuoReviewRollbackPreservesRequestIndex(t *testing.T) {
	admin := openTestPool(t)
	ctx := context.Background()
	schema := fmt.Sprintf("yixiezuo_review_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE") })
	pool := openTestPoolWithSearchPath(t, schema)
	if _, err := pool.Exec(ctx, `CREATE TABLE yixiezuo_operation (
        workspace_id UUID NOT NULL, requested_by UUID NOT NULL,
        kind TEXT CHECK (kind IN ('preview','refresh','publish'))
    )`); err != nil {
		t.Fatal(err)
	}
	versions := []string{"538_yixiezuo_channel_review", "539_yixiezuo_operation_request_index"}
	options := runOptions{Direction: "up", Files: realMigrationFiles(t, versions, "up"), SchemaMigrationsTable: schema + ".schema_migrations", AdvisoryLockKey: time.Now().UnixNano(), Hooks: hooksForDirection("up")}
	if err := runMigrations(ctx, pool, options); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO yixiezuo_operation (workspace_id,requested_by,kind) VALUES (gen_random_uuid(),gen_random_uuid(),'review')`); err != nil {
		t.Fatal(err)
	}
	options.Direction, options.Hooks = "down", hooksForDirection("down")
	options.Files = realMigrationFiles(t, []string{versions[1], versions[0]}, "down")
	if err := runMigrations(ctx, pool, options); err == nil || !strings.Contains(err.Error(), "review records exist") {
		t.Fatalf("rollback did not protect retained review: %v", err)
	}
	var retained bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('idx_yixiezuo_operation_request') IS NOT NULL`).Scan(&retained); err != nil || !retained {
		t.Fatalf("failed rollback removed the live request index: retained=%v err=%v", retained, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM yixiezuo_operation`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, pool, options); err != nil {
		t.Fatalf("empty review schema could not roll back: %v", err)
	}
}
