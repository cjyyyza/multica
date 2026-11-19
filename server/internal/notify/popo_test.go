package notify

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func popoResponse(payload any) []byte {
	result, _ := json.Marshal(map[string]any{"ok": true, "code": "FABRIC_OK", "data": map[string]any{"code": "FABRIC_OK", "data": map[string]any{"code": "FABRIC_OK", "downstream_http_status": 200, "data": map[string]any{"status": 1, "message": "成功", "data": payload}}}})
	return result
}

func popoLookup(uid, display string) any {
	return map[string]any{"search_results": map[string]any{"person": []any{map[string]any{"content": map[string]any{"uid": uid, "name": "俞坚", "showName": display}}}}}
}

func argValue(args []string, name string) string {
	for _, arg := range args {
		if value, ok := strings.CutPrefix(arg, name+"="); ok {
			return value
		}
	}
	return ""
}

func TestPopoSendsAsExactPartnerWithRealMentionsAndUTF8Files(t *testing.T) {
	target := Target{RobotName: "DeepSeek Harness", Receiver: "8493291", AtUIDs: []string{"yujian01@corp.netease.com"}}
	notification := Notification{Event: "question", Title: "确认方案", Message: "请确认：A 还是 B？\n保留 `code`、$() 和 \\n。", URL: "https://multica.example/team/issues/MUL-1"}
	var calls []string
	var messagePath, atPath string
	popo := Popo{Bin: filepath.Join(t.TempDir(), "fake-popo"), Run: func(ctx context.Context, bin string, args []string) ([]byte, error) {
		if !strings.HasSuffix(bin, "fake-popo") || args[0] != "popo" {
			t.Fatal("wrong CLI")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("CLI invocation lacks a deadline")
		}
		calls = append(calls, args[1])
		switch args[1] {
		case "userinfo_search":
			if argValue(args, "query") != target.AtUIDs[0] {
				t.Fatal("mention lookup was not exact")
			}
			return popoResponse(popoLookup(target.AtUIDs[0], "鲣鱼(俞坚)")), nil
		case "team_info_query":
			return popoResponse(map[string]any{"tid": "8493291", "members": []any{map[string]any{"name": "DeepSeek Harness"}, map[string]any{"name": "鲣鱼(俞坚)"}}}), nil
		case "use_robot_send_msg":
			if argValue(args, "robotName") != target.RobotName || argValue(args, "receiver") != target.Receiver || argValue(args, "isAtAll") != "false" || argValue(args, "msgType") != "text" {
				t.Fatalf("wrong send routing: %v", args)
			}
			messagePath = strings.TrimPrefix(argValue(args, "message"), "@text:")
			atPath = strings.TrimPrefix(argValue(args, "atUids"), "@file:")
			message, err := os.ReadFile(messagePath)
			if err != nil {
				t.Fatal(err)
			}
			text, _ := notification.Text()
			if !utf8.Valid(message) || string(message) != text+"\n@鲣鱼(俞坚)" || strings.HasPrefix(string(message), "\ufeff") {
				t.Fatalf("message was changed: %q", message)
			}
			atJSON, err := os.ReadFile(atPath)
			var uids []string
			if err != nil || json.Unmarshal(atJSON, &uids) != nil || !reflect.DeepEqual(uids, target.AtUIDs) {
				t.Fatalf("real mentions missing: %s %v", atJSON, err)
			}
			return popoResponse(map[string]any{"success": true, "msgId": "8493291-test"}), nil
		default:
			t.Fatalf("unexpected command %s", args[1])
			return nil, nil
		}
	}}
	receipt, err := popo.Send(context.Background(), target, notification)
	if err != nil || receipt.MessageID != "8493291-test" || !reflect.DeepEqual(receipt.AtUIDs, target.AtUIDs) {
		t.Fatalf("receipt: %+v %v", receipt, err)
	}
	if !reflect.DeepEqual(calls, []string{"userinfo_search", "team_info_query", "use_robot_send_msg"}) {
		t.Fatalf("unexpected calls: %v", calls)
	}
	for _, path := range []string{messagePath, atPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("temporary file remains: %s", path)
		}
	}
}

