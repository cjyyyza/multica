package migrations

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRequirementRollbackGuardsRejectNewWrites(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("integration test requires Postgres at DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	schema := "requirement_rollback_guards_test"
	_, _ = pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE") })
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SELECT set_config('search_path',$1,false)`, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `
		CREATE TABLE schema_migrations(version TEXT PRIMARY KEY,applied_at TIMESTAMPTZ);
		INSERT INTO schema_migrations VALUES
			('9084_issue_wakeup_requirement_fold',now()-interval '1 hour'),
			('9085_popo_bridge_command_requirement_outbox',now()-interval '1 hour');
		CREATE TABLE requirement_wakeup(current_wakeup_id UUID,generation BIGINT);
		CREATE TABLE issue_wakeup(
			id UUID,last_triggered_comment_id UUID,updated_at TIMESTAMPTZ
		);
		INSERT INTO issue_wakeup VALUES('00000000-0000-0000-0000-000000000041',NULL,now());
		INSERT INTO requirement_wakeup VALUES('00000000-0000-0000-0000-000000000041',1);
		CREATE TABLE popo_bridge_command(
			id UUID,bridge_id UUID,requirement_id UUID,attempt INTEGER,updated_at TIMESTAMPTZ
		);
		INSERT INTO popo_bridge_command VALUES(
			'00000000-0000-0000-0000-000000000042',NULL,'00000000-0000-0000-0000-000000000043',0,now()
		);
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, readMigrationFile(t, "9084_issue_wakeup_requirement_fold.down.sql")); err == nil {
		t.Fatal("9084 down accepted a folded wakeup written after migration")
	}
	if _, err := conn.Exec(ctx, readMigrationFile(t, "9085_popo_bridge_command_requirement_outbox.down.sql")); err == nil {
		t.Fatal("9085 down accepted a command written after migration")
	}
	var wakeups, commands int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM issue_wakeup`).Scan(&wakeups); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM popo_bridge_command`).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if wakeups != 1 || commands != 1 {
		t.Fatalf("failed rollback discarded data: wakeups=%d commands=%d", wakeups, commands)
	}
}
