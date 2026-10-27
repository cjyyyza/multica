package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

func TestRequirementCLIWithRealPlanTaskTokenThroughAPI(t *testing.T) {
	ctx := context.Background()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	var userID, workspaceID string
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`, "Requirement CLI integration", "requirement-cli-"+suffix+"@example.test").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspace (name, slug, issue_prefix) VALUES ($1, $2, 'RCL') RETURNING id`, "Requirement CLI integration", "requirement-cli-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	fixture := testutil.New(pool, workspaceID, userID)
	fixture.Cleanup(t, `DELETE FROM "user" WHERE id = $1`, userID)
	fixture.Cleanup(t, `DELETE FROM workspace WHERE id = $1`, workspaceID)
	fixture.InsertNoID(t, "member", testutil.Cols{"workspace_id": workspaceID, "user_id": userID, "role": "owner"}, "workspace_id = $1 AND user_id = $2", workspaceID, userID)
	runtimeID := fixture.Runtime(t, "Requirement CLI runtime", testutil.Cols{"provider": "claude"})
	agentID := fixture.Agent(t, "Requirement CLI agent", runtimeID)
	chatID := fixture.ChatSession(t, agentID)
	requirementID := fixture.Insert(t, "requirement", testutil.Cols{
		"workspace_id": workspaceID, "source_host": "yixiezuo", "external_id": "cli-" + suffix,
		"source_url":      "https://dj01.pm.netease.com/issues/cli-" + suffix,
		"source_snapshot": testutil.Raw(`'{}'::jsonb`), "source_digest": "cli-digest-" + suffix,
		"requester_id": userID, "agent_id": agentID, "chat_session_id": chatID, "intake_state": "clarifying",
	})
	for _, table := range []string{"popo_bridge_command", "requirement_outbox", "requirement_action", "requirement_candidate", "requirement_event"} {
		fixture.Cleanup(t, `DELETE FROM `+table+` WHERE requirement_id = $1`, requirementID)
	}
	taskID := fixture.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "status": "running", "mode": "plan", "requirement_id": requirementID,
		"chat_session_id": chatID, "initiator_user_id": userID, "originator_user_id": userID, "accountable_user_id": userID,
	})
	token, err := auth.GenerateAgentTaskToken()
	if err != nil {
		t.Fatal(err)
	}
	fixture.Insert(t, "task_token", testutil.Cols{
		"token_hash": auth.HashToken(token), "task_id": taskID, "agent_id": agentID,
		"workspace_id": workspaceID, "user_id": userID, "expires_at": testutil.Raw(`now() + interval '1 hour'`),
	})

	queries := db.New(pool)
	h := handler.New(queries, pool, nil, events.New(), service.NewEmailService(), nil, nil, analytics.NoopClient{}, handler.Config{})
	h.FeatureFlags, err = featureflag.NewServiceFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FF_REQUIREMENT_FLOW", "on")
	router := chi.NewRouter()
	router.Route("/api/workspaces/{id}/requirements", func(r chi.Router) {
		r.Use(middleware.RequireWorkspaceMemberFromURL(queries, "id"))
		r.Get("/{requirementId}", h.GetRequirementByID)
		r.Post("/{requirementId}/candidates", h.SubmitRequirementCandidate)
	})
	server := httptest.NewServer(middleware.Auth(queries, nil, nil, nil, router)(router))
	defer server.Close()
	t.Chdir(t.TempDir())
	t.Setenv("MULTICA_SERVER_URL", server.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", workspaceID)
	t.Setenv("MULTICA_TOKEN", token)
	t.Setenv("MULTICA_TASK_ID", taskID)
	t.Setenv("MULTICA_AGENT_ID", agentID)

	get := newRequirementCommand()
	var getOutput bytes.Buffer
	get.SetOut(&getOutput)
	get.SetArgs([]string{"get", requirementID})
	if err := get.Execute(); err != nil {
		t.Fatal(err)
	}
	var detail struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}
	if err := json.Unmarshal(getOutput.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ID != requirementID || detail.Revision != 1 {
		t.Fatalf("get detail = %+v", detail)
	}
	snapshot := fmt.Sprintf(`{"goal":"Ship CLI plan flow","nonGoals":["No execution"],"scope":["CLI and API"],"constraints":["Real task token"],"acceptanceCriteria":["Candidate persisted"],"projectId":null,"agentId":%q}`, agentID)
	if err := os.WriteFile("candidate.json", []byte(snapshot), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		candidate := newRequirementCommand()
		candidate.SetArgs([]string{"candidate", requirementID, "--expected-revision", "1", "--snapshot-file", "./candidate.json"})
		if err := candidate.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	if got := fixture.Count(t, `SELECT count(*) FROM requirement_candidate WHERE requirement_id = $1 AND created_by_task_id = $2`, requirementID, taskID); got != 1 {
		t.Fatalf("candidate rows after same-key replay = %d, want 1", got)
	}
	otherID := fixture.Insert(t, "requirement", testutil.Cols{
		"workspace_id": workspaceID, "source_host": "yixiezuo", "external_id": "cli-other-" + suffix,
		"source_url":      "https://dj01.pm.netease.com/issues/cli-other-" + suffix,
		"source_snapshot": testutil.Raw(`'{}'::jsonb`), "source_digest": "cli-other-digest-" + suffix,
		"requester_id": userID, "agent_id": agentID,
	})
	outsideScope := newRequirementCommand()
	outsideScope.SetArgs([]string{"get", otherID})
	if err := outsideScope.Execute(); err == nil {
		t.Fatal("expected the real plan token to be rejected outside its requirement scope")
	}
}
