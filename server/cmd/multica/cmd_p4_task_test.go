package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestP4OpenedFormatPrintsDepotAndChange(t *testing.T) {
	payload := `{"action":"opened","client":"alice_DEV","path":"E:\\DJ","changelist":"1001","output":"//depot/A.cpp#edit\n//depot/B.cpp#add","files":[{"depot_file":"//depot/A.cpp","client_file":"E:\\DJ\\A.cpp","action":"edit","change":"1001","type":"text"},{"depot_file":"//depot/B.cpp","action":"add","change":"1001","type":"text"}]}`
	for _, args := range [][]string{
		{"-F", "%depotFile% %change%", "opened"},
		{"opened", "-F", "%depotFile% %change%"},
		{"opened", "--format", "%depotFile% %change%"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			body := runP4OpenedAgainst(t, args, payload)
			if body["action"] != "opened" {
				t.Fatalf("daemon action = %v", body["action"])
			}
			if _, ok := body["format"]; ok {
				t.Fatalf("format was forwarded to the daemon: %+v", body)
			}
		})
	}
}

func TestP4OpenedFormatDoesNotDumpJSONAndRejectsRawFlags(t *testing.T) {
	out, err := runP4Command(t, []string{"opened", "-F", "%depotFile% %change%"}, `{"action":"opened","client":"alice_DEV","output":"","changelist":""}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "" || strings.Contains(out, "{") {
		t.Fatalf("empty -F output = %q", out)
	}

	out, err = runP4Command(t, []string{"opened", "--output", "text"}, `{"action":"opened","client":"alice_DEV","path":"E:\\DJ","output":""}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "{") || strings.Contains(out, "action") {
		t.Fatalf("--output text dumped the JSON envelope: %q", out)
	}

	_, err = runP4Command(t, []string{"opened", "-a"}, `{"action":"opened"}`)
	if err == nil || !strings.Contains(err.Error(), "does not pass options through") {
		t.Fatalf("raw p4 flag error = %v", err)
	}
	_, err = runP4Command(t, []string{"edit", "-F", "%depotFile%", "--", "A.cpp"}, `{"action":"edit"}`)
	if err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("edit -F error = %v", err)
	}
}

