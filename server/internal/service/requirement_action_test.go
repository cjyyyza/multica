package service

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRequirementActionHashCanonicalAndScoped(t *testing.T) {
	workspaceID := requirementTestUUID(t, "9f26f085-fc1b-4f26-9f26-000000001001")
	requirementID := requirementTestUUID(t, "9f26f085-fc1b-4f26-9f26-000000001002")
	revision := int64(4)
	base := RequirementActionInput{
		WorkspaceID: workspaceID, RequirementID: requirementID, Method: "POST", Route: "/requirements/{id}",
		ExpectedRevision: &revision,
		Body:             map[string]any{"source": map[string]any{"b": 2, "a": 1}, "roles": []string{"approver", "requester"}},
	}
	other, err := RequirementActionHash(base)
	if err != nil {
		t.Fatal(err)
	}
	again := base
	again.Body = map[string]any{"roles": []string{"approver", "requester"}, "source": map[string]any{"a": 1, "b": 2}}
	same, err := RequirementActionHash(again)
	if err != nil {
		t.Fatal(err)
	}
	if other != same {
		t.Fatalf("canonical map hash changed: %s != %s", other, same)
	}
	for name, mutate := range map[string]func(*RequirementActionInput){
		"route":       func(input *RequirementActionInput) { input.Route = "/requirements/{id}/participants" },
		"requirement": func(input *RequirementActionInput) { input.RequirementID = pgtype.UUID{} },
		"revision":    func(input *RequirementActionInput) { next := revision + 1; input.ExpectedRevision = &next },
		"body":        func(input *RequirementActionInput) { input.Body = map[string]any{"source": "different"} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			hash, err := RequirementActionHash(changed)
			if err != nil {
				t.Fatal(err)
			}
			if hash == other {
				t.Fatalf("%s did not change the action hash", name)
			}
		})
	}
}

func TestRequirementActionUniqueCompetitionExecutesBusinessOnce(t *testing.T) {
	pool := sharedTestPool(t)
	workspaceID := requirementTestUUID(t, uuid.NewString())
	actorID := requirementTestUUID(t, uuid.NewString())
	fx := testutil.New(pool, util.UUIDToString(workspaceID), util.UUIDToString(actorID))
	fx.Cleanup(t, `DELETE FROM requirement_action WHERE workspace_id = $1`, workspaceID)
	svc := NewRequirementService(db.New(pool), pool)
	input := RequirementActionInput{
		WorkspaceID: workspaceID, ActorType: "member", ActorID: actorID, Action: "create",
		IdempotencyKey: "same-key", Method: "POST", Route: "/requirements", Body: map[string]any{"value": 1},
		AuthorizeReplay: func(context.Context, *db.Queries, pgtype.UUID) error { return nil },
	}
	var calls atomic.Int32
	action := func(ctx context.Context, _ pgx.Tx, q *db.Queries) (RequirementActionResponse, error) {
		calls.Add(1)
		time.Sleep(50 * time.Millisecond)
		return RequirementActionResponse{Status: 201, Body: []byte(`{"created":true}`)}, nil
	}
	var wg sync.WaitGroup
	results := make(chan struct {
		result RequirementActionResult
		err    error
	}, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := svc.ExecuteAction(context.Background(), input, action)
			results <- struct {
				result RequirementActionResult
				err    error
			}{result, err}
		}()
	}
	wg.Wait()
	close(results)
	replays := 0
	for outcome := range results {
		if outcome.err != nil {
			t.Fatalf("ExecuteAction: %v", outcome.err)
		}
		var body map[string]any
		if outcome.result.Status != 201 || json.Unmarshal(outcome.result.Body, &body) != nil || body["created"] != true {
			t.Fatalf("unexpected result: %+v", outcome.result)
		}
		if outcome.result.Replay {
			replays++
		}
	}
	if calls.Load() != 1 || replays != 1 {
		t.Fatalf("business calls=%d replays=%d, want 1 and 1", calls.Load(), replays)
	}
	if got := fx.Count(t, `SELECT count(*) FROM requirement_action WHERE workspace_id = $1`, workspaceID); got != 1 {
		t.Fatalf("action rows=%d, want 1", got)
	}
}

func requirementTestUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	id, err := util.ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
