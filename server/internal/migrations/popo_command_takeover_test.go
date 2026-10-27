package migrations

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPopoAcceptedAndV2TakeoverMigrations(t *testing.T) {
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
	schema := "popo_command_takeover_test"
	_, _ = pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE") })
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SELECT set_config('search_path',$1,false)`, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `
		CREATE TABLE popo_bridge_command(
			id UUID PRIMARY KEY, workspace_id UUID, bridge_id UUID, installation_id UUID,
			delivery_id UUID UNIQUE, requirement_id UUID, status TEXT,
			accepted_at TIMESTAMPTZ, dispatched_at TIMESTAMPTZ, not_sent BOOLEAN,
			retryable BOOLEAN, evidence_finalized BOOLEAN, needs_reconcile BOOLEAN,
			unknown_at TIMESTAMPTZ, lease_id UUID, lease_expires_at TIMESTAMPTZ,
			last_error TEXT, recipient_binding_id UUID, payload JSONB, payload_hash TEXT,
			attempt INTEGER, expires_at TIMESTAMPTZ, remote_message_id TEXT,
			partial_delivery BOOLEAN, delivered_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ DEFAULT now(), updated_at TIMESTAMPTZ DEFAULT now()
		);
		CREATE TABLE requirement_outbox(
			requirement_id UUID, transport_delivery_id UUID, accepted_at TIMESTAMPTZ
		);
		CREATE TABLE popo_v2_delivery(
			command_id UUID, delivery_id UUID, bridge_id UUID, installation_id UUID,
			recipient_binding_id UUID, payload_snapshot JSONB, payload_hash TEXT,
			latest_attempt INTEGER, expires_at TIMESTAMPTZ, remote_message_id TEXT,
			partial_delivery BOOLEAN, retryable BOOLEAN, not_sent BOOLEAN,
			evidence_finalized BOOLEAN, status TEXT, created_at TIMESTAMPTZ, updated_at TIMESTAMPTZ
		);
		CREATE TABLE popo_v2_lease(delivery_id UUID, state TEXT, created_at TIMESTAMPTZ);
		CREATE TABLE popo_bridge(
			selected_protocol_version INTEGER, supported_protocol_versions JSONB,
			protocol_health_json JSONB DEFAULT '{}'
		);
	`); err != nil {
		t.Fatal(err)
	}

	applyMigrationFile(t, ctx, conn.Conn(), "9088_popo_requirement_accepted_unknown.up.sql")
	if _, err := conn.Exec(ctx, `
		INSERT INTO popo_bridge_command(id,delivery_id,requirement_id,status,accepted_at,dispatched_at)
		VALUES ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000011','00000000-0000-0000-0000-000000000021','pending',now(),now());
		INSERT INTO popo_bridge_command(id,delivery_id,requirement_id,status,accepted_at,dispatched_at)
		VALUES ('00000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000012','00000000-0000-0000-0000-000000000021','pending',now(),NULL);
	`); err != nil {
		t.Fatal(err)
	}
	applyMigrationFile(t, ctx, conn.Conn(), "9088_popo_requirement_accepted_unknown.up.sql")
	var status string
	if err := conn.QueryRow(ctx, `SELECT status FROM popo_bridge_command WHERE id='00000000-0000-0000-0000-000000000001'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "unknown" {
		t.Fatalf("accepted migration status = %s", status)
	}
	if err := conn.QueryRow(ctx, `SELECT status FROM popo_bridge_command WHERE id='00000000-0000-0000-0000-000000000002'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("undispatched migration status = %s", status)
	}

	if _, err := conn.Exec(ctx, `
		INSERT INTO requirement_outbox(requirement_id,transport_delivery_id)
		VALUES ('00000000-0000-0000-0000-000000000021','00000000-0000-0000-0000-000000000013');
		INSERT INTO popo_bridge_command(id,delivery_id,requirement_id,status,payload,attempt)
		VALUES ('00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000013','00000000-0000-0000-0000-000000000021','pending','{}',0);
		INSERT INTO popo_v2_delivery(
			command_id,delivery_id,status,latest_attempt,payload_snapshot,payload_hash,
			partial_delivery,retryable,not_sent,evidence_finalized,created_at,updated_at
		) VALUES (
			'00000000-0000-0000-0000-000000000031','00000000-0000-0000-0000-000000000013',
			'pending',1,'{"text":"hello"}','sha256:test',false,false,true,false,now(),now()
		);
		INSERT INTO popo_v2_lease(delivery_id,state,created_at)
		VALUES ('00000000-0000-0000-0000-000000000013','leased',now());
		INSERT INTO popo_bridge(selected_protocol_version,supported_protocol_versions)
		VALUES (2,'[1,2]');
	`); err != nil {
		t.Fatal(err)
	}
	applyMigrationFile(t, ctx, conn.Conn(), "9089_popo_v2_command_takeover.up.sql")
	var notSent, needsReconcile bool
	if err := conn.QueryRow(ctx, `SELECT status,not_sent,needs_reconcile FROM popo_bridge_command WHERE id='00000000-0000-0000-0000-000000000031'`).Scan(&status, &notSent, &needsReconcile); err != nil {
		t.Fatal(err)
	}
	if status != "unknown" || notSent || !needsReconcile {
		t.Fatalf("v2 dispatched takeover = status %s not_sent %v reconcile %v", status, notSent, needsReconcile)
	}
	var selected int
	if err := conn.QueryRow(ctx, `SELECT selected_protocol_version FROM popo_bridge`).Scan(&selected); err != nil {
		t.Fatal(err)
	}
	if selected != 1 {
		t.Fatalf("bridge protocol = %d", selected)
	}
}
