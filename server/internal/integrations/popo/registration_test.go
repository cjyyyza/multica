package popo

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestPickOnlineBridgePrefersLatestHeartbeat(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	older := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	newer := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	offline := util.MustParseUUID("33333333-3333-3333-3333-333333333333")
	bridges := []db.PopoBridge{
		{
			ID:              older,
			Status:          BridgeStatusActive,
			LastHeartbeatAt: pgtype.Timestamptz{Time: now.Add(-20 * time.Second), Valid: true},
		},
		{
			ID:              newer,
			Status:          BridgeStatusActive,
			LastHeartbeatAt: pgtype.Timestamptz{Time: now.Add(-5 * time.Second), Valid: true},
		},
		{
			ID:              offline,
			Status:          BridgeStatusActive,
			LastHeartbeatAt: pgtype.Timestamptz{Time: now.Add(-2 * time.Minute), Valid: true},
		},
	}
	got, err := pickOnlineBridge(bridges, pgtype.UUID{}, now)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if !uuidEqual(got.ID, newer) {
		t.Fatalf("got %s, want newest heartbeat %s", util.UUIDToString(got.ID), util.UUIDToString(newer))
	}

	picked, err := pickOnlineBridge(bridges, older, now)
	if err != nil || !uuidEqual(picked.ID, older) {
		t.Fatalf("preferred older online bridge: %v %+v", err, picked)
	}

	if _, err := pickOnlineBridge(bridges, offline, now); !errors.Is(err, ErrNoOnlineBridge) {
		t.Fatalf("offline preferred: %v", err)
	}
	if _, err := pickOnlineBridge(nil, pgtype.UUID{}, now); !errors.Is(err, ErrNoOnlineBridge) {
		t.Fatalf("none online: %v", err)
	}
}

func TestValidateQRURL(t *testing.T) {
	if err := validateQRURL("https://popo.example/qr"); err != nil {
		t.Fatalf("https: %v", err)
	}
	if err := validateQRURL("javascript:alert(1)"); err == nil {
		t.Fatal("expected javascript URL rejected")
	}
	if err := validateQRURL(""); err == nil {
		t.Fatal("expected empty rejected")
	}
}
