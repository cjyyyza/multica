//go:build windows

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The zcode fixtures are POSIX `zcode app-server` scripts (same pattern as the
// other ACP fakes). CreateProcess cannot launch a shebang script, and a bare
// file named "zcode" has no executable extension on Windows, so the fixture is
// materialised as a .cmd launcher around Git for Windows' Bash. This keeps the
// protocol fixtures identical on every platform instead of re-encoding the
// NDJSON conversations twice.
func writeZcodeTestScript(t *testing.T, dir, script string) string {
	t.Helper()
	shell := zcodeTestPosixShell(t)
	sh := filepath.Join(dir, "zcode.sh")
	if err := os.WriteFile(sh, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake zcode: %v", err)
	}
	bin := filepath.Join(dir, "zcode.cmd")
	// %* is intentionally unquoted: the backend passes exactly one argument
	// ("app-server" or fixture-specific flags) and cmd would otherwise forward
	// the quotes into the script's positional parameters.
	body := "@echo off\r\n" +
		"\"" + shell + "\" --noprofile --norc \"" + sh + "\" %*\r\n" +
		"exit /b %ERRORLEVEL%\r\n"
	if err := os.WriteFile(bin, []byte(body), 0o644); err != nil {
		t.Fatalf("write fake zcode launcher: %v", err)
	}
	return bin
}

// zcodeTestPosixShell resolves Git for Windows' Bash. POSIX fixtures cannot be
// launched by CreateProcess directly, and the `bash.exe` Windows ships on PATH
// may be the WSL launcher (which needs a distribution and is not the shell the
// fixtures were written for), so only Git's own Bash is accepted. An explicit
// MULTICA_TEST_GIT_BASH wins, matching the repository's opt-in Git Bash tests.
func zcodeTestPosixShell(t *testing.T) string {
	t.Helper()
	var candidates []string
	if explicit := os.Getenv("MULTICA_TEST_GIT_BASH"); explicit != "" {
		candidates = append(candidates, explicit)
	}
	// Derive the installation root from git.exe so non-standard installs work
	// without a hardcoded Program Files path.
	if git, err := exec.LookPath("git.exe"); err == nil {
		if resolved, err := filepath.EvalSymlinks(git); err == nil {
			git = resolved
		}
		root := filepath.Dir(filepath.Dir(git))
		candidates = append(candidates,
			filepath.Join(root, "bin", "bash.exe"),
			filepath.Join(root, "usr", "bin", "bash.exe"),
		)
	}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
		if base := os.Getenv(env); base != "" {
			candidates = append(candidates,
				filepath.Join(base, "Git", "bin", "bash.exe"),
				filepath.Join(base, "Git", "usr", "bin", "bash.exe"),
			)
		}
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	// Keep the suite green for hosts without Git for Windows rather than
	// reporting an environment prerequisite as a zcode protocol regression.
	t.Skipf("zcode protocol fixtures require Git for Windows Bash; set MULTICA_TEST_GIT_BASH or install Git for Windows (checked %s)", strings.Join(candidates, ", "))
	return ""
}
