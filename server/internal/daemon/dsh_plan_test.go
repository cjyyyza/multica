package daemon

import (
	"testing"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

func TestPrepareClaimedDshPlanRequiresItsOwnPermission(t *testing.T) {
	t.Parallel()
	task := Task{Mode: taskpolicy.ModePlan, RequirementID: "req", ChatSessionID: "chat", PlanPermission: taskpolicy.PlanPermissionDsh}
	if _, mode, err := prepareClaimedTaskMode(task, "dsh"); err != nil || mode != agent.PermissionModePlan {
		t.Fatalf("DSH plan prepare = %q, %v", mode, err)
	}
	task.PlanPermission = taskpolicy.PlanPermissionClaude
	if _, _, err := prepareClaimedTaskMode(task, "dsh"); err == nil {
		t.Fatal("DSH accepted Claude's permission policy")
	}
	task.PlanPermission = taskpolicy.PlanPermissionDsh
	if _, _, err := prepareClaimedTaskMode(task, "claude"); err == nil {
		t.Fatal("Claude accepted DSH's permission policy")
	}
}
