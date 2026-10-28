package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
)

func writeP4ReviewProgram(t *testing.T, program string) string {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "main.go")
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "fake-p4"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(root, name)
	if out, err := exec.Command("go", "build", "-o", bin, source).CombinedOutput(); err != nil {
		t.Fatalf("build fake P4: %v\n%s", err, out)
	}
	return bin
}

func TestP4SlowWorkspaceDetectionKeepsPrepareLease(t *testing.T) {
	bin := writeP4ReviewProgram(t, `package main
import("fmt";"os";"time")
func main() {
    args := os.Args[1:]
    for len(args) > 0 {
        switch args[0] {
        case "-p", "-u", "-c", "-C": args = args[2:]
        case "-ztag": args = args[1:]
        default: goto command
        }
    }
command:
        switch args[0] {
        case "info":
            if os.Getenv("P4_LEASE_STAGE") == "detect" { time.Sleep(3*time.Second); return }
            fmt.Printf("... userName alice\n... clientName client\n... clientRoot %s\n", os.Getenv("P4_LEASE_ROOT")); return
        case "client":
            fmt.Printf("... Client client\n... Owner alice\n... Root %s\n... Stream //depot/main\n", os.Getenv("P4_LEASE_ROOT")); return
        case "fstat": time.Sleep(3*time.Second); return
        }
    fmt.Print("P4PORT=p4:1666\n")
}
`)
	for _, stage := range []string{"detect", "read-only sidecar"} {
		t.Run(stage, func(t *testing.T) {
			t.Setenv("P4_LEASE_STAGE", stage)
			var leases atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/prepare-lease") {
					leases.Add(1)
				}
				_, _ = w.Write([]byte("{}"))
			}))
			defer api.Close()
			root := t.TempDir()
			t.Setenv("P4_LEASE_ROOT", root)
			raw, _ := json.Marshal(localDirectoryRef{LocalPath: root, DaemonID: "daemon", ExecutionMode: localDirectoryModeInPlace})
			task := Task{
				ID: "slow-detect", IssueID: "issue", WorkspaceID: "ws", RuntimeID: "runtime",
				AgentID: "agent", Agent: &AgentData{ID: "agent"},
				P4Depots:         []P4DepotData{{Port: "p4:1666", Depot: "//depot/main"}},
				ProjectResources: []ProjectResourceData{{ResourceType: "local_directory", ResourceRef: raw}},
			}
			if stage == "read-only sidecar" {
				task.ChatSessionID, task.IssueID = "chat", ""
			}
			d := &Daemon{
				cfg: Config{DaemonID: "daemon"}, logger: slog.Default(), client: NewClient(api.URL),
				p4Cache: &p4cache.Cache{P4Path: bin}, p4ClientLocks: NewLocalPathLocker(),
				prepareLeaseRefresh: 10 * time.Millisecond,
			}
			ctx, cancel := context.WithTimeout(t.Context(), 600*time.Millisecond)
			defer cancel()
			if _, err := d.runTask(ctx, task, "codex", 0, slog.Default()); err == nil {
				t.Fatal("expected cancelled slow P4 detection")
			}
			if ctx.Err() != context.DeadlineExceeded {
				t.Fatal("P4 probe returned before the slow command was cancelled")
			}
			if leases.Load() == 0 {
				t.Fatal("slow P4 detection did not refresh its task prepare lease")
			}
			finished := leases.Load()
			time.Sleep(30 * time.Millisecond)
			if got := leases.Load(); got != finished {
				t.Fatalf("prepare lease kept refreshing after binding returned: %d -> %d", finished, got)
			}
		})
	}
}

