package daemon

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/p4depot"
)

func TestP4ProjectAuthorizationIsScopedToActiveTask(t *testing.T) {
	d := &Daemon{workspaces: map[string]*workspaceState{"ws": {}}}
	ref := p4depot.Ref{Port: "p4:1666", Depot: "//depot/UE"}
	d.registerTaskP4Depots("ws", "first", []P4DepotData{P4DepotData(ref)})
	if !d.workspaceP4Allowed("ws", "first", ref) {
		t.Fatal("own project depot was rejected")
	}
	if d.workspaceP4Allowed("ws", "second", ref) {
		t.Fatal("another task inherited project-only authorization")
	}
	d.clearTaskP4Refs("ws", "first")
	if d.workspaceP4Allowed("ws", "first", ref) {
		t.Fatal("completed task retained project-only authorization")
	}
	d.workspaces["ws"].setP4Depots([]P4DepotData{P4DepotData(ref)})
	if !d.workspaceP4Allowed("ws", "second", ref) {
		t.Fatal("workspace depot should remain available")
	}
	if d.workspaceP4Allowed("other", "second", ref) {
		t.Fatal("workspace boundary was lost")
	}
}
