package handler

import (
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

func TestDshRequirementOpenAndPlanClaim(t *testing.T) {
	enableRequirementFlowForTest(t)
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	runtimeID := dbfx.Runtime(t, "DSH requirement planner", testutil.Cols{
		"provider": "dsh", "daemon_id": "dsh-plan-" + uuid.NewString(),
		"metadata": testutil.Raw(`'{"capabilities":["plan-mode-v1"]}'::jsonb`),
	})
	agentID := dbfx.Agent(t, "DSH requirement planner", runtimeID)
	detail := createRequirementForTest(t, testUserID, newRequirementSourceFixture("92581").body(agentID), "dsh-plan-create")
	opened := openRequirementForTest(t, testUserID, detail.ID, 1, "dsh-plan-open")
	if opened.PendingTask == nil {
		t.Fatal("DSH requirement did not enqueue its Plan task")
	}
	fixture := planTaskFixture{
		RuntimeID: runtimeID, AgentID: agentID, ChatID: opened.Session.ID,
		RequirementID: detail.ID, TaskID: opened.PendingTask.TaskID,
	}
	claimed, _ := claimPlanResponse(t, planClaimRequest(t, fixture, true))
	if claimed.Mode != taskpolicy.ModePlan || claimed.PlanPermission != taskpolicy.PlanPermissionDsh || claimed.RequirementID != fixture.RequirementID {
		t.Fatalf("DSH claim did not select its native Plan policy: %+v", claimed)
	}
}
