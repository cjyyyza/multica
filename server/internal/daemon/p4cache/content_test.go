package p4cache

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/p4depot"
)

func TestReadChangelistContent(t *testing.T) {
	cache := &Cache{P4Path: writeFakeP4(t, t.TempDir())}
	ref := p4depot.Ref{Port: "p4:1666", User: "alice"}
	log := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("P4_CONTENT_LOG", log)
	for _, tc := range []struct {
		name, status, shelf, action, kind, file, output, source, want, args string
		binary, wantErr                                                     bool
	}{
		{name: "shelf uses base revision and shelf selector", status: "pending", shelf: "yes", action: "edit", kind: "text", file: "//depot/A.cpp", output: "@@ -1 +1 @@\n-old\n+new\n", source: "shelf", want: "+new", args: "diff2 -du //depot/A.cpp#6 //depot/A.cpp@=1001"},
		{name: "submitted uses preceding revision", status: "submitted", action: "edit", kind: "text", file: "//depot/A.cpp", output: "@@ -1 +1 @@\n-old\n+new\n", source: "submitted", want: "+new", args: "diff2 -du //depot/A.cpp#5 //depot/A.cpp@=1001"},
		{name: "added file", status: "pending", shelf: "yes", action: "add", kind: "text", file: "//depot/A.cpp", output: "new\n", source: "shelf", want: "@@ -0,0 +1,1 @@\n+new", args: "print -q //depot/A.cpp@=1001"},
		{name: "deleted file", status: "submitted", action: "delete", kind: "text", file: "//depot/A.cpp", output: "old\n", source: "submitted", want: "@@ -1,1 +0,0 @@\n-old", args: "print -q //depot/A.cpp#5"},
		{name: "binary", status: "pending", shelf: "yes", action: "edit", kind: "binary+l", file: "//depot/A.cpp", source: "shelf", binary: true},
		{name: "outside changelist", status: "submitted", action: "edit", kind: "text", file: "//depot/secret", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.WriteFile(log, nil, 0600)
			metadata := fmt.Sprintf("... change 1001\n... status %s\n... client alice_DEV\n... depotFile0 //depot/A.cpp\n... action0 %s\n... type0 %s\n... rev0 6\n", tc.status, tc.action, tc.kind)
			t.Setenv("P4_FAKE_DESCRIBE", metadata)
			t.Setenv("P4_FAKE_SHELF", "")
			if tc.shelf != "" {
				t.Setenv("P4_FAKE_SHELF", metadata)
			}
			t.Setenv("P4_CONTENT_DIFF2", tc.output)
			t.Setenv("P4_CONTENT_PRINT", tc.output)
			result, err := cache.ReadChangelistContent(t.Context(), ref, 1001, tc.file, false)
			if tc.wantErr {
				if err == nil {
					t.Fatal("accepted a file outside the changelist")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Source != tc.source || result.Binary != tc.binary || !strings.Contains(result.Diff, tc.want) {
				t.Fatalf("content = %+v", result)
			}
			commands, _ := os.ReadFile(log)
			if tc.args != "" && !strings.Contains(string(commands), tc.args) {
				t.Fatalf("commands = %s, want %s", commands, tc.args)
			}
			if tc.binary && (strings.Contains(string(commands), " diff2 ") || strings.Contains(string(commands), " print ")) {
				t.Fatalf("read a binary body: %s", commands)
			}
		})
	}

	t.Run("shelf list excludes later workspace changes", func(t *testing.T) {
		t.Setenv("P4_FAKE_DESCRIBE", "... change 1001\n... status pending\n... depotFile0 //depot/unshelved.cpp\n")
		t.Setenv("P4_FAKE_SHELF", "... change 1001\n... depotFile0 //depot/shelved.cpp\n... action0 edit\n... type0 text\n... rev0 4\n")
		result, err := cache.ReadChangelistContent(t.Context(), ref, 1001, "", false)
		if err != nil || len(result.Files) != 1 || result.Files[0].DepotPath != "//depot/shelved.cpp" {
			t.Fatalf("content = %+v, error = %v", result, err)
		}
	})
}

func TestReadChangelistContentPendingAndLimits(t *testing.T) {
	cache := &Cache{P4Path: writeFakeP4(t, t.TempDir())}
	ref := p4depot.Ref{Port: "p4:1666", User: "alice"}
	t.Setenv("P4_FAKE_DESCRIBE", "... change 1001\n... status pending\n... client alice_DEV\n... depotFile0 //depot/A.cpp\n... action0 add\n... type0 text\n... rev0 1\n")
	t.Setenv("P4_FAKE_SHELF_ERR", "Change 1001 has no shelved files.")
	result, err := cache.ReadChangelistContent(t.Context(), ref, 1001, "//depot/A.cpp", false)
	if err != nil || result.Unavailable != "workspace_unavailable" {
		t.Fatalf("content = %+v, error = %v", result, err)
	}
	root := t.TempDir()
	local := filepath.Join(root, "A.cpp")
	if err := os.WriteFile(local, []byte("new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("P4_CONTENT_CLIENT", fmt.Sprintf("... Root %s\n... Owner alice\n", root))
	t.Setenv("P4_CONTENT_WHERE", fmt.Sprintf("... depotFile //depot/A.cpp\n... path %s\n", local))
	result, err = cache.ReadChangelistContent(t.Context(), ref, 1001, "//depot/A.cpp", true)
	if err != nil || result.Source != "workspace" || !strings.Contains(result.Diff, "+new") {
		t.Fatalf("content = %+v, error = %v", result, err)
	}
	t.Setenv("P4_CONTENT_CLIENT", fmt.Sprintf("... Root %s\n... Owner bob\n", root))
	result, err = cache.ReadChangelistContent(t.Context(), ref, 1001, "//depot/A.cpp", true)
	if err != nil || result.Unavailable != "workspace_unavailable" {
		t.Fatalf("read another owner's workspace: %+v, %v", result, err)
	}
	t.Setenv("P4_FAKE_SHELF_ERR", "")
	t.Setenv("P4_FAKE_SHELF", "... change 1001\n... depotFile0 //depot/A.cpp\n... action0 edit\n... type0 text\n... rev0 1\n")
	t.Setenv("P4_CONTENT_DIFF2", strings.Repeat("+new\n", p4ContentLimit/4))
	result, err = cache.ReadChangelistContent(t.Context(), ref, 1001, "//depot/A.cpp", false)
	if err != nil || !result.Truncated || len(result.Diff) > p4ContentLimit {
		t.Fatalf("unbounded diff: length=%d truncated=%t error=%v", len(result.Diff), result.Truncated, err)
	}
}
