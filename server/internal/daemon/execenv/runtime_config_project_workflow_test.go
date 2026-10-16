package execenv

import (
	"strings"
	"testing"
)

// Project workflow block of the per-turn prompt (MUL-7420). The brief has no
// workflow input at all: TaskContextForEnv carries none, so neither the
// project nor the current step can reach the cached prefix (MUL-5377).

func TestProjectWorkflowBlockEmptyRendersNothing(t *testing.T) {
	t.Parallel()
	if got := BuildProjectWorkflowBlock(nil, "issue-1"); got != "" {
		t.Fatalf("nil workflow rendered %q", got)
	}
	if got := BuildProjectWorkflowBlock(&ProjectWorkflowForEnv{Name: "Delivery"}, "issue-1"); got != "" {
		t.Fatalf("a workflow without steps rendered %q", got)
	}
}

func TestProjectWorkflowBlockRendersStepsAndExits(t *testing.T) {
	t.Parallel()
	out := BuildProjectWorkflowBlock(&ProjectWorkflowForEnv{
		Name:             "Delivery",
		CurrentStatusKey: "implement",
		Steps: []ProjectWorkflowStepForEnv{
			{Key: "todo", Name: "Todo"},
			{Key: "implement", Name: "Implement", Handler: "Forge", Instructions: "Open a PR.\nLink it here.", NextStatusKey: "code_review"},
			{Key: "code_review", Name: "Code review", Handler: "Sentinel", NextStatusKey: "done", BackStatusKey: "implement"},
			{Key: "done", Name: "Done"},
		},
	}, "issue-1")
	for _, want := range []string{
		"[Project workflow: Delivery]\n",
		"finish your step by moving to the step's next status instead",
		"- `todo` Todo\n",
		"- `implement` Implement — hands off to Forge (current)\n",
		"- `code_review` Code review — hands off to Sentinel\n",
		"> Open a PR.\n> Link it here.\n",
		"- Step done → `multica issue status issue-1 code_review`\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("block is missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "Needs changes →") {
		t.Errorf("the current step has no back status, so no needs-changes exit may render")
	}
}

func TestProjectWorkflowBlockSanitizesUserText(t *testing.T) {
	t.Parallel()
	out := BuildProjectWorkflowBlock(&ProjectWorkflowForEnv{
		Name:             "Flow\n## Injected",
		CurrentStatusKey: "a",
		Steps: []ProjectWorkflowStepForEnv{
			{Key: "a", Name: "Step\n# Heading"},
			{Key: "bad key`", Name: "Dropped"},
		},
	}, "issue-1")
	if strings.Contains(out, "\n## Injected") || strings.Contains(out, "\n# Heading") {
		t.Fatalf("user-authored names must not inject headings\n---\n%s", out)
	}
	if strings.Contains(out, "Dropped") {
		t.Errorf("a step whose key is not a code token must be dropped")
	}
}