func TestPopoRejectsUnresolvedMentionsBeforeSending(t *testing.T) {
	for _, roster := range []any{
		map[string]any{"tid": "8493291", "members": []any{map[string]any{"name": "Someone else"}}},
		map[string]any{"tid": "another-group", "members": []any{map[string]any{"name": "鲣鱼(俞坚)"}}},
	} {
		popo := Popo{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
			switch args[1] {
			case "userinfo_search":
				return popoResponse(popoLookup("yujian01@corp.netease.com", "鲣鱼(俞坚)")), nil
			case "team_info_query":
				return popoResponse(roster), nil
			default:
				t.Fatalf("sent or invited without valid group membership: %s", args[1])
				return nil, nil
			}
		}}
		_, err := popo.Send(context.Background(), Target{RobotName: "DeepSeek Harness", Receiver: "8493291", AtUIDs: []string{"yujian01@corp.netease.com"}}, Notification{Message: "Question"})
		var uncertain *SendError
		if err == nil || errors.As(err, &uncertain) && uncertain.Unknown {
			t.Fatalf("preflight must be retryable after fixing routing: %v", err)
		}
	}
}

func TestPopoRequiresConfirmedDownstreamReceipt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     []byte
		runErr  error
		unknown bool
	}{
		{"response lost", nil, errors.New("timeout"), true},
		{"malformed", []byte("not JSON"), nil, true},
		{"malformed status", []byte(`{"ok":true,"data":{"status":"invalid"}}`), nil, true},
		{"outer success only", []byte(`{"ok":true,"code":"FABRIC_OK","data":{}}`), nil, true},
		{"missing receipt", popoResponse(map[string]any{"success": true}), nil, true},
		{"missing success", popoResponse(map[string]any{"msgId": "invented"}), nil, true},
		{"business refusal", popoResponse(map[string]any{"success": false}), nil, false},
		{"CLI rejection", []byte(`{"ok":false,"code":"FABRIC_ERROR"}`), nil, true},
		{"downstream rejection", []byte(`{"ok":true,"data":{"status":0,"message":"robot not found"}}`), nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var messagePath string
			popo := Popo{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
				if args[1] != "use_robot_send_msg" {
					t.Fatal("private sends should not search or change sender")
				}
				messagePath = strings.TrimPrefix(argValue(args, "message"), "@text:")
				return tc.raw, tc.runErr
			}}
			_, err := popo.Send(context.Background(), Target{RobotName: "DeepSeek Harness", Receiver: "yujian01@corp.netease.com"}, Notification{Message: "测试"})
			var sendErr *SendError
			if !errors.As(err, &sendErr) || sendErr.Unknown != tc.unknown {
				t.Fatalf("wrong failure classification: %v", err)
			}
			if _, err := os.Stat(messagePath); !os.IsNotExist(err) {
				t.Fatal("temporary file not cleaned after failure")
			}
		})
	}
}

func TestNotificationValidationDoesNotInvokeCLI(t *testing.T) {
	popo := Popo{Run: func(context.Context, string, []string) ([]byte, error) {
		t.Fatal("invalid notification invoked CLI")
		return nil, nil
	}}
	for _, notification := range []Notification{{}, {Message: "\xff"}, {Message: "body", Event: "question\nnotice"}, {Message: "body", URL: "file:///tmp/result"}, {Message: strings.Repeat("中", 3001)}} {
		if _, err := popo.Send(context.Background(), Target{RobotName: "DeepSeek Harness", Receiver: "8493291"}, notification); err == nil {
			t.Fatal("invalid message accepted")
		}
	}
}

func TestPopoSendP2PUsesDirectAccountCommand(t *testing.T) {
	const receiver = "user@corp.netease.com"
	var messagePath string
	popo := Popo{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[0] != "popo" || args[1] != "message_send_p2p" {
			t.Fatalf("unexpected command: %v", args)
		}
		if argValue(args, "receiver") != receiver || argValue(args, "source") != "AGENT" {
			t.Fatalf("unexpected P2P route: %v", args)
		}
		if argValue(args, "robotName") != "" || argValue(args, "msgType") != "" {
			t.Fatalf("P2P send must not use robot routing or an explicit msgType: %v", args)
		}
		messagePath = strings.TrimPrefix(argValue(args, "message"), "@text:")
		message, err := os.ReadFile(messagePath)
		if err != nil {
			t.Fatal(err)
		}
		if string(message) != "Your Multica verification code is 123456." {
			t.Fatalf("message = %q", message)
		}
		return popoResponse(map[string]any{"success": true, "msgId": "p2p-message-1"}), nil
	}}
	receipt, err := popo.SendP2P(context.Background(), receiver, "Your Multica verification code is 123456.")
	if err != nil || receipt.MessageID != "p2p-message-1" || receipt.Receiver != receiver {
		t.Fatalf("receipt = %+v, err = %v", receipt, err)
	}
	if _, err := os.Stat(messagePath); !os.IsNotExist(err) {
		t.Fatal("temporary P2P message file remains")
	}
}

