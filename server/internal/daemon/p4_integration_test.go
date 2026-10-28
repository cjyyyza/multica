//go:build p4integration

package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

// This opt-in test starts a disposable loopback server. It never connects to
// the developer's P4 server or uses their client, ticket or workspace.
func TestP4IsolatedServerSubmitAndImportedSync(t *testing.T) {
	p4bin, p4dbin := os.Getenv("MULTICA_TEST_P4_BIN"), os.Getenv("MULTICA_TEST_P4D_BIN")
	if p4bin == "" || p4dbin == "" {
		t.Skip("set MULTICA_TEST_P4_BIN and MULTICA_TEST_P4D_BIN to run the isolated P4 integration test")
	}
	root := t.TempDir()
	serverRoot := filepath.Join(root, "server")
	if err := os.Mkdir(serverRoot, 0755); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().String()
	listener.Close()
	for key, value := range map[string]string{
		"P4PORT": port, "P4USER": "multica_test", "P4CLIENT": "dev_client", "P4CHARSET": "none",
		"P4CONFIG": ".multica-test-no-config", "P4ENVIRO": filepath.Join(root, "enviro"),
		"P4TICKETS": filepath.Join(root, "tickets"), "P4TRUST": filepath.Join(root, "trust"), "P4PASSWD": "",
	} {
		t.Setenv(key, value)
	}
	// Recent P4D versions require authentication by default. Disable it only
	// in this disposable database, before the loopback listener is started.
	for _, setting := range []string{"security=0", "dm.user.noautocreate=0", "dm.user.setinitialpasswd=1", "dm.user.resetpassword=0", "dm.info.hide=0"} {
		initServer := exec.Command(p4dbin, "-r", serverRoot, "-cset "+setting)
		initServer.Dir = serverRoot
		if out, err := initServer.CombinedOutput(); err != nil {
			t.Fatalf("initialize isolated server: %v %s", err, out)
		}
	}
	server := exec.Command(p4dbin, "-r", serverRoot, "-p", port, "-L", filepath.Join(root, "p4d.log"), "-J", filepath.Join(root, "journal"))
	if err := os.WriteFile(filepath.Join(serverRoot, "server.id"), []byte("multica-isolated-server\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server.Dir = serverRoot
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Process.Kill(); _ = server.Wait() })
	run := func(client, dir, input string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, p4bin, append([]string{"-p", port, "-u", "multica_test", "-c", client}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PWD="+dir)
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	ready := false
	var lastInfo string
	var lastErr error
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		lastInfo, lastErr = run("dev_client", root, "", "info")
		if lastErr == nil && strings.Contains(strings.ToLower(filepath.ToSlash(lastInfo)), strings.ToLower(filepath.ToSlash(serverRoot))) {
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		log, _ := os.ReadFile(filepath.Join(root, "p4d.log"))
		t.Fatalf("disposable P4 server did not start: %v\n%s\n%s", lastErr, lastInfo, log)
	}
	must := func(client, dir, input string, args ...string) string {
		t.Helper()
		out, err := run(client, dir, input, args...)
		if err != nil {
			t.Fatalf("p4 %v: %v\n%s", args, err, out)
		}
		return out
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// P4ENVIRO and P4TICKETS above point into this test's temporary directory,
	// including the Windows settings written by the passwd command.
	const fixturePassword = "Multica-Fixture-Only-123!"
	must("dev_client", root, fixturePassword+"\n"+fixturePassword+"\n", "passwd")
	must("dev_client", root, fixturePassword+"\n", "login")
	must("dev_client", root, "User: multica_test\nEmail: test@example.invalid\nFullName: Isolated P4 test\n", "user", "-i")
	must("dev_client", root, "Depot: test\nType: stream\nStreamDepth: 1\nMap: test/...\n", "depot", "-i")
	for _, name := range []string{"Engine", "Dev"} {
		paths := "\tshare ...\n"
		if name == "Dev" {
			paths += "\timport engine/... //test/Engine/...\n"
		}
		must("dev_client", root, fmt.Sprintf("Stream: //test/%s\nOwner: multica_test\nName: %s\nParent: none\nType: mainline\nParentView: noinherit\nPaths:\n%s", name, name, paths), "stream", "-i")
	}
	engineRoot, devRoot := filepath.Join(root, "engine"), filepath.Join(root, "dev")
	for client, dir := range map[string]string{"engine_client": engineRoot, "dev_client": devRoot} {
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		stream := "Dev"
		if client == "engine_client" {
			stream = "Engine"
		}
		must(client, dir, fmt.Sprintf("Client: %s\nOwner: multica_test\nRoot: %s\nStream: //test/%s\nSubmitOptions: submitunchanged\n", client, dir, stream), "client", "-i")
	}
	engineFile := filepath.Join(engineRoot, "version.txt")
	write(engineFile, "engine v1\n")
	must("engine_client", engineRoot, "", "add", engineFile)
	must("engine_client", engineRoot, "", "submit", "-d", "seed engine")
	must("dev_client", devRoot, "", "sync")
	for _, file := range []string{"game.txt", "unchanged.txt"} {
		write(filepath.Join(devRoot, file), "v1\n")
		must("dev_client", devRoot, "", "add", file)
	}
	must("dev_client", devRoot, "", "submit", "-d", "seed game")
	must("engine_client", engineRoot, "", "edit", engineFile)
	write(engineFile, "engine v2\n")
	must("engine_client", engineRoot, "", "submit", "-d", "update imported engine")
	cache := &p4cache.Cache{P4Path: p4bin}
	ws, err := cache.DetectWorkspace(t.Context(), devRoot)
	if err != nil {
		t.Fatal(err)
	}
	mainRef := p4depot.Ref{Port: port, Depot: "//test/Dev", AllowSubmit: true, AllowSync: true}
	b := &p4TaskBinding{ws: ws, ref: mainRef, ticket: "123", issueKey: "MUL-7", taskID: "initial-task"}
	d := &Daemon{p4Cache: cache, client: p4RegistrationTestClient(t)}
	if _, rej, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "sync"}, devRoot); rej != nil || err != nil {
		t.Fatalf("sync: %+v %v", rej, err)
	}
	imported, err := os.ReadFile(filepath.Join(devRoot, "engine", "version.txt"))
	if err != nil || string(imported) != "engine v2\n" && string(imported) != "engine v2\r\n" {
		t.Fatalf("import did not sync: %q %v", imported, err)
	}
	change, err := cache.CreateTaskChange(t.Context(), ws, p4depot.ChangeDescription("123", "fix", "MUL-7"))
	if err != nil {
		t.Fatal(err)
	}
	b.change = change
	if _, rej, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "edit", Files: []string{"game.txt", "unchanged.txt"}}, devRoot); rej != nil || err != nil {
		t.Fatalf("open task files: %+v %v", rej, err)
	}
	write(filepath.Join(devRoot, "game.txt"), "v2\n")
	must("dev_client", devRoot, "", "shelve", "-c", change)
	if out, err := run("dev_client", devRoot, "", "submit", "-c", change); err == nil || !strings.Contains(out, "shelved") {
		t.Fatalf("expected real P4 to reject submitting a CL with a shelf: %v %s", err, out)
	}
	result, rej, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "submit"}, devRoot)
	if rej != nil || err != nil {
		t.Fatalf("submit with preflight: %+v %v", rej, err)
	}
	receipt, err := cache.SubmittedChange(t.Context(), ws, change)
	if err != nil || !receipt.Submitted || result.Changelist != receipt.Change || len(receipt.Files) != 1 || receipt.Files[0].DepotFile != "//test/Dev/game.txt" {
		t.Fatalf("wrong submission or unchanged revision included: %+v %v", receipt, err)
	}
	t.Run("poll keeps the server submission timestamp", func(t *testing.T) {
		raw := must("dev_client", devRoot, "", "-ztag", "describe", "-s", receipt.Change)
		var serverTime string
		for _, line := range strings.Split(raw, "\n") {
			if value, ok := strings.CutPrefix(line, "... time "); ok {
				serverTime = strings.TrimSpace(value)
				break
			}
		}
		number, err := strconv.Atoi(receipt.Change)
		if err != nil || serverTime == "" {
			t.Fatalf("real describe did not supply a submission timestamp: %v\n%s", err, raw)
		}
		update, ok := d.observeP4Changelist(t.Context(), p4OpenChangelist{Number: number}, ws.Ref())
		stamp, err := time.Parse(time.RFC3339, update.SubmittedAt)
		if !ok || err != nil || strconv.FormatInt(stamp.Unix(), 10) != serverTime {
			t.Fatalf("poll replaced server time %s: %+v, %v", serverTime, update, err)
		}
		if !receipt.SubmittedAt.Equal(stamp) || d.changelistReport(t.Context(), b, receipt).SubmittedAt != update.SubmittedAt {
			t.Fatal("task delivery did not preserve the same server timestamp as polling")
		}
	})
	if _, rej, _ := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "submit"}, devRoot); rej == nil {
		t.Fatal("duplicate submit accepted")
	}
	nextChange, err := cache.CreateTaskChange(t.Context(), ws, p4depot.ChangeDescription("123", "next fix", "MUL-7"))
	if err != nil {
		t.Fatal(err)
	}
	must("dev_client", devRoot, "", "edit", "-c", nextChange, "game.txt")
	write(filepath.Join(devRoot, "game.txt"), "v3\n")
	must("dev_client", devRoot, "", "shelve", "-c", nextChange)
	if err := cache.SetChangeDescription(t.Context(), ws, nextChange, "Human removed the ticket\n\nMultica: MUL-7"); err != nil {
		t.Fatal(err)
	}
	nextBinding := &p4TaskBinding{ws: ws, ref: b.ref, ticket: "123", issueKey: "MUL-7", change: nextChange}
	if _, rej, err := d.runWorkspaceP4(t.Context(), nextBinding, p4MutateRequest{Action: "submit"}, devRoot); err != nil || rej == nil || rej.Code != p4CodeTicketRequired {
		t.Fatalf("human-edited description was not rejected: %+v %v", rej, err)
	}
	must("dev_client", devRoot, "", "describe", "-S", "-s", nextChange)
	// A rejected submit can leave the task without its previous shelf. The
	// normal close-out must be able to recreate it while keeping files open.
	if err := cache.DeleteShelf(t.Context(), ws, nextChange); err != nil {
		t.Fatal(err)
	}
	finished, err := cache.FinishChange(t.Context(), ws, nextChange, p4cache.FinishChangeOptions{RevertFiles: []string{"//" + ws.Client + "/..."}})
	if err != nil || !finished.Shelved || len(finished.Files) != 1 {
		t.Fatalf("close-out did not restore the shelf: %+v %v", finished, err)
	}
	must("dev_client", devRoot, "", "describe", "-S", "-s", nextChange)
	t.Run("preserve reverted shelf and merge new work", func(t *testing.T) {
		must("dev_client", devRoot, "", "revert", "-c", nextChange, "game.txt")
		oldContent := must("dev_client", devRoot, "", "print", "-q", "//test/Dev/game.txt@="+nextChange)
		result, err := cache.FinishChange(t.Context(), ws, nextChange, p4cache.FinishChangeOptions{RevertFiles: []string{"//" + ws.Client + "/..."}})
		if err != nil || !result.Shelved || result.Deleted || len(result.Files) != 1 {
			t.Fatalf("shelf-only CL was not preserved: %+v %v", result, err)
		}
		must("dev_client", devRoot, "", "edit", "-c", nextChange, "unchanged.txt")
		write(filepath.Join(devRoot, "unchanged.txt"), "new work\n")
		result, err = cache.FinishChange(t.Context(), ws, nextChange, p4cache.FinishChangeOptions{RevertFiles: []string{"//" + ws.Client + "/..."}})
		if err != nil || !result.Shelved || len(result.Files) != 2 {
			t.Fatalf("new work did not retain the old shelf file: %+v %v", result, err)
		}
		if got := must("dev_client", devRoot, "", "print", "-q", "//test/Dev/game.txt@="+nextChange); got != oldContent {
			t.Fatalf("shelf-only content changed: before=%q after=%q", oldContent, got)
		}
		b := &p4TaskBinding{ws: ws, ref: mainRef, change: nextChange, ticket: "123", issueKey: "MUL-7"}
		if _, rej, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "shelve"}, devRoot); rej != nil || err != nil {
			t.Fatalf("explicit shelve: %+v %v", rej, err)
		}
		if got := must("dev_client", devRoot, "", "print", "-q", "//test/Dev/game.txt@="+nextChange); got != oldContent {
			t.Fatalf("explicit shelve replaced shelf-only content: %q", got)
		}
		if err := cache.SetChangeDescription(t.Context(), ws, nextChange, p4depot.ChangeDescription("123", "fix", "MUL-7")); err != nil {
			t.Fatal(err)
		}
		if _, rej, err := d.runWorkspaceP4(t.Context(), b, p4MutateRequest{Action: "submit"}, devRoot); err != nil || rej == nil || rej.Code != p4CodeActionNotAllowed {
			t.Fatalf("submit failed to protect shelf-only work: %+v %v", rej, err)
		}
		if got := must("dev_client", devRoot, "", "print", "-q", "//test/Dev/game.txt@="+nextChange); got != oldContent {
			t.Fatalf("rejected submit lost shelf-only content: %q", got)
		}
	})
	t.Run("delete a genuinely empty CL", func(t *testing.T) {
		empty, err := cache.CreateTaskChange(t.Context(), ws, p4depot.ChangeDescription("123", "empty", "MUL-8"))
		if err != nil {
			t.Fatal(err)
		}
		result, err := cache.FinishChange(t.Context(), ws, empty, p4cache.FinishChangeOptions{RevertFiles: []string{"//" + ws.Client + "/..."}})
		if err != nil || !result.Deleted || result.Shelved {
			t.Fatalf("empty CL was not deleted: %+v %v", result, err)
		}
	})
	t.Run("retain pre-existing human opens during a continuation", func(t *testing.T) {
		oldShelf := must("dev_client", devRoot, "", "print", "-q", "//test/Dev/game.txt@="+nextChange)
		must("dev_client", devRoot, "", "edit", "-c", nextChange, "game.txt")
		write(filepath.Join(devRoot, "game.txt"), "human unshelved work\n")
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("{}"))
		}))
		defer api.Close()
		daemon := &Daemon{p4Cache: cache, p4ClientLocks: NewLocalPathLocker(), client: NewClient(api.URL), cfg: Config{WorkspacesRoot: filepath.Join(root, "multica")}}
		task := Task{ID: "continuation", WorkspaceID: "ws", IssueID: "issue", IssueIdentifier: "MUL-7", P4Ticket: "123", P4Depots: []P4DepotData{{Port: port, Depot: "//test/Dev", NoAutoReview: true}}}
		binding, err := daemon.bindP4Workspace(t.Context(), task, &localDirectoryAssignment{AbsPath: devRoot, RealPath: devRoot}, "codex")
		if err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(devRoot, "unchanged.txt"), "task continuation\n")
		if _, rej, err := daemon.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: "shelve"}, devRoot); err != nil || rej != nil {
			t.Fatalf("continuation shelve: %+v %v", rej, err)
		}
		if got := must("dev_client", devRoot, "", "print", "-q", "//test/Dev/game.txt@="+nextChange); got != oldShelf {
			t.Fatalf("explicit continuation shelve replaced the human's shelf: %q", got)
		}
		if err := daemon.finishP4Workspace(t.Context(), task, binding); err != nil {
			t.Fatal(err)
		}
		if got := must("dev_client", devRoot, "", "print", "-q", "//test/Dev/game.txt@="+nextChange); got != oldShelf {
			t.Fatalf("continuation replaced the human's pre-existing shelf: %q", got)
		}
		got, err := os.ReadFile(filepath.Join(devRoot, "game.txt"))
		if err != nil || !strings.Contains(string(got), "human unshelved work") {
			t.Fatalf("human open was changed: %q %v", got, err)
		}
		if got := must("dev_client", devRoot, "", "print", "-q", "//test/Dev/unchanged.txt@="+nextChange); !strings.Contains(got, "task continuation") {
			t.Fatalf("direct continuation edit was not shelved: %q", got)
		}
	})
	t.Run("verified alias and tracked native rules", func(t *testing.T) {
		_, portNumber, err := net.SplitHostPort(port)
		if err != nil {
			t.Fatal(err)
		}
		alias := mainRef
		alias.Port = "localhost:" + portNumber
		if _, _, err := d.matchP4Workspace(t.Context(), []P4DepotData{P4DepotData(alias)}, ws); err != nil {
			info, infoErr := cache.ServerInfo(t.Context(), alias)
			t.Fatalf("registered server identity did not recognize loopback alias: %v; workspace=%+v; info=%v (%v)", err, ws, info, infoErr)
		}
		for _, file := range []string{"AGENTS.md", "CLAUDE.md"} {
			write(filepath.Join(devRoot, file), "Repository-owned instructions\n")
			must("dev_client", devRoot, "", "add", file)
		}
		must("dev_client", devRoot, "", "submit", "-d", "repository rules")
		task := Task{ID: "rules", WorkspaceID: "ws", IssueID: "issue", IssueIdentifier: "MUL-RULES", P4Depots: []P4DepotData{P4DepotData(alias)}}
		daemon := &Daemon{p4Cache: cache, p4ClientLocks: NewLocalPathLocker()}
		binding, err := daemon.bindP4Workspace(t.Context(), task, &localDirectoryAssignment{AbsPath: devRoot, RealPath: devRoot}, "claude")
		if err != nil {
			t.Fatal(err)
		}
		defer binding.release()
		ctx := execenv.TaskContextForEnv{P4Workspace: binding.envSpec(), AgentSkills: []execenv.SkillContextForEnv{{Name: "task-skill", Content: "Private managed skill"}}}
		env, err := execenv.Prepare(execenv.PrepareParams{WorkspacesRoot: t.TempDir(), WorkspaceID: "ws", TaskID: "rules", Provider: "claude", LocalWorkDir: devRoot, Task: ctx}, slog.Default())
		if err != nil {
			t.Fatal(err)
		}
		defer env.Cleanup(true)
		ctx.PrivateRuntimeDir = env.PrivateRuntimeDir
		if _, err := execenv.InjectRuntimeConfig(devRoot, "claude", ctx); err != nil {
			t.Fatal(err)
		}
		if err := execenv.CleanupSidecars(env.RootDir); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{"AGENTS.md", "CLAUDE.md"} {
			got, err := os.ReadFile(filepath.Join(devRoot, file))
			if err != nil || strings.TrimSpace(string(got)) != "Repository-owned instructions" {
				t.Fatalf("native tracked rule changed: %s %q %v", file, got, err)
			}
		}
	})
	t.Run("import writes require their own depot grant", func(t *testing.T) {
		engineRef := p4depot.Ref{Port: port, Depot: "//test/Engine"}
		binding := &p4TaskBinding{ws: ws, ref: mainRef, ticket: "123", issueKey: "MUL-IMPORT"}
		edit := p4MutateRequest{Action: "edit", Files: []string{"engine/version.txt"}}
		if _, rej, err := d.runWorkspaceP4(t.Context(), binding, edit, devRoot); err != nil || rej == nil || rej.Code != p4CodeNotInWorkspace {
			t.Fatalf("mapping alone authorized the import: %+v %v", rej, err)
		}
		binding.depots = []p4depot.Ref{mainRef, engineRef}
		// P4 permits a local edit of an import, but warns that this stream
		// cannot submit it. Preserve that diagnostic instead of claiming the
		// mapping itself supplies submit permission.
		if result, rej, err := d.runWorkspaceP4(t.Context(), binding, edit, devRoot); err != nil || rej != nil || !strings.Contains(result.Output, "cannot submit") {
			t.Fatalf("readonly import diagnostic was lost: %+v %+v %v", result, rej, err)
		}
		must("dev_client", devRoot, "", "revert", "-c", binding.changelist(), "engine/version.txt")
		spec := must("dev_client", devRoot, "", "stream", "-o", "//test/Dev")
		if !strings.Contains(spec, "import engine/...") {
			t.Fatalf("readonly import missing from stream spec: %s", spec)
		}
		must("dev_client", devRoot, strings.Replace(spec, "import engine/...", "import+ engine/...", 1), "stream", "-i")
		if _, rej, err := d.runWorkspaceP4(t.Context(), binding, edit, devRoot); rej != nil || err != nil {
			t.Fatalf("authorized import+ edit failed: %+v %v", rej, err)
		}
		write(filepath.Join(devRoot, "engine", "version.txt"), "import task work\n")
		if _, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: "shelve"}, devRoot); rej != nil || err != nil {
			t.Fatalf("authorized import+ shelve failed: %+v %v", rej, err)
		}
		shelf := must("dev_client", devRoot, "", "print", "-q", "//test/Engine/version.txt@="+binding.changelist())
		if _, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: "submit"}, devRoot); err != nil || rej == nil || rej.Code != p4CodeActionNotAllowed {
			t.Fatalf("primary submit grant leaked to imported depot: %+v %v", rej, err)
		}
		if got := must("dev_client", devRoot, "", "print", "-q", "//test/Engine/version.txt@="+binding.changelist()); got != shelf {
			t.Fatalf("rejected import submit altered the shelf: %q", got)
		}
	})
	t.Logf("isolated P4: imported engine updated; shelved CL %s submitted as %s; unchanged file excluded", change, receipt.Change)
}
