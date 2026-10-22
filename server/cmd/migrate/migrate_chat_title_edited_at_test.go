package main

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// TestChatSessionTitleEditedAtMigrationBackfillsLegacyRenames pins the
// upgrade path for sessions renamed before this marker existed. The marker
// itself is written by UpdateChatSessionTitle; the migration can only infer
// legacy renames from "has a title, has no user message, and the row was
// touched after insert".
func TestChatSessionTitleEditedAtMigrationBackfillsLegacyRenames(t *testing.T) {
	adminPool := openTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	schema := createScratchSchema(t, ctx, adminPool, "migrate_chat_title_")
	pool := openTestPoolWithSearchPath(t, schema)

	for _, statement := range []string{
		`CREATE TABLE chat_session (
			id UUID PRIMARY KEY,
			title TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE TABLE chat_message (
			chat_session_id UUID NOT NULL,
			role TEXT NOT NULL,
			message_kind TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE channel_chat_session_binding (
			chat_session_id UUID NOT NULL
		)`,
		// 1: legacy empty chat renamed before its first message -> backfill.
		`INSERT INTO chat_session (id, title, created_at, updated_at) VALUES
			('00000000-0000-0000-0000-000000000001', 'Manual name',
			 '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z')`,
		// 2: insert-time seed (created_at = updated_at) -> stays eligible.
		`INSERT INTO chat_session (id, title, created_at, updated_at) VALUES
			('00000000-0000-0000-0000-000000000002', 'Seed title',
			 '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		// 3: already has a user message -> marker is irrelevant.
		`INSERT INTO chat_session (id, title, created_at, updated_at) VALUES
			('00000000-0000-0000-0000-000000000003', 'Messaged chat',
			 '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z')`,
		`INSERT INTO chat_message (chat_session_id, role, message_kind) VALUES
			('00000000-0000-0000-0000-000000000003', 'user', '')`,
		// 4: channel-origin session -> not a first-party rename.
		`INSERT INTO chat_session (id, title, created_at, updated_at) VALUES
			('00000000-0000-0000-0000-000000000004', 'Channel chat',
			 '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z')`,
		`INSERT INTO channel_chat_session_binding (chat_session_id) VALUES
			('00000000-0000-0000-0000-000000000004')`,
		// 5: no title -> nothing to protect.
		`INSERT INTO chat_session (id, title, created_at, updated_at) VALUES
			('00000000-0000-0000-0000-000000000005', '',
			 '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z')`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}

	const version = "9013_chat_session_title_edited_at"
	options := runOptions{
		Direction:             "up",
		Files:                 realMigrationFiles(t, []string{version}, "up"),
		SchemaMigrationsTable: schema + ".schema_migrations",
		AdvisoryLockKey:       int64(rand.Uint64()&0x7fffffffffffffff) | 1,
		Hooks:                 hooksForDirection("up"),
	}
	if err := runMigrations(ctx, pool, options); err != nil {
		t.Fatalf("run migration: %v", err)
	}

	rows, err := pool.Query(ctx, `SELECT id::text, title_edited_at FROM chat_session`)
	if err != nil {
		t.Fatalf("read backfill: %v", err)
	}
	defer rows.Close()

	got := map[string]pgtype.Timestamptz{}
	for rows.Next() {
		var id string
		var edited pgtype.Timestamptz
		if err := rows.Scan(&id, &edited); err != nil {
			t.Fatalf("scan backfill: %v", err)
		}
		got[id] = edited
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate backfill: %v", err)
	}

	legacy := got["00000000-0000-0000-0000-000000000001"]
	if !legacy.Valid || !legacy.Time.Equal(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("legacy rename marker = %v, want the row's updated_at", legacy)
	}
	for _, id := range []string{
		"00000000-0000-0000-0000-000000000002", // insert-time seed
		"00000000-0000-0000-0000-000000000003", // already has a user message
		"00000000-0000-0000-0000-000000000004", // channel-origin
		"00000000-0000-0000-0000-000000000005", // no title
	} {
		if edited := got[id]; edited.Valid {
			t.Errorf("session %s marker = %v, want NULL", id, edited)
		}
	}
}
