package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

func TestWorkspaceQueryForwardsP4TextInTheBoundClient(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_CLIENT", "... Client alice_DEV\n... Root "+root+"\n... Stream //DJ01_Proj/Dev\n")
	t.Setenv("P4_FAKE_CHANGES", "Change 10 on 2024/01/01 by alice@alice_DEV 'fix'\n")
	t.Setenv("P4_FAKE_FSTAT", "... depotFile //DJ01_Proj/Dev/Foo.cpp\n... action edit\n... otherOpen0 bob@other\n")
	binding := queryBinding(root)
	binding.readOnly = true
	binding.registrationPending = true
	binding.submitted = true
	d := queryDaemon(bin)

	listed, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: "changes"}, root)
	if err != nil || rej != nil {
		t.Fatalf("changes: err=%v rej=%+v", err, rej)
	}
	if !strings.Contains(listed.Output, "Change 10") || listed.ExitCode != 0 {
		t.Fatalf("changes output = %q exit=%d", listed.Output, listed.ExitCode)
	}

	stated, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: "Fstat", Args: []string{"-Ol", "Foo.cpp"}}, root)
	if err != nil || rej != nil {
		t.Fatalf("fstat: err=%v rej=%+v", err, rej)
	}
	if !strings.Contains(stated.Output, "otherOpen0 bob@other") {
		t.Fatalf("fstat dropped p4's text: %q", stated.Output)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(log)
	if !strings.Contains(text, "changes -m 50 //DJ01_Proj/Dev/...") {
		t.Fatalf("changes was not scoped and capped: %s", text)
	}
	if !strings.Contains(text, "fstat -Ol") || !strings.Contains(text, "Foo.cpp") {
		t.Fatalf("fstat did not forward -Ol and the file: %s", text)
	}
	if strings.Contains(text, "fstat -Ol -") {
		t.Fatalf("fstat forwarded a flag the caller did not pass: %s", text)
	}
}

func TestWorkspaceQueryRejectsBroadOrForeignReads(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_CLIENT", "... Root "+root+"\n... Stream //DJ01_Proj/Dev\n")
	t.Setenv("P4_FAKE_FSTAT", "... depotFile //secret/Hidden.cpp\n... action edit\n")
	binding := queryBinding(root)
	d := queryDaemon(bin)

	cases := []p4MutateRequest{
		{Action: "fstat"},
		{Action: "fstat", Args: []string{"//DJ01_Proj/Dev/..."}},
		{Action: "fstat", Args: []string{"//DJ01_Proj/Dev/Source/..."}},
		{Action: "fstat", Args: []string{"-p", "evil:1666", "Foo.cpp"}},
		{Action: "print", Args: []string{"-o", "out.txt", "Foo.cpp"}},
		{Action: "print", Args: []string{"Foo.cpp/..."}},
		{Action: "changes", Args: []string{"-m", "500"}},
		{Action: "changes", Changelist: "999"},
		{Action: "fstat", Args: []string{"//secret/Hidden.cpp"}},
	}
	for _, req := range cases {
		t.Run(req.Action+" "+strings.Join(req.Args, " "), func(t *testing.T) {
			result, rej, err := d.runWorkspaceP4(t.Context(), binding, req, root)
			if err != nil || result != nil || rej == nil {
				t.Fatalf("expected a refusal, got result=%+v rej=%+v err=%v", result, rej, err)
			}
			if strings.Contains(rej.Message, "//secret") || strings.Contains(strings.Join(rej.Files, " "), "//secret") {
				t.Fatalf("refusal leaked the other depot: %+v", rej)
			}
		})
	}

	result, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: "fstat", Args: []string{"Foo.cpp"}}, root)
	if err != nil || result != nil || rej == nil || rej.Code != p4CodeNotInWorkspace {
		t.Fatalf("foreign fstat output was returned: result=%+v rej=%+v err=%v", result, rej, err)
	}
	if strings.Contains(rej.Message, "//secret") || strings.Contains(strings.Join(rej.Files, " "), "Hidden") {
		t.Fatalf("refusal leaked the other depot: %+v", rej)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(log)
	if strings.Contains(text, " -p evil:1666") || strings.Contains(text, "print ") {
		t.Fatalf("a rejected flag reached p4: %s", text)
	}
	if !strings.Contains(text, "fstat ") {
		t.Fatalf("authorized fstat did not run before its output was refused: %s", text)
	}
}

