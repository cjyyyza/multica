package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func squadTaskFromSource(t *testing.T, f squadConcurrencyFixture, source, runtimeID string) db.AgentTaskQueue {
	t.Helper()
	ctx := context.Background()
	var task db.AgentTaskQueue
	var err error
	switch source {
	case "quick_create":
		payload, marshalErr := json.Marshal(QuickCreateContext{Type: QuickCreateContextType, SquadID: f.squadID})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		task, err = f.queries.CreateQuickCreateTask(ctx, db.CreateQuickCreateTaskParams{
			AgentID: util.MustParseUUID(f.agentB), RuntimeID: util.MustParseUUID(runtimeID), Context: payload,
		})
	case "run_only":
		autopilotID := f.Insert(t, "autopilot", testutil.Cols{
			"workspace_id": f.WorkspaceID, "title": "capped run-only", "assignee_type": "squad",
			"assignee_id": f.squadID, "execution_mode": "run_only", "created_by_type": "member", "created_by_id": f.UserID,
		})
		runID := f.Insert(t, "autopilot_run", testutil.Cols{
			"autopilot_id": autopilotID, "source": "manual", "status": "running", "squad_id": f.squadID,
		})
		task, err = f.queries.CreateAutopilotTask(ctx, db.CreateAutopilotTaskParams{
			AgentID: util.MustParseUUID(f.agentB), RuntimeID: util.MustParseUUID(runtimeID), AutopilotRunID: util.MustParseUUID(runID),
		})
	case "guest_delegation", "deep_guest_delegation":
		issueID := f.Issue(t, "guest squad issue", testutil.Cols{"assignee_type": "member", "assignee_id": f.UserID})
		if source == "guest_delegation" {
			otherSquad := f.Squad(t, "assigned squad", f.agentA)
			f.Exec(t, `UPDATE issue SET assignee_type = 'squad', assignee_id = $1 WHERE id = $2`, otherSquad, issueID)
		}
		parentID := f.Task(t, f.agentA, testutil.Cols{
			"issue_id": issueID, "runtime_id": f.runtimeID, "squad_id": f.squadID, "status": "completed", "completed_at": testutil.Raw("now()"),
		})
		if source == "deep_guest_delegation" {
			for range 4 {
				parentID = f.Task(t, f.agentA, testutil.Cols{
					"issue_id": issueID, "runtime_id": f.runtimeID, "delegated_from_task_id": parentID, "status": "completed", "completed_at": testutil.Raw("now()"),
				})
			}
		}
		taskID := f.Task(t, f.agentB, testutil.Cols{"issue_id": issueID, "runtime_id": runtimeID, "delegated_from_task_id": parentID})
		task, err = f.queries.GetAgentTask(ctx, util.MustParseUUID(taskID))
	default:
		t.Fatalf("unknown squad source %q", source)
	}
	if err != nil {
		t.Fatalf("create %s task: %v", source, err)
	}
	if !task.ID.Valid {
		t.Fatal("source task was not created")
	}
	if source == "quick_create" || source == "run_only" {
		f.Cleanup(t, `DELETE FROM agent_task_queue WHERE id = $1`, task.ID)
	}
	return task
}

