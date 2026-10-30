package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateSkillHomesKeepAuthSessionsAndUserSkills(t *testing.T) {
	for provider, key := range map[string]string{"kimi": "KIMI_CODE_HOME", "qwen": "QWEN_HOME", "kiro": "KIRO_HOME", "reasonix": "REASONIX_HOME", "mcode": "MINIMAX_DATA_DIR"} {
		t.Run(provider, func(t *testing.T) {
			source, private := t.TempDir(), t.TempDir()
			for rel, body := range map[string]string{
				"config.toml":                      "default_model = 'owner'\n",
				"auth.json":                        "credential fixture",
				"sessions/resume.json":             "session",
				"credentials/token":                "auth",
				"skills/own/SKILL.md":              "User skill",
				"skills/multica-platform/SKILL.md": "Older user skill",
			} {
				path := filepath.Join(source, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeSkillFiles(filepath.Join(private, "skills"), []SkillContextForEnv{{Name: "multica-platform", Content: "Task skill"}}, nil); err != nil {
				t.Fatal(err)
			}
			input := map[string]string{key: source}
			env, err := PreparePrivateSkillHome(provider, private, t.TempDir(), "2.33.0", 1, input)
			if err != nil {
				t.Fatal(err)
			}
			if input[key] != source || len(env) != 1 || env[key] == source {
				t.Fatalf("source environment mutated or unrelated variables changed: %#v %#v", input, env)
			}
			srcInfo, err := os.Stat(filepath.Join(source, "auth.json"))
			if err != nil {
				t.Fatal(err)
			}
			dstInfo, err := os.Stat(filepath.Join(env[key], "auth.json"))
			if err != nil || !os.SameFile(srcInfo, dstInfo) {
				t.Fatalf("auth state was copied instead of shared: %v", err)
			}
			for rel, want := range map[string]string{"sessions/resume.json": "session", "credentials/token": "auth", "skills/own/SKILL.md": "User skill"} {
				if raw, err := os.ReadFile(filepath.Join(env[key], filepath.FromSlash(rel))); err != nil || string(raw) != want {
					t.Fatalf("%s unavailable in task home: %q (%v)", rel, raw, err)
				}
			}
			if raw, err := os.ReadFile(filepath.Join(env[key], "skills", "multica-platform", "SKILL.md")); err != nil || !strings.Contains(string(raw), "Task skill") {
				t.Fatalf("managed skill lost to a user copy: %q (%v)", raw, err)
			}
			if err := os.WriteFile(filepath.Join(env[key], "config.toml"), []byte("changed"), 0o600); err != nil {
				t.Fatal(err)
			}
			if raw, err := os.ReadFile(filepath.Join(source, "config.toml")); err != nil || string(raw) != "default_model = 'owner'\n" {
				t.Fatalf("task config wrote through to the user's config: %q (%v)", raw, err)
			}
		})
	}
}

func TestPrivateSkillHomeFirstSessionSurvivesNextTask(t *testing.T) {
	for provider, key := range map[string]string{"kimi": "KIMI_CODE_HOME", "qwen": "QWEN_HOME", "kiro": "KIRO_HOME"} {
		t.Run(provider, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "new-profile")
			store := "sessions"
			if provider == "qwen" {
				store = "tmp"
			}
			var firstPrivate string
			for turn := range 2 {
				private := t.TempDir()
				if err := writeSkillFiles(filepath.Join(private, "skills"), []SkillContextForEnv{{Name: "managed", Content: "task"}}, nil); err != nil {
					t.Fatal(err)
				}
				env, err := PreparePrivateSkillHome(provider, private, t.TempDir(), "2.33.0", 1, map[string]string{key: source})
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(env[key], store, "first-session")
				if turn == 0 {
					firstPrivate = private
					if err := os.WriteFile(path, []byte("resume"), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.RemoveAll(firstPrivate); err != nil {
						t.Fatal(err)
					}
				} else if data, err := os.ReadFile(path); err != nil || string(data) != "resume" {
					t.Fatalf("new home lost the first session after task cleanup: %q (%v)", data, err)
				}
			}
			if _, err := os.Stat(filepath.Join(source, "skills", "managed")); !os.IsNotExist(err) {
				t.Fatalf("managed skill escaped into the owner profile: %v", err)
			}
		})
	}
}
