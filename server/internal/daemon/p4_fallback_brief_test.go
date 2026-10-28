package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
)

func TestRunTaskP4FallbackBriefMatchesBinding(t *testing.T) {
	buildDir := t.TempDir()
	capture := filepath.Join(buildDir, "brief.md")
	source := filepath.Join(buildDir, "agent.go")
	program := fmt.Sprintf(`package main
import("bufio";"fmt";"os";"encoding/json";"strings";"net/http";"io")
func texts(v any)string { switch x:=v.(type){case string:return x; case []any:var out []string;for _,v:=range x{out=append(out,texts(v))};return strings.Join(out,"\n");case map[string]any:var out []string;for _,v:=range x{out=append(out,texts(v))};return strings.Join(out,"\n")};return "" }
func main(){
    for _, arg := range os.Args[1:] { if arg == "--version" { fmt.Println("2.1.100"); return } }
    provider:=os.Getenv("P4_BRIEF_TEST_PROVIDER")
    config:="CLAUDE.md"
    if provider=="dsh" {
        config="AGENTS.md"
        fmt.Println("{\"v\":1,\"type\":\"ready\",\"runtime\":\"dsh\"}")
    }
    raw,_:=os.ReadFile(config)
    scan:=bufio.NewScanner(os.Stdin);scan.Buffer(make([]byte,4096),4<<20);scan.Scan()
    var message any;json.Unmarshal(scan.Bytes(),&message)
    raw=append(raw,[]byte(texts(message))...)
    req,err:=http.NewRequest(http.MethodGet,os.Getenv("P4_BRIEF_TEST_STATUS_URL"),nil)
    if err!=nil{panic(err)}
    req.Header.Set("Authorization","Bearer "+os.Getenv("MULTICA_TOKEN"))
    resp,err:=http.DefaultClient.Do(req)
    if err!=nil{panic(err)}
    defer resp.Body.Close()
    status,err:=io.ReadAll(resp.Body)
    if err!=nil{panic(err)}
    capture,err:=json.Marshal(map[string]any{"task_id":os.Getenv("MULTICA_TASK_ID"),"brief":string(raw),"status_code":resp.StatusCode,"status":json.RawMessage(status)})
    if err!=nil{panic(err)}
    if err:=os.WriteFile(%q,capture,0600);err!=nil{panic(err)}
    if provider=="dsh" {
        fmt.Println("{\"v\":1,\"type\":\"result\",\"status\":\"completed\",\"session_id\":\"p4-brief-test\",\"output\":\"done\"}")
        return
    }
    fmt.Println("{\"type\":\"system\",\"session_id\":\"p4-brief-test\"}")
    fmt.Println("{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"p4-brief-test\",\"result\":\"done\"}")
}
`, capture)
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(buildDir, "fake-agent")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, source).CombinedOutput(); err != nil {
		t.Fatalf("build fake agent: %v\n%s", err, out)
	}
	p4bin := writeDaemonFakeP4(t)
	t.Setenv("P4USER", "alice")
	t.Setenv("P4_FAKE_SET", "P4PORT=p4:1666\nP4USER=alice\n")
	t.Setenv("P4_FAKE_FSTAT_ERR", "no such file(s).")
	for _, provider := range []string{"claude", "dsh"} {
		for _, mode := range []string{"projectless", "non-p4-directory", "p4-directory", "p4-sync-enabled", "p4-no-ticket", "explicit-unbound"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				bound := strings.HasPrefix(mode, "p4-")
				api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{}`) }))
				defer api.Close()
				d := &Daemon{
					client: NewClient(api.URL), logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
					workspaces: map[string]*workspaceState{}, activeEnvRoots: map[string]int{},
					agentVersions: map[string]string{},
					runtimeIndex:  map[string]Runtime{"runtime": {ID: "runtime", Provider: provider}},
					p4Cache:       &p4cache.Cache{P4Path: p4bin}, p4ClientLocks: NewLocalPathLocker(),
					cfg: Config{DaemonID: "daemon", WorkspacesRoot: t.TempDir(), AgentTimeout: 30 * time.Second, ServerBaseURL: api.URL,
						Agents: map[string]AgentEntry{provider: {Path: bin}}},
				}
				statusAPI := httptest.NewServer(d.p4StatusHandler())
				defer statusAPI.Close()
				task := Task{ID: "brief-first", WorkspaceID: "workspace", RuntimeID: "runtime", IssueID: "issue",
					AuthToken: "mat_brief_test",
					AgentID:   "agent", Agent: &AgentData{ID: "agent", Name: "agent", CustomEnv: map[string]string{
						"P4_BRIEF_TEST_PROVIDER":   provider,
						"P4_BRIEF_TEST_STATUS_URL": statusAPI.URL + "/p4/status",
					}},
					P4Ticket: "123",
					P4Depots: []P4DepotData{{Port: "p4:1666", Depot: "//depot/main", AllowSync: mode == "p4-sync-enabled"}},
				}
				if mode == "p4-no-ticket" {
					task.P4Ticket = ""
				}
				if mode == "explicit-unbound" {
					task.IsLeaderTask = true
					task.ProjectResources = []ProjectResourceData{{ResourceType: "perforce_depot"}}
				} else if mode != "projectless" {
					root := t.TempDir()
					raw, _ := json.Marshal(localDirectoryRef{LocalPath: root, DaemonID: "daemon", ExecutionMode: localDirectoryModeInPlace})
					task.ProjectID = "project"
					task.ProjectResources = []ProjectResourceData{{ResourceType: "local_directory", ResourceRef: raw}}
					if bound {
						t.Setenv("P4_FAKE_INFO", "... userName alice\n... clientName client\n... clientRoot "+root+"\n")
						t.Setenv("P4_FAKE_CLIENT", "... Client client\n... Owner alice\n... Root "+root+"\n... Stream //depot/main\n")
					} else {
						t.Setenv("P4_FAKE_INFO", "... userName alice\n... clientName unknown\n")
					}
				}
				for turn := 0; turn < 2; turn++ {
					result, err := d.runTask(t.Context(), task, provider, 0, d.logger)
					if err != nil {
						t.Fatalf("turn %d runTask: %v", turn, err)
					}
					if result.Status != "completed" {
						t.Fatalf("turn %d task status = %s: %s", turn, result.Status, result.Comment)
					}
					raw, err := os.ReadFile(capture)
					if err != nil {
						t.Fatal(err)
					}
					var captured struct {
						TaskID     string           `json:"task_id"`
						Brief      string           `json:"brief"`
						StatusCode int              `json:"status_code"`
						Status     p4StatusResponse `json:"status"`
					}
					if err := json.Unmarshal(raw, &captured); err != nil {
						t.Fatalf("turn %d capture: %v", turn, err)
					}
					if captured.TaskID != task.ID {
						t.Fatalf("turn %d captured another task's brief: %q", turn, captured.TaskID)
					}
					brief := captured.Brief
					if bound {
						if captured.StatusCode != http.StatusOK || captured.Status.Client != "client" || captured.Status.Root != result.WorkDir || captured.Status.Stream != "//depot/main" {
							t.Fatalf("turn %d workspace status: code=%d status=%+v", turn, captured.StatusCode, captured.Status)
						}
						for _, want := range []string{"client `" + captured.Status.Client + "`", "root `" + captured.Status.Root + "`", "stream `" + captured.Status.Stream + "`"} {
							if !strings.Contains(brief, want) {
								t.Errorf("turn %d brief disagrees with workspace status: missing %q", turn, want)
							}
						}
						if strings.Contains(brief, "not bound to an existing Helix workspace") {
							t.Errorf("turn %d bound task received unbound workspace instructions", turn)
						}
						allowsSync := strings.Contains(brief, "This daemon allows `multica p4 sync`")
						if allowsSync != (mode == "p4-sync-enabled") {
							t.Errorf("turn %d brief sync permission = %v", turn, allowsSync)
						}
					} else if captured.StatusCode != http.StatusNotFound {
						t.Fatalf("turn %d unbound workspace status = %d", turn, captured.StatusCode)
					}
					if mode == "explicit-unbound" {
						if !strings.Contains(brief, "\n## Perforce depots\n") {
							t.Errorf("turn %d explicit resource lost its workspace setup guidance", turn)
						}
					} else if bound {
						if !strings.Contains(brief, "\n## Perforce workspace\n") || strings.Contains(brief, "\n## Perforce depots\n") {
							t.Errorf("turn %d lost the bound workspace instructions", turn)
						}
					} else {
						if strings.Contains(brief, "\n## Perforce depots\n") || strings.Contains(brief, "\n## Perforce workspace\n") || strings.Contains(brief, "Ask a person to attach an `in_place`") {
							t.Errorf("turn %d ordinary task retained inherited P4 setup instructions", turn)
						}
						if !strings.Contains(brief, "Git commits use the user's configured identity") {
							t.Errorf("turn %d inherited depot hid ordinary Git guidance", turn)
						}
					}
					task.ID = "brief-second"
					task.PriorWorkDir, task.PriorSessionID = result.WorkDir, result.SessionID
				}
			})
		}
	}
}
