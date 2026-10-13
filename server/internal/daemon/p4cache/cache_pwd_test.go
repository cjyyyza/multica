package p4cache

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// p4 locates P4CONFIG (p4config.txt) by walking up from $PWD, not from the
// process working directory. Live on the DJ01 machine, a command run with
// only cmd.Dir set picked the registry P4CLIENT — a client bound to another
// host — instead of the one D:\DJ\p4config.txt names. Every command run in a
// workspace directory must carry PWD for that directory.
func TestCommandInWorkspaceDirSetsPWD(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "fake_pwd_p4.go")
	program := `package main
import ("fmt";"os")
func main(){ fmt.Print(os.Getenv("PWD")) }
`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "fake-p4")
	if runtimeExeSuffix := filepath.Ext(os.Args[0]); runtimeExeSuffix == ".exe" {
		bin += ".exe"
	}
	if output, err := exec.Command("go", "build", "-o", bin, source).CombinedOutput(); err != nil {
		t.Fatalf("build fake p4: %v\n%s", err, output)
	}

	workspace := filepath.Join(dir, "DJ")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Cache{P4Path: bin}
	stdout, stderr, err := c.runAt(context.Background(), workspace, []string{"info"})
	if err != nil {
		t.Fatalf("runAt: %v: %s", err, stderr)
	}
	if got := strings.TrimSpace(stdout); got != workspace {
		t.Fatalf("PWD seen by p4 = %q, want the workspace dir %q", got, workspace)
	}
}
