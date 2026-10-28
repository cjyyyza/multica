package execenv

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRequirementExecuteBriefOffersDedicatedBlockCommand(t *testing.T) {
	out := buildMetaSkillContent("claude", TaskContextForEnv{IssueID: "issue-1", RequirementID: "req-1"})
	for _, want := range []string{
		"multica requirement get req-1",
		"participants and current Issue revision",
		"multica requirement block req-1",
		"--question-file ./question.md",
		"do NOT use `issue comment add` plus `issue status blocked`",
		"daemon marks it in_progress when your run starts and in_review when your run completes",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("requirement brief missing %q", want)
		}
	}
	for _, unwanted := range []string{"set `in_progress` FIRST", "You delivered what the issue itself asks for"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("requirement brief retains ordinary status workflow %q", unwanted)
		}
	}
}

func TestOrdinaryIssueBriefOmitsDedicatedRequirementBlockCommand(t *testing.T) {
	out := buildMetaSkillContent("claude", TaskContextForEnv{IssueID: "issue-1"})
	if strings.Contains(out, "multica requirement block") {
		t.Fatal("ordinary issue brief advertises a requirement-only command")
	}
}

// Plan Mode is chat-bound: prepareClaimedTaskMode requires a ChatSessionID and
// forbids an IssueID, so classifyTask reports kindChat for every real plan task.
// Candidate writes are performed by the daemon, never by a model tool.
func TestRequirementPlanBriefUsesCapturedContextAndStructuredOutput(t *testing.T) {
	out := buildMetaSkillContent("claude", TaskContextForEnv{
		RequirementID: "req-1", ChatSessionID: "chat-1", PlanMode: true,
		ProjectID: "project-1", ProjectTitle: "Planning project",
		ProjectResources: []ProjectResourceForEnv{{ResourceType: "github_repo", ResourceRef: json.RawMessage(`{"url":"https://example.test/repo.git"}`)}},
	})
	for _, want := range []string{
		"Return structured output",
		"candidate",
		"captured requirement revision",
		"Planning project",
		"goal, nonGoals, scope, constraints, acceptanceCriteria, projectId, agentId",
		"the server canonicalizes it and computes the candidate digest",
		"attachmentIds",
		"sourceSnapshot.attachments",
		"external ID",
		"local_id",
		"Approval happens only in Multica Chat",
		"current user message",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan requirement brief missing %q", want)
		}
	}
	if strings.Contains(out, "multica requirement block") {
		t.Fatal("plan brief advertises the execute-only block command")
	}
}

func TestDshRequirementPlanBriefUsesNativeSemantics(t *testing.T) {
	out := buildMetaSkillContent("dsh", TaskContextForEnv{RequirementID: "req-1", ChatSessionID: "chat-1", PlanMode: true})
	if !strings.Contains(out, "DSH's native Plan Mode") || !strings.Contains(out, "Approval happens only in Multica Chat") {
		t.Fatal("DSH plan brief is missing native planning or the requirement approval boundary")
	}
	if strings.Contains(out, "Read, Glob and Grep only") {
		t.Fatal("DSH plan brief inherited Claude's tool restrictions")
	}
}

// TestPlanBriefOmitsDeniedWriteCommands locks the contradiction this mode used
// to ship: the brief listed issue and p4 writes the task token refuses, and told
// the agent to upload attachments through a route Plan Mode denies.
func TestPlanBriefOmitsDeniedWriteCommands(t *testing.T) {
	out := buildMetaSkillContent("claude", TaskContextForEnv{
		RequirementID: "req-1", ChatSessionID: "chat-1", PlanMode: true, AgentName: "Eve", AgentID: "eve-1",
	})
	for _, banned := range []string{
		"multica issue create",
		"multica issue get",
		"multica issue children",
		"multica repo checkout",
		"multica issue update",
		"multica issue assign",
		"multica issue status",
		"- `multica issue wakeup <create|list|get|update|disable|trigger|delete|runs|events>`",
		"multica issue comment add",
		"multica p4 edit",
		"multica attachment upload",
		"create issues, update status",
		"multica requirement candidate",
		"multica requirement get",
		"candidate.json",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("plan brief advertises denied command %q\n---\n%s", banned, out)
		}
	}
	for _, want := range []string{
		"ask the user in this conversation and stop",
		"candidate=null",
		"return the candidate in structured output",
		"cannot attach local files",
		"Read, Glob and Grep only",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan brief missing plan workflow copy %q\n---\n%s", want, out)
		}
	}
}

// Execute-mode chat keeps the mutating surface, so the assertions above cannot
// pass by the plan branches never running.
func TestExecuteChatBriefKeepsIssueWrites(t *testing.T) {
	out := buildMetaSkillContent("claude", TaskContextForEnv{ChatSessionID: "chat-1", AgentName: "Eve", AgentID: "eve-1"})
	for _, want := range []string{
		"multica issue create",
		"multica issue status",
		"multica attachment upload <local-path>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("execute chat brief lost %q", want)
		}
	}
}
