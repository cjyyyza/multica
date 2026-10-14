//go:build p4integration

package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	b := &p4TaskBinding{ws: ws, ref: p4depot.Ref{AllowSubmit: true, AllowSync: true}, ticket: "123", issueKey: "MUL-7"}
	d := &Daemon{p4Cache: cache}
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
	must("dev_client", devRoot, "", "edit", "-c", change, "game.txt", "unchanged.txt")
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
	finished, err := cache.FinishChange(t.Context(), ws, nextChange)
	if err != nil || !finished.Shelved || len(finished.Files) != 1 {
		t.Fatalf("close-out did not restore the shelf: %+v %v", finished, err)
	}
	must("dev_client", devRoot, "", "describe", "-S", "-s", nextChange)
	t.Logf("isolated P4: imported engine updated; shelved CL %s submitted as %s; unchanged file excluded", change, receipt.Change)
}
