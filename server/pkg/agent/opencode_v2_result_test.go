package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

const nativeOpenCodeExport = `{"info":{"id":"ses_current"},"messages":[
{"id":"old","type":"assistant","finish":"stop","time":{"completed":1},"tokens":{"input":9000,"output":1000}},
{"id":"input","type":"user"},
{"id":"current","type":"assistant","finish":"stop","time":{"completed":2},"tokens":{"input":12,"output":5,"cache":{"read":7,"write":3}}},
{"id":"idle","type":"idle","outcome":"succeeded"}]}`

func TestOpenCodeV2NativeTerminalReadback(t *testing.T) {
	stream := func() eventResult {
		return eventResult{status: "failed", errMsg: "step still open", sessionID: "ses_current", messageIDs: map[string]struct{}{"current": {}}, noTerminalSignal: true}
	}
	got, err := opencodeV2ResultFromExport([]byte(nativeOpenCodeExport), stream())
	if err != nil || got.status != "completed" || got.noTerminalSignal || !got.sawTerminalSignal {
		t.Fatalf("valid native terminal: %+v, %v", got, err)
	}
	if got.usage != (TokenUsage{InputTokens: 12, OutputTokens: 5, CacheReadTokens: 7, CacheWriteTokens: 3}) {
		t.Fatalf("resumed history billed again: %+v", got.usage)
	}
	for _, tc := range []struct{ name, raw string }{
		{"invalid JSON", `{`},
		{"different session", strings.Replace(nativeOpenCodeExport, `ses_current`, `ses_other`, 1)},
		{"still running", strings.Replace(nativeOpenCodeExport, `"type":"idle"`, `"type":"system"`, 1)},
		{"failed", strings.Replace(nativeOpenCodeExport, `"outcome":"succeeded"`, `"outcome":"failed"`, 1)},
		{"cancelled", strings.Replace(nativeOpenCodeExport, `"outcome":"succeeded"`, `"outcome":"cancelled"`, 1)},
		{"unobserved later turn", strings.Replace(nativeOpenCodeExport, `"id":"current"`, `"id":"other"`, 1)},
		{"incomplete message", strings.Replace(nativeOpenCodeExport, `"completed":2`, `"completed":0`, 1)},
		{"pending tool continuation", strings.ReplaceAll(nativeOpenCodeExport, `"finish":"stop"`, `"finish":"tool-calls"`)},
		{"no usage", strings.Replace(nativeOpenCodeExport, `"input":12,"output":5,"cache":{"read":7,"write":3}`, `"input":0,"output":0`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := opencodeV2ResultFromExport([]byte(tc.raw), stream()); err == nil {
				t.Fatal("accepted incomplete or unrelated run")
			}
		})
	}
	missing := stream()
	missing.messageIDs["missing"] = struct{}{}
	if _, err := opencodeV2ResultFromExport([]byte(nativeOpenCodeExport), missing); err == nil {
		t.Fatal("accepted missing streamed message")
	}
}

func TestOpenCodeV2ExportOwnConnection(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, standalone := range []bool{false, true} {
		t.Run(fmt.Sprint(standalone), func(t *testing.T) {
			connection := opencodeRunConnection{cmd: NewCommand(bin, []string{"-test.run=^TestOpenCodeV2ExportHelper$", "--"}), server: "http://127.0.0.1:12345", standalone: standalone, dir: t.TempDir(), env: buildEnv(map[string]string{"MULTICA_TEST_EXPORT_HELPER": "1", "MULTICA_TEST_EXPORT_STANDALONE": fmt.Sprint(standalone)})}
			stream := eventResult{sessionID: "ses_current", messageIDs: map[string]struct{}{"current": {}}, noTerminalSignal: true}
			if _, err := opencodeV2ConfirmRun(context.Background(), connection, stream, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenCodeV2ExportHelper(t *testing.T) {
	if os.Getenv("MULTICA_TEST_EXPORT_HELPER") != "1" {
		return
	}
	args := strings.Join(os.Args, " ")
	if !strings.Contains(args, "session export ses_current") {
		os.Exit(2)
	}
	if os.Getenv("MULTICA_TEST_EXPORT_STANDALONE") == "true" {
		if !strings.Contains(args, "--standalone") || strings.Contains(args, "--server") {
			os.Exit(3)
		}
	} else if !strings.Contains(args, "--server http://127.0.0.1:12345") {
		os.Exit(4)
	}
	fmt.Fprint(os.Stdout, nativeOpenCodeExport)
	os.Exit(0)
}

func TestOpenCodeV2ExecuteUsesNativeTerminalOnlyAfterCleanExit(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, version, outcome string
		exitCode               int
		empty, completed       bool
	}{
		{"native success", "opencode v2.2.1", "succeeded", 0, false, true},
		{"native failure", "opencode v2.2.1", "failed", 0, false, false},
		{"empty native stream", "opencode v2.2.1", "succeeded", 0, true, false},
		{"process crashed", "opencode v2.2.1", "succeeded", 1, false, false},
		{"v1 keeps stream guard", "1.18.31", "succeeded", 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend, err := New("opencode", Config{ExecutablePath: bin, BuiltinRuntime: true, CLIVersion: tc.version, LaunchPrefix: []string{"-test.run=^TestOpenCodeV2ExecuteHelper$", "--"}, Env: map[string]string{"MULTICA_TEST_EXECUTE_HELPER": "1", "MULTICA_TEST_EXPORT_OUTCOME": tc.outcome, "MULTICA_TEST_EXIT_CODE": fmt.Sprint(tc.exitCode), "MULTICA_TEST_EMPTY_STREAM": fmt.Sprint(tc.empty)}})
			if err != nil {
				t.Fatal(err)
			}
			session, err := backend.Execute(t.Context(), "test prompt", ExecOptions{Cwd: t.TempDir(), Timeout: 10 * time.Second, CustomArgs: []string{"--standalone"}})
			if err != nil {
				t.Fatal(err)
			}
			go func() {
				for range session.Messages {
				}
			}()
			result := <-session.Result
			if (result.Status == "completed") != tc.completed {
				t.Fatalf("status=%s error=%s", result.Status, result.Error)
			}
			if tc.completed && result.Usage["unknown"].InputTokens != 12 {
				t.Fatalf("native usage missing: %+v", result.Usage)
			}
		})
	}
}

func TestOpenCodeV2ExecuteHelper(t *testing.T) {
	if os.Getenv("MULTICA_TEST_EXECUTE_HELPER") != "1" {
		return
	}
	args := strings.Join(os.Args, " ")
	if strings.Contains(args, "session export") {
		fmt.Fprint(os.Stdout, strings.Replace(nativeOpenCodeExport, `"outcome":"succeeded"`, `"outcome":"`+os.Getenv("MULTICA_TEST_EXPORT_OUTCOME")+`"`, 1))
		os.Exit(0)
	}
	if !strings.Contains(args, "run --format json") {
		os.Exit(3)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	if os.Getenv("MULTICA_TEST_EMPTY_STREAM") == "true" {
		os.Exit(0)
	}
	fmt.Fprintln(os.Stdout, `{"type":"step_start","sessionID":"ses_current","part":{"messageID":"current"}}`)
	fmt.Fprintln(os.Stdout, `{"type":"text","sessionID":"ses_current","part":{"messageID":"current","text":"Native result"}}`)
	var exitCode int
	_ = json.Unmarshal([]byte(os.Getenv("MULTICA_TEST_EXIT_CODE")), &exitCode)
	os.Exit(exitCode)
}
