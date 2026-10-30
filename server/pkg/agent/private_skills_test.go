package agent

import (
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
)

func TestPrivateSkillPluginsPreserveUserPlugins(t *testing.T) {
	for name, build := range map[string]func(ExecOptions, *slog.Logger) []string{
		"codebuddy": buildCodebuddyArgs,
		"copilot":   buildCopilotArgs,
		"cursor":    buildCursorArgs,
		"grok":      buildGrokArgs,
		"qoder":     buildQoderArgs,
	} {
		t.Run(name, func(t *testing.T) {
			args := build(ExecOptions{SkillPluginDir: "/task/private", CustomArgs: []string{"--plugin-dir", "/user/plugin"}}, slog.Default())
			for _, dir := range []string{"/task/private", "/user/plugin"} {
				i := slices.Index(args, dir)
				if i < 1 || args[i-1] != "--plugin-dir" {
					t.Fatalf("plugin %s not passed as a native directory: %#v", dir, args)
				}
				if name == "grok" && i >= slices.Index(args, "stdio") {
					t.Fatalf("Grok plugin flag must precede the stdio subcommand: %#v", args)
				}
			}
			without := build(ExecOptions{}, slog.Default())
			if slices.Contains(without, "--plugin-dir") {
				t.Fatalf("ordinary task gained a plugin flag: %#v", without)
			}
		})
	}
}

func TestAntigravityPrivateSkillWorkspaceKeepsPrimaryDirectory(t *testing.T) {
	args := buildAntigravityArgs("task", "/tmp/log", 0, ExecOptions{Cwd: "/work/p4", SkillWorkspaceDir: "/task/private"}, slog.Default())
	for _, dir := range []string{filepath.Clean("/work/p4"), "/task/private"} {
		i := slices.Index(args, dir)
		if i < 1 || args[i-1] != "--add-dir" {
			t.Fatalf("workspace %s not passed to native discovery: %#v", dir, args)
		}
	}
}
