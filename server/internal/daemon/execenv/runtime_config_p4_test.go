package execenv

import (
	"strings"
	"testing"
)

func TestP4ReadOnlyWorkspaceBrief(t *testing.T) {
	t.Parallel()
	out := buildMetaSkillContent("codex", TaskContextForEnv{
		P4Workspace: &P4WorkspaceForEnv{ReadOnly: true, Ticket: "123", Client: "alice_DEV"},
	})
	if !strings.Contains(out, "read-only Perforce access") || strings.Contains(out, "Open a file with") {
		t.Fatalf("coordination task received editing instructions:\n%s", out)
	}
}

// A task configured with Perforce and no repository has nothing to check out
// and no commit to author, yet the brief shipped it `multica repo checkout`
// and a git-identity paragraph. Both read as instructions, so agents went
// looking for a repository the claim never carried (plan §2.5).
func TestP4OnlyBriefOmitsRepoCheckoutAndGitIdentity(t *testing.T) {
	t.Parallel()
	ctx := TaskContextForEnv{
		IssueID: "i-1", AgentName: "Eve", AgentID: "eve-1",
		P4Depots: []P4DepotForEnv{{Port: "p4:1666", Depot: "//DJ01_Proj/Dev"}},
	}
	out := buildMetaSkillContent("claude", ctx)

	for _, banned := range []string{
		"multica repo checkout",
		"Git commits use the user's configured identity",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("P4-only brief should not carry git guidance %q; got:\n%s", banned, out)
		}
	}
	// The Perforce half is unaffected.
	if !strings.Contains(out, "## Perforce depots") {
		t.Errorf("P4-only brief lost its Perforce depots section; got:\n%s", out)
	}
}

// The gate is "no repository", not "any Perforce". A project carrying both
// still checks out the repo, so both git lines stay.
func TestMixedRepoAndP4BriefKeepsGitGuidance(t *testing.T) {
	t.Parallel()
	ctx := TaskContextForEnv{
		IssueID: "i-1", AgentName: "Eve", AgentID: "eve-1",
		Repos:    []RepoContextForEnv{{URL: "https://github.com/o/r"}},
		P4Depots: []P4DepotForEnv{{Port: "p4:1666", Depot: "//DJ01_Proj/Dev"}},
	}
	out := buildMetaSkillContent("claude", ctx)

	for _, want := range []string{
		"multica repo checkout",
		"Git commits use the user's configured identity",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("brief with a repo attached should keep %q; got:\n%s", want, out)
		}
	}
}

// A unicode Helix server rejects commands whose charset does not match, and
// the charset is not something the agent can read off the claim itself.
func TestP4DepotLineRendersCharset(t *testing.T) {
	t.Parallel()
	ctx := TaskContextForEnv{
		IssueID: "i-1", AgentName: "Eve", AgentID: "eve-1",
		P4Depots: []P4DepotForEnv{{
			Port: "p4:1666", Depot: "//DJ01_Proj/Dev",
			Stream: "//DJ01_Proj/Dev", Charset: "utf8",
		}},
	}
	out := buildMetaSkillContent("claude", ctx)

	if !strings.Contains(out, "charset `utf8`") {
		t.Errorf("depot line should name the charset when the depot sets one; got:\n%s", out)
	}
}

func TestP4DepotLineOmitsCharsetWhenUnset(t *testing.T) {
	t.Parallel()
	ctx := TaskContextForEnv{
		IssueID: "i-1", AgentName: "Eve", AgentID: "eve-1",
		P4Depots: []P4DepotForEnv{{Port: "p4:1666", Depot: "//DJ01_Proj/Dev"}},
	}
	out := buildMetaSkillContent("claude", ctx)

	if strings.Contains(out, "charset") {
		t.Errorf("no charset configured, so the depot line must not mention one; got:\n%s", out)
	}
}

// workspaceCtx is the standard workspace-mode fixture: the task is bound to a
// human-owned client and has the 易协作 ticket its changelist description
// needs.
func workspaceCtx() TaskContextForEnv {
	return TaskContextForEnv{
		IssueID: "i-1", AgentName: "Eve", AgentID: "eve-1",
		P4Depots: []P4DepotForEnv{{Port: "p4:1666", Depot: "//DJ01_Proj/Dev"}},
		P4Workspace: &P4WorkspaceForEnv{
			Port: "p4:1666", User: "yujian01",
			Client: "yujian01_HIH-D-35258Z_Dev", Root: `D:\DJ`,
			Stream: "//DJ01_Proj/Dev", Ticket: "123456",
		},
	}
}

