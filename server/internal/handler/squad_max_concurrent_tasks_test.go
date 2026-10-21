package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestUpdateSquadMaxConcurrentTasksWaitsForClaimCapacityLock(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	leaderID := createHandlerTestAgent(t, "squad-cap-lock-leader", nil)
	squad := createSquadAs(t, "", "Locked Limit Squad", leaderID)
	decodeSquadResponse(t, updateSquadAs(t, "", squad.ID, map[string]any{"max_concurrent_tasks": 3}))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	holder, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.Background())
	if err := testHandler.Queries.WithTx(holder).LockSquadClaimAdvisory(ctx, parseUUID(squad.ID)); err != nil {
		t.Fatal(err)
	}
	holderPID := holderBackendPID(t, ctx, holder)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		r := squadScopeReq("", http.MethodPut, "/api/squads", map[string]any{"max_concurrent_tasks": 1}, map[string]string{"id": squad.ID})
		requestCtx, cancelRequest := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancelRequest()
		testHandler.UpdateSquad(w, r.WithContext(requestCtx))
		done <- w
	}()
	if !waitForWaiterBlockedBy(t, holderPID, 2*time.Second) {
		t.Fatal("limit update did not wait for the claim capacity lock")
	}
	before, err := testHandler.Queries.GetSquad(ctx, parseUUID(squad.ID))
	if err != nil || before.MaxConcurrentTasks.Int32 != 3 {
		t.Fatalf("limit changed while a claim held capacity: limit=%v, error=%v", before.MaxConcurrentTasks, err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		updated := decodeSquadResponse(t, w)
		if updated.MaxConcurrentTasks == nil || *updated.MaxConcurrentTasks != 1 {
			t.Fatalf("updated limit=%v, want 1", updated.MaxConcurrentTasks)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestUpdateSquadLeaderAndLimitUsesAgentBeforeCapacityLock(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	leaderID := createHandlerTestAgent(t, "squad-lock-order-leader", nil)
	nextLeaderID := createHandlerTestAgent(t, "squad-lock-order-next", nil)
	squad := createSquadAs(t, "", "Leader And Limit Squad", leaderID)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	claim, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer claim.Rollback(context.Background())
	qtx := testHandler.Queries.WithTx(claim)
	if _, err := qtx.GetAgentForClaimUpdate(ctx, parseUUID(nextLeaderID)); err != nil {
		t.Fatal(err)
	}
	holderPID := holderBackendPID(t, ctx, claim)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		r := squadScopeReq("", http.MethodPut, "/api/squads", map[string]any{
			"leader_id": nextLeaderID, "max_concurrent_tasks": 1,
		}, map[string]string{"id": squad.ID})
		requestCtx, cancelRequest := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancelRequest()
		testHandler.UpdateSquad(w, r.WithContext(requestCtx))
		done <- w
	}()
	if !waitForWaiterBlockedBy(t, holderPID, 2*time.Second) {
		t.Fatal("leader update did not wait for the claim's agent lock")
	}
	// A claim already holding the agent must still acquire capacity. Taking
	// capacity before the agent in UpdateSquad would deadlock this interleaving.
	if err := qtx.LockSquadClaimAdvisory(ctx, parseUUID(squad.ID)); err != nil {
		t.Fatalf("claim could not acquire capacity while leader update waited: %v", err)
	}
	if err := claim.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		updated := decodeSquadResponse(t, w)
		if updated.LeaderID != nextLeaderID || updated.MaxConcurrentTasks == nil || *updated.MaxConcurrentTasks != 1 {
			t.Fatalf("combined leader and limit update did not complete: %+v", updated)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func updateSquadAs(t *testing.T, userID, squadID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := squadScopeReq(userID, "PATCH", "/api/squads", body, map[string]string{"id": squadID})
	testHandler.UpdateSquad(w, r)
	return w
}

func decodeSquadResponse(t *testing.T, w *httptest.ResponseRecorder) SquadResponse {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp SquadResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode squad: %v", err)
	}
	return resp
}

func TestCreateSquadMaxConcurrentTasks(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	leaderID := createHandlerTestAgent(t, "squad-max-tasks-leader", nil)

	w := httptest.NewRecorder()
	r := squadScopeReq("", "POST", "/api/squads", map[string]any{
		"name":                 "Concurrency Limited Squad",
		"leader_id":            leaderID,
		"max_concurrent_tasks": 5,
	}, nil)
	testHandler.CreateSquad(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created SquadResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad_member WHERE squad_id = $1`, created.ID)
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, created.ID)
	})
	if created.MaxConcurrentTasks == nil || *created.MaxConcurrentTasks != 5 {
		t.Fatalf("max_concurrent_tasks = %v, want 5", created.MaxConcurrentTasks)
	}

	for _, value := range []int{0, -1, 51} {
		w := httptest.NewRecorder()
		r := squadScopeReq("", "POST", "/api/squads", map[string]any{
			"name":                 "Invalid Limit Squad",
			"leader_id":            leaderID,
			"max_concurrent_tasks": value,
		}, nil)
		testHandler.CreateSquad(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("create with max_concurrent_tasks=%d: expected 400, got %d: %s", value, w.Code, w.Body.String())
		}
	}
}

func TestUpdateSquadMaxConcurrentTasksTriState(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	leaderID := createHandlerTestAgent(t, "squad-max-tasks-update-leader", nil)
	squad := createSquadAs(t, "", "Tri-State Squad", leaderID)

	// Set a limit.
	set := decodeSquadResponse(t, updateSquadAs(t, "", squad.ID, map[string]any{
		"max_concurrent_tasks": 9,
	}))
	if set.MaxConcurrentTasks == nil || *set.MaxConcurrentTasks != 9 {
		t.Fatalf("after set, max_concurrent_tasks = %v, want 9", set.MaxConcurrentTasks)
	}

	// An update that omits the field leaves it unchanged.
	omitted := decodeSquadResponse(t, updateSquadAs(t, "", squad.ID, map[string]any{
		"name": "Tri-State Squad Renamed",
	}))
	if omitted.MaxConcurrentTasks == nil || *omitted.MaxConcurrentTasks != 9 {
		t.Fatalf("after omitted update, max_concurrent_tasks = %v, want 9", omitted.MaxConcurrentTasks)
	}

	// Explicit null clears it to unlimited.
	clearedNull := decodeSquadResponse(t, updateSquadAs(t, "", squad.ID, map[string]any{
		"max_concurrent_tasks": nil,
	}))
	if clearedNull.MaxConcurrentTasks != nil {
		t.Fatalf("after null, max_concurrent_tasks = %v, want nil", *clearedNull.MaxConcurrentTasks)
	}

	// 0 also clears it.
	setAgain := decodeSquadResponse(t, updateSquadAs(t, "", squad.ID, map[string]any{
		"max_concurrent_tasks": 3,
	}))
	if setAgain.MaxConcurrentTasks == nil || *setAgain.MaxConcurrentTasks != 3 {
		t.Fatalf("after second set, max_concurrent_tasks = %v, want 3", setAgain.MaxConcurrentTasks)
	}
	clearedZero := decodeSquadResponse(t, updateSquadAs(t, "", squad.ID, map[string]any{
		"max_concurrent_tasks": 0,
	}))
	if clearedZero.MaxConcurrentTasks != nil {
		t.Fatalf("after 0, max_concurrent_tasks = %v, want nil", *clearedZero.MaxConcurrentTasks)
	}

	// Out-of-range values are rejected and do not mutate the row.
	for _, value := range []int{-1, 51} {
		w := updateSquadAs(t, "", squad.ID, map[string]any{"max_concurrent_tasks": value})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("update with max_concurrent_tasks=%d: expected 400, got %d: %s", value, w.Code, w.Body.String())
		}
	}
}

// squadWakeupRecorder captures the runtimes the task service wakes so a test
// can assert capacity changes reach the runtimes parked behind a squad cap.
type squadWakeupRecorder struct {
	runtimes []string
}

func (s *squadWakeupRecorder) NotifyTaskAvailable(runtimeID, _ string) {
	s.runtimes = append(s.runtimes, runtimeID)
}

func (s *squadWakeupRecorder) saw(runtimeID string) bool {
	for _, got := range s.runtimes {
		if got == runtimeID {
			return true
		}
	}
	return false
}

// TestUpdateSquadMaxConcurrentTasksWakesBlockedRuntimes pins the invalidation
// half of the cap: raising or clearing max_concurrent_tasks frees runtimes
// parked behind it, so NotifySquadCapacityChanged must reach them instead of
// leaving them idle until the empty-claim TTL expires.
func TestUpdateSquadMaxConcurrentTasksWakesBlockedRuntimes(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	wakeup := &squadWakeupRecorder{}
	prevWakeup := testHandler.TaskService.Wakeup
	testHandler.TaskService.Wakeup = wakeup
	t.Cleanup(func() { testHandler.TaskService.Wakeup = prevWakeup })

	// EmptyClaimCache needs Redis. Where the test environment provides one, the
	// stale verdict itself is asserted; otherwise the recorded wakeup still
	// proves the handler collected the blocked runtimes and notified them.
	var cache *service.EmptyClaimCache
	if os.Getenv("REDIS_TEST_URL") != "" {
		cache = service.NewEmptyClaimCache(newRedisTestClient(t))
		prevEmptyClaim := testHandler.TaskService.EmptyClaim
		testHandler.TaskService.EmptyClaim = cache
		t.Cleanup(func() { testHandler.TaskService.EmptyClaim = prevEmptyClaim })
	}

	leaderID := createHandlerTestAgent(t, "squad-capacity-wakeup-leader", nil)
	squad := createSquadAs(t, "", "Capacity Wakeup Squad", leaderID)

	runtimeID := handlerTestRuntimeID(t)
	issueID := dbfx.Issue(t, "capacity wakeup issue", testutil.Cols{
		"assignee_type": "squad",
		"assignee_id":   squad.ID,
	})
	workerID := createHandlerTestAgent(t, "squad-capacity-wakeup-worker", nil)
	dbfx.Task(t, workerID, testutil.Cols{
		"issue_id":   issueID,
		"runtime_id": runtimeID,
		"status":     "queued",
	})

	ctx := context.Background()
	armBlockedRuntime := func(t *testing.T) {
		t.Helper()
		wakeup.runtimes = nil
		if cache == nil {
			return
		}
		cache.MarkEmpty(ctx, runtimeID, cache.CurrentVersion(ctx, runtimeID))
		if !cache.IsEmpty(ctx, runtimeID) {
			t.Fatal("precondition: runtime should hold a cached empty-claim verdict")
		}
	}
	assertWoken := func(t *testing.T, action string) {
		t.Helper()
		if !wakeup.saw(runtimeID) {
			t.Fatalf("%s max_concurrent_tasks must wake the runtime holding queued squad work; woke %v", action, wakeup.runtimes)
		}
		if cache != nil && cache.IsEmpty(ctx, runtimeID) {
			t.Fatalf("%s max_concurrent_tasks must invalidate the blocked runtime's empty-claim verdict", action)
		}
	}

	armBlockedRuntime(t)
	raised := decodeSquadResponse(t, updateSquadAs(t, "", squad.ID, map[string]any{"max_concurrent_tasks": 2}))
	if raised.MaxConcurrentTasks == nil || *raised.MaxConcurrentTasks != 2 {
		t.Fatalf("after raise, max_concurrent_tasks = %v, want 2", raised.MaxConcurrentTasks)
	}
	assertWoken(t, "raising")

	armBlockedRuntime(t)
	cleared := decodeSquadResponse(t, updateSquadAs(t, "", squad.ID, map[string]any{"max_concurrent_tasks": 0}))
	if cleared.MaxConcurrentTasks != nil {
		t.Fatalf("after clear, max_concurrent_tasks = %v, want nil", *cleared.MaxConcurrentTasks)
	}
	assertWoken(t, "clearing")
}

// TestDeleteSquadWakesRuntimesBlockedByCap pins the archive path: the runtimes
// parked behind a squad's cap must be collected before TransferSquadAssignees
// rewrites the issue assignee, otherwise the post-archive lookup resolves no
// squad for the queued task and nobody is woken.
func TestDeleteSquadWakesRuntimesBlockedByCap(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	wakeup := &squadWakeupRecorder{}
	prevWakeup := testHandler.TaskService.Wakeup
	testHandler.TaskService.Wakeup = wakeup
	t.Cleanup(func() { testHandler.TaskService.Wakeup = prevWakeup })

	leaderID := createHandlerTestAgent(t, "squad-archive-wakeup-leader", nil)
	w := httptest.NewRecorder()
	r := squadScopeReq("", "POST", "/api/squads", map[string]any{
		"name":                 "Archive Wakeup Squad",
		"leader_id":            leaderID,
		"max_concurrent_tasks": 1,
	}, nil)
	testHandler.CreateSquad(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var squad SquadResponse
	if err := json.NewDecoder(w.Body).Decode(&squad); err != nil {
		t.Fatalf("decode squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad_member WHERE squad_id = $1`, squad.ID)
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squad.ID)
	})

	runtimeID := handlerTestRuntimeID(t)
	issueID := dbfx.Issue(t, "archive wakeup issue", testutil.Cols{
		"assignee_type": "squad",
		"assignee_id":   squad.ID,
	})
	workerID := createHandlerTestAgent(t, "squad-archive-wakeup-worker", nil)
	dbfx.Task(t, workerID, testutil.Cols{
		"issue_id":   issueID,
		"runtime_id": runtimeID,
		"status":     "queued",
	})

	w = httptest.NewRecorder()
	testHandler.DeleteSquad(w, squadScopeReq("", "DELETE", "/api/squads", nil, map[string]string{"id": squad.ID}))
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
	if !wakeup.saw(runtimeID) {
		t.Fatalf("archiving the squad must wake the runtime parked behind its cap; woke %v", wakeup.runtimes)
	}

	// The transfer that made the pre-archive capture necessary: the issue now
	// points at the leader agent, so the queued task no longer resolves a squad.
	var assigneeID string
	if err := testPool.QueryRow(context.Background(), `SELECT assignee_id::text FROM issue WHERE id = $1`, issueID).Scan(&assigneeID); err != nil {
		t.Fatalf("read transferred assignee: %v", err)
	}
	if assigneeID != leaderID {
		t.Fatalf("issue assignee = %s, want leader %s", assigneeID, leaderID)
	}
}