func TestClaimTaskSquadSourcesRespectCapacityAndWakePeers(t *testing.T) {
	for _, source := range []string{"quick_create", "run_only", "guest_delegation", "deep_guest_delegation"} {
		t.Run(source, func(t *testing.T) {
			f := newSquadConcurrencyFixture(t, 1)
			ctx := context.Background()
			peerRuntime := f.Runtime(t, "source-peer-runtime")
			f.Exec(t, `UPDATE agent SET runtime_id = $1 WHERE id = $2`, peerRuntime, f.agentB)
			busyIssue := f.squadIssue(t, "squad at capacity")
			busyTask := f.Task(t, f.agentA, testutil.Cols{
				"issue_id": busyIssue, "runtime_id": f.runtimeID, "squad_id": f.squadID, "status": "running", "started_at": testutil.Raw("now()"),
			})
			candidate := squadTaskFromSource(t, f, source, peerRuntime)
			if got := claimFor(t, f.svc, f.agentB); got != nil {
				t.Fatalf("%s bypassed the squad cap: claimed %s", source, util.UUIDToString(got.ID))
			}
			capacity, err := f.queries.GetEffectiveSquadConcurrencyForTask(ctx, candidate.ID)
			if err != nil || util.UUIDToString(capacity.SquadID) != f.squadID {
				t.Fatalf("effective squad = %+v, error = %v; want %s", capacity, err, f.squadID)
			}
			runtimes, err := f.queries.ListQueuedTaskRuntimesBySquad(ctx, util.MustParseUUID(f.squadID))
			if err != nil || len(runtimes) != 1 || util.UUIDToString(runtimes[0]) != peerRuntime {
				t.Fatalf("queued runtimes = %v, error = %v; want [%s]", runtimes, err, peerRuntime)
			}
			f.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, busyTask)
			claimed := claimFor(t, f.svc, f.agentB)
			if claimed == nil || claimed.ID != candidate.ID {
				t.Fatalf("claim after releasing capacity = %v; want %s", claimed, util.UUIDToString(candidate.ID))
			}
			active, err := f.queries.CountActiveTasksForEffectiveSquad(ctx, util.MustParseUUID(f.squadID))
			if err != nil || active != 1 {
				t.Fatalf("active squad tasks = %d, error = %v; want 1", active, err)
			}

			f.Task(t, f.agentA, testutil.Cols{"issue_id": busyIssue, "runtime_id": f.runtimeID, "squad_id": f.squadID})
			wakeup := &stubWakeup{}
			f.svc.Wakeup = wakeup
			f.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, candidate.ID)
			f.svc.NotifyTaskFinished(*claimed)
			for _, call := range wakeup.calls {
				if call.runtimeID == f.runtimeID {
					return
				}
			}
			t.Fatalf("finishing %s did not wake the blocked peer; calls = %v", source, wakeup.calls)
		})
	}
}

func TestClaimTaskGuestSquadDoesNotConsumeAssigneeSquadCapacity(t *testing.T) {
	f := newSquadConcurrencyFixture(t, 1)
	otherSquad := f.Squad(t, "busy assigned squad", f.agentA, testutil.Cols{"max_concurrent_tasks": 1})
	busyIssue := f.Issue(t, "assigned squad at capacity", testutil.Cols{"assignee_type": "squad", "assignee_id": otherSquad})
	f.Task(t, f.agentA, testutil.Cols{
		"issue_id": busyIssue, "runtime_id": f.runtimeID, "squad_id": otherSquad, "status": "running", "started_at": testutil.Raw("now()"),
	})
	guest := squadTaskFromSource(t, f, "guest_delegation", f.runtimeID)
	f.Exec(t, `UPDATE issue SET assignee_id = $1 WHERE id = $2`, otherSquad, guest.IssueID)
	if got := claimFor(t, f.svc, f.agentB); got == nil || got.ID != guest.ID {
		t.Fatalf("guest claim = %v; another squad's capacity must not block it", got)
	}
	for _, squadID := range []string{f.squadID, otherSquad} {
		active, err := f.queries.CountActiveTasksForEffectiveSquad(context.Background(), util.MustParseUUID(squadID))
		if err != nil || active != 1 {
			t.Fatalf("squad %s active tasks = %d, error = %v; want 1", squadID, active, err)
		}
	}
}

func TestSquadSchedulingOwnershipIgnoresUnrelatedOrMalformedContext(t *testing.T) {
	f := newSquadConcurrencyFixture(t, 1)
	for _, payload := range []map[string]string{
		{"type": QuickCreateContextType, "squad_id": "not-a-uuid"},
		{"type": "other", "squad_id": f.squadID},
	} {
		contextJSON, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		taskID := f.Task(t, f.agentA, testutil.Cols{"runtime_id": f.runtimeID, "context": contextJSON})
		if _, err := f.queries.GetEffectiveSquadConcurrencyForTask(context.Background(), util.MustParseUUID(taskID)); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("context %s: got error %v, want no squad cap", contextJSON, err)
		}
	}
}

