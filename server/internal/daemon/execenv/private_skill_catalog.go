package execenv

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// cataloguesPrivateSkills reports whether a P4 task's managed skills reach
// the runtime catalog from outside the Helix client.
//
// Codex, Hermes and QwenPaw already load a task-owned home. dsh gets the
// private directory through --patch. Claude loads it through --add-dir, and
// Pi (including omp) through --skill. Plugin-capable runtimes load a
// task-owned skill plugin. Runtimes without an implemented private-root
// integration still receive the SKILL.md paths in their brief.
func cataloguesPrivateSkills(provider string, ctx TaskContextForEnv) bool {
	if agent.SupportsPrivateSkillConfig(provider, ctx.RuntimeVersion, !ctx.CustomRuntime) {
		return true
	}
	if privateSkillPluginManifest(provider) != "" {
		return true
	}
	if agent.SupportsPrivateSkillHome(provider, ctx.RuntimeVersion) {
		return true
	}
	switch skillCatalogFamily(provider) {
	case "dsh", "codex", "hermes", "qwenpaw", "claude", "pi", "openclaw", "antigravity":
		return true
	default:
		return false
	}
}

func PrivateSkillWorkspaceDir(provider, privateRuntimeDir string, skillCount int) (string, error) {
	if provider != "antigravity" || privateRuntimeDir == "" || skillCount == 0 {
		return "", nil
	}
	if _, err := os.Stat(filepath.Join(privateRuntimeDir, ".agents", "skills")); err != nil {
		return "", fmt.Errorf("antigravity private skill workspace: %w", err)
	}
	return privateRuntimeDir, nil
}

func privateSkillsDirectory(privateRuntimeDir string, skillCount int) string {
	if privateRuntimeDir == "" || skillCount == 0 {
		return ""
	}
	return filepath.Join(privateRuntimeDir, "skills")
}

const privateSkillPluginName = "multica-task"

// Each runtime keeps its own manifest contract; the plugin's skills/ directory
// is the same task-owned directory written by writeContextFiles.
func privateSkillPluginManifest(provider string) string {
	switch skillCatalogFamily(provider) {
	case "codebuddy":
		return filepath.Join(".codebuddy-plugin", "plugin.json")
	case "cursor":
		return filepath.Join(".cursor-plugin", "plugin.json")
	case "copilot", "grok":
		return filepath.Join(".claude-plugin", "plugin.json")
	case "qoder", "qoderclicn":
		return filepath.Join(".qoder-plugin", "plugin.json")
	default:
		return ""
	}
}

// PrivateSkillPluginDir returns the additive plugin root for this launch.
// Missing materialization is an error rather than a skill-less launch.
func PrivateSkillPluginDir(provider, privateRuntimeDir string, skillCount int) (string, error) {
	manifest := privateSkillPluginManifest(provider)
	if manifest == "" || privateRuntimeDir == "" || skillCount == 0 {
		return "", nil
	}
	if _, err := os.Stat(filepath.Join(privateRuntimeDir, manifest)); err != nil {
		return "", fmt.Errorf("%s private skill plugin: %w", provider, err)
	}
	if _, err := PrivateSkillDirs(privateRuntimeDir); err != nil {
		return "", err
	}
	return privateRuntimeDir, nil
}

func writePrivateSkillPlugin(provider, privateRuntimeDir string, manifest *sidecarManifest) error {
	rel := privateSkillPluginManifest(provider)
	if rel == "" {
		return nil
	}
	path := filepath.Join(privateRuntimeDir, rel)
	if err := recordMkdirAll(filepath.Dir(path), 0o700, manifest); err != nil {
		return err
	}
	return recordWriteFile(path, []byte(`{"name":"`+privateSkillPluginName+`","version":"1.0.0"}`+"\n"), 0o600, manifest)
}

func skillCatalogFamily(provider string) string {
	if desc, ok := agent.BuiltinRuntimeByID(provider); ok {
		return desc.ProtocolFamily
	}
	return provider
}

// ConfigurePrivateSkillLaunch returns the Claude --add-dir root and the Pi
// --skill directories for a P4 task. Both stay empty when this provider does
// not need them. A provider that should catalog private skills fails closed
// when those directories were not materialized.
func ConfigurePrivateSkillLaunch(provider, privateRuntimeDir string, skillCount int) (claudeAddDir string, piSkillDirs []string, err error) {
	if privateRuntimeDir == "" || skillCount == 0 {
		return "", nil, nil
	}
	switch skillCatalogFamily(provider) {
	case "claude":
		dir := filepath.Join(privateRuntimeDir, ".claude", "skills")
		if _, statErr := os.Stat(dir); statErr != nil {
			return "", nil, fmt.Errorf("claude private skills: %w", statErr)
		}
		return privateRuntimeDir, nil, nil
	case "pi":
		dirs, listErr := PrivateSkillDirs(privateRuntimeDir)
		if listErr != nil {
			return "", nil, listErr
		}
		return "", dirs, nil
	default:
		return "", nil, nil
	}
}

// PrivateSkillDirs lists skill directories under the P4 private root. Each
// entry contains a SKILL.md and is suitable for a runtime that accepts one
// skill path at a time.
func PrivateSkillDirs(privateRuntimeDir string) ([]string, error) {
	root := filepath.Join(privateRuntimeDir, "skills")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("private skills: %w", err)
	}
	dirs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
			continue
		}
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	if len(dirs) == 0 {
		return nil, fmt.Errorf("private skills: no SKILL.md under %s", root)
	}
	return dirs, nil
}
