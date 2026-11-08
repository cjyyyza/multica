package execenv

import (
	"strings"
	"testing"
)

func TestP4OnlyBriefOmitsRepoCheckoutAndGitIdentity(t *testing.T) {
	t.Parallel()
	ctx := TaskContextForEnv{IssueID: "i-1", AgentName: "Eve", AgentID: "eve-1", P4Depots: []P4DepotForEnv{{Port: "p4:1666", Depot: "//DJ01_Proj/Dev"}}}
	out := buildMetaSkillContent("claude", ctx)
	for _, banned := range []string{"multica repo checkout", "Git commits use the user's configured identity"} {
		if strings.Contains(out, banned) {
			t.Errorf("P4-only brief carried git guidance %q", banned)
		}
	}
	if !strings.Contains(out, "## Perforce depots") {
		t.Error("P4-only brief lost its depot section")
	}
}

func TestMixedRepoAndP4BriefKeepsGitGuidance(t *testing.T) {
	t.Parallel()
	ctx := TaskContextForEnv{IssueID: "i-1", AgentName: "Eve", AgentID: "eve-1", Repos: []RepoContextForEnv{{URL: "https://github.com/o/r"}}, P4Depots: []P4DepotForEnv{{Port: "p4:1666", Depot: "//DJ01_Proj/Dev"}}}
	out := buildMetaSkillContent("claude", ctx)
	for _, want := range []string{"multica repo checkout", "Git commits use the user's configured identity"} {
		if !strings.Contains(out, want) {
			t.Errorf("mixed brief missing %q", want)
		}
	}
}

func TestP4DepotLineRendersCharset(t *testing.T) {
	t.Parallel()
	ctx := TaskContextForEnv{IssueID: "i-1", AgentName: "Eve", AgentID: "eve-1", P4Depots: []P4DepotForEnv{{Port: "p4:1666", Depot: "//DJ01_Proj/Dev", Stream: "//DJ01_Proj/Dev", Charset: "utf8"}}}
	out := buildMetaSkillContent("claude", ctx)
	if !strings.Contains(out, "charset `utf8`") {
		t.Fatalf("depot line lost charset: %s", out)
	}
}

func workspaceCtx() TaskContextForEnv {
	return TaskContextForEnv{
		IssueID: "i-1", AgentName: "Eve", AgentID: "eve-1",
		P4Depots:    []P4DepotForEnv{{Port: "p4:1666", Depot: "//DJ01_Proj/Dev"}},
		P4Workspace: &P4WorkspaceForEnv{Port: "p4:1666", User: "yujian01", Client: "yujian01_HIH-D-35258Z_Dev", Root: `D:\DJ`, Stream: "//DJ01_Proj/Dev", Ticket: "123456"},
	}
}

func TestP4WorkspaceBriefUsesNativeCommands(t *testing.T) {
	t.Parallel()
	out := buildMetaSkillContent("claude", workspaceCtx())
	for _, want := range []string{"client `yujian01_HIH-D-35258Z_Dev`", "installed native `p4` CLI directly", "does not inject changelists", "does not create, reuse, revert, shelve, submit", "refs #123456", "multica p4 changelist link --changelist <number>", "p4 shelve -c <number>", "never revert the whole shared client"} {
		if !strings.Contains(out, want) {
			t.Errorf("native workspace brief missing %q", want)
		}
	}
	for _, banned := range []string{"ticket_required", "allow_submit", "multica p4 status", "multica p4 <args>", "thin proxy", "do not use raw `p4`", "opens a Helix Swarm review"} {
		if strings.Contains(strings.ToLower(out), strings.ToLower(banned)) {
			t.Errorf("native workspace brief retained old rule %q", banned)
		}
	}
}

func TestP4WorkspaceWithoutTicketStillAllowsNativeP4(t *testing.T) {
	t.Parallel()
	ctx := workspaceCtx()
	ctx.P4Workspace.Ticket = ""
	out := buildMetaSkillContent("claude", ctx)
	if !strings.Contains(out, "Use the installed native `p4` CLI directly") || strings.Contains(out, "cannot open") || strings.Contains(out, "ticket_required") {
		t.Fatalf("missing ticket disabled native P4 in the brief: %s", out)
	}
}

func TestProjectContextP4OnlyProjectSkipsRepoCheckoutSentence(t *testing.T) {
	t.Parallel()
	ctx := TaskContextForEnv{IssueID: "i-1", AgentName: "Eve", AgentID: "eve-1", ProjectID: "p-1", ProjectTitle: "DJ01", ProjectResources: []ProjectResourceForEnv{{ResourceType: "perforce_depot", ResourceRef: []byte(`{"port":"p4:1666","depot":"//DJ01_Proj/Dev"}`)}}}
	out := buildMetaSkillContent("claude", ctx)
	if strings.Contains(out, "For `github_repo` resources") || !strings.Contains(out, "For `perforce_depot` resources") {
		t.Fatalf("wrong project resource guidance: %s", out)
	}
}

func TestP4DepotWithoutWorkspaceRefusesToCreateAClient(t *testing.T) {
	t.Parallel()
	out := buildMetaSkillContent("claude", TaskContextForEnv{IssueID: "i-1", AgentName: "Eve", AgentID: "eve-1", P4Depots: []P4DepotForEnv{{Port: "p4:1666", Depot: "//DJ01_Proj/Dev"}}})
	for _, want := range []string{"## Perforce depots", "does not create a task Perforce client", "in_place"} {
		if !strings.Contains(out, want) {
			t.Errorf("unbound depot brief missing %q", want)
		}
	}
}

func TestProjectContextPointsAtWorkspaceSectionInWorkspaceMode(t *testing.T) {
	t.Parallel()
	ctx := workspaceCtx()
	ctx.ProjectID = "p-1"
	ctx.ProjectTitle = "DJ01"
	ctx.ProjectResources = []ProjectResourceForEnv{{ResourceType: "perforce_depot", ResourceRef: []byte(`{"port":"p4:1666","depot":"//DJ01_Proj/Dev"}`)}}
	out := buildMetaSkillContent("claude", ctx)
	if !strings.Contains(out, "already checked out in the client named under `## Perforce workspace`") {
		t.Fatalf("Project Context did not use the workspace section: %s", out)
	}
}
