package popo

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRobotIdleOnHeartbeat(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	agent := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	other := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	idle, _ := json.Marshal([]RobotReport{{
		RobotID: "default", Connected: true, OccupiedBy: nil,
	}})
	occupied, _ := json.Marshal([]RobotReport{{
		RobotID: "default", Connected: true, OccupiedBy: strPtr(OccupiedByDJ01Bot),
	}})
	multica, _ := json.Marshal([]RobotReport{{
		RobotID: "default", Connected: true, OccupiedBy: strPtr(OccupiedByMultica),
	}})
	disconnected, _ := json.Marshal([]RobotReport{{
		RobotID: "default", Connected: false,
	}})

	bridge := func(robots []byte, heartbeat time.Time) db.PopoBridge {
		return db.PopoBridge{
			Status:          BridgeStatusActive,
			LastHeartbeatAt: pgtype.Timestamptz{Time: heartbeat, Valid: true},
			RobotsJson:      robots,
		}
	}

	tests := []struct {
		name  string
		b     db.PopoBridge
		owner pgtype.UUID
		want  error
	}{
		{name: "idle", b: bridge(idle, now), want: nil},
		{name: "stale heartbeat", b: bridge(idle, now.Add(-time.Minute)), want: ErrRobotNotIdle},
		{name: "missing robot", b: bridge([]byte(`[]`), now), want: ErrRobotNotIdle},
		{name: "disconnected", b: bridge(disconnected, now), want: ErrRobotNotIdle},
		{name: "dj01bot occupied", b: bridge(occupied, now), want: ErrRobotOccupied},
		{name: "multica first bind", b: bridge(multica, now), want: nil},
		{name: "multica reentry", b: bridge(multica, now), owner: agent, want: nil},
		{name: "multica other agent", b: bridge(multica, now), owner: other, want: ErrRobotOccupied},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := robotIdleOnHeartbeat(tc.b, "default", agent, tc.owner, now)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func strPtr(s string) *string { return &s }
