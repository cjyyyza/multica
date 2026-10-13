package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// writeDaemonFakeP4 builds a p4 stand-in. Each invocation is appended to
// P4_FAKE_LOG as "<cwd>\t<args>", and the reply for a verb comes from
// P4_FAKE_<VERB> (stderr and a non-zero exit when P4_FAKE_<VERB>_ERR is set).
func writeDaemonFakeP4(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "fake_p4.go")
	program := `package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

func emit(key string) {
	if body := os.Getenv("P4_FAKE_" + key + "_ERR"); body != "" {
		fmt.Fprintln(os.Stderr, body)
		fmt.Print(os.Getenv("P4_FAKE_" + key))
		os.Exit(1)
	}
	fmt.Print(os.Getenv("P4_FAKE_" + key))
}

func main() {
	args := os.Args[1:]
	cwd, _ := os.Getwd()
	if log := os.Getenv("P4_FAKE_LOG"); log != "" {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(f, "%s\t%s\n", cwd, strings.Join(args, " "))
		f.Close()
	}
	rest := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "-u", "-c", "-C":
			i++
		case "-ztag":
		default:
			rest = append(rest, args[i:]...)
			i = len(args)
		}
	}
	if len(rest) == 0 {
		panic("no p4 command")
	}
	joined := " " + strings.Join(rest, " ") + " "
	switch rest[0] {
	case "set":
		emit("SET")
	case "info":
		emit("INFO")
	case "client":
		emit("CLIENT")
	case "fstat":
		// P4_FAKE_FSTAT_WILDCARD answers only arguments ending in "...", the
		// way a real server reports nothing for a bare directory path.
		if body := os.Getenv("P4_FAKE_FSTAT_WILDCARD"); body != "" {
			if strings.Contains(joined, "... ") {
				fmt.Print(body)
			}
			return
		}
		emit("FSTAT")
	case "where":
		emit("WHERE")
	case "opened":
		emit("OPENED")
	case "changes":
		emit("CHANGES")
	case "describe":
		if strings.Contains(joined, " 111 ") {
			fmt.Fprintln(os.Stderr, "111 - no such changelist.")
			os.Exit(1)
		}
		if body := os.Getenv("P4_FAKE_DESCRIBE"); body != "" {
			emit("DESCRIBE")
			return
		}
		fmt.Print("... change 795115\n... oldChange 795095\n... status submitted\n")
	case "change":
		switch {
		case strings.Contains(joined, " -i "):
			io.Copy(io.Discard, os.Stdin)
			emit("CHANGEIN")
		case strings.Contains(joined, " -o "):
			emit("CHANGEOUT")
		default:
			emit("CHANGEDEL")
		}
	case "tickets":
		if body := os.Getenv("P4_FAKE_TICKETS"); body != "" {
			fmt.Print(body)
			return
		}
		fmt.Println("p4.example:1666 (alice) TICKET")
	case "revert", "shelve", "edit", "add", "delete", "reopen", "move", "reconcile":
		emit("MUTATE")
	default:
		panic("unexpected p4 command: " + joined)
	}
}
`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "fake-p4"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if out, err := exec.Command("go", "build", "-o", path, source).CombinedOutput(); err != nil {
		t.Fatalf("build fake p4: %v\n%s", err, out)
	}
	return path
}