func TestPopoSendP2PAcceptsSubmissionWithoutMessageID(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{name: "empty submission acknowledgement", raw: []byte(`{"ok":true,"data":{"code":"FABRIC_OK","data":{"code":"FABRIC_OK","data":{"status":1,"message":"success","data":{}}}}}`)},
		{name: "null payload", raw: popoResponse(nil)},
		{name: "explicit success", raw: popoResponse(map[string]any{"success": true})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Popo{Run: func(context.Context, string, []string) ([]byte, error) {
				return tc.raw, nil
			}}
			receipt, err := p.SendP2P(t.Context(), "user@corp.netease.com", "123456")
			if err != nil || receipt.Receiver != "user@corp.netease.com" || receipt.MessageID != "" {
				t.Fatalf("receipt = %+v, err = %v", receipt, err)
			}
		})
	}
}

func TestPopoSendP2PRejectsFailedOrUnconfirmedSubmission(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     []byte
		runErr  error
		unknown bool
	}{
		{name: "response lost", runErr: errors.New("timeout"), unknown: true},
		{name: "missing POPO status", raw: []byte(`{"ok":true,"data":{}}`), unknown: true},
		{name: "malformed POPO payload", raw: popoResponse([]bool{true}), unknown: true},
		{name: "business refusal", raw: popoResponse(map[string]any{"success": false}), unknown: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			popo := Popo{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
				if args[1] != "message_send_p2p" {
					t.Fatalf("unexpected command: %v", args)
				}
				return tc.raw, tc.runErr
			}}
			_, err := popo.SendP2P(context.Background(), "user@corp.netease.com", "123456")
			var sendErr *SendError
			if !errors.As(err, &sendErr) || sendErr.Unknown != tc.unknown {
				t.Fatalf("wrong failure classification: %v", err)
			}
		})
	}
}

func TestPopoSendP2PRejectsInvalidReceiverWithoutCLI(t *testing.T) {
	popo := Popo{Run: func(context.Context, string, []string) ([]byte, error) {
		t.Fatal("invalid receiver invoked the CLI")
		return nil, nil
	}}
	if _, err := popo.SendP2P(context.Background(), "8493291", "123456"); err == nil {
		t.Fatal("group ID accepted as a P2P account")
	}
}

