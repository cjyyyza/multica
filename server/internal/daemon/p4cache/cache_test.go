package p4cache

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func writeFakeP4(t *testing.T, dir string) string {
	t.Helper()
	source := filepath.Join(dir, "fake_p4.go")
	program := `package main
import("fmt";"os";"strings")
func main(){
 joined:=" "+strings.Join(os.Args[1:]," ")+" "
 if strings.Contains(joined," info "){fmt.Println("User name: testuser");return}
 if strings.Contains(joined," login -s"){fmt.Println("... TicketExpiration 3600\n... User alice");return}
 if strings.Contains(joined," tickets"){fmt.Println("p4:1666 (alice) TESTTICKET");return}
 panic("unexpected p4 command: "+joined)
}`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "fake-p4"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", path, source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake p4: %v\n%s", err, output)
	}
	return path
}
