package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestYixiezuoPullAndPushUseLocalCLI(t *testing.T) {
	bin := writeYixiezuoFakeCLI(t)
	var pulled any
	var acked any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/yixiezuo"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"connection": map[string]any{
					"id":                  "conn-1",
					"cli_bin":             bin,
					"gcp_host":            "dj01.pm.netease.com",
					"list_query_id":       "9",
					"external_project_id": "7",
					"status_map":          map[string]string{},
				},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pull"):
			_ = json.NewDecoder(r.Body).Decode(&pulled)
			_ = json.NewEncoder(w).Encode(map[string]any{"created": 1, "updated": 0, "skipped": 0})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/export"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"connection": map[string]any{"id": "conn-1", "cli_bin": bin},
				"updates": []map[string]any{{
					"issue": map[string]any{
						"id":     "11111111-1111-1111-1111-111111111111",
						"title":  "看板改动",
						"status": "done",
					},
					"external_issue_id": "7",
					"status_name":       "已解决",
				}},
				"creates": []map[string]any{},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/push-ack"):
			_ = json.NewDecoder(r.Body).Decode(&acked)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_YIXIEZUO_CLI", bin)

	cmd := &cobra.Command{Use: "pull"}
	cmd.SetOut(&strings.Builder{})
	cmd.PersistentFlags().String("server-url", srv.URL, "")
	cmd.PersistentFlags().String("workspace-id", "ws-1", "")
	cmd.PersistentFlags().String("profile", "", "")
	if err := runYixiezuoPull(cmd, nil); err != nil {
		t.Fatalf("pull: %v", err)
	}
	body, _ := json.Marshal(pulled)
	if !strings.Contains(string(body), `"external_id":"7"`) {
		t.Fatalf("pull payload missing card: %s", body)
	}

	push := &cobra.Command{Use: "push"}
	push.SetOut(&strings.Builder{})
	push.PersistentFlags().String("server-url", srv.URL, "")
	push.PersistentFlags().String("workspace-id", "ws-1", "")
	push.PersistentFlags().String("profile", "", "")
	if err := runYixiezuoPush(push, nil); err != nil {
		t.Fatalf("push: %v", err)
	}
	ackBody, _ := json.Marshal(acked)
	if !strings.Contains(string(ackBody), `"external_issue_id":"7"`) {
		t.Fatalf("push ack missing: %s", ackBody)
	}
}

func writeYixiezuoFakeCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	program := `package main
import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func main() {
	tool, argsJSON := "", ""
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "toolName=") {
			tool = strings.TrimPrefix(arg, "toolName=")
		}
		if strings.HasPrefix(arg, "arguments=") {
			argsJSON = strings.TrimPrefix(arg, "arguments=")
		}
	}
	fabric := func(inner any) {
		raw, _ := json.Marshal(inner)
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"ok": true,
			"data": map[string]any{"data": map[string]any{"data": []any{map[string]any{"type": "text", "text": string(raw)}}}},
		})
	}
	switch tool {
	case "list_issues":
		if !strings.Contains(argsJSON, "kanban") || !strings.Contains(argsJSON, "query_id") {
			fmt.Fprintln(os.Stderr, "missing kanban query_id")
			os.Exit(1)
		}
		fabric(map[string]any{
			"res_code": 1,
			"data": map[string]any{
				"list": []any{map[string]any{
					"id": 7, "project_id": 7, "subject": "来自易协作", "status": "新建",
					"updated_on": "2026-01-01T00:00:00Z",
				}},
				"page": 1, "total_page": 1, "total_count": 1,
			},
		})
	case "listIssueStatuses":
		fabric(map[string]any{
			"res_code": 1,
			"data":     map[string]any{"list": []any{map[string]any{"id": 3, "name": "已解决"}}, "total_page": 1},
		})
	case "update_issue":
		fabric(map[string]any{
			"res_code": 1,
			"data": map[string]any{"base": map[string]any{
				"id": 7, "subject": "看板改动", "status": "已解决", "updated_on": "2026-01-04T00:00:00Z",
			}},
		})
	default:
		fmt.Fprintf(os.Stderr, "unexpected %v\n", os.Args)
		os.Exit(1)
	}
}
`
	if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
		t.Fatal(err)
	}
	name := "popo-cli"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake popo-cli: %v\n%s", err, out)
	}
	return bin
}

func TestYixiezuoRuntimeRefusesMissingQueryID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"connection": map[string]any{
				"id":      "conn-1",
				"cli_bin": "popo-cli",
			},
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_YIXIEZUO_QUERY_ID", "")
	cmd := &cobra.Command{Use: "pull"}
	cmd.SetOut(&strings.Builder{})
	cmd.PersistentFlags().String("server-url", srv.URL, "")
	cmd.PersistentFlags().String("workspace-id", "ws-1", "")
	cmd.PersistentFlags().String("profile", "", "")
	if err := runYixiezuoPull(cmd, nil); err == nil {
		t.Fatal("expected missing query_id to fail")
	}
}

func TestYixiezuoAPIPath(t *testing.T) {
	client := cli.NewAPIClient("http://example", "ws-9", "tok")
	if got := yixiezuoAPIPath(client, "/pull"); got != "/api/workspaces/ws-9/yixiezuo/pull" {
		t.Fatalf("path %q", got)
	}
}
