package main

import (
	"context"
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/spf13/cobra"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type yixiezuoDownloadTransport func(*http.Request) (*http.Response, error)

func (f yixiezuoDownloadTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestYixiezuoStagesAttachmentBytesAndRejectsMissingReceipt(t *testing.T) {
	for _, durable := range []bool{true, false} {
		t.Run(map[bool]string{true: "durable", false: "missing attachment row"}[durable], func(t *testing.T) {
			uploaded := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/upload-file" {
					t.Fatalf("unexpected path %s", r.URL.Path)
				}
				file, header, err := r.FormFile("file")
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				body, _ := io.ReadAll(file)
				if string(body) != "source log bytes" || header.Filename != "error.log" {
					t.Errorf("attachment changed: %q %s", body, header.Filename)
				}
				uploaded = true
				id := ""
				if durable {
					id = "11111111-1111-1111-1111-111111111111"
				}
				json.NewEncoder(w).Encode(map[string]string{"id": id, "url": "https://multica.test/api/files/local"})
			}))
			defer srv.Close()
			driver := yixiezuo.NewExecDriver(yixiezuo.ExecOptions{Bin: "fake", GCPHost: "dj01.pm.netease.com", LookPath: func(s string) (string, error) { return s, nil }, Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
				inner, _ := json.Marshal(map[string]any{"res_code": 1, "data": map[string]string{"url": "https://files.netease.com/log"}})
				return json.Marshal(map[string]any{"ok": true, "data": []any{map[string]string{"type": "text", "text": string(inner)}}})
			}})
			client := cli.NewAPIClient(srv.URL, "ws", "test")
			downloader := &http.Client{Transport: yixiezuoDownloadTransport(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Authorization") != "" {
					t.Fatal("Multica token leaked to source storage")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("source log bytes")), Header: http.Header{}}, nil
			})}
			snapshot := yixiezuo.Snapshot{Attachments: []yixiezuo.Attachment{{ID: "1", Name: "error.log", URL: "https://dj01.pm.netease.com/attachments/1/error.log"}}}
			err := mirrorYixiezuoAttachments(context.Background(), client, driver, &snapshot, downloader)
			if !uploaded || (err == nil) != durable {
				t.Fatalf("durable=%v upload=%v err=%v", durable, uploaded, err)
			}
			if durable && snapshot.Attachments[0].LocalID == "" {
				t.Fatal("missing staged attachment identity")
			}
		})
	}
}

func TestYixiezuoPreviewNeverCreatesIssues(t *testing.T) {
	bin := writeYixiezuoFakeCLI(t)
	t.Setenv("MULTICA_YIXIEZUO_CLI", bin)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().String("cli", bin, "")
	out := &strings.Builder{}
	cmd.SetOut(out)
	if err := runYixiezuoPreview(cmd, []string{"https://dj01.pm.netease.com/issues/7"}); err != nil {
		t.Fatal(err)
	}
	var snapshot yixiezuo.Snapshot
	if err := json.Unmarshal([]byte(out.String()), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Source.ID != "7" || snapshot.Title != "Imported issue" {
		t.Fatalf("snapshot %+v", snapshot)
	}
}
func TestYixiezuoBridgeReportsOnlyClaimedOperation(t *testing.T) {
	bin := writeYixiezuoFakeCLI(t)
	source, _ := yixiezuo.ParseSource("https://dj01.pm.netease.com/issues/7")
	receipts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/workspaces/ws/yixiezuo/bridge/claim":
			json.NewEncoder(w).Encode(map[string]any{"operation": yixiezuo.Operation{ID: "op", Kind: "preview", LeaseToken: "lease", Payload: yixiezuo.OperationPayload{Source: source}}})
		case "/api/workspaces/ws/yixiezuo/operations/op/complete":
			receipts++
			var done yixiezuo.Completion
			json.NewDecoder(r.Body).Decode(&done)
			if done.State != "succeeded" || done.LeaseToken != "lease" || done.Snapshot.Source.ID != "7" {
				t.Errorf("receipt %+v", done)
			}
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := cli.NewAPIClient(srv.URL, "ws", "test")
	processed, err := processYixiezuoRequest(context.Background(), client, yixiezuo.NewExecDriver(yixiezuo.ExecOptions{Bin: bin}))
	if err != nil || !processed || receipts != 1 {
		t.Fatalf("processed=%v receipts=%d err=%v", processed, receipts, err)
	}
}
func writeYixiezuoFakeCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	program := `package main
import("encoding/json";"fmt";"os";"strings")
func main(){tool:="";for _,arg:=range os.Args[1:]{if strings.HasPrefix(arg,"toolName="){tool=strings.TrimPrefix(arg,"toolName=")}}
var data any
switch tool{
case "get_issue_base":data=map[string]any{"base":map[string]any{"id":7,"subject":"Imported issue","status":"Ready"}}
case "getIssueForm":data=map[string]any{"issue":map[string]any{"id":7,"subject":"Imported issue","description":"Source repro","lock_version":3,"status_id":1}}
case "get_issue_journals","get_issue_attachments":data=map[string]any{"list":[]any{}}
case "getIssueFieldOptions":data=map[string]any{"core_fields":map[string]any{"status_id":map[string]any{"options":[]any{map[string]any{"id":1,"name":"Ready"}}}}}
default:fmt.Fprintln(os.Stderr,"unexpected tool",tool);os.Exit(1)
};inner,_:=json.Marshal(map[string]any{"res_code":1,"data":data});json.NewEncoder(os.Stdout).Encode(map[string]any{"ok":true,"data":[]any{map[string]any{"type":"text","text":string(inner)}}})}
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