// Workspace mode is the one mode where the agent shares a mutable working
// area with the person who asked for the work. The section has to name the
// client it is in, and state the rules that keep that person's own changelists
// and read-only files intact (plan §1.8).
func TestP4WorkspaceSectionNamesTheClientAndItsRules(t *testing.T) {
	t.Parallel()
	out := buildMetaSkillContent("claude", workspaceCtx())

	for _, want := range []string{
		"## Perforce workspace",
		// Which workspace, so the agent can tell it apart from a task client.
		"`yujian01`",
		"client `yujian01_HIH-D-35258Z_Dev`",
		"root `D:\\DJ`",
		"stream `//DJ01_Proj/Dev`",
		// The ticket trailer the changelist description must keep.
		"refs #123456",
		// How to write, and why the read-only bit is not the way.
		"multica p4 edit -- <files>",
		"Do not pass `--port`",
		"multica p4 add-files -- <files>",
		"never clear the read-only bit",
		// The task changelist is implicit, and discoverable.
		"multica p4 status",
		"multica p4 opened -F \"%depotFile% %change%\"",
		"do not run `p4 opened -a`",
		// The three hard prohibitions.
		"Never sync, never submit, and never touch a changelist other than this task's",
		"Do not use raw `p4` to write",
		// Read-only p4 stays available: it is how history is read.
		"`p4 print`",
		// Stop-and-explain, not work-around.
		"`opened_by_you`",
		"`exclusive_locked`",
		"`exclusive_not_allowed`",
		"`not_head`",
		"stop on that file and explain it in the issue",
		// What happens after the turn, and what the reply owes the human.
		"shelves the rest while retaining existing shelf-only files",
		"the changelist number (`multica p4 status`), and how to verify it in Unreal Editor",
		// Project rules live in the depot.
		"Read `Game/DJ01/AGENTS.md` first",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("workspace-mode section missing %q; got:\n%s", want, out)
		}
	}

	// The unbound depot list is the section that names `p4 sync` as refused.
	// Workspace mode does not carry that section.
	for _, banned := range []string{
		"## Perforce depots",
		"multica p4 sync",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("workspace-mode brief should not carry task-client copy %q; got:\n%s", banned, out)
		}
	}
}

// Exclusive-lock files are opt-in per depot (D5). Mentioning the allowance on
// a depot that does not grant it would invite the agent to try, and the daemon
// would reject every attempt.
func TestP4WorkspaceMentionsExclusiveOnlyWhenAllowed(t *testing.T) {
	t.Parallel()

	off := buildMetaSkillContent("claude", workspaceCtx())
	if strings.Contains(off, "exclusive-lock (`+l`) files") {
		t.Errorf("depot does not allow +l files, so the brief must not offer them; got:\n%s", off)
	}

	ctx := workspaceCtx()
	ctx.P4Workspace.AllowExclusive = true
	on := buildMetaSkillContent("claude", ctx)
	if !strings.Contains(on, "allows opening exclusive-lock (`+l`) files") {
		t.Errorf("depot allows +l files, so the brief should say so; got:\n%s", on)
	}
}

// I7: with no ticket there is no legal changelist description, so no file can
// be opened. The brief says that and routes the agent to ask, instead of
// listing commands that would all be rejected — or worse, inviting it to
// invent a number (D1).
func TestP4WorkspaceWithoutTicketRefusesToOpenFiles(t *testing.T) {
	t.Parallel()
	ctx := workspaceCtx()
	ctx.P4Workspace.Ticket = ""
	out := buildMetaSkillContent("claude", ctx)

	for _, want := range []string{
		"## Perforce workspace",
		"client `yujian01_HIH-D-35258Z_Dev`",
		"you cannot open any file",
		"`refs #<ticket>`",
		"post a comment asking for `refs #<ticket>` to be added to the issue description",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ticket-less workspace brief missing %q; got:\n%s", want, out)
		}
	}
	// Available Commands still names the invocation and points here for
	// permission. This section is what must not teach it: an agent that has
	// read this far should ask for a ticket, not open a file.
	section := out[strings.Index(out, "## Perforce workspace"):]
	if next := strings.Index(section[len("## Perforce workspace"):], "\n## "); next >= 0 {
		section = section[:len("## Perforce workspace")+next]
	}
	for _, banned := range []string{
		"multica p4 edit -- <files>",
		"multica p4 add-files -- <files>",
	} {
		if strings.Contains(section, banned) {
			t.Errorf("ticket-less workspace section should not teach %q; got:\n%s", banned, section)
		}
	}
}