func TestWorkspaceQueryAllowsABoundedSubdirectory(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_CLIENT", "... Root "+root+"\n... Stream //DJ01_Proj/Dev\n")
	t.Setenv("P4_FAKE_FSTAT", "... depotFile //DJ01_Proj/Dev/Source/A.cpp\n... action edit\n")
	d := queryDaemon(bin)
	result, rej, err := d.runWorkspaceP4(t.Context(), queryBinding(root), p4MutateRequest{
		Action: "fstat", Args: []string{"-m", "20", "-ztag", "//DJ01_Proj/Dev/Source/..."},
	}, root)
	if err != nil || rej != nil {
		t.Fatalf("subdirectory fstat: err=%v rej=%+v", err, rej)
	}
	if !strings.Contains(result.Output, "Source/A.cpp") {
		t.Fatalf("output = %q", result.Output)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(log)
	if !strings.Contains(text, "-ztag ") || !strings.Contains(text, "fstat -m 20 //DJ01_Proj/Dev/Source/...") {
		t.Fatalf("ztag was not lifted or the wildcard was rewritten: %s", text)
	}
	if strings.Contains(text, "fstat -m 20 -ztag") {
		t.Fatalf("ztag was left after the verb: %s", text)
	}
}

func TestClassifyReadArgsPreservesNativeFlagValues(t *testing.T) {
	cases := []struct {
		action string
		args   []string
		want   string
	}{
		{p4cache.ActionDiff, []string{"-du3", "Foo.cpp"}, "-du3\x00Foo.cpp"},
		{p4cache.ActionPrint, []string{"--offset", "100", "--size=50", "-Q", "utf8", "-L", "unix", "Foo.cpp"}, "--offset\x00100\x00--size\x0050\x00-Q\x00utf8\x00-L\x00unix\x00Foo.cpp"},
		{p4cache.ActionPrint, []string{"-Qutf8", "Foo.cpp"}, "-Q\x00utf8\x00Foo.cpp"},
		{p4cache.ActionChanges, []string{"-m20"}, "-m\x0020"},
		{p4cache.ActionFstat, []string{"-Ss", "Foo.cpp"}, "-Ss\x00Foo.cpp"},
		{p4cache.ActionSizes, []string{"-b", "1024", "-H", "Foo.cpp"}, "-b\x001024\x00-H\x00Foo.cpp"},
		{p4cache.ActionAnnotate, []string{"-u", "--tab=8", "Foo.cpp"}, "-u\x00--tab\x008\x00Foo.cpp"},
		{p4cache.ActionChanges, []string{"--stream", "--nostream"}, "--stream\x00--nostream"},
		{p4cache.ActionGrep, []string{"-e", "needle", "-A", "3", "//DJ01_Proj/Dev/Source/..."}, "-e\x00needle\x00-A\x003\x00//DJ01_Proj/Dev/Source/..."},
		{p4cache.ActionResolved, []string{"-o", "Foo.cpp"}, "-o\x00Foo.cpp"},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			_, flags, files, rej := classifyReadArgs(p4ReadPolicies()[tc.action], tc.args)
			if rej != nil {
				t.Fatalf("classify: %+v", rej)
			}
			if got := strings.Join(rebuildReadArgs(flags, files), "\x00"); got != tc.want {
				t.Fatalf("argv = %q, want %q", got, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		action string
		args   []string
	}{
		{p4cache.ActionPrint, []string{"-o", "out.txt", "Foo.cpp"}},
		{p4cache.ActionPrint, []string{`-o..\outside.txt`, "Foo.cpp"}},
		{p4cache.ActionDiff2, []string{"-b", "branch", "Foo.cpp"}},
		{p4cache.ActionDiff2, []string{"-botherbranch", "Foo.cpp"}},
		{p4cache.ActionDiff2, []string{"-Pparent", "Foo.cpp"}},
		{p4cache.ActionDiff2, []string{"-S", "//DJ01_Proj/Dev", "Foo.cpp"}},
		{p4cache.ActionDiff2, []string{"-S//DJ01_Proj/Dev", "Foo.cpp"}},
		{p4cache.ActionDiff, []string{"-As", "//DJ01_Proj/Dev"}},
		{p4cache.ActionDiff2, []string{"-As", "//DJ01_Proj/Dev", "//DJ01_Proj/Other"}},
		{p4cache.ActionIntegrated, []string{"-b", "branch", "Foo.cpp"}},
		{p4cache.ActionIntegrated, []string{"-bbranch", "Foo.cpp"}},
		{p4cache.ActionInterchange, []string{"-bbranch", "Foo.cpp"}},
		{p4cache.ActionInterchange, []string{"-Pparent", "Foo.cpp"}},
		{p4cache.ActionInterchange, []string{"-Sstream", "Foo.cpp"}},
	} {
		if _, _, _, rej := classifyReadArgs(p4ReadPolicies()[tc.action], tc.args); rej == nil {
			t.Errorf("%s %v was not rejected", tc.action, tc.args)
		}
	}
}

func TestWorkspaceQueryRejectsStreamSpecDiffBeforeP4(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	d := queryDaemon(bin)
	for _, tc := range []struct {
		action string
		args   []string
	}{
		{p4cache.ActionDiff, []string{"-As", "//DJ01_Proj/Dev"}},
		{p4cache.ActionDiff2, []string{"-As", "//DJ01_Proj/Dev", "//DJ01_Proj/Other"}},
	} {
		result, rej, err := d.runWorkspaceP4(t.Context(), queryBinding(root), p4MutateRequest{Action: tc.action, Args: tc.args}, root)
		if err != nil || rej == nil || result != nil {
			t.Errorf("%s -As: result=%+v rej=%+v err=%v", tc.action, result, rej, err)
		}
	}
	log, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if strings.Contains(string(log), "diff") {
		t.Fatalf("stream-spec diff reached p4: %s", log)
	}
}

func TestIntegratedOutputChecksBothSides(t *testing.T) {
	policy := p4ReadPolicies()[p4cache.ActionIntegrated]
	paths := readOutputDepotPaths(policy, "//DJ01_Proj/Dev/A.cpp#2 //secret/Hidden.cpp#3\n")
	if len(paths) != 2 || paths[1] != "//secret/Hidden.cpp" {
		t.Fatalf("depot paths = %v", paths)
	}
}

func TestReadOutputChecksAllDepotBearingFields(t *testing.T) {
	fstat := readOutputDepotPaths(p4ReadPolicies()[p4cache.ActionFstat], strings.Join([]string{
		"... depotFile //DJ01_Proj/Dev/A.cpp",
		"... movedFile //secret/Moved.cpp",
		"... resolveFromFile0 //secret/From.cpp",
		"... resolveBaseFile1 //secret/Base.cpp",
		"... resolveFromFileX //ignored/NotAField.cpp",
	}, "\n"))
	if len(fstat) != 4 {
		t.Fatalf("fstat paths = %v", fstat)
	}
	for _, action := range []string{p4cache.ActionResolved, p4cache.ActionInterchange} {
		paths := readOutputDepotPaths(p4ReadPolicies()[action], "//DJ01_Proj/Dev/A.cpp#3 //secret/From.cpp#2\n")
		if len(paths) != 2 || paths[1] != "//secret/From.cpp" {
			t.Errorf("%s paths = %v", action, paths)
		}
	}
}

func TestWorkspaceQueryRejectsNumberedIntegrationSourceFields(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	t.Setenv("P4_FAKE_CLIENT", "... Root "+root+"\n... Stream //DJ01_Proj/Dev\n")
	for _, field := range []string{"resolveFromFile0", "resolveBaseFile1"} {
		t.Run(field, func(t *testing.T) {
			t.Setenv("P4_FAKE_FSTAT", "... depotFile //DJ01_Proj/Dev/Foo.cpp\n... "+field+" //secret/From.cpp\n")
			result, rej, err := queryDaemon(bin).runWorkspaceP4(t.Context(), queryBinding(root), p4MutateRequest{
				Action: p4cache.ActionFstat, Args: []string{"-Or", "//DJ01_Proj/Dev/Foo.cpp"},
			}, root)
			if err != nil || rej == nil || result != nil {
				t.Fatalf("fstat -Or: result=%+v rej=%+v err=%v", result, rej, err)
			}
		})
	}
}

func TestWorkspaceQueryRejectsForeignStderr(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	t.Setenv("P4_FAKE_CLIENT", "... Root "+root+"\n... Stream //DJ01_Proj/Dev\n")
	t.Setenv("P4_FAKE_FSTAT_ERR", "//secret/Hidden.cpp - no such file\n")
	result, rej, err := queryDaemon(bin).runWorkspaceP4(t.Context(), queryBinding(root), p4MutateRequest{
		Action: p4cache.ActionFstat, Args: []string{"//DJ01_Proj/Dev/Foo.cpp"},
	}, root)
	if err != nil || rej == nil || result != nil {
		t.Fatalf("foreign stderr: result=%+v rej=%+v err=%v", result, rej, err)
	}
}

func TestWorkspaceQueryRejectsLargeOutput(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	t.Setenv("P4_FAKE_CLIENT", "... Root "+root+"\n... Stream //DJ01_Proj/Dev\n")
	t.Setenv("P4_FAKE_GREP", strings.Repeat("x", p4cache.QueryOutputMax+1))
	result, rej, err := queryDaemon(bin).runWorkspaceP4(t.Context(), queryBinding(root), p4MutateRequest{
		Action: p4cache.ActionGrep, Args: []string{"-e", "needle", "//DJ01_Proj/Dev/Source/..."},
	}, root)
	if err != nil || rej == nil || result != nil {
		t.Fatalf("large output: result=%+v rej=%+v err=%v", result, rej, err)
	}
	if !strings.Contains(rej.Message, "too large") {
		t.Fatalf("rejection = %+v", rej)
	}
}

func TestWorkspaceQueryAdditionalReadCommands(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_CLIENT", "... Root "+root+"\n... Stream //DJ01_Proj/Dev\n")
	binding := queryBinding(root)
	binding.readOnly = true
	d := queryDaemon(bin)
	cases := []struct {
		action string
		args   []string
		body   string
	}{
		{p4cache.ActionDirs, []string{"//DJ01_Proj/Dev/Source/*"}, "//DJ01_Proj/Dev/Source/Game\n"},
		{p4cache.ActionGrep, []string{"-e", "needle", "//DJ01_Proj/Dev/Source/..."}, "//DJ01_Proj/Dev/Source/A.cpp#1: needle\n"},
		{p4cache.ActionIntegrated, []string{"-m", "5", "//DJ01_Proj/Dev/Source/..."}, "//DJ01_Proj/Dev/Source/A.cpp#2 //DJ01_Proj/Dev/Target/A.cpp#3\n"},
		{p4cache.ActionInterchange, []string{"//DJ01_Proj/Dev/Source/...", "//DJ01_Proj/Dev/Target/..."}, "Change 10 on 2024/01/01 by alice 'integrate'\n"},
		{p4cache.ActionResolved, []string{"-o", "//DJ01_Proj/Dev/Source/A.cpp"}, "//DJ01_Proj/Dev/Source/A.cpp#3 - resolved\n"},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			t.Setenv("P4_FAKE_"+strings.ToUpper(tc.action), tc.body)
			result, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: tc.action, Args: tc.args}, root)
			if err != nil || rej != nil {
				t.Fatalf("run: err=%v rej=%+v", err, rej)
			}
			if result.Output != strings.TrimSpace(tc.body) {
				t.Fatalf("output = %q, want %q", result.Output, tc.body)
			}
		})
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"dirs", "grep", "integrated", "interchanges", "resolved"} {
		if !strings.Contains(string(log), action+" ") {
			t.Errorf("%s did not reach p4: %s", action, log)
		}
	}
}

