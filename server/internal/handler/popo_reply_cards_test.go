package handler

import (
	"context"
	"log/slog"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/popo"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestPopoReplyRecoveryProjectsRunAndFinalFromDatabase(t *testing.T) {
	for _, terminal := range []string{"completed", "failed", "cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			e := popoP2Setup(t)
			e.inbound(t, "reply-recovery", "prepare a result", nil)
			var taskID, sessionID string
			if err := testPool.QueryRow(context.Background(), `SELECT t.id,t.chat_session_id FROM agent_task_queue t JOIN channel_task_delivery d ON d.task_id=t.id WHERE d.installation_id=$1`, e.install).Scan(&taskID, &sessionID); err != nil {
				t.Fatal(err)
			}
			dbfx.Exec(t, "UPDATE agent_task_queue SET status='running' WHERE id=$1", taskID)
			dbfx.Insert(t, "task_message", testutil.Cols{"task_id": taskID, "seq": 3, "type": "tool_use", "tool": "read", "input": testutil.Raw(`'{"secret":"private-tool-input"}'::jsonb`)})
			out := popo.NewOutbound(testHandler.Queries, testHandler.PopoBridge, slog.Default())
			reconcile := func() {
				t.Helper()
				for n := 0; n < 2; n++ {
					if _, err := out.Reconcile(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
			}
			reconcile()
			var runID string
			if err := testPool.QueryRow(context.Background(), `SELECT payload->'reply'->>'id' FROM popo_bridge_command WHERE installation_id=$1 AND payload->>'outbound_kind'='task_progress'`, e.install).Scan(&runID); err != nil {
				t.Fatal(err)
			}
			if count := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE installation_id=$1 AND payload->>'outbound_kind'='task_progress'`, e.install); count != 1 {
				t.Fatal(count)
			}
			if terminal == "completed" {
				dbfx.Insert(t, "chat_message", testutil.Cols{"chat_session_id": sessionID, "task_id": taskID, "role": "assistant", "content": "final result"})
			}
			dbfx.Exec(t, "UPDATE agent_task_queue SET status=$2,completed_at=now() WHERE id=$1", taskID, terminal)
			reconcile()
			if count := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE installation_id=$1 AND payload->'reply'->>'id'=$2 AND payload->'reply'->>'status'=$3 AND jsonb_array_length(payload->'reply'->'steps')=2`, e.install, runID, terminal); count != 1 {
				t.Fatalf("terminal snapshots=%d", count)
			}
			if count := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE installation_id=$1 AND payload::text LIKE '%private-tool-input%'`, e.install); count != 0 {
				t.Fatal("tool input leaked")
			}
		})
	}
}