func TestP4StatusFormatUsesTaskChange(t *testing.T) {
	out, err := runP4Command(t, []string{"-F", "%depotFile% %change%", "status"}, `{"client":"alice_DEV","change":"1001","files":[{"depot_file":"//depot/A.cpp","action":"edit","type":"text"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "//depot/A.cpp 1001\n" {
		t.Fatalf("stdout = %q", out)
	}
}

func TestSplitP4ReadCLIArgsPreservesNativeFlags(t *testing.T) {
	output, rest, err := splitP4ReadCLIArgs([]string{"--offset", "100", "--size=50", "-du3", "--stream", "Foo.cpp"})
	if err != nil {
		t.Fatal(err)
	}
	if output != "text" {
		t.Fatalf("output = %q", output)
	}
	if got := strings.Join(rest, " "); got != "--offset 100 --size=50 -du3 --stream Foo.cpp" {
		t.Fatalf("p4 args = %q", got)
	}
}

func TestFormatP4Opened(t *testing.T) {
	files := []p4OpenedLine{{
		DepotFile:  "//depot/A.cpp",
		ClientFile: `E:\DJ\A.cpp`,
		Action:     "edit",
		Change:     "1001",
		Type:       "text",
		Client:     "alice_DEV",
	}, {
		DepotFile: "//depot/B.cpp",
		Action:    "add",
		Change:    "default",
		Client:    "alice_DEV",
	}}
	got, err := formatP4Opened("%depotFile% %change%", files)
	if err != nil {
		t.Fatal(err)
	}
	if got != "//depot/A.cpp 1001\n//depot/B.cpp default" {
		t.Fatalf("format = %q", got)
	}
	got, err = formatP4Opened("%%change%%", files[:1])
	if err != nil || got != "%change%" {
		t.Fatalf("literal percent = %q, %v", got, err)
	}
	if _, err := formatP4Opened("%rev%", files); err == nil || !strings.Contains(err.Error(), "rev") {
		t.Fatalf("unknown field = %v", err)
	}
	if _, err := formatP4Opened("%depotFile", files); err == nil {
		t.Fatal("unmatched percent accepted")
	}
	// Older daemons only return depotFile#action lines. The task changelist still fills %change%.
	got, err = formatP4OpenedPayload("%depotFile% %change%", []byte(`{"client":"alice_DEV","changelist":"1001","output":"//depot/A.cpp#edit"}`), "", "", "")
	if err != nil || got != "//depot/A.cpp 1001" {
		t.Fatalf("legacy output = %q, %v", got, err)
	}
}

func runP4OpenedAgainst(t *testing.T, args []string, payload string) map[string]any {
	t.Helper()
	var body map[string]any
	out, err := runP4Command(t, args, payload, func(r *http.Request) {
		if r.URL.Path != "/p4/run" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "//depot/A.cpp 1001\n//depot/B.cpp 1001\n" {
		t.Fatalf("stdout = %q", out)
	}
	return body
}

func runP4Command(t *testing.T, args []string, payload string, inspect ...func(*http.Request)) (string, error) {
	t.Helper()
	resetP4Flags()
	t.Cleanup(resetP4Flags)
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, fn := range inspect {
			fn(r)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(daemon.Close)
	t.Setenv("MULTICA_DAEMON_PORT", strings.TrimPrefix(daemon.URL, "http://127.0.0.1:"))
	t.Setenv("MULTICA_TOKEN", "mat_test_p4")
	t.Setenv("MULTICA_TASK_ID", "task")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws")
	rootCmd.SetArgs(append([]string{"p4"}, args...))
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})
	return captureStdout(t, rootCmd.Execute)
}

func TestP4NativeArgvMapsOntoGuardedFields(t *testing.T) {
	cases := []struct {
		name string
		args []string
		path string
		want map[string]any
	}{
		{"add files", []string{"add", "New.cpp"}, "/p4/run", map[string]any{"action": "add"}},
		{"add-files", []string{"add-files", "New.cpp"}, "/p4/run", map[string]any{"action": "add"}},
		{"edit -c", []string{"edit", "-c", "123", "--", "A.cpp"}, "/p4/run", map[string]any{"action": "edit", "changelist": "123"}},
		{"describe number", []string{"describe", "12345"}, "/p4/run", map[string]any{"action": "describe", "changelist": "12345"}},
		{"describe -c", []string{"describe", "-c", "12345"}, "/p4/run", map[string]any{"action": "describe", "changelist": "12345"}},
		{"filelog", []string{"filelog", "Foo.gbd"}, "/p4/run", map[string]any{"action": "filelog"}},
		{"fstat flags", []string{"fstat", "-Ol", "--output", "text", "--", "Foo.cpp"}, "/p4/run", map[string]any{"action": "fstat"}},
		{"changes max", []string{"changes", "-m", "5", "-s", "submitted"}, "/p4/run", map[string]any{"action": "changes"}},
		{"changelists alias", []string{"changelists", "--stream"}, "/p4/run", map[string]any{"action": "changes"}},
		{"dirs", []string{"dirs", "//DJ01_Proj/Dev/Source/*"}, "/p4/run", map[string]any{"action": "dirs"}},
		{"grep", []string{"grep", "-e", "needle", "//DJ01_Proj/Dev/Source/..."}, "/p4/run", map[string]any{"action": "grep"}},
		{"integrated", []string{"integrated", "//DJ01_Proj/Dev/Source/..."}, "/p4/run", map[string]any{"action": "integrated"}},
		{"interchanges", []string{"interchanges", "//DJ01_Proj/Dev/Source/...", "//DJ01_Proj/Dev/Target/..."}, "/p4/run", map[string]any{"action": "interchanges"}},
		{"resolved", []string{"resolved", "-o", "Foo.cpp"}, "/p4/run", map[string]any{"action": "resolved"}},
		{"resolve", []string{"resolve", "-am", "Foo.cpp"}, "/p4/run", map[string]any{"action": "resolve"}},
		{"filelog max", []string{"filelog", "-m", "5", "--long", "--", "Foo.gbd"}, "/p4/run", map[string]any{"action": "filelog", "max": float64(5), "long": true}},
		{"revert -a", []string{"revert", "-a", "--", "A.cpp"}, "/p4/run", map[string]any{"action": "revert", "revert_unchanged": true}},
		{"reconcile preview", []string{"reconcile", "-n", "A.cpp"}, "/p4/run", map[string]any{"action": "reconcile", "preview": true}},
		{"shelve -f", []string{"shelve", "-f"}, "/p4/run", map[string]any{"action": "shelve", "force": true}},
		{"move dest", []string{"move", "A.cpp", "B.cpp"}, "/p4/run", map[string]any{"action": "move", "dest": "B.cpp"}},
		{"rename alias", []string{"rename", "A.cpp", "B.cpp"}, "/p4/run", map[string]any{"action": "move", "dest": "B.cpp"}},
		{"changelist alias", []string{"changelist", "--description", "Fix"}, "/p4/run", map[string]any{"action": "change"}},
		{"sync file", []string{"sync", "A.cpp"}, "/p4/sync", map[string]any{"files": []any{"A.cpp"}}},
		{"update alias", []string{"update", "A.cpp"}, "/p4/sync", map[string]any{"files": []any{"A.cpp"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			_, err := runP4Command(t, tc.args, `{"action":"ok","output":"history","changelist":"12345"}`, func(r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("path = %s, want %s", r.URL.Path, tc.path)
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			for key, want := range tc.want {
				if fmt.Sprint(body[key]) != fmt.Sprint(want) {
					t.Errorf("%s = %#v, want %#v", key, body[key], want)
				}
			}
			if tc.name == "filelog" {
				if _, ok := body["changelist"]; ok {
					t.Errorf("filelog forwarded a changelist: %#v", body)
				}
				if _, ok := body["max"]; ok {
					t.Errorf("default max was forwarded: %#v", body)
				}
			}
			if tc.name == "fstat flags" {
				args, _ := body["args"].([]any)
				if len(args) != 2 || args[0] != "-Ol" || args[1] != "Foo.cpp" {
					t.Fatalf("fstat args = %#v", body["args"])
				}
				if _, ok := body["output"]; ok {
					t.Errorf("--output was forwarded: %#v", body)
				}
			}
			if tc.name == "changes max" {
				args, _ := body["args"].([]any)
				if len(args) != 4 || args[0] != "-m" || args[1] != "5" || args[2] != "-s" || args[3] != "submitted" {
					t.Fatalf("changes args = %#v", body["args"])
				}
			}
			if tc.name == "add files" {
				files, _ := body["files"].([]any)
				if len(files) != 1 || files[0] != "New.cpp" {
					t.Fatalf("files = %#v", body["files"])
				}
			}
		})
	}

	_, err := runP4Command(t, []string{"opened", "-a"}, `{"action":"opened"}`)
	if err == nil || !strings.Contains(err.Error(), "does not pass options through") {
		t.Fatalf("opened -a error = %v", err)
	}
	_, err = runP4Command(t, []string{"sync", "-c", "9"}, `{"path":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("sync -c error = %v", err)
	}
	_, err = runP4Command(t, []string{"filelog", "-m", "0", "A.cpp"}, `{"action":"filelog"}`)
	if err == nil || !strings.Contains(err.Error(), "--max") {
		t.Fatalf("filelog max error = %v", err)
	}
	_, err = runP4Command(t, []string{"describe", "1", "-c", "2"}, `{"action":"describe"}`)
	if err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("describe both error = %v", err)
	}
}

