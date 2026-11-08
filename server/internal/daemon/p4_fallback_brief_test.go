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
	capture := filepath.Join(buildDir, "brief.json")
	source := filepath.Join(buildDir, "agent.go")
	program := fmt.Sprintf(`package main
import("bufio";"fmt";"os";"encoding/json";"strings")
func texts(v any)string { switch x:=v.(type){case string:return x; case []any:var out []string;for _,v:=range x{out=append(out,texts(v))};return strings.Join(out,"\n");case map[string]any:var out []string;for _,v:=range x{out=append(out,texts(v))};return strings.Join(out,"\n")};return "" }
func main(){
	for _, arg := range os.Args[1:] { if arg == "--version" { fmt.Println("2.1.100"); return } }
	provider:=os.Getenv("P4_BRIEF_TEST_PROVIDER")
	config:="CLAUDE.md"
	if provider=="dsh" { config="AGENTS.md";fmt.Println("{\"v\":1,\"type\":\"ready\",\"runtime\":\"dsh\"}") }
	raw,_:=os.ReadFile(config)
	scan:=bufio.NewScanner(os.Stdin);scan.Buffer(make([]byte,4096),4<<20);scan.Scan()
	var message any;json.Unmarshal(scan.Bytes(),&message)
	raw=append(raw,[]byte(texts(message))...)
	_,configSet:=os.LookupEnv("P4CONFIG")
	capture,_:=json.Marshal(map[string]any{"task_id":os.Getenv("MULTICA_TASK_ID"),"brief":string(raw),"p4_client":os.Getenv("P4CLIENT"),"p4_config_set":configSet})
	if err:=os.WriteFile(%q,capture,0600);err!=nil{panic(err)}
	if provider=="dsh" { fmt.Println("{\"v\":1,\"type\":\"result\",\"status\":\"completed\",\"session_id\":\"p4-brief-test\",\"output\":\"done\"}");return }
	fmt.Println("{\"type\":\"system\",\"session_id\":\"p4-brief-test\"}")
	fmt.Println("{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"p4-brief-test\",\"result\":\"done\"}")
}`, capture)
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
	for _, provider := range []string{"claude", "dsh"} {
		for _, mode := range []string{"projectless", "non-p4-directory", "p4-directory", "p4-no-ticket", "explicit-unbound"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				bound := strings.HasPrefix(mode, "p4-")
				api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{}`) }))
				defer api.Close()
				d := &Daemon{
					client: NewClient(api.URL), logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
					workspaces: map[string]*workspaceState{}, activeEnvRoots: map[string]int{}, agentVersions: map[string]string{},
					runtimeIndex: map[string]Runtime{"runtime": {ID: "runtime", Provider: provider}}, p4Cache: &p4cache.Cache{P4Path: p4bin},
					cfg: Config{DaemonID: "daemon", WorkspacesRoot: t.TempDir(), AgentTimeout: 30 * time.Second, ServerBaseURL: api.URL, Agents: map[string]AgentEntry{provider: {Path: bin}}},
				}
				task := Task{ID: "brief-first", WorkspaceID: "workspace", RuntimeID: "runtime", IssueID: "issue", AuthToken: "mat_brief_test",
					AgentID: "agent", Agent: &AgentData{ID: "agent", Name: "agent", CustomEnv: map[string]string{"P4_BRIEF_TEST_PROVIDER": provider}},
					P4Ticket: "123", P4Depots: []P4DepotData{{Port: "p4:1666", Depot: "//depot/main"}}}
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
					if err != nil || result.Status != "completed" {
						t.Fatalf("turn %d result=%+v err=%v", turn, result, err)
					}
					raw, err := os.ReadFile(capture)
					if err != nil {
						t.Fatal(err)
					}
					var captured struct {
						TaskID      string `json:"task_id"`
						Brief       string `json:"brief"`
						P4Client    string `json:"p4_client"`
						P4ConfigSet bool   `json:"p4_config_set"`
					}
					if err := json.Unmarshal(raw, &captured); err != nil {
						t.Fatal(err)
					}
					if captured.TaskID != task.ID {
						t.Fatalf("captured task=%q want=%q", captured.TaskID, task.ID)
					}
					if bound {
						for _, want := range []string{"client `client`", "root `" + result.WorkDir + "`", "stream `//depot/main`", "installed native `p4` CLI directly"} {
							if !strings.Contains(captured.Brief, want) {
								t.Errorf("bound brief missing %q", want)
							}
						}
						if captured.P4Client != "client" || captured.P4ConfigSet {
							t.Fatalf("native P4 env client=%q configSet=%v", captured.P4Client, captured.P4ConfigSet)
						}
					} else if captured.P4Client != "" {
						t.Fatalf("unbound task inherited client %q", captured.P4Client)
					}
					if mode == "explicit-unbound" {
						if !strings.Contains(captured.Brief, "\n## Perforce depots\n") {
							t.Error("explicit resource lost setup guidance")
						}
					} else if !bound && (strings.Contains(captured.Brief, "\n## Perforce depots\n") || strings.Contains(captured.Brief, "\n## Perforce workspace\n")) {
						t.Error("ordinary task retained inherited P4 guidance")
					}
					task.ID = "brief-second"
					task.PriorWorkDir, task.PriorSessionID = result.WorkDir, result.SessionID
				}
			})
		}
	}
}
