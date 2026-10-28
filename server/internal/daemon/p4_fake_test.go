package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func p4RegistrationTestClient(t *testing.T) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/p4/changelists") {
			t.Errorf("unexpected P4 registration request: %s", r.URL.Path)
		}
		w.Write([]byte("{}"))
	}))
	t.Cleanup(server.Close)
	return NewClient(server.URL)
}

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
	"path/filepath"
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
	case "server":
		emit("SERVER")
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
		if _, set := os.LookupEnv("P4_FAKE_WHERE"); set || os.Getenv("P4_FAKE_WHERE_ERR") != "" { emit("WHERE"); return }
		root, depot := cwd, "//DJ01_Proj/Dev"
		for _, line := range strings.Split(os.Getenv("P4_FAKE_CLIENT"), "\n") {
			if value, ok := strings.CutPrefix(line, "... Root "); ok { root = strings.TrimSpace(value) }
			if value, ok := strings.CutPrefix(line, "... Stream "); ok { depot = strings.TrimSpace(value) }
		}
		if value := os.Getenv("P4_FAKE_DEPOT"); value != "" { depot = value }
		file := rest[len(rest)-1]
		local, mapped := file, ""
		if strings.HasPrefix(file, "//") {
			mapped = file
			local = filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(file, depot), "/")))
		} else {
			if !filepath.IsAbs(local) { local = filepath.Join(cwd, local) }
			rel, err := filepath.Rel(root, local); if err != nil { panic(err) }
			mapped = depot + "/" + filepath.ToSlash(rel)
		}
		fmt.Printf("... depotFile %s\n... path %s\n", mapped, local)
	case "diff2":
		emit("DIFF2")
	case "opened":
		emit("OPENED")
	case "changes":
		emit("CHANGES")
	case "describe":
		if strings.Contains(joined, " -S ") {
			if _, ok := os.LookupEnv("P4_FAKE_SHELF"); ok || os.Getenv("P4_FAKE_SHELF_ERR") != "" {
				emit("SHELF")
			} else {
				fmt.Printf("... change %s\n... status pending\n", rest[len(rest)-1])
			}
			return
		}
		if strings.Contains(joined, " 111 ") {
			fmt.Fprintln(os.Stderr, "111 - no such changelist.")
			os.Exit(1)
		}
		if body := os.Getenv("P4_FAKE_DESCRIBE"); body != "" || os.Getenv("P4_FAKE_DESCRIBE_ERR") != "" {
			emit("DESCRIBE")
			return
		}
		fmt.Print("... change 795115\n... oldChange 795095\n... status submitted\n... time 1700000000\n")
	case "change":
		switch {
		case strings.Contains(joined, " -i "):
			if path := os.Getenv("P4_FAKE_SPEC"); path != "" {
				f, err := os.Create(path)
				if err != nil { panic(err) }
				io.Copy(f, os.Stdin)
				f.Close()
			} else {
				io.Copy(io.Discard, os.Stdin)
			}
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
	case "revert":
        if _, ok := os.LookupEnv("P4_FAKE_REVERT_ERR"); ok { emit("REVERT"); return }
        emit("MUTATE")
    case "shelve":
        if strings.Contains(joined, " -d ") {
            if _, ok := os.LookupEnv("P4_FAKE_SHELFDELETE_ERR"); ok { emit("SHELFDELETE"); return }
        }
        emit("MUTATE")
    case "edit", "add", "delete", "reopen", "move", "reconcile", "submit", "sync", "unshelve":
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
