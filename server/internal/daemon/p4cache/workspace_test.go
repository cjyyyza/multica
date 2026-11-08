package p4cache

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/p4depot"
)

func TestWorkspaceOwnershipChecks(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "project")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, owner, host, dir string
		wantErr                bool
		outsideRoot            bool
	}{
		{name: "current user host and root", owner: "alice", host: hostname, dir: inside},
		{name: "another owner", owner: "bob", host: hostname, dir: inside, wantErr: true},
		{name: "another host", owner: "alice", host: "definitely-another-host", dir: inside, wantErr: true},
		{name: "outside root", owner: "alice", host: hostname, dir: t.TempDir(), wantErr: true, outsideRoot: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := &Workspace{User: "alice", Client: "alice_DEV", Root: root, Dir: tc.dir, Host: tc.host}
			err := ws.checkOwnership(tc.owner)
			if (err != nil) != tc.wantErr {
				t.Fatalf("checkOwnership=%v wantErr=%v", err, tc.wantErr)
			}
			if tc.outsideRoot && !errors.Is(err, ErrWorkspaceNotConfigured) {
				t.Fatalf("outside root error=%v", err)
			}
		})
	}
}

func TestParseZtagKeepsMultilineClientSpec(t *testing.T) {
	out := "... Client alice_DEV\n... Owner alice\n... Description:\n\tfirst line\n\n\tsecond line\n... Root D:\\DJ\n... View0 //depot/main/... //alice_DEV/main/...\n"
	record := firstZtagRecord(out)
	if record["Client"] != "alice_DEV" || record["Owner"] != "alice" || record["Root"] != `D:\DJ` || record["View0"] == "" {
		t.Fatalf("record=%+v", record)
	}
}

func TestDescribeTaskChangeReadsClientAndShelf(t *testing.T) {
	cache := &Cache{P4Path: writeFakeP4(t, t.TempDir())}
	t.Setenv("P4USER", "alice")
	t.Setenv("P4_FAKE_DESCRIBE", "... change 1001\n... oldChange 1001\n... status pending\n... client alice_DEV\n... description Fix the editor crash\n... depotFile0 //depot/UE/Source/A.cpp\n")
	t.Setenv("P4_FAKE_SHELF", "... change 1001\n... status pending\n... depotFile0 //depot/UE/Source/A.cpp\n")
	info, err := cache.DescribeTaskChange(t.Context(), p4depot.Ref{Port: "p4:1666", User: "alice"}, "1001")
	if err != nil {
		t.Fatal(err)
	}
	if info.Client != "alice_DEV" || info.Description != "Fix the editor crash" || info.FilesCount != 1 || !info.Shelved {
		t.Fatalf("unexpected change details: %+v", info)
	}
}
