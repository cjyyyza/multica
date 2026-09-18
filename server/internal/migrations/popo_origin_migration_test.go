package migrations

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const popoOriginMigrationTestSchema = "popo_origin_migration_test"

func TestPopoOriginMigrationsUpDownAndCatalog(t *testing.T) {
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
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+popoOriginMigrationTestSchema+" CASCADE")
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+popoOriginMigrationTestSchema); err != nil {
		t.Fatalf("create isolated migration schema: %v", err)
	}
	if _, err := conn.Exec(ctx, `SELECT set_config('search_path', $1, false)`, popoOriginMigrationTestSchema); err != nil {
		t.Fatalf("set isolated migration search path: %v", err)
	}

	if _, err := conn.Exec(ctx, `
		CREATE TABLE issue (
			id UUID PRIMARY KEY,
			origin_type TEXT NULL
		);
		ALTER TABLE issue ADD CONSTRAINT issue_origin_type_check
			CHECK (origin_type IN ('autopilot', 'quick_create', 'lark_chat', 'slack_chat', 'agent_create', 'dingtalk_chat', 'wecom_chat', 'telegram_chat'));
	`); err != nil {
		t.Fatalf("create pre-POPO issue table: %v", err)
	}

	assertPopoOriginRejected(t, ctx, conn.Conn(), "00000000-0000-4000-8000-000000000001")

	applyMigrationFile(t, ctx, conn.Conn(), "500_issue_origin_popo_chat.up.sql")
	assertPopoOriginConstraint(t, ctx, conn.Conn(), false, true)
	if _, err := conn.Exec(ctx, `INSERT INTO issue (id, origin_type) VALUES ($1, 'popo_chat')`, "00000000-0000-4000-8000-000000000002"); err != nil {
		t.Fatalf("insert popo_chat after widening constraint: %v", err)
	}

	applyMigrationFile(t, ctx, conn.Conn(), "501_issue_origin_popo_chat_validate.up.sql")
	assertPopoOriginConstraint(t, ctx, conn.Conn(), true, true)

	applyMigrationFile(t, ctx, conn.Conn(), "501_issue_origin_popo_chat_validate.down.sql")
	assertPopoOriginConstraint(t, ctx, conn.Conn(), false, true)
	if _, err := conn.Exec(ctx, `DELETE FROM issue WHERE origin_type = 'popo_chat'`); err != nil {
		t.Fatalf("remove POPO row before narrowing rollback: %v", err)
	}
	applyMigrationFile(t, ctx, conn.Conn(), "500_issue_origin_popo_chat.down.sql")
	assertPopoOriginConstraint(t, ctx, conn.Conn(), true, false)
	assertPopoOriginRejected(t, ctx, conn.Conn(), "00000000-0000-4000-8000-000000000003")
}

func assertPopoOriginConstraint(t *testing.T, ctx context.Context, conn *pgx.Conn, wantValidated, wantPopo bool) {
	t.Helper()
	var validated bool
	var definition string
	if err := conn.QueryRow(ctx, `
		SELECT convalidated, pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conrelid = 'issue'::regclass AND conname = 'issue_origin_type_check'
	`).Scan(&validated, &definition); err != nil {
		t.Fatalf("inspect issue origin constraint: %v", err)
	}
	if validated != wantValidated {
		t.Fatalf("constraint validated = %t, want %t", validated, wantValidated)
	}
	if strings.Contains(definition, "popo_chat") != wantPopo {
		t.Fatalf("constraint POPO membership = %t, want %t: %s", strings.Contains(definition, "popo_chat"), wantPopo, definition)
	}
}

func assertPopoOriginRejected(t *testing.T, ctx context.Context, conn *pgx.Conn, id string) {
	t.Helper()
	if _, err := conn.Exec(ctx, `INSERT INTO issue (id, origin_type) VALUES ($1, 'popo_chat')`, id); !isCheckViolation(err) {
		t.Fatalf("insert popo_chat under pre-POPO constraint: got %v, want check violation", err)
	}
}