func TestSquadSchedulingOwnershipStopsAtDelegationCycles(t *testing.T) {
	f := newSquadConcurrencyFixture(t, 1)
	first := f.Task(t, f.agentA, testutil.Cols{"runtime_id": f.runtimeID})
	second := f.Task(t, f.agentB, testutil.Cols{"runtime_id": f.runtimeID, "delegated_from_task_id": first})
	f.Exec(t, `UPDATE agent_task_queue SET delegated_from_task_id = $1 WHERE id = $2`, second, first)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := f.queries.GetEffectiveSquadConcurrencyForTask(ctx, util.MustParseUUID(first)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cyclic delegation returned %v, want no squad cap without timing out", err)
	}
}

func TestClaimTaskSquadReloadsLimitAfterCapacityWait(t *testing.T) {
	for _, tc := range []struct {
		name               string
		initiallyUnlimited bool
		reassigned         bool
	}{
		{name: "lower_finite_limit"},
		{name: "limit_previously_unlimited_squad", initiallyUnlimited: true},
		{name: "reassigned_while_waiting", reassigned: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSquadConcurrencyFixture(t, 3)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			if tc.initiallyUnlimited {
				f.Exec(t, `UPDATE squad SET max_concurrent_tasks = NULL WHERE id = $1`, f.squadID)
			}
			busyIssue := f.squadIssue(t, "busy during limit change")
			f.Task(t, f.agentA, testutil.Cols{
				"issue_id": busyIssue, "runtime_id": f.runtimeID, "squad_id": f.squadID,
				"status": "running", "started_at": testutil.Raw("now()"),
			})
			waitingIssue := f.squadIssue(t, "waiting during limit change")
			waitingTask := f.Task(t, f.agentB, testutil.Cols{"issue_id": waitingIssue, "runtime_id": f.runtimeID})
			var targetSquad string
			if tc.reassigned {
				targetSquad = f.Squad(t, "new capacity owner", f.agentA, testutil.Cols{"max_concurrent_tasks": 1})
			}
			barrier, err := f.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer barrier.Rollback(context.Background())
			if err := f.queries.WithTx(barrier).LockSquadClaimAdvisory(ctx, util.MustParseUUID(f.squadID)); err != nil {
				t.Fatal(err)
			}
			var holderPID int
			if err := barrier.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
				t.Fatal(err)
			}
			type claimResult struct {
				task *db.AgentTaskQueue
				err  error
			}
			result := make(chan claimResult, 1)
			go func() {
				task, err := f.svc.ClaimTask(ctx, util.MustParseUUID(f.agentB))
				result <- claimResult{task, err}
			}()
			for {
				var waiting bool
				if err := f.Pool.QueryRow(ctx, `
					SELECT EXISTS (
					    SELECT 1 FROM pg_stat_activity
					    WHERE datname = current_database() AND wait_event = 'advisory'
					      AND $1::int = ANY(pg_blocking_pids(pid))
					)`, holderPID).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case got := <-result:
					t.Fatalf("claim completed before taking the capacity lock: error=%v", got.err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(5 * time.Millisecond):
				}
			}
			// Publish changed capacity under the lock before the waiting claim
			// continues. It must discard its pre-lock configuration read.
			if tc.reassigned {
				if _, err := barrier.Exec(ctx, `UPDATE issue SET assignee_id = $1 WHERE id = $2`, targetSquad, waitingIssue); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.queries.WithTx(barrier).UpdateSquad(ctx, db.UpdateSquadParams{
					ID: util.MustParseUUID(f.squadID), MaxConcurrentTasks: pgtype.Int4{Int32: 1, Valid: true},
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := barrier.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-result:
				if got.err != nil {
					t.Fatal(got.err)
				}
				if got.task != nil {
					t.Fatalf("claim used stale capacity after waiting: task=%s", util.UUIDToString(got.task.ID))
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			queued, err := f.queries.GetAgentTask(ctx, util.MustParseUUID(waitingTask))
			if err != nil || queued.Status != "queued" {
				t.Fatalf("waiting task status=%s, error=%v; want queued", queued.Status, err)
			}
			if tc.reassigned {
				if got := claimFor(t, f.svc, f.agentB); got == nil || util.UUIDToString(got.ID) != waitingTask {
					t.Fatal("task did not become claimable under its new squad's lock")
				}
			}
		})
	}
}

var squadConcurrencySeq atomic.Int64

type squadConcurrencyFixture struct {
	*testutil.Fixture
	queries   *db.Queries
	svc       *TaskService
	squadID   string
	runtimeID string
	agentA    string
	agentB    string
}

// newSquadConcurrencyFixture builds a workspace with one squad, two agent
// members on the same runtime, and the requested squad-wide cap.
func newSquadConcurrencyFixture(t *testing.T, squadLimit int) squadConcurrencyFixture {
	t.Helper()
	pool := newResolveOriginatorPool(t)
	queries := db.New(pool)

	n := squadConcurrencySeq.Add(1)
	bootstrap := testutil.New(pool, "", "")
	userID := bootstrap.User(t, "squad-concurrency", fmt.Sprintf("squad-concurrency-%d-%d@multica.test", time.Now().UnixNano(), n))
	workspaceID := bootstrap.Workspace(t, "squad-concurrency", fmt.Sprintf("squad-concurrency-%d-%d", time.Now().UnixNano(), n))
	bootstrap.Member(t, workspaceID, userID, "owner")

	fx := testutil.New(pool, workspaceID, userID)
	runtimeID := fx.Runtime(t, "squad-concurrency-runtime")
	agentA := fx.Agent(t, "squad-agent-a", runtimeID, testutil.Cols{"max_concurrent_tasks": 5})
	agentB := fx.Agent(t, "squad-agent-b", runtimeID, testutil.Cols{"max_concurrent_tasks": 5})
	squadID := fx.Squad(t, "limit-squad", agentA, testutil.Cols{"max_concurrent_tasks": squadLimit})
	fx.SquadMember(t, squadID, "agent", agentA, testutil.Cols{"role": "leader"})
	fx.SquadMember(t, squadID, "agent", agentB, testutil.Cols{"role": "worker"})

	return squadConcurrencyFixture{
		Fixture:   fx,
		queries:   queries,
		svc:       NewTaskService(queries, pool, nil, events.New()),
		squadID:   squadID,
		runtimeID: runtimeID,
		agentA:    agentA,
		agentB:    agentB,
	}
}

// squadIssue creates an issue assigned to the fixture's squad so effective
// squad resolution exercises the issue-assignee fallback (agent_task_queue's
// own squad_id stays NULL for generic worker tasks).
func (f squadConcurrencyFixture) squadIssue(t *testing.T, title string) string {
	t.Helper()
	return f.Issue(t, title, testutil.Cols{
		"assignee_type": "squad",
		"assignee_id":   f.squadID,
	})
}

func claimFor(t *testing.T, svc *TaskService, agentID string) *db.AgentTaskQueue {
	t.Helper()
	task, err := svc.ClaimTask(context.Background(), util.MustParseUUID(agentID))
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	return task
}

func TestClaimTaskSquadActiveProcessLimit(t *testing.T) {
	f := newSquadConcurrencyFixture(t, 1)
	issueID := f.squadIssue(t, "shared issue")
	f.Task(t, f.agentA, testutil.Cols{"issue_id": issueID, "runtime_id": f.runtimeID, "status": "queued"})
	f.Task(t, f.agentB, testutil.Cols{"issue_id": issueID, "runtime_id": f.runtimeID, "status": "queued"})

	if got := claimFor(t, f.svc, f.agentA); got == nil {
		t.Fatal("expected the first squad claim to succeed")
	}
	if got := claimFor(t, f.svc, f.agentB); got != nil {
		t.Fatalf("expected the second squad claim to be blocked by max_concurrent_tasks=1, got task %s", util.UUIDToString(got.ID))
	}
}

func TestClaimTaskSquadCrossIssueInFlightChainLimit(t *testing.T) {
	f := newSquadConcurrencyFixture(t, 1)
	busyIssue := f.squadIssue(t, "busy issue")
	waitingIssue := f.squadIssue(t, "waiting issue")
	activeTask := f.Task(t, f.agentA, testutil.Cols{
		"issue_id":   busyIssue,
		"runtime_id": f.runtimeID,
		"status":     "running",
		"started_at": testutil.Raw("now()"),
	})
	f.Task(t, f.agentA, testutil.Cols{"issue_id": waitingIssue, "runtime_id": f.runtimeID, "status": "queued"})

	if got := claimFor(t, f.svc, f.agentA); got != nil {
		t.Fatalf("expected no claim while another issue is in flight, got task %s", util.UUIDToString(got.ID))
	}

	if _, err := f.Pool.Exec(context.Background(),
		`UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`,
		activeTask,
	); err != nil {
		t.Fatalf("complete active task: %v", err)
	}
	if got := claimFor(t, f.svc, f.agentA); got == nil {
		t.Fatal("expected the waiting issue to become claimable once the in-flight chain finished")
	}
}

func TestClaimTaskSquadConcurrentClaimsRespectLimit(t *testing.T) {
	f := newSquadConcurrencyFixture(t, 1)
	issueA := f.squadIssue(t, "agent a issue")
	issueB := f.squadIssue(t, "agent b issue")
	f.Task(t, f.agentA, testutil.Cols{"issue_id": issueA, "runtime_id": f.runtimeID, "status": "queued"})
	f.Task(t, f.agentB, testutil.Cols{"issue_id": issueB, "runtime_id": f.runtimeID, "status": "queued"})

	start := make(chan struct{})
	claimed := make(chan string, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, agentID := range []string{f.agentA, f.agentB} {
		wg.Add(1)
		go func(agentID string) {
			defer wg.Done()
			<-start
			task, err := f.svc.ClaimTask(context.Background(), util.MustParseUUID(agentID))
			if err != nil {
				errs <- err
				return
			}
			if task != nil {
				claimed <- util.UUIDToString(task.ID)
			}
		}(agentID)
	}
	close(start)
	wg.Wait()
	close(claimed)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("claim task: %v", err)
		}
	}
	if got := len(claimed); got != 1 {
		t.Fatalf("concurrent squad claims = %d, want exactly 1", got)
	}
	active := f.Count(t, `
		SELECT count(*) FROM agent_task_queue
		WHERE agent_id IN ($1, $2)
		  AND status IN ('dispatched', 'running', 'waiting_local_directory')
	`, f.agentA, f.agentB)
	if active != 1 {
		t.Fatalf("active tasks after concurrent claims = %d, want 1", active)
	}
}

func TestClaimTaskSquadLimitAllowsParallelWorkOnSameIssue(t *testing.T) {
	f := newSquadConcurrencyFixture(t, 2)
	issueID := f.squadIssue(t, "parallel issue")
	f.Task(t, f.agentA, testutil.Cols{
		"issue_id":   issueID,
		"runtime_id": f.runtimeID,
		"status":     "running",
		"started_at": testutil.Raw("now()"),
	})
	f.Task(t, f.agentB, testutil.Cols{"issue_id": issueID, "runtime_id": f.runtimeID, "status": "queued"})

	if got := claimFor(t, f.svc, f.agentB); got == nil {
		t.Fatal("expected a second agent on the same issue to claim while the squad is under its cap")
	}
}

func TestListQueuedSquadTaskRuntimesFindsPeerRuntime(t *testing.T) {
	f := newSquadConcurrencyFixture(t, 1)
	peerRuntime := f.Runtime(t, "squad-peer-runtime")
	peerAgent := f.Agent(t, "squad-peer-agent", peerRuntime)
	f.SquadMember(t, f.squadID, "agent", peerAgent, testutil.Cols{"role": "worker"})

	busyIssue := f.squadIssue(t, "busy issue")
	peerIssue := f.squadIssue(t, "peer issue")
	finishedTask := f.Task(t, f.agentA, testutil.Cols{
		"issue_id":   busyIssue,
		"runtime_id": f.runtimeID,
		"status":     "running",
		"started_at": testutil.Raw("now()"),
	})
	f.Task(t, peerAgent, testutil.Cols{"issue_id": peerIssue, "runtime_id": peerRuntime, "status": "queued"})

	runtimes, err := f.queries.ListQueuedSquadTaskRuntimes(context.Background(), db.ListQueuedSquadTaskRuntimesParams{
		TaskID:            util.MustParseUUID(finishedTask),
		FinishedRuntimeID: util.MustParseUUID(f.runtimeID),
	})
	if err != nil {
		t.Fatalf("list queued squad runtimes: %v", err)
	}
	if len(runtimes) != 1 || util.UUIDToString(runtimes[0]) != peerRuntime {
		t.Fatalf("queued squad runtimes = %v, want [%s]", runtimes, peerRuntime)
	}
}

func TestSquadMaxConcurrentTasksCreateAndUpdateTriState(t *testing.T) {
	f := newSquadConcurrencyFixture(t, 3)
	ctx := context.Background()
	workspaceID := f.WorkspaceID

	created, err := f.queries.CreateSquad(ctx, db.CreateSquadParams{
		WorkspaceID:        util.MustParseUUID(workspaceID),
		Name:               "created-with-limit",
		Description:        "",
		LeaderID:           util.MustParseUUID(f.agentA),
		CreatorID:          util.MustParseUUID(f.UserID),
		MaxConcurrentTasks: pgtype.Int4{Int32: 5, Valid: true},
	})
	if err != nil {
		t.Fatalf("create squad with limit: %v", err)
	}
	if !created.MaxConcurrentTasks.Valid || created.MaxConcurrentTasks.Int32 != 5 {
		t.Fatalf("created max_concurrent_tasks = %+v, want 5", created.MaxConcurrentTasks)
	}

	updated, err := f.queries.UpdateSquad(ctx, db.UpdateSquadParams{
		ID:                 created.ID,
		MaxConcurrentTasks: pgtype.Int4{Int32: 9, Valid: true},
	})
	if err != nil {
		t.Fatalf("update squad limit: %v", err)
	}
	if !updated.MaxConcurrentTasks.Valid || updated.MaxConcurrentTasks.Int32 != 9 {
		t.Fatalf("updated max_concurrent_tasks = %+v, want 9", updated.MaxConcurrentTasks)
	}

	cleared, err := f.queries.UpdateSquad(ctx, db.UpdateSquadParams{
		ID:                      created.ID,
		ClearMaxConcurrentTasks: true,
	})
	if err != nil {
		t.Fatalf("clear squad limit: %v", err)
	}
	if cleared.MaxConcurrentTasks.Valid {
		t.Fatalf("cleared max_concurrent_tasks = %+v, want NULL", cleared.MaxConcurrentTasks)
	}
}

func TestClaimTaskSquadQueuedDelegatedHandoffHoldsSlotUntilHumanWait(t *testing.T) {
	f := newSquadConcurrencyFixture(t, 1)
	issue1 := f.squadIssue(t, "issue 1 in handoff")
	issue2 := f.squadIssue(t, "issue 2 waiting")

	// Leader turn on issue 1 completed after delegating to worker (agentB).
	leaderTurn1 := f.Task(t, f.agentA, testutil.Cols{
		"issue_id":       issue1,
		"runtime_id":     f.runtimeID,
		"status":         "completed",
		"is_leader_task": true,
		"squad_id":       f.squadID,
		"completed_at":   testutil.Raw("now()"),
	})
	workerTurn := f.Task(t, f.agentB, testutil.Cols{
		"issue_id":               issue1,
		"runtime_id":             f.runtimeID,
		"status":                 "queued",
		"delegated_from_task_id": leaderTurn1,
	})
	f.Task(t, f.agentA, testutil.Cols{
		"issue_id":       issue2,
		"runtime_id":     f.runtimeID,
		"status":         "queued",
		"is_leader_task": true,
		"squad_id":       f.squadID,
	})

	// Even though 0 tasks are currently running, issue 1's queued delegated handoff
	// holds the squad's single issue slot, so agentA cannot start issue 2 yet.
	if got := claimFor(t, f.svc, f.agentA); got != nil {
		t.Fatalf("expected issue 2 to stay blocked during issue 1's handoff gap, got task %s", util.UUIDToString(got.ID))
	}

	// Worker on issue 1 owns the slot and can claim its delegated task.
	claimedWorker := claimFor(t, f.svc, f.agentB)
	if claimedWorker == nil || util.UUIDToString(claimedWorker.ID) != workerTurn {
		t.Fatalf("expected worker on issue 1 to claim %s, got %v", workerTurn, claimedWorker)
	}

	// Worker finishes and waits for human reply (no new delegated task queued on issue 1).
	if _, err := f.Pool.Exec(context.Background(),
		`UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`,
		workerTurn,
	); err != nil {
		t.Fatalf("complete worker task: %v", err)
	}

	// Slot is immediately released; issue 2 can now be claimed.
	if got := claimFor(t, f.svc, f.agentA); got == nil {
		t.Fatal("expected issue 2 to become claimable once issue 1 paused for human reply")
	}
}

// TestClaimTaskSquadConcurrentClaimsBlockedByQueuedHandoffOnOtherIssue pins the
// Gate 2 re-check under LockSquadClaimAdvisory: with a cap of 2, issue 1 holds a
// chain slot through a queued handoff while 0 tasks run, so two simultaneous
// claims on issues 2 and 3 must not both pass — the loser must observe the
// winner's committed slot rather than a stale snapshot.
func TestClaimTaskSquadConcurrentClaimsBlockedByQueuedHandoffOnOtherIssue(t *testing.T) {
	f := newSquadConcurrencyFixture(t, 2)
	handoffIssue := f.squadIssue(t, "issue 1 in handoff")
	issue2 := f.squadIssue(t, "issue 2 racing")
	issue3 := f.squadIssue(t, "issue 3 racing")

	// Issue 1's leader turn finished after delegating to a worker, so issue 1
	// occupies its slot with no running task during the handoff gap.
	leaderTurn := f.Task(t, f.agentA, testutil.Cols{
		"issue_id":       handoffIssue,
		"runtime_id":     f.runtimeID,
		"status":         "completed",
		"is_leader_task": true,
		"squad_id":       f.squadID,
		"completed_at":   testutil.Raw("now()"),
	})
	// The queued follow-up belongs to a third agent so the two racing claims
	// below can only pick the issue 2 / issue 3 tasks.
	agentC := f.Agent(t, "squad-agent-c", f.runtimeID)
	f.SquadMember(t, f.squadID, "agent", agentC, testutil.Cols{"role": "worker"})
	f.Task(t, agentC, testutil.Cols{
		"issue_id":               handoffIssue,
		"runtime_id":             f.runtimeID,
		"status":                 "queued",
		"delegated_from_task_id": leaderTurn,
	})
	f.Task(t, f.agentA, testutil.Cols{"issue_id": issue2, "runtime_id": f.runtimeID, "status": "queued"})
	f.Task(t, f.agentB, testutil.Cols{"issue_id": issue3, "runtime_id": f.runtimeID, "status": "queued"})

	start := make(chan struct{})
	claimed := make(chan string, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, agentID := range []string{f.agentA, f.agentB} {
		wg.Add(1)
		go func(agentID string) {
			defer wg.Done()
			<-start
			task, err := f.svc.ClaimTask(context.Background(), util.MustParseUUID(agentID))
			if err != nil {
				errs <- err
				return
			}
			if task != nil {
				claimed <- util.UUIDToString(task.ID)
			}
		}(agentID)
	}
	close(start)
	wg.Wait()
	close(claimed)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("claim task: %v", err)
		}
	}
	if got := len(claimed); got != 1 {
		t.Fatalf("concurrent claims while issue 1 holds a queued handoff = %d, want exactly 1", got)
	}

	// Two distinct issues hold a chain slot: issue 1's queued handoff and the
	// single winner. A second winner would make this 3 and exceed the cap.
	inFlight := f.Count(t, `
		SELECT count(DISTINCT issue_id) FROM agent_task_queue
		WHERE agent_id = ANY($1)
		  AND issue_id IS NOT NULL
		  AND (
		    status IN ('dispatched', 'running', 'waiting_local_directory')
		    OR (status = 'queued' AND delegated_from_task_id IS NOT NULL)
		  )
	`, []string{f.agentA, f.agentB, agentC})
	if inFlight != 2 {
		t.Fatalf("in-flight issues after concurrent claims = %d, want 2 (handoff issue + one winner)", inFlight)
	}
}

