package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateSkillConfigDirPreservesNativeInputs(t *testing.T) {
	for provider, key := range map[string]string{"opencode": "OPENCODE_CONFIG_DIR", "deveco": "DEVECO_CONFIG_DIR"} {
		t.Run(provider, func(t *testing.T) {
			source, private, work := t.TempDir(), t.TempDir(), t.TempDir()
			configName := provider + ".jsonc"
			config := "{\n// owner configuration\n\"skills\":{\"paths\":[\"/additional/user/skills\"]},\"mcp\":{\"own\":{\"type\":\"local\",\"command\":[\"own\"]}}\n}"
			inputs := map[string]string{configName: config, "skills/own/SKILL.md": "Owner skill", "tools/helper.ts": "Owner tool", "skill/singular/SKILL.md": "Singular skill"}
			for rel, body := range inputs {
				path := filepath.Join(source, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeSkillFiles(filepath.Join(private, "skills"), []SkillContextForEnv{{Name: "managed", Content: "Task skill"}}, nil); err != nil {
				t.Fatal(err)
			}
			inputKey := key
			if reasonixGOOS == "windows" {
				inputKey = strings.ToLower(key)
			}
			input := map[string]string{inputKey: source}
			env, err := PreparePrivateSkillConfigDir(provider, private, work, "1.18.31", true, 1, input)
			if err != nil {
				t.Fatal(err)
			}
			target := env[key]
			if target == "" || target == source || input[inputKey] != source {
				t.Fatalf("invalid native config overlay: %#v %#v", input, env)
			}
			for rel, want := range inputs {
				if raw, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(rel))); err != nil || string(raw) != want {
					t.Fatalf("native input %s changed: %q (%v)", rel, raw, err)
				}
			}
			if raw, err := os.ReadFile(filepath.Join(target, "skills", "managed", "SKILL.md")); err != nil || !strings.Contains(string(raw), "Task skill") {
				t.Fatalf("native skill missing: %q (%v)", raw, err)
			}
			if err := os.WriteFile(filepath.Join(target, configName), []byte("changed"), 0o600); err != nil {
				t.Fatal(err)
			}
			if raw, err := os.ReadFile(filepath.Join(source, configName)); err != nil || string(raw) != config {
				t.Fatalf("source config changed: %q (%v)", raw, err)
			}
			if entries, err := os.ReadDir(work); err != nil || len(entries) != 0 {
				t.Fatalf("config overlay wrote into the client: %v (%v)", entries, err)
			}
		})
	}
}

func TestPrivateSkillConfigDirDoesNotReplaceDefaultsOrUnsupportedVersions(t *testing.T) {
	private := t.TempDir()
	if err := writeSkillFiles(filepath.Join(private, "skills"), []SkillContextForEnv{{Name: "managed", Content: "task"}}, nil); err != nil {
		t.Fatal(err)
	}
	env, err := PreparePrivateSkillConfigDir("deveco", private, t.TempDir(), "0.2.5", true, 1, map[string]string{"DEVECO_CONFIG_DIR": ""})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(env["DEVECO_CONFIG_DIR"], "deveco.json")); !os.IsNotExist(err) {
		t.Fatalf("default config was replaced instead of adding skills: %v", err)
	}
	for _, provider := range []string{"opencode", "codearts", "cursor"} {
		got, err := PreparePrivateSkillConfigDir(provider, t.TempDir(), t.TempDir(), "opencode v2.2.1", true, 1, nil)
		if err != nil || len(got) != 0 {
			t.Fatalf("unsupported native contract prepared a directory: %#v (%v)", got, err)
		}
	}
}
