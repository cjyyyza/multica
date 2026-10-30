package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestP4ContextPreservesRepositoryRulesAndSkills(t *testing.T) {
	for _, provider := range []string{"claude", "codex", "copilot", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			work := t.TempDir()
			rules := RuntimeConfigPath(work, provider)
			if err := os.WriteFile(rules, []byte("Repository rules\n"), 0o444); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(rules, 0o644) })
			env, err := Prepare(PrepareParams{WorkspacesRoot: t.TempDir(), WorkspaceID: "ws", TaskID: "task", Provider: provider, LocalWorkDir: work,
				Task: TaskContextForEnv{AgentID: "agent", P4Workspace: &P4WorkspaceForEnv{Client: "client"}, AgentSkills: []SkillContextForEnv{{Name: "managed", Content: "Read the task"}}},
			}, testLogger())
			if err != nil {
				t.Fatal(err)
			}
			defer env.Cleanup(true)
			brief, err := InjectRuntimeConfig(work, provider, TaskContextForEnv{
				PrivateRuntimeDir: env.PrivateRuntimeDir,
				AgentSkills:       []SkillContextForEnv{{Name: "managed", Content: "Read the task"}},
				P4Workspace:       &P4WorkspaceForEnv{Client: "client"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if cataloguesPrivateSkills(provider, TaskContextForEnv{}) {
				if !strings.Contains(brief, "discovered automatically") || strings.Contains(brief, "reading their SKILL.md") {
					t.Fatalf("brief did not treat private skills as catalog skills: %s", brief)
				}
			} else if !strings.Contains(brief, filepath.Join(env.PrivateRuntimeDir, "skills", "managed", "SKILL.md")) {
				t.Fatalf("brief did not expose the private skill path: %s", brief)
			}
			if provider == "claude" {
				claudeSkill := filepath.Join(env.PrivateRuntimeDir, ".claude", "skills", "managed", "SKILL.md")
				if raw, err := os.ReadFile(claudeSkill); err != nil || !strings.Contains(string(raw), "Read the task") {
					t.Fatalf("claude private skill missing: %q %v", raw, err)
				}
				addDir, piDirs, err := ConfigurePrivateSkillLaunch(provider, env.PrivateRuntimeDir, 1)
				if err != nil || addDir != env.PrivateRuntimeDir || len(piDirs) != 0 {
					t.Fatalf("claude launch = %q %#v %v", addDir, piDirs, err)
				}
			}
			if raw, err := os.ReadFile(filepath.Join(env.PrivateRuntimeDir, "runtime.md")); err != nil || string(raw) != brief {
				t.Fatalf("private runtime instructions missing: %v", err)
			}
			if raw, err := os.ReadFile(rules); err != nil || string(raw) != "Repository rules\n" {
				t.Fatalf("repository rules changed: %q %v", raw, err)
			}
			if dir := SkillsDirPath(work, provider); dir != "" {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("managed skills wrote to the repository: %s (%v)", dir, err)
				}
			}
			file := filepath.Join(env.PrivateRuntimeDir, "skills", "managed", "SKILL.md")
			if raw, err := os.ReadFile(file); err != nil || !strings.Contains(string(raw), "Read the task") {
				t.Fatalf("private skill missing: %q %v", raw, err)
			}
			if err := CleanupSidecars(env.RootDir); err != nil {
				t.Fatal(err)
			}
			if raw, err := os.ReadFile(rules); err != nil || string(raw) != "Repository rules\n" {
				t.Fatalf("cleanup changed repository rules: %q %v", raw, err)
			}
		})
	}
}