func TestP4SwarmReportsEffectiveURLAndLinkFailure(t *testing.T) {
	for _, tc := range []struct {
		name, action string
		status       int
	}{
		{"link", "link", http.StatusOK},
		{"create", "create", http.StatusOK},
		{"show", "show", http.StatusOK},
		{"link failed", "link", http.StatusBadRequest},
		{"create succeeded but link failed", "create", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetP4Flags()
			t.Cleanup(resetP4Flags)
			var reported map[string]any
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/issues/MUL-7/swarm-reviews" {
					t.Errorf("unexpected API path: %s", r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&reported); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":"link rejected"}`))
			}))
			defer api.Close()
			daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/p4/swarm" {
					t.Errorf("unexpected daemon path: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"action":"show","swarm_url":"https://configured.example/swarm","review":{"id":77,"url":"https://configured.example/swarm/reviews/77","description":"Fix issue","changes":[1001]}}`))
			}))
			defer daemon.Close()
			t.Setenv("MULTICA_DAEMON_PORT", strings.TrimPrefix(daemon.URL, "http://127.0.0.1:"))
			t.Setenv("MULTICA_TOKEN", "mat_test_p4")
			t.Setenv("MULTICA_TASK_ID", "task")
			t.Setenv("MULTICA_ISSUE_ID", "MUL-7")
			t.Setenv("MULTICA_WORKSPACE_ID", "ws")
			t.Setenv("MULTICA_SERVER_URL", api.URL)
			p4Port, p4DepotPath = "p4:1666", "//depot/UE"
			cmd := &cobra.Command{}
			cmd.Flags().String("output", "json", "")
			err := runP4Swarm(tc.action)(cmd, []string{"77"})
			if tc.status == http.StatusOK && err != nil {
				t.Fatalf("command failed: %v", err)
			}
			if tc.status != http.StatusOK && (err == nil || !strings.Contains(err.Error(), "77")) {
				t.Fatalf("must report failed linking and retain review identity: %v", err)
			}
			if reported["swarm_url"] != "https://configured.example/swarm" || reported["changelist"] != "1001" {
				t.Fatalf("wrong link payload: %+v", reported)
			}
		})
	}
}