func TestWorkspaceResolveInjectsTaskChangeAndRequiresAutomaticMode(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_CLIENT", "... Root "+root+"\n... Stream //DJ01_Proj/Dev\n")
	binding := queryBinding(root)
	binding.change = "1001"
	d := queryDaemon(bin)
	result, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{
		Action: p4cache.ActionResolve, Args: []string{"-am", "-f", "-Aa", "-du3"},
	}, root)
	if err != nil || rej != nil {
		t.Fatalf("resolve: err=%v rej=%+v", err, rej)
	}
	if result.Changelist != "1001" {
		t.Fatalf("changelist = %q", result.Changelist)
	}
	for _, args := range [][]string{{"-c", "1001"}, {"-So", "-am"}, {"Foo.cpp"}, {"--unknown", "-am"}, {"-A", "-am"}, {"-d", "-am"}} {
		if _, rej, err := d.runWorkspaceP4(t.Context(), binding, p4MutateRequest{Action: p4cache.ActionResolve, Args: args}, root); err != nil || rej == nil {
			t.Errorf("resolve %v: err=%v rej=%+v", args, err, rej)
		}
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "resolve -am -f -Aa -du3 -c 1001") {
		t.Fatalf("resolve was not scoped to the task changelist: %s", log)
	}
}

func TestWorkspaceReconcilePreviewDoesNotCreateOrTouchChange(t *testing.T) {
	bin := writeDaemonFakeP4(t)
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "p4.log")
	t.Setenv("P4_FAKE_LOG", logPath)
	t.Setenv("P4_FAKE_CLIENT", "... Root "+root+"\n... Stream //DJ01_Proj/Dev\n")
	t.Setenv("P4_FAKE_FSTAT", "... depotFile //DJ01_Proj/Dev/Foo.cpp\n... action edit\n... change 1001\n")
	binding := queryBinding(root)
	binding.change = "1001"
	binding.ticket = "123"
	d := queryDaemon(bin)
	req := p4MutateRequest{Action: p4cache.ActionReconcile, Files: []string{"//DJ01_Proj/Dev/Foo.cpp"}, Preview: true}
	if _, rej, err := d.runWorkspaceP4(t.Context(), binding, req, root); err != nil || rej != nil {
		t.Fatalf("preview: err=%v rej=%+v", err, rej)
	}
	if binding.changeTouched {
		t.Fatal("preview marked the task change as touched")
	}
	empty := queryBinding(root)
	empty.ticket = "123"
	if _, rej, err := d.runWorkspaceP4(t.Context(), empty, req, root); err != nil || rej == nil {
		t.Fatalf("preview without a task change: err=%v rej=%+v", err, rej)
	}
	if empty.change != "" {
		t.Fatalf("preview created changelist %s", empty.change)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "reconcile -aed -n -c 1001") {
		t.Fatalf("preview flags = %s", log)
	}
}

func queryBinding(root string) *p4TaskBinding {
	return &p4TaskBinding{
		ws:  &p4cache.Workspace{Port: "p4:1666", User: "alice", Client: "alice_DEV", Root: root, Dir: root, Stream: "//DJ01_Proj/Dev"},
		ref: p4depot.Ref{Port: "p4:1666", Depot: "//DJ01_Proj/Dev", Stream: "//DJ01_Proj/Dev"},
	}
}

func queryDaemon(bin string) *Daemon {
	return &Daemon{p4Cache: &p4cache.Cache{P4Path: bin}}
}
