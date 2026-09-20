package daemon

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

func TestP4SwarmDestinationMustBeConfigured(t *testing.T) {
	for _, scope := range []string{"workspace", "task"} {
		for _, requested := range []string{"", "https://trusted.example", "https://untrusted.example"} {
			t.Run(scope+"/"+requested, func(t *testing.T) {
				d := &Daemon{workspaces: map[string]*workspaceState{"ws": {}}, logger: slog.Default()}
				configured := p4depot.Ref{Port: "p4:1666", Depot: "//depot/UE", SwarmURL: "https://trusted.example"}
				if scope == "task" {
					d.registerTaskP4Depots("ws", "task", []P4DepotData{P4DepotData(configured)})
				} else {
					d.workspaces["ws"].setP4Depots([]P4DepotData{P4DepotData(configured)})
				}
				ref := configured
				ref.SwarmURL = requested
				rec := httptest.NewRecorder()
				got, ok := d.allowP4Ref(rec, httptest.NewRequest(http.MethodPost, "/p4/swarm", nil), "ws", "task", ref)
				if requested == "https://untrusted.example" {
					if ok || rec.Code != http.StatusForbidden {
						t.Fatalf("unconfigured credential destination accepted: %+v, status %d", got, rec.Code)
					}
				} else if !ok || got.SwarmURL != configured.SwarmURL {
					t.Fatalf("configured destination lost: %+v, status %d", got, rec.Code)
				}
			})
		}
	}
}

func TestP4SwarmCannotSupplyURLForDepotWithoutSwarm(t *testing.T) {
	d := &Daemon{workspaces: map[string]*workspaceState{"ws": {}}, logger: slog.Default()}
	ref := p4depot.Ref{Port: "p4:1666", Depot: "//depot/UE"}
	d.registerTaskP4Depots("ws", "task", []P4DepotData{P4DepotData(ref)})
	ref.SwarmURL = "https://untrusted.example"
	rec := httptest.NewRecorder()
	if _, ok := d.allowP4Ref(rec, httptest.NewRequest(http.MethodPost, "/p4/swarm", nil), "ws", "task", ref); ok {
		t.Fatal("task supplied a credential destination that was never configured")
	}
}

func TestP4HandlersUseStableTaskRoot(t *testing.T) {
	dir := t.TempDir()
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "fake_p4.go")
	program := `package main
import("encoding/json";"io";"os")
func main(){io.Copy(io.Discard,os.Stdin);cwd,_:=os.Getwd();json.NewEncoder(os.Stdout).Encode(map[string]any{"args":os.Args[1:],"cwd":cwd})}`
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(filepath.Dir(source), "fake-p4")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, source).CombinedOutput(); err != nil {
		t.Fatalf("build fake p4: %v: %s", err, out)
	}
	d := &Daemon{workspaces: map[string]*workspaceState{"ws": {}}, logger: slog.Default(), p4Cache: &p4cache.Cache{P4Path: bin}}
	ref := p4depot.Ref{Port: "p4:1666", Depot: "//depot/UE", User: "alice"}
	d.registerTaskP4Depots("ws", "task", []P4DepotData{P4DepotData(ref)})
	d.registerActiveRepoCheckoutTask("mat_p4_test", activeRepoCheckoutTask{WorkspaceID: "ws", TaskID: "task", WorkDir: root})
	call := func(handler http.HandlerFunc, workdir, action string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"workspace_id": "ws", "task_id": "task", "workdir": workdir, "cwd": workdir, "port": ref.Port, "depot": ref.Depot, "action": action, "files": []string{"Foo.cpp"}})
		req := httptest.NewRequest(http.MethodPost, "/p4", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer mat_p4_test")
		rec := httptest.NewRecorder()
		handler(rec, req)
		return rec
	}
	checkout := p4cache.ClientRoot(root, "ws", "task", ref)
	for _, cwd := range []string{root, checkout, filepath.Join(checkout, "Source")} {
		if err := os.MkdirAll(cwd, 0755); err != nil {
			t.Fatal(err)
		}
		rec := call(d.p4SyncHandler(), cwd, "")
		var result p4cache.SyncResult
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &result) != nil || result.Path != checkout {
			t.Fatalf("sync from %s moved the checkout: %d %s", cwd, rec.Code, rec.Body.String())
		}
		if cwd == root {
			continue
		}
		rec = call(d.p4RunHandler(), cwd, "edit")
		var mutation p4cache.MutateResult
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &mutation) != nil || mutation.Path != checkout {
			t.Fatalf("edit from %s failed: %d %s", cwd, rec.Code, rec.Body.String())
		}
		var command struct{ Args []string }
		if err := json.Unmarshal([]byte(mutation.Output), &command); err != nil {
			t.Fatal(err)
		}
		if got := command.Args[len(command.Args)-1]; got != filepath.Join(cwd, "Foo.cpp") {
			t.Fatalf("relative path resolved to %s", got)
		}
	}
	for _, handler := range []http.HandlerFunc{d.p4SyncHandler(), d.p4RunHandler(), d.p4SwarmHandler()} {
		if rec := call(handler, t.TempDir(), "edit"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "workdir") {
			t.Fatalf("outside workdir accepted: %d %s", rec.Code, rec.Body.String())
		}
	}
}
