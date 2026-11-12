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
