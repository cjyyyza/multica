package agent

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrivateSkillConfigReachesNativeBackends(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"opencode", "deveco"} {
		t.Run(provider, func(t *testing.T) {
			key, contentKey := "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT"
			if provider == "deveco" {
				key, contentKey = "DEVECO_CONFIG_DIR", "DEVECO_CONFIG_CONTENT"
			}
			work, private := t.TempDir(), t.TempDir()
			capture := filepath.Join(t.TempDir(), "env.json")
			original := `{"skills":{"paths":["/own/skills"]},"model":"owner/model"}`
			backend, err := New(provider, Config{ExecutablePath: self, Logger: slog.Default(), Env: map[string]string{
				opencodeStdinHelperEnv: "1", privateSkillHelperEnvFile: capture,
				opencodeStdinHelperArgvFile: filepath.Join(t.TempDir(), "argv"),
				opencodeStdinHelperInFile:   filepath.Join(t.TempDir(), "stdin"),
				key:                         private, contentKey: original,
			}})
			if err != nil {
				t.Fatal(err)
			}
			session, err := backend.Execute(t.Context(), "task", ExecOptions{Cwd: work, Timeout: 10 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			go func() {
				for range session.Messages {
				}
			}()
			if result := <-session.Result; result.Status != "completed" {
				t.Fatalf("native backend failed: %+v", result)
			}
			raw, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]string
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got[key] != private || got[contentKey] != original {
				t.Fatalf("child lost native skill config or changed inline config: %#v", got)
			}
			if !strings.EqualFold(filepath.Clean(got["cwd"]), filepath.Clean(work)) {
				t.Fatalf("child working directory changed: %#v", got)
			}
		})
	}
}
