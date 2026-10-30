package p4cache

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestFilelogPassesOnlyBoundedFlags(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ws := fixture.detect(t)
	t.Setenv("P4_FAKE_FILELOG", "//DJ01_Proj/Dev/Foo.gbd\n... #1 change 10 add on 2024/01/01 00:00:00 by alice@alice_DEV (text) 'added'\n")
	text, revs, err := fixture.cache.Filelog(t.Context(), ws, []string{filepath.Join(fixture.root, "Foo.gbd")}, 5, true)
	if err != nil {
		t.Fatal(err)
	}
	if text == "" || len(revs) != 1 || revs[0].Change != "10" {
		t.Fatalf("text=%q revs=%+v", text, revs)
	}
	log := fixture.log(t)
	if !strings.Contains(log, "filelog -t -l -m 5") {
		t.Fatalf("filelog args = %s", log)
	}
	if _, _, err := fixture.cache.Filelog(t.Context(), ws, []string{"-x"}, 5, false); err == nil {
		t.Fatal("flag-shaped path was accepted")
	}
}

func TestParseFilelogKeepsChangelistDescriptions(t *testing.T) {
	text := strings.TrimSpace(`
//depot/Game/Foo.gbd
... #3 change 12345 edit on 2024/01/15 12:00:00 by alice@alice_DEV (binary+l) '修正 GBD 碰撞'

	修正 GBD 碰撞

	先例：沿用 MBoss01 的写法
... ... copy from //depot/Game/Old.gbd#2
... #2 change 12000 add on 2024/01/01 09:00:00 by bob@bob_DEV (binary) 'initial'
//depot/Game/Bar.gbd#1,1
... #1 change 99 add on 2023/01/01 00:00:00 by alice@alice_DEV (text) 'added bar'
`)
	revs := ParseFilelog(text)
	if len(revs) != 3 {
		t.Fatalf("revisions = %+v", revs)
	}
	if revs[0].DepotFile != "//depot/Game/Foo.gbd" || revs[0].Rev != "3" || revs[0].Change != "12345" || revs[0].Action != "edit" {
		t.Fatalf("first revision = %+v", revs[0])
	}
	if revs[0].User != "alice" || revs[0].Client != "alice_DEV" || revs[0].Type != "binary+l" {
		t.Fatalf("identity = %+v", revs[0])
	}
	if !strings.Contains(revs[0].Description, "先例：沿用 MBoss01 的写法") {
		t.Fatalf("long description dropped: %q", revs[0].Description)
	}
	if revs[1].Change != "12000" || revs[1].Description != "initial" || revs[1].DepotFile != "//depot/Game/Foo.gbd" {
		t.Fatalf("second revision = %+v", revs[1])
	}
	if revs[2].DepotFile != "//depot/Game/Bar.gbd" || revs[2].Change != "99" {
		t.Fatalf("third revision = %+v", revs[2])
	}
}

func TestParseDescribeReadsFilesAndDescription(t *testing.T) {
	text := strings.TrimSpace(`
Change 12345 by alice@alice_DEV on 2024/01/15 12:00:00 *pending*

	修正 GBD 碰撞

	see //depot/notes in the prose

Affected files ...

... //depot/Game/Foo.gbd#3 edit
... //depot/Game/Bar.gbd#1 add

Shelved files ...

... //depot/Game/Foo.gbd#3 edit
`)
	parsed, ok := ParseDescribe(text)
	if !ok {
		t.Fatal("describe header not recognized")
	}
	if parsed.Change != "12345" || parsed.User != "alice" || parsed.Client != "alice_DEV" || parsed.Status != "pending" {
		t.Fatalf("header = %+v", parsed)
	}
	if !strings.Contains(parsed.Description, "修正 GBD 碰撞") || strings.Contains(parsed.Description, "Affected files") {
		t.Fatalf("description = %q", parsed.Description)
	}
	if len(parsed.Files) != 3 || parsed.Files[0].DepotFile != "//depot/Game/Foo.gbd" || parsed.Files[0].Rev != "3" || parsed.Files[0].Action != "edit" {
		t.Fatalf("files = %+v", parsed.Files)
	}
	if _, ok := ParseDescribe("... change 12345\n... status submitted\n"); ok {
		t.Fatal("ztag describe was accepted as text")
	}
}

func TestValidChangeNumberRejectsFlags(t *testing.T) {
	for _, bad := range []string{"", "default", "-1", "0", "123 -x", "12345extra", "+12", "12.5"} {
		if ValidChangeNumber(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
	if !ValidChangeNumber("12345") || !ValidChangeNumber("007") {
		t.Fatal("positive numbers rejected")
	}
}

func TestParseDescribePreservesSpacesAndRejectsPartialFileLists(t *testing.T) {
	const header = "Change 12345 by alice@client on 2026/10/29 12:00:00\n\n"
	parsed, ok := ParseDescribe(header + "\t... //allowed/prose.cpp#1 edit\n\nAffected files ...\n\n... //depot/Folder/File Name.cpp#2 edit\n")
	if !ok || len(parsed.Files) != 1 || parsed.Files[0].DepotFile != "//depot/Folder/File Name.cpp" {
		t.Fatalf("describe file list = %+v, parsed=%v", parsed.Files, ok)
	}
	if !strings.Contains(parsed.Description, "//allowed/prose.cpp") {
		t.Fatalf("description was treated as file evidence: %q", parsed.Description)
	}
	if _, ok := ParseDescribe(header + "Affected files ...\n\n... //depot/Good.cpp#1 edit\n... //secret/unrecognized-file-line\n"); ok {
		t.Fatal("a partially parsed file list must not authorize a changelist")
	}
}
