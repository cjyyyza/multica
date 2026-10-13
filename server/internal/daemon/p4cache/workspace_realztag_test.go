package p4cache

import (
	"os"
	"path/filepath"
	"testing"
)

// The testdata/real fixtures are `p4 -ztag` answers captured from the DJ01
// commit server with CRLF line endings intact. The fake p4 used by the other
// tests writes a shape we chose; these pin the parser to the one the server
// actually sends — including the blank line `client -o` prints after the
// Description field, which once ended the record before Root and Stream.
func readRealZtag(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "real", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestParseZtagRealClientSpecKeepsFieldsAfterDescription(t *testing.T) {
	records := parseZtag(readRealZtag(t, "client.ztag"))
	if len(records) != 1 {
		t.Fatalf("records = %d, want one client spec", len(records))
	}
	spec := records[0]
	for key, want := range map[string]string{
		"Client": "yujian01_HIH-D-35258Z_Dev",
		"Owner":  "yujian01",
		"Host":   "HIH-D-35258Z",
		"Root":   `D:\DJ`,
		"Stream": "//DJ01_Proj/Dev",
		"View0":  "//DJ01_Proj/Dev/... //yujian01_HIH-D-35258Z_Dev/...",
	} {
		if got := spec[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestParseZtagRealInfo(t *testing.T) {
	info := firstZtagRecord(readRealZtag(t, "info.ztag"))
	for key, want := range map[string]string{
		"userName":       "yujian01",
		"clientName":     "yujian01_HIH-D-35258Z_Dev",
		"clientRoot":     `D:\DJ`,
		"serverServices": "commit-server",
		"serverCluster":  "p4_dj01",
	} {
		if got := info[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestParseZtagRealMultiFileFstatSplitsPerFile(t *testing.T) {
	records := parseZtag(readRealZtag(t, "fstat2.ztag"))
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2 (one per file)", len(records))
	}
	for i, record := range records {
		state := fileStateFromZtag(record)
		if state.DepotFile == "" || state.HeadType != "text" || state.HeadRev == 0 {
			t.Errorf("record %d = %+v, want a depot file with type and head rev", i, state)
		}
	}
	if records[0]["depotFile"] == records[1]["depotFile"] {
		t.Fatalf("both records name %s", records[0]["depotFile"])
	}
}

func TestParseZtagRealOpenedDefaultChangelist(t *testing.T) {
	records := parseZtag(readRealZtag(t, "opened.ztag"))
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	state := fileStateFromZtag(records[0])
	if state.OpenChange != defaultChangelist || state.Action != "edit" {
		t.Fatalf("state = %+v, want an edit in the default changelist", state)
	}
}
