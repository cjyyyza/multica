package execenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP4PrivateSkillPlugins(t *testing.T) {
	for provider, manifestPath := range map[string]string{
		"codebuddy":  ".codebuddy-plugin/plugin.json",
		"copilot":    ".claude-plugin/plugin.json",
		"cursor":     ".cursor-plugin/plugin.json",
		"grok":       ".claude-plugin/plugin.json",
		"qoder":      ".qoder-plugin/plugin.json",
		"qoderclicn": ".qoder-plugin/plugin.json",
	} {
		t.Run(provider, func(t *testing.T) {
			work := t.TempDir()
			task := TaskContextForEnv{
				AgentID: "agent", P4Workspace: &P4WorkspaceForEnv{Client: "client"},
				AgentSkills: []SkillContextForEnv{{Name: "multica-platform", Description: "Platform contracts", Content: "Read the task"}},
			}
			env, err := Prepare(PrepareParams{WorkspacesRoot: t.TempDir(), WorkspaceID: "ws", TaskID: "task", Provider: provider, LocalWorkDir: work, Task: task}, testLogger())
			if err != nil {
				t.Fatal(err)
			}
			defer env.Cleanup(true)
			raw, err := os.ReadFile(filepath.Join(env.PrivateRuntimeDir, filepath.FromSlash(manifestPath)))
			if err != nil {
				t.Fatalf("private plugin was not materialized: %v", err)
			}
			var manifest map[string]any
			if err := json.Unmarshal(raw, &manifest); err != nil || manifest["name"] != "multica-task" {
				t.Fatalf("plugin manifest = %s (%v)", raw, err)
			}
			if _, err := os.Stat(filepath.Join(env.PrivateRuntimeDir, "skills", "multica-platform", "SKILL.md")); err != nil {
				t.Fatal(err)
			}
			root, err := PrivateSkillPluginDir(provider, env.PrivateRuntimeDir, len(task.AgentSkills))
			if err != nil || root != env.PrivateRuntimeDir {
				t.Fatalf("launch plugin = %q (%v)", root, err)
			}
			if _, err := os.Stat(SkillsDirPath(work, provider)); !os.IsNotExist(err) {
				t.Fatalf("managed skills wrote into the Helix client: %v", err)
			}
			task.PrivateRuntimeDir = env.PrivateRuntimeDir
			brief, err := InjectRuntimeConfig(work, provider, task)
			if err != nil || !strings.Contains(brief, "discovered automatically") || !strings.Contains(brief, "multica-task") || strings.Contains(brief, "reading their SKILL.md") {
				t.Fatalf("plugin skills were not advertised through the native catalog: %s (%v)", brief, err)
			}
		})
	}
}

func TestPrivateSkillPluginMissingFilesRejectLaunch(t *testing.T) {
	if _, err := PrivateSkillPluginDir("cursor", t.TempDir(), 1); err == nil {
		t.Fatal("missing manifest did not reject the launch")
	}
	root := t.TempDir()
	if err := writePrivateSkillPlugin("cursor", root, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := PrivateSkillPluginDir("cursor", root, 1); err == nil {
		t.Fatal("missing skills did not reject the launch")
	}
	for _, provider := range []string{"cursor", "opencode"} {
		if dir, err := PrivateSkillPluginDir(provider, "", 1); err != nil || dir != "" {
			t.Fatalf("ordinary task plugin = %q (%v)", dir, err)
		}
	}
}

func TestP4PrivateSkillBriefTracksRuntimeContract(t *testing.T) {
	for _, tt := range []struct {
		provider, version string
		custom, native    bool
	}{
		{"opencode", "1.18.31", false, true},
		{"opencode", "opencode v2.2.1", false, false},
		{"opencode", "wrapper 2.0.0", true, true},
		{"traecli", "2.0.0", false, false},
		{"codearts", "26.10.13", false, false},
		{"kiro", "2.1.1", false, false},
		{"kiro", "", false, false},
		{"kiro", "2.33.0", false, true},
		{"muse", "1.4.10", false, false},
		{"dim", "0.5.22", false, false},
		{"zeroclaw", "0.8.4", false, false},
	} {
		t.Run(tt.provider+tt.version, func(t *testing.T) {
			ctx := TaskContextForEnv{PrivateRuntimeDir: t.TempDir(), RuntimeVersion: tt.version, CustomRuntime: tt.custom,
				AgentSkills: []SkillContextForEnv{{Name: "managed", Content: "task"}},
			}
			var brief strings.Builder
			writeSkills(&brief, tt.provider, ctx)
			got := brief.String()
			if strings.Contains(got, "discovered automatically") != tt.native {
				t.Fatalf("brief disagrees with the selected execution contract: %s", got)
			}
			if !tt.native && !strings.Contains(got, filepath.Join(ctx.PrivateRuntimeDir, "skills", "managed", "SKILL.md")) {
				t.Fatalf("runtime without a native hook lost the explicit skill path: %s", got)
			}
		})
	}
}

func TestP4PrivateSkillWorkspaceRefreshesWithoutClientWrites(t *testing.T) {
	work := t.TempDir()
	params := PrepareParams{WorkspacesRoot: t.TempDir(), WorkspaceID: "ws", TaskID: "task", Provider: "antigravity", LocalWorkDir: work,
		Task: TaskContextForEnv{AgentID: "agent", P4Workspace: &P4WorkspaceForEnv{Client: "client"}, AgentSkills: []SkillContextForEnv{{Name: "first", Content: "first"}}},
	}
	env, err := Prepare(params, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup(true)
	root, err := PrivateSkillWorkspaceDir("antigravity", env.PrivateRuntimeDir, 1)
	if err != nil || root != env.PrivateRuntimeDir {
		t.Fatalf("native workspace = %q (%v)", root, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agents", "skills", "first", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	params.Task.AgentSkills = []SkillContextForEnv{{Name: "second", Content: "second"}}
	if reused := Reuse(ReuseParams{WorkspacesRoot: params.WorkspacesRoot, WorkDir: work, Provider: "antigravity", LocalDirectory: true, Task: params.Task}, testLogger()); reused != nil {
		t.Fatal("local P4 reuse must allocate fresh scratch through Prepare")
	}
	params.TaskID = "next-task"
	reused, err := Prepare(params, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer reused.Cleanup(true)
	if _, err := os.Stat(filepath.Join(reused.PrivateRuntimeDir, ".agents", "skills", "second", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(reused.PrivateRuntimeDir, ".agents", "skills", "first")); !os.IsNotExist(err) {
		t.Fatalf("old task skills leaked into the new catalog: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("native workspace materialization wrote to the client: %v", err)
	}
}