func TestResolvePopoWrapperScript(t *testing.T) {
	t.Run("default npm layout", func(t *testing.T) {
		dir := t.TempDir()
		wrapper := filepath.Join(dir, "popo-cli.cmd")
		script := filepath.Join(dir, "node_modules", "@popo", "cli", "bin", "popo.js")
		if err := os.MkdirAll(filepath.Dir(script), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(script, []byte("console.log('ok')"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(wrapper, []byte("@echo off\n"), 0600); err != nil {
			t.Fatal(err)
		}
		got, ok := resolvePopoWrapperScript(wrapper)
		if !ok || got != script {
			t.Fatalf("resolvePopoWrapperScript = (%q, %v), want (%q, true)", got, ok, script)
		}
	})

	t.Run("pnpm cmd shim", func(t *testing.T) {
		dir := t.TempDir()
		wrapper := filepath.Join(dir, "popo-cli.cmd")
		script := filepath.Join(dir, "global", "5", "node_modules", "@popo", "cli", "bin", "popo.mjs")
		if err := os.MkdirAll(filepath.Dir(script), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(script, []byte("console.log('ok')"), 0600); err != nil {
			t.Fatal(err)
		}
		shim := "@SETLOCAL\r\n\"%_prog%\"  \"%dp0%\\global\\5\\node_modules\\@popo\\cli\\bin\\popo.mjs\" %*\r\n"
		if err := os.WriteFile(wrapper, []byte(shim), 0600); err != nil {
			t.Fatal(err)
		}
		got, ok := resolvePopoWrapperScript(wrapper)
		if !ok || got != script {
			t.Fatalf("resolvePopoWrapperScript = (%q, %v), want (%q, true)", got, ok, script)
		}
	})

	t.Run("ps1 shim", func(t *testing.T) {
		dir := t.TempDir()
		wrapper := filepath.Join(dir, "popo-cli.ps1")
		script := filepath.Join(dir, "custom", "bin", "popo.js")
		if err := os.MkdirAll(filepath.Dir(script), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(script, []byte("console.log('ok')"), 0600); err != nil {
			t.Fatal(err)
		}
		shim := "& \"node$exe\"  \"$basedir/custom/bin/popo.js\" $args\n"
		if err := os.WriteFile(wrapper, []byte(shim), 0600); err != nil {
			t.Fatal(err)
		}
		got, ok := resolvePopoWrapperScript(wrapper)
		if !ok || got != script {
			t.Fatalf("resolvePopoWrapperScript = (%q, %v), want (%q, true)", got, ok, script)
		}
	})

	t.Run("shim entry takes precedence over stale default layout", func(t *testing.T) {
		dir := t.TempDir()
		wrapper := filepath.Join(dir, "popo-cli.cmd")
		staleDefault := filepath.Join(dir, "node_modules", "@popo", "cli", "bin", "popo.js")
		activeScript := filepath.Join(dir, "global", "5", "node_modules", "@popo", "cli", "bin", "popo.mjs")
		for _, script := range []string{staleDefault, activeScript} {
			if err := os.MkdirAll(filepath.Dir(script), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(script, []byte("console.log('ok')"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		shim := "@SETLOCAL\r\n\"%_prog%\"  \"%dp0%\\global\\5\\node_modules\\@popo\\cli\\bin\\popo.mjs\" %*\r\n"
		if err := os.WriteFile(wrapper, []byte(shim), 0600); err != nil {
			t.Fatal(err)
		}
		got, ok := resolvePopoWrapperScript(wrapper)
		if !ok || got != activeScript {
			t.Fatalf("resolvePopoWrapperScript = (%q, %v), want (%q, true)", got, ok, activeScript)
		}
	})

	t.Run("unknown wrapper fails", func(t *testing.T) {
		dir := t.TempDir()
		wrapper := filepath.Join(dir, "popo-cli.cmd")
		if err := os.WriteFile(wrapper, []byte("@echo off\necho missing\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if got, ok := resolvePopoWrapperScript(wrapper); ok {
			t.Fatalf("unexpected resolved wrapper: %q", got)
		}
	})
}

func TestPopoMatchesMentionAccountsCaseInsensitively(t *testing.T) {
	target := Target{RobotName: "DeepSeek Harness", Receiver: "8493291", AtUIDs: []string{"YuJian01@corp.netease.com"}}
	var sentAtUIDs []string
	popo := Popo{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		switch args[1] {
		case "userinfo_search":
			return popoResponse(popoLookup("yujian01@corp.netease.com", "鲣鱼(俞坚)")), nil
		case "team_info_query":
			return popoResponse(map[string]any{"tid": "8493291", "members": []any{map[string]any{"email": "YUJIAN01@CORP.NETEASE.COM"}}}), nil
		case "use_robot_send_msg":
			atPath := strings.TrimPrefix(argValue(args, "atUids"), "@file:")
			raw, err := os.ReadFile(atPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &sentAtUIDs); err != nil {
				t.Fatal(err)
			}
			return popoResponse(map[string]any{"success": true, "msgId": "case-msg"}), nil
		default:
			t.Fatalf("unexpected command %s", args[1])
			return nil, nil
		}
	}}
	receipt, err := popo.Send(context.Background(), target, Notification{Message: "hello"})
	if err != nil || receipt.MessageID != "case-msg" {
		t.Fatalf("case-insensitive mention send: receipt=%+v err=%v", receipt, err)
	}
	wantUIDs := []string{"yujian01@corp.netease.com"}
	if !reflect.DeepEqual(sentAtUIDs, wantUIDs) || !reflect.DeepEqual(receipt.AtUIDs, wantUIDs) {
		t.Fatalf("canonical atUids = sent:%v receipt:%v, want %v", sentAtUIDs, receipt.AtUIDs, wantUIDs)
	}
}

func TestRunPopoNonZeroExitDiagnosticsAndRefusalClassification(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("explicit downstream refusal on non-zero exit stays retryable", func(t *testing.T) {
		t.Setenv("MULTICA_TEST_POPO_HELPER", "refusal")
		_, err := runPopo(context.Background(), exe, []string{"-test.run=^TestPopoHelperProcess$"})
		var sendErr *SendError
		if !errors.As(err, &sendErr) || sendErr.Unknown {
			t.Fatalf("err = %v, want non-unknown SendError", err)
		}
		if !strings.Contains(err.Error(), "robot not found") {
			t.Fatalf("err = %q, want downstream refusal message", err.Error())
		}
	})
	t.Run("stderr diagnostics preserved while unknown send stays unknown", func(t *testing.T) {
		t.Setenv("MULTICA_TEST_POPO_HELPER", "stderr")
		_, err := runPopo(context.Background(), exe, []string{"-test.run=^TestPopoHelperProcess$"})
		if err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Fatalf("err = %v, want stderr diagnostic", err)
		}
		var sendErr *SendError
		if errors.As(err, &sendErr) {
			t.Fatalf("raw stderr failure must stay unclassified until Send marks Unknown: %v", err)
		}
	})
	t.Run("non-JSON stdout does not swallow stderr diagnostics", func(t *testing.T) {
		t.Setenv("MULTICA_TEST_POPO_HELPER", "stdout-noise-and-stderr")
		_, err := runPopo(context.Background(), exe, []string{"-test.run=^TestPopoHelperProcess$"})
		if err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Fatalf("err = %v, want stderr diagnostic preserved", err)
		}
		var sendErr *SendError
		if errors.As(err, &sendErr) {
			t.Fatalf("non-JSON stdout + stderr failure must stay unclassified: %v", err)
		}
	})
	t.Run("JSON envelope failure preserves both envelope reason and stderr", func(t *testing.T) {
		t.Setenv("MULTICA_TEST_POPO_HELPER", "json-error-and-stderr")
		_, err := runPopo(context.Background(), exe, []string{"-test.run=^TestPopoHelperProcess$"})
		if err == nil || !strings.Contains(err.Error(), "without a confirmed POPO outcome") || !strings.Contains(err.Error(), "token expired") {
			t.Fatalf("err = %v, want both envelope error and stderr diagnostic", err)
		}
		var sendErr *SendError
		if errors.As(err, &sendErr) {
			t.Fatalf("unconfirmed envelope failure must stay unclassified: %v", err)
		}
	})
	t.Run("non-JSON stdout diagnostic used when stderr is empty", func(t *testing.T) {
		t.Setenv("MULTICA_TEST_POPO_HELPER", "stdout-only-error")
		_, err := runPopo(context.Background(), exe, []string{"-test.run=^TestPopoHelperProcess$"})
		if err == nil || !strings.Contains(err.Error(), "please run popo login") {
			t.Fatalf("err = %v, want sanitized stdout diagnostic", err)
		}
		var sendErr *SendError
		if errors.As(err, &sendErr) {
			t.Fatalf("stdout-only failure must stay unclassified: %v", err)
		}
	})
}

func TestPopoHelperProcess(t *testing.T) {
	mode := os.Getenv("MULTICA_TEST_POPO_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "refusal":
		_, _ = os.Stdout.WriteString(`{"ok":true,"data":{"status":0,"message":"robot not found"}}`)
		os.Exit(1)
	case "stderr":
		_, _ = os.Stderr.WriteString("Error: not logged in\nrun popo login first\n")
		os.Exit(1)
	case "stdout-noise-and-stderr":
		_, _ = os.Stdout.WriteString("Checking popo profile...\n")
		_, _ = os.Stderr.WriteString("Error: not logged in\n")
		os.Exit(1)
	case "json-error-and-stderr":
		_, _ = os.Stdout.WriteString(`{"ok":false,"code":"FABRIC_ERROR"}`)
		_, _ = os.Stderr.WriteString("token expired\n")
		os.Exit(1)
	case "stdout-only-error":
		_, _ = os.Stdout.WriteString("Error: please run popo login\n")
		os.Exit(1)
	}
}
