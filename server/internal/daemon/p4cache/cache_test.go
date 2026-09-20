package p4cache

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/p4depot"
)

func writeFakeP4(t *testing.T, dir string) string {
	t.Helper()
	source := filepath.Join(dir, "fake_p4.go")
	program := `package main
import("fmt";"io";"os";"path/filepath";"strings")
func main(){
 args:=os.Args[1:]; joined:=" "+strings.Join(args," ")+" "
 log:=os.Getenv("P4_FAKE_LOG"); f,err:=os.OpenFile(log,os.O_APPEND|os.O_CREATE|os.O_WRONLY,0600); if err!=nil{panic(err)}; fmt.Fprintln(f,strings.Join(args," ")); f.Close()
 specPath:=os.Getenv("P4_FAKE_SPEC")
 if strings.Contains(joined," info "){fmt.Println("User name: testuser");return}
 if strings.Contains(joined," client -i "){b,_:=io.ReadAll(os.Stdin);if err:=os.WriteFile(specPath,b,0600);err!=nil{panic(err)};return}
 if strings.Contains(joined," sync "){
   b,err:=os.ReadFile(specPath);if err!=nil{panic(err)};root:="";client:=""
   for _,line:=range strings.Split(string(b),"\n"){if strings.HasPrefix(line,"Root: "){root=strings.TrimSpace(strings.TrimPrefix(line,"Root: "))};if strings.HasPrefix(line,"Client: "){client=strings.TrimSpace(strings.TrimPrefix(line,"Client: "))}}
   if root==""{panic("missing root")}
   have:=specPath+"."+client+".have"
   if _,err:=os.Stat(have);err==nil&&!strings.Contains(joined," -f "){return}
   if err:=os.MkdirAll(root,0755);err!=nil{panic(err)}
   if err:=os.WriteFile(filepath.Join(root,".p4synced"),[]byte("synced"),0600);err!=nil{panic(err)}
   if err:=os.WriteFile(have,[]byte("have"),0600);err!=nil{panic(err)}
   return
 }
 if strings.Contains(joined," login -s"){fmt.Println("... TicketExpiration 3600");fmt.Println("... User alice");return}
 if strings.Contains(joined," tickets"){fmt.Println("p4:1666 (alice) TESTTICKET");return}
 if strings.Contains(joined," change -i"){fmt.Println("Change 1001 created.");return}
 if strings.Contains(joined," submit "){fmt.Println("Change 1001 submitted.");return}
 if strings.Contains(joined," edit ")||strings.Contains(joined," add ")||strings.Contains(joined," delete ")||strings.Contains(joined," revert ")||strings.Contains(joined," opened ")||strings.Contains(joined," reconcile ")||strings.Contains(joined," shelve ")||strings.Contains(joined," unshelve ")||strings.Contains(joined," describe ")||strings.Contains(joined," reopen ")||strings.Contains(joined," move "){
   fmt.Println("ok");return
 }
 panic("unexpected p4 command")
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

func TestSyncCreatesClientAndSyncs(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeP4(t, dir)
	logPath := filepath.Join(dir, "p4.log")
	specPath := filepath.Join(dir, "client.spec")
	work := filepath.Join(dir, "work")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_SPEC", specPath)
	t.Setenv("P4USER", "alice")

	cache := &Cache{P4Path: bin}
	result, err := cache.Sync(context.Background(), SyncParams{
		WorkspaceID: "ws-1",
		TaskID:      "task-1",
		WorkDir:     work,
		Ref:         p4depot.Ref{Port: "ssl:perforce.example.com:1666", Depot: "//depot/proj"},
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if filepath.Dir(result.Path) != filepath.Join(work, "p4") {
		t.Fatalf("Path = %q is outside the task P4 directory", result.Path)
	}
	if !strings.HasPrefix(result.Client, "mc") {
		t.Fatalf("Client = %q", result.Client)
	}

	logBody, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(logBody), "client -i") {
		t.Fatalf("expected client -i in %s", logBody)
	}
	if !strings.Contains(string(logBody), "sync //depot/proj/...") {
		t.Fatalf("expected sync in %s", logBody)
	}

	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	if !strings.Contains(string(spec), "Owner: alice") {
		t.Fatalf("spec missing owner: %s", spec)
	}
	if !strings.Contains(string(spec), "//depot/proj/...") {
		t.Fatalf("spec missing view: %s", spec)
	}
	if _, err := os.Stat(filepath.Join(result.Path, ".p4synced")); err != nil {
		t.Fatalf("sync marker: %v", err)
	}
}

func TestSyncUsesStreamSpec(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeP4(t, dir)
	specPath := filepath.Join(dir, "client.spec")
	t.Setenv("P4_FAKE_LOG", filepath.Join(dir, "p4.log"))
	t.Setenv("P4_FAKE_SPEC", specPath)
	t.Setenv("P4USER", "alice")

	cache := &Cache{P4Path: bin}
	if _, err := cache.Sync(context.Background(), SyncParams{
		WorkspaceID: "ws-1",
		TaskID:      "task-1",
		WorkDir:     filepath.Join(dir, "work"),
		Ref: p4depot.Ref{
			Port:   "perforce:1666",
			Depot:  "//streams",
			Stream: "//streams/main",
		},
	}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	if !strings.Contains(string(spec), "Stream: //streams/main") {
		t.Fatalf("spec missing stream: %s", spec)
	}
	if strings.Contains(string(spec), "View:") {
		t.Fatalf("stream spec should not set View: %s", spec)
	}
	log, err := os.ReadFile(filepath.Join(dir, "p4.log"))
	if err != nil || !strings.Contains(string(log), "sync //streams/main/...") {
		t.Fatalf("sync did not narrow the parent depot to the selected stream: %s %v", log, err)
	}
}

func TestFreshSyncRestoresFilesDespiteExistingHaveList(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("P4_FAKE_LOG", filepath.Join(dir, "p4.log"))
	t.Setenv("P4_FAKE_SPEC", filepath.Join(dir, "client.spec"))
	t.Setenv("P4USER", "alice")
	cache := &Cache{P4Path: writeFakeP4(t, dir)}
	params := SyncParams{WorkspaceID: "ws", TaskID: "task", WorkDir: filepath.Join(dir, "work"), Ref: p4depot.Ref{Port: "p4:1666", Depot: "//depot/UE"}}
	first, err := cache.Sync(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	params.Fresh = true
	second, err := cache.Sync(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if first.Path != second.Path {
		t.Fatal("fresh sync changed the task client")
	}
	if _, err := os.Stat(filepath.Join(second.Path, ".p4synced")); err != nil {
		t.Fatalf("fresh sync left an empty checkout: %v", err)
	}
}

func TestDifferentServersCannotShareCheckoutDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("P4_FAKE_LOG", filepath.Join(dir, "p4.log"))
	t.Setenv("P4_FAKE_SPEC", filepath.Join(dir, "client.spec"))
	t.Setenv("P4USER", "alice")
	cache := &Cache{P4Path: writeFakeP4(t, dir)}
	params := SyncParams{WorkspaceID: "ws", TaskID: "task", WorkDir: filepath.Join(dir, "work"), Ref: p4depot.Ref{Port: "first:1666", Depot: "//depot/UE"}}
	first, err := cache.Sync(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	params.Ref.Port = "second:1666"
	second, err := cache.Sync(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if first.Path == second.Path || first.Client == second.Client {
		t.Fatal("different server identities share a checkout")
	}
}

func TestSyncRejectsInvalidRef(t *testing.T) {
	cache := &Cache{P4Path: "/nonexistent/p4"}
	_, err := cache.Sync(context.Background(), SyncParams{
		WorkspaceID: "ws",
		TaskID:      "task",
		WorkDir:     t.TempDir(),
		Ref:         p4depot.Ref{Port: "not a port", Depot: "nope"},
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
}