// TestClaimTaskArchivedSquadDoesNotBlockClaims verifies the cap stops applying
// the moment the squad is archived, even for tasks carrying the squad's stamped
// squad_id: no row cleanup happens on archive, so the claim gate must consult
// archived_at.
func TestClaimTaskArchivedSquadDoesNotBlockClaims(t *testing.T) {
	f := newSquadConcurrencyFixture(t, 1)
	issueID := f.squadIssue(t, "archived squad issue")
	f.Task(t, f.agentA, testutil.Cols{
		"issue_id":       issueID,
		"runtime_id":     f.runtimeID,
		"status":         "running",
		"is_leader_task": true,
		"squad_id":       f.squadID,
		"started_at":     testutil.Raw("now()"),
	})
	f.Task(t, f.agentB, testutil.Cols{
		"issue_id":   issueID,
		"runtime_id": f.runtimeID,
		"status":     "queued",
		"squad_id":   f.squadID,
	})

	// Precondition: the live squad's cap of 1 blocks the second task.
	if got := claimFor(t, f.svc, f.agentB); got != nil {
		t.Fatalf("expected a live squad cap of 1 to block the claim, got task %s", util.UUIDToString(got.ID))
	}

	f.Exec(t, `UPDATE squad SET archived_at = now() WHERE id = $1`, f.squadID)
	if got := claimFor(t, f.svc, f.agentB); got == nil {
		t.Fatal("expected an archived squad to stop capping claims")
	}
}