// Unbound tasks and workspaces without submit permission must not be offered
// the command. Enabled workspaces are covered by the action-policy tests.
func TestP4BriefWithoutPermissionNeverOffersSubmitAsACommand(t *testing.T) {
	t.Parallel()
	for name, ctx := range map[string]TaskContextForEnv{
		"depot mode": {
			IssueID: "i-1", AgentName: "Eve", AgentID: "eve-1",
			P4Depots: []P4DepotForEnv{{Port: "p4:1666", Depot: "//DJ01_Proj/Dev"}},
		},
		"workspace mode": workspaceCtx(),
	} {
		name, ctx := name, ctx
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out := buildMetaSkillContent("claude", ctx)
			for _, banned := range []string{
				"multica p4 submit",
				"p4 submit",
				"`submit`",
			} {
				if strings.Contains(out, banned) {
					t.Errorf("brief still offers the submit command %q; got:\n%s", banned, out)
				}
			}
			if !strings.Contains(out, "submit") {
				t.Errorf("brief should still forbid submitting in words; got:\n%s", out)
			}
		})
	}
}

// The per-type sentences in Project Context are each stated only when that
// resource type is actually attached — same wrong-steer as the git lines in
// `## Available Commands`.
func TestProjectContextP4OnlyProjectSkipsRepoCheckoutSentence(t *testing.T) {
	t.Parallel()
	ctx := TaskContextForEnv{
		IssueID: "i-1", AgentName: "Eve", AgentID: "eve-1",
		ProjectID: "p-1", ProjectTitle: "DJ01",
		ProjectResources: []ProjectResourceForEnv{{
			ResourceType: "perforce_depot",
			ResourceRef:  []byte(`{"port":"p4:1666","depot":"//DJ01_Proj/Dev"}`),
		}},
	}
	out := buildMetaSkillContent("claude", ctx)

	if strings.Contains(out, "For `github_repo` resources") {
		t.Errorf("no github_repo resource is attached, so the sentence is a wrong steer; got:\n%s", out)
	}
	if !strings.Contains(out, "For `perforce_depot` resources") {
		t.Errorf("the attached perforce_depot resource should still be explained; got:\n%s", out)
	}
	if strings.Contains(out, "multica p4 sync") {
		t.Errorf("a perforce_depot resource must not tell the agent to sync a new client; got:\n%s", out)
	}
}

func TestP4DepotWithoutWorkspaceRefusesToCreateAClient(t *testing.T) {
	t.Parallel()
	out := buildMetaSkillContent("claude", TaskContextForEnv{
		IssueID: "i-1", AgentName: "Eve", AgentID: "eve-1",
		P4Depots: []P4DepotForEnv{{Port: "p4:1666", Depot: "//DJ01_Proj/Dev"}},
	})
	for _, want := range []string{
		"## Perforce depots",
		"does not create a task Perforce client",
		"Do not run `multica p4 sync`",
		"in_place",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("unbound depot brief missing %q; got:\n%s", want, out)
		}
	}
}

// In workspace mode the `p4 sync` sentence is wrong: the daemon refuses to
// sync the human's client, so Project Context points at the workspace section
// that states what to do instead.
func TestProjectContextPointsAtWorkspaceSectionInWorkspaceMode(t *testing.T) {
	t.Parallel()
	ctx := workspaceCtx()
	ctx.ProjectID = "p-1"
	ctx.ProjectTitle = "DJ01"
	ctx.ProjectResources = []ProjectResourceForEnv{{
		ResourceType: "perforce_depot",
		ResourceRef:  []byte(`{"port":"p4:1666","depot":"//DJ01_Proj/Dev"}`),
	}}
	out := buildMetaSkillContent("claude", ctx)

	if !strings.Contains(out, "already checked out in the client named under `## Perforce workspace`") {
		t.Errorf("Project Context should route workspace-mode P4 work to the workspace section; got:\n%s", out)
	}
	if strings.Contains(out, "For `perforce_depot` resources, use `multica p4 sync") {
		t.Errorf("workspace mode cannot sync, so the sync sentence must not render; got:\n%s", out)
	}
}

func TestP4WorkspaceBriefNamesActionsTheDaemonEnabled(t *testing.T) {
	t.Parallel()
	ctx := workspaceCtx()
	ctx.P4Workspace.AllowSubmit = true
	ctx.P4Workspace.AllowSync = true
	ctx.P4Workspace.AllowUnshelve = true
	out := buildMetaSkillContent("claude", ctx)
	for _, want := range []string{
		"`multica p4 submit`",
		"`multica p4 sync`",
		"`multica p4 unshelve`",
		"Never touch a changelist other than this task's",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("enabled-action brief missing %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Never sync, never submit, and never touch") {
		t.Errorf("the all-off prohibition should not render when the daemon enabled the actions; got:\n%s", out)
	}
	for _, forbidden := range []string{"Agents do not submit changelists", "or sync a depot", "Do not run `multica p4 sync`"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("enabled-action brief contains contradictory rule %q", forbidden)
		}
	}
}