// squadReassignWakeup describes a capped-squad setup whose single slot is held
// by one running task while another queued task waits behind it. Both tasks
// resolve the squad ONLY through issue.assignee_id.
type squadReassignWakeup struct {
	holderIssue    string
	queuedIssue    string
	blockedRuntime string
	blockedAgent   string
	blockedTask    string
}

// seedSquadReassignWakeup builds that setup. Moving the queued issue exercises
// the moved issue's own runtime; moving the holder issue exercises a peer
// runtime that stays in the old squad.
func seedSquadReassignWakeup(t *testing.T, prefix string) squadReassignWakeup {
	t.Helper()
	leaderID := createHandlerTestAgent(t, prefix+"-leader", nil)
	squad := decodeSquadResponse(t, updateSquadAs(t, "",
		createSquadAs(t, "", prefix+" squad", leaderID).ID,
		map[string]any{"max_concurrent_tasks": 1}))

	holderIssue := dbfx.Issue(t, prefix+" holder issue", testutil.Cols{
		"assignee_type": "squad",
		"assignee_id":   squad.ID,
	})
	holderAgent := createHandlerTestAgent(t, prefix+"-holder", nil)
	holderTask := dbfx.Task(t, holderAgent, testutil.Cols{
		"issue_id":   holderIssue,
		"runtime_id": handlerTestRuntimeID(t),
		"status":     "running",
		"started_at": testutil.Raw("now()"),
	})

	queuedIssue := dbfx.Issue(t, prefix+" queued issue", testutil.Cols{
		"assignee_type": "squad",
		"assignee_id":   squad.ID,
	})
	blockedRuntime := dbfx.Runtime(t, prefix+"-runtime")
	blockedAgent := dbfx.Agent(t, prefix+"-agent", blockedRuntime)
	blockedTask := dbfx.Task(t, blockedAgent, testutil.Cols{
		"issue_id":   queuedIssue,
		"runtime_id": blockedRuntime,
		"status":     "queued",
	})

	for _, taskID := range []string{holderTask, blockedTask} {
		if dbfx.Count(t, `
			SELECT count(*) FROM agent_task_queue
			WHERE id = $1 AND squad_id IS NULL AND delegated_from_task_id IS NULL
		`, taskID) != 1 {
			t.Fatalf("precondition: task %s must resolve its squad only through the issue assignee", taskID)
		}
	}
	if claimed, err := testHandler.TaskService.ClaimTask(context.Background(), parseUUID(blockedAgent)); err != nil || claimed != nil {
		t.Fatalf("precondition: queued task must be blocked by the squad cap; claimed=%v err=%v", claimed, err)
	}
	return squadReassignWakeup{
		holderIssue:    holderIssue,
		queuedIssue:    queuedIssue,
		blockedRuntime: blockedRuntime,
		blockedAgent:   blockedAgent,
		blockedTask:    blockedTask,
	}
}