func TestP4PiSkillsLaunchOutsideTheClient(t *testing.T) {
	work := t.TempDir()
	env, err := Prepare(PrepareParams{WorkspacesRoot: t.TempDir(), WorkspaceID: "ws", TaskID: "task", Provider: "pi", LocalWorkDir: work,
		Task: TaskContextForEnv{AgentID: "agent", P4Workspace: &P4WorkspaceForEnv{Client: "client"}, AgentSkills: []SkillContextForEnv{{Name: "multica-platform", Content: "Read the task"}}},
	}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup(true)
	if _, err := os.Stat(filepath.Join(work, ".pi", "skills")); !os.IsNotExist(err) {
		t.Fatalf("managed skills wrote into the Helix client: %v", err)
	}
	addDir, piDirs, err := ConfigurePrivateSkillLaunch("omp", env.PrivateRuntimeDir, 1)
	if err != nil || addDir != "" || len(piDirs) != 1 || !strings.HasSuffix(piDirs[0], filepath.Join("skills", "multica-platform")) {
		t.Fatalf("omp launch = %q %#v %v", addDir, piDirs, err)
	}
	brief, err := InjectRuntimeConfig(work, "pi", TaskContextForEnv{
		PrivateRuntimeDir: env.PrivateRuntimeDir,
		AgentSkills:       []SkillContextForEnv{{Name: "multica-platform", Content: "Read the task"}},
		P4Workspace:       &P4WorkspaceForEnv{Client: "client"},
	})
	if err != nil || !strings.Contains(brief, "discovered automatically") || strings.Contains(brief, "reading their SKILL.md") {
		t.Fatalf("pi brief = %s %v", brief, err)
	}
}

func TestP4DshSkillsRegisterOutsideTheClient(t *testing.T) {
	work := t.TempDir()
	env, err := Prepare(PrepareParams{WorkspacesRoot: t.TempDir(), WorkspaceID: "ws", TaskID: "task", Provider: "dsh", LocalWorkDir: work,
		Task: TaskContextForEnv{AgentID: "agent", P4Workspace: &P4WorkspaceForEnv{Client: "client"}, AgentSkills: []SkillContextForEnv{{Name: "multica-platform", Description: "Platform contracts", Content: "Read the task"}}},
	}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup(true)
	if _, err := os.Stat(filepath.Join(work, ".dsh", "skills")); !os.IsNotExist(err) {
		t.Fatalf("managed skills wrote into the Helix client: %v", err)
	}
	skillFile := filepath.Join(env.PrivateRuntimeDir, "skills", "multica-platform", "SKILL.md")
	if raw, err := os.ReadFile(skillFile); err != nil || !strings.Contains(string(raw), "Read the task") {
		t.Fatalf("private skill missing: %q %v", raw, err)
	}
	patch := DshSkillPatchPath(env.PrivateRuntimeDir)
	raw, err := os.ReadFile(patch)
	if err != nil {
		t.Fatal(err)
	}
	var entries []dshSkillPatchEntry
	if err := yaml.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.ToSlash(filepath.Join(env.PrivateRuntimeDir, "skills"))
	if len(entries) != 1 || entries[0].ID != "skill-filesystem" || !entries[0].Config.IncludeDefaultRoots || len(entries[0].Config.CustomSkillDirs) != 1 || entries[0].Config.CustomSkillDirs[0] != wantDir {
		t.Fatalf("patch = %s", raw)
	}
	got, err := DshConfigPatchForTask("dsh", env.PrivateRuntimeDir, 1)
	if err != nil || got != patch {
		t.Fatalf("patch for task = %q, %v", got, err)
	}
	if got, err := DshConfigPatchForTask("claude", env.PrivateRuntimeDir, 1); err != nil || got != "" {
		t.Fatalf("non-dsh patch = %q, %v", got, err)
	}
	missing, err := DshConfigPatchForTask("dsh", t.TempDir(), 1)
	if err == nil || missing != "" {
		t.Fatalf("missing patch = %q, %v", missing, err)
	}
	brief, err := InjectRuntimeConfig(work, "dsh", TaskContextForEnv{
		PrivateRuntimeDir: env.PrivateRuntimeDir,
		AgentSkills:       []SkillContextForEnv{{Name: "multica-platform", Description: "Platform contracts", Content: "Read the task"}},
		P4Workspace:       &P4WorkspaceForEnv{Client: "client"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(brief, "discovered automatically") || !strings.Contains(brief, "multica-platform") || strings.Contains(brief, "reading their SKILL.md") {
		t.Fatalf("dsh brief did not treat private skills as catalog skills: %s", brief)
	}
}

func TestP4ProjectResourcesStayInPrivateRuntimeDirectory(t *testing.T) {
	work := t.TempDir()
	ctx := TaskContextForEnv{AgentID: "agent", ProjectID: "project", P4Workspace: &P4WorkspaceForEnv{Client: "client"},
		ProjectResources: []ProjectResourceForEnv{{ID: "directory", ResourceType: "local_directory", ResourceRef: []byte(`{"local_path":"C:/private/member-client"}`)}},
	}
	env, err := Prepare(PrepareParams{WorkspacesRoot: t.TempDir(), WorkspaceID: "ws", TaskID: "task", Provider: "claude", LocalWorkDir: work, Task: ctx}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup(true)
	if _, err := os.Stat(filepath.Join(work, ".multica", "project", "resources.json")); !os.IsNotExist(err) {
		t.Fatalf("private resource context was written into the Helix client: %v", err)
	}
	privateFile := filepath.Join(env.PrivateRuntimeDir, "project", "resources.json")
	if raw, err := os.ReadFile(privateFile); err != nil || !strings.Contains(string(raw), "C:/private/member-client") {
		t.Fatalf("private resource context missing: %s %v", raw, err)
	}
	ctx.PrivateRuntimeDir = env.PrivateRuntimeDir
	brief, err := InjectRuntimeConfig(work, "claude", ctx)
	if err != nil || !strings.Contains(brief, privateFile) || strings.Contains(brief, "`.multica/project/resources.json`") {
		t.Fatalf("brief did not point to private project context: %s %v", brief, err)
	}
}