func TestOptionalP4PortDepotForwardsDepotWithoutPort(t *testing.T) {
	resetP4Flags()
	t.Cleanup(resetP4Flags)
	p4DepotPath = "//depot/UE"
	ref, err := optionalP4PortDepot()
	if err != nil {
		t.Fatal(err)
	}
	if ref.Port != "" || ref.Depot != "//depot/UE" {
		t.Fatalf("partial ref = %+v, want depot forwarded and port empty", ref)
	}
	p4Port = "not a port"
	if _, err := optionalP4PortDepot(); err == nil || !strings.Contains(err.Error(), "P4PORT") {
		t.Fatalf("invalid port = %v", err)
	}
}

func TestP4SwarmLinkRequiresIssueContext(t *testing.T) {
	t.Setenv("MULTICA_ISSUE_ID", "")
	resetP4Flags()
	t.Cleanup(resetP4Flags)
	p4Port, p4DepotPath = "p4:1666", "//depot/UE"
	err := runP4Swarm("link")(&cobra.Command{}, []string{"77"})
	if err == nil || !strings.Contains(err.Error(), "MULTICA_ISSUE_ID") {
		t.Fatalf("link without an issue must fail before making requests: %v", err)
	}
}

func TestReportSwarmReviewRejectsMalformedReceipt(t *testing.T) {
	t.Setenv("MULTICA_ISSUE_ID", "MUL-7")
	for _, payload := range []string{
		`not JSON`,
		`{"swarm_url":"https://configured.example"}`,
		`{"review":{"id":77,"url":"https://configured.example/reviews/77"}}`,
	} {
		if err := reportSwarmReviewToIssue(&cobra.Command{}, []byte(payload)); err == nil {
			t.Fatalf("malformed receipt accepted: %s", payload)
		}
	}
}

func TestP4SyncRejectsUnsupportedOptionsBeforeCallingDaemon(t *testing.T) {
	for _, args := range [][]string{{"--changelist", "1234"}, {"--fresh"}, {"--fresh=false"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			resetP4Flags()
			t.Cleanup(resetP4Flags)
			cmd := &cobra.Command{}
			cmd.Flags().StringVar(&p4Changelist, "changelist", "", "")
			cmd.Flags().BoolVar(&p4Fresh, "fresh", false, "")
			if err := cmd.ParseFlags(args); err != nil {
				t.Fatal(err)
			}
			if err := runP4Sync(cmd, nil); err == nil || !strings.Contains(err.Error(), "does not support") {
				t.Fatalf("unsupported option was ignored: %v", err)
			}
		})
	}
}

func TestP4AddPreservesExistingMetadataWhenFlagsOmitted(t *testing.T) {
	resetP4Flags()
	t.Cleanup(resetP4Flags)
	var patched map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"ws","name":"WS","slug":"ws","p4_depots":[{"port":"p4:1666","depot":"//depot/main","user":"alice","charset":"utf8","changelist":"100","description":"initial","swarm_url":"https://swarm.example","allow_submit":true,"allow_exclusive":true}]}`))
		case http.MethodPatch:
			if err := json.NewDecoder(r.Body).Decode(&patched); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte(`{"id":"ws","name":"WS","slug":"ws","p4_depots":[]}`))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer api.Close()
	t.Setenv("MULTICA_TOKEN", "mat_test")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws")
	t.Setenv("MULTICA_SERVER_URL", api.URL)

	cmd := &cobra.Command{}
	addP4TaskFlags(cmd, true)
	cmd.Flags().StringVar(&p4SwarmURL, "swarm-url", "", "")
	if err := cmd.ParseFlags([]string{"--port", "p4:1666", "--depot", "//depot/main"}); err != nil {
		t.Fatal(err)
	}
	if err := runP4Add(cmd, nil); err != nil {
		t.Fatalf("runP4Add: %v", err)
	}
	depots, _ := patched["p4_depots"].([]any)
	if len(depots) != 1 {
		t.Fatalf("patched depots = %#v", patched)
	}
	got, _ := depots[0].(map[string]any)
	if got["user"] != "alice" || got["charset"] != "utf8" || got["changelist"] != "100" || got["description"] != "initial" || got["swarm_url"] != "https://swarm.example" || got["allow_submit"] != true || got["allow_exclusive"] != true {
		t.Fatalf("metadata lost on update: %#v", got)
	}
}