func TestP4RegisteredEdgePollKeepsCanonicalIdentity(t *testing.T) {
	bin := writeP4ReviewProgram(t, `package main
import("encoding/json";"fmt";"os";"strings")
func main() {
    args, port := os.Args[1:], ""
    for len(args) > 0 {
        switch args[0] {
        case "-p": port = args[1]; args = args[2:]
        case "-u", "-c", "-C": args = args[2:]
        case "-ztag": args = args[1:]
        default: goto command
        }
    }
command:
    if log := os.Getenv("P4_REVIEW_LOG"); log != "" {
        f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
        if err != nil { panic(err) }
        fmt.Fprintln(f, port, strings.Join(args, " ")); f.Close()
    }
    switch args[0] {
    case "info":
        var infos map[string]string
        if err := json.Unmarshal([]byte(os.Getenv("P4_REVIEW_INFOS")), &infos); err != nil { panic(err) }
        info, ok := infos[port]; if !ok { panic("unconfigured endpoint: " + port) }; fmt.Print(info)
    case "server": fmt.Print(os.Getenv("P4_REVIEW_SERVER"))
    case "describe":
        if err := os.Getenv("P4_REVIEW_DESCRIBE_ERR"); err != "" { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
        fmt.Print(os.Getenv("P4_REVIEW_DESCRIBE"))
    default: panic("unexpected command: " + strings.Join(args, " "))
    }
}
`)
	const edgeInfo = "... ServerID edge-1\n... serverAddress edge:1666\n... serverServices edge-server\n... replica ssl:commit:1666\n"
	const commitInfo = "... ServerID central\n... serverAddress commit:1666\n... serverServices commit-server\n"
	const registry = "... ServerID edge-1\n... Services edge-server\n... Address ssl:edge:1666\n"
	const canonical = "server:central@tcp:commit:1666"
	depots := []P4DepotData{
		{Port: "ssl:commit:1666", Depot: "//depot/main", User: "alice"},
		{Port: "ssl:edge:1666", Depot: "//depot/main", User: "alice"},
	}
	setInfos := func(edge, commit string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"ssl:edge:1666": edge, "ssl:commit:1666": commit})
		t.Setenv("P4_REVIEW_INFOS", string(raw))
	}
	setInfos(edgeInfo, commitInfo)
	t.Setenv("P4_REVIEW_SERVER", registry)
	root := t.TempDir()
	ws := &p4cache.Workspace{
		Port: "ssl:edge:1666", User: "alice", Client: "client", Dir: root, Root: root,
		ServerCluster: "server:edge-1@tcp:edge:1666", ServerID: "edge-1",
		Replica: "ssl:commit:1666", Edge: true, Stream: "//depot/main",
	}
	d := &Daemon{p4Cache: &p4cache.Cache{P4Path: bin}}
	if _, _, err := d.matchP4Workspace(t.Context(), depots, ws); err != nil {
		t.Fatal(err)
	}
	if p4ServerKey(ws) != canonical {
		t.Fatalf("binding did not establish the canonical identity: %q", p4ServerKey(ws))
	}
	for _, tc := range []struct {
		name, describe, describeErr, registry, edgeInfo, commitInfo string
		wantStatus                                                  string
		withoutUpstream, manual                                     bool
	}{
		{name: "submitted", describe: "... change 1002\n... oldChange 1001\n... status submitted\n... time 1700000000\n", wantStatus: "submitted"},
		{name: "pending", describe: "... change 1001\n... status pending\n", wantStatus: "shelved"},
		{name: "deleted on original edge", describeErr: "1001 - no such changelist.", wantStatus: "deleted"},
		{name: "unregistered edge", registry: "... ServerID edge-1\n... Services edge-server\n... Address ssl:another:1666\n"},
		{name: "wrong upstream identity", commitInfo: "... ServerID different\n... serverAddress commit:1666\n"},
		{name: "unconfigured upstream", withoutUpstream: true},
		{name: "TLS downgrade", edgeInfo: "... ServerID edge-1\n... serverAddress edge:1666\n... serverServices edge-server\n... replica commit:1666\n"},
		{name: "manual row has no verified edge association", manual: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setInfos(firstNonEmpty(tc.edgeInfo, edgeInfo), firstNonEmpty(tc.commitInfo, commitInfo))
			t.Setenv("P4_REVIEW_SERVER", firstNonEmpty(tc.registry, registry))
			t.Setenv("P4_REVIEW_DESCRIBE", tc.describe)
			t.Setenv("P4_REVIEW_DESCRIBE_ERR", tc.describeErr)
			log := filepath.Join(t.TempDir(), "p4.log")
			t.Setenv("P4_REVIEW_LOG", log)
			row := p4OpenChangelist{ID: "cl", Port: ws.Port, ServerKey: canonical, Client: ws.Client, DaemonID: "owner", Number: 1001, OriginalNumber: 1001, Status: "shelved"}
			if tc.manual {
				row.Client, row.DaemonID = "", ""
			}
			configured := depots
			if tc.withoutUpstream {
				configured = depots[1:]
			}
			claims, reports := 0, 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/open"):
					_ = json.NewEncoder(w).Encode(p4OpenChangelistsResponse{LeaseRequired: true, Changelists: []p4OpenChangelist{row}, P4Depots: configured})
				case strings.HasSuffix(r.URL.Path, "/claim"):
					claims++
					_ = json.NewEncoder(w).Encode(map[string]int{"poll_generation": claims})
				case strings.HasSuffix(r.URL.Path, "/status"):
					reports++
					var body p4StatusUpdateRequest
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if len(body.Updates) != 1 {
						t.Errorf("updates = %+v", body.Updates)
					} else if update := body.Updates[0]; update.Status != tc.wantStatus ||
						(update.ServerKey != canonical && !(tc.wantStatus == "deleted" && update.ServerKey == "")) || update.PollGeneration != 2 {
						t.Errorf("unexpected canonical edge update: %+v", update)
					}
					_ = json.NewEncoder(w).Encode(p4StatusUpdateResponse{Updated: 1})
				default:
					t.Errorf("unexpected API path %s", r.URL.Path)
				}
			}))
			defer api.Close()
			d.client = NewClient(api.URL)
			d.workspaces = map[string]*workspaceState{"ws": {}}
			d.pollP4ChangelistStatuses(t.Context())
			if (reports == 1) != (tc.wantStatus != "") {
				t.Fatalf("status reports = %d, expected status %q", reports, tc.wantStatus)
			}
			content, _ := os.ReadFile(log)
			if tc.withoutUpstream && strings.Contains(string(content), "ssl:commit:1666") {
				t.Fatalf("contacted an unconfigured upstream: %s", content)
			}
		})
	}
}
