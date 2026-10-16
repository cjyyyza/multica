package daemon

import (
	"strings"
	"testing"
)

func deliveryWorkflowAt(step string) *ProjectWorkflowData {
	return &ProjectWorkflowData{
		Name:             "Delivery",
		CurrentStatusKey: step,
		Steps: []ProjectWorkflowStepData{
			{Key: "todo"},
			{Key: "in_progress", Handler: "Forge", Instructions: "Open a PR.", NextStatusKey: "in_review"},
			{Key: "in_review", Handler: "Forge", Instructions: "Review your own PR.", NextStatusKey: "done", BackStatusKey: "in_progress"},
			{Key: "done"},
		},
	}
}

// The workflow and its current step are per-turn context (MUL-7420): appended
// after everything else, so the prompt ahead of it is the same whatever step
// the issue is at (MUL-5377).
func TestBuildPromptAppendsProjectWorkflowBlock(t *testing.T) {
	base := Task{IssueID: "issue-1", TriggerCommentID: "c-1", TriggerCommentContent: "please continue"}
	plain := BuildPrompt(base, "claude")

	for _, step := range []string{"in_progress", "in_review"} {
		task := base
		task.ProjectWorkflow = deliveryWorkflowAt(step)
		out := BuildPrompt(task, "claude")
		if !strings.HasPrefix(out, plain) {
			t.Fatalf("step %s: the workflow block must be appended, not change the prompt ahead of it", step)
		}
		block := out[len(plain):]
		for _, want := range []string{"[Project workflow: Delivery]", "- `" + step + "`", "(current)", "multica issue status issue-1 "} {
			if !strings.Contains(block, want) {
				t.Errorf("step %s: block is missing %q\n---\n%s", step, want, block)
			}
		}
	}
}

func TestBuildPromptSkipsWorkflowBlockForHandoffRun(t *testing.T) {
	task := Task{IssueID: "issue-1", HandoffNote: "This project's workflow — Delivery\nYou are handling the step.", ProjectWorkflow: deliveryWorkflowAt("in_progress")}
	if out := BuildPrompt(task, "claude"); strings.Contains(out, "[Project workflow:") {
		t.Fatalf("a handoff run already has the workflow in its note:\n%s", out)
	}
}

func TestBuildPromptKeepsWorkflowBlockForWakeup(t *testing.T) {
	task := Task{IssueID: "issue-1", WakeupID: "w-1", HandoffNote: "check the PR", ProjectWorkflow: deliveryWorkflowAt("in_review")}
	if out := BuildPrompt(task, "claude"); !strings.Contains(out, "[Project workflow: Delivery]") {
		t.Fatalf("a wakeup's note is not the workflow brief, so the block must render:\n%s", out)
	}
}