func installSquadWakeupRecorder(t *testing.T) *squadWakeupRecorder {
	t.Helper()
	wakeup := &squadWakeupRecorder{}
	prevWakeup := testHandler.TaskService.Wakeup
	testHandler.TaskService.Wakeup = wakeup
	t.Cleanup(func() { testHandler.TaskService.Wakeup = prevWakeup })
	return wakeup
}

// reassignIssueToMember pushes one issue off the squad through the single or
// batch update handler, whichever the subtest exercises.
func reassignIssueToMember(t *testing.T, issueID string, batch bool) {
	t.Helper()
	w := httptest.NewRecorder()
	if batch {
		req := newRequest(http.MethodPatch, "/api/issues/batch?workspace_id="+testWorkspaceID, map[string]any{
			"issue_ids": []string{issueID},
			"updates": map[string]any{
				"assignee_type": "member",
				"assignee_id":   testUserID,
			},
		})
		testHandler.BatchUpdateIssues(w, req)
	} else {
		req := withURLParam(newRequest(http.MethodPut, "/api/issues/"+issueID, map[string]any{
			"assignee_type": "member",
			"assignee_id":   testUserID,
		}), "id", issueID)
		testHandler.UpdateIssue(w, req)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("reassign issue: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func assertBlockedRuntimeWoken(t *testing.T, wakeup *squadWakeupRecorder, fx squadReassignWakeup) {
	t.Helper()
	if !wakeup.saw(fx.blockedRuntime) {
		t.Fatalf("reassigning an issue off a squad must wake the runtime holding queued squad work; woke %v", wakeup.runtimes)
	}
	claimed, err := testHandler.TaskService.ClaimTask(context.Background(), parseUUID(fx.blockedAgent))
	if err != nil {
		t.Fatalf("claim queued task: %v", err)
	}
	if claimed == nil || uuidToString(claimed.ID) != fx.blockedTask {
		t.Fatalf("claim after reassignment = %v, want %s", claimed, fx.blockedTask)
	}
}

// TestUpdateIssueAwayFromSquadWakesPeerRuntimes covers both wakeup paths for
// reassignment, using queued tasks that resolve a squad ONLY through
// issue.assignee_id (no stamped squad_id, no delegation). The issue row is
// already updated when the wakeup runs, so those tasks vanish from a squad-wide
// lookup; the moved issue's own queued task must still be found and woken.
func TestUpdateIssueAwayFromSquadWakesPeerRuntimes(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	t.Run("moved issue's own runtime", func(t *testing.T) {
		wakeup := installSquadWakeupRecorder(t)
		fx := seedSquadReassignWakeup(t, "squad-reassign-own")
		// Moving the queued issue leaves the old squad full (the holder issue
		// still runs), so only the issue-targeted lookup can find its task.
		reassignIssueToMember(t, fx.queuedIssue, false)
		assertBlockedRuntimeWoken(t, wakeup, fx)
	})

	t.Run("peer runtime remaining in old squad", func(t *testing.T) {
		wakeup := installSquadWakeupRecorder(t)
		fx := seedSquadReassignWakeup(t, "squad-reassign-peer")
		reassignIssueToMember(t, fx.holderIssue, false)
		assertBlockedRuntimeWoken(t, wakeup, fx)
	})

	t.Run("batch moved issue's own runtime", func(t *testing.T) {
		wakeup := installSquadWakeupRecorder(t)
		fx := seedSquadReassignWakeup(t, "squad-reassign-batch")
		reassignIssueToMember(t, fx.queuedIssue, true)
		assertBlockedRuntimeWoken(t, wakeup, fx)
	})
}