// TestClaimTaskSquadTwoHopGuestDelegationRespectsCap covers a guest squad on an
// issue assigned to someone else: leader -> worker 1 -> worker 2. Worker 1's task
// carries no stamped squad and the issue's assignee is not the squad, so only a
// two-hop delegation lookup resolves the effective squad and enforces its cap.
func TestClaimTaskSquadTwoHopGuestDelegationRespectsCap(t *testing.T) {
	f := newSquadConcurrencyFixture(t, 1)
	guestIssue := f.Issue(t, "member owned guest issue", testutil.Cols{
		"assignee_type": "member",
		"assignee_id":   f.UserID,
	})
	busyIssue := f.squadIssue(t, "busy squad issue")

	leaderTurn := f.Task(t, f.agentA, testutil.Cols{
		"issue_id":       guestIssue,
		"runtime_id":     f.runtimeID,
		"status":         "completed",
		"is_leader_task": true,
		"squad_id":       f.squadID,
		"completed_at":   testutil.Raw("now()"),
	})
	worker1Turn := f.Task(t, f.agentB, testutil.Cols{
		"issue_id":               guestIssue,
		"runtime_id":             f.runtimeID,
		"status":                 "completed",
		"delegated_from_task_id": leaderTurn,
		"completed_at":           testutil.Raw("now()"),
	})
	worker2Turn := f.Task(t, f.agentA, testutil.Cols{
		"issue_id":               guestIssue,
		"runtime_id":             f.runtimeID,
		"status":                 "queued",
		"delegated_from_task_id": worker1Turn,
	})

	// A second issue already occupies the squad's single execution slot.
	f.Task(t, f.agentB, testutil.Cols{
		"issue_id":   busyIssue,
		"runtime_id": f.runtimeID,
		"status":     "running",
		"squad_id":   f.squadID,
		"started_at": testutil.Raw("now()"),
	})

	// Precondition: worker 1's task genuinely needs the second hop.
	if stamped := f.Count(t, `SELECT count(*) FROM agent_task_queue WHERE id = $1 AND squad_id IS NOT NULL`, worker1Turn); stamped != 0 {
		t.Fatalf("precondition: worker 1 task must carry no stamped squad_id, found %d", stamped)
	}

	capacity, err := f.queries.GetEffectiveSquadConcurrencyForTask(context.Background(), util.MustParseUUID(worker2Turn))
	if err != nil {
		t.Fatalf("resolve effective squad for second-hop task: %v", err)
	}
	if util.UUIDToString(capacity.SquadID) != f.squadID {
		t.Fatalf("effective squad = %s, want %s", util.UUIDToString(capacity.SquadID), f.squadID)
	}
	if !capacity.MaxConcurrentTasks.Valid || capacity.MaxConcurrentTasks.Int32 != 1 {
		t.Fatalf("effective cap = %+v, want 1", capacity.MaxConcurrentTasks)
	}

	if got := claimFor(t, f.svc, f.agentA); got != nil {
		t.Fatalf("expected the guest squad cap to block the second-hop worker, got task %s", util.UUIDToString(got.ID))
	}
}
