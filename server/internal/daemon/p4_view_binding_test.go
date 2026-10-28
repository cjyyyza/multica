package daemon

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

func TestP4BindingUsesEffectiveViewScope(t *testing.T) {
	for _, tc := range []struct {
		name, stream, mapping string
		allowed               bool
	}{
		{"quoted mapping", "", `"//depot/Team/Space Dir/..." "//client/Team/Space Dir/..."`, true},
		{"overlay", "", `+//depot/Team/... //client/Team/...`, true},
		{"ditto", "", `&//depot/Team/... //client/Team/...`, true},
		{"broad classic view", "", `//depot/... //client/...`, true},
		{"excluded", "", `-//depot/Team/... //client/Team/...`, false},
		{"virtual stream maps authorized files", "//streams/virtual", `//depot/Team/... //client/...`, true},
		{"unrelated stream", "//streams/other", `//other/... //client/...`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := matchTaskDepot([]P4DepotData{{Port: "p4:1666", Depot: "//depot/Team"}}, &p4cache.Workspace{Port: "p4:1666", Stream: tc.stream, View: []string{tc.mapping}})
			if (err == nil) != tc.allowed {
				t.Fatalf("binding error = %v, allowed = %v", err, tc.allowed)
			}
		})
	}
}

func TestP4BindingDoesNotInheritParentNamespaceGrants(t *testing.T) {
	parent := P4DepotData{Port: "p4:1666", Depot: "//DJ01_Proj", AllowSubmit: true, AllowUnshelve: true, AllowExclusive: true}
	main := P4DepotData{Port: parent.Port, Depot: "//DJ01_Proj/Dev"}
	ws := &p4cache.Workspace{Port: parent.Port, Client: "client", Stream: main.Depot, View: []string{
		main.Depot + "/... //client/...", "//DJ01_Proj/Imported/... //client/imported/...",
	}}
	if ref, err := matchTaskDepot([]P4DepotData{parent}, ws); err == nil {
		t.Fatalf("parent authorized a child stream: %+v", ref)
	}
	d := &Daemon{}
	ref, accepted, err := d.matchP4Workspace(t.Context(), []P4DepotData{parent, main}, ws)
	if err != nil {
		t.Fatal(err)
	}
	b := &p4TaskBinding{ws: ws, ref: ref, depots: accepted}
	if _, ok := b.depotRef(main.Depot + "/Source/file.cpp"); !ok {
		t.Fatal("the stream's own files must remain authorized")
	}
	if inherited, ok := b.depotRef("//DJ01_Proj/Imported/asset.uasset"); ok {
		t.Fatalf("import inherited parent grants: %+v", inherited)
	}
	imported := p4depot.Ref{Port: parent.Port, Depot: "//DJ01_Proj/Imported"}
	b.depots = append(b.depots, imported)
	for _, action := range []string{"submit", "unshelve"} {
		if rejection := b.rejectDepotFiles(action, []p4cache.FileState{{DepotFile: imported.Depot + "/asset.uasset"}}); rejection == nil {
			t.Errorf("import borrowed the parent's %s grant", action)
		}
	}
	if grant, ok := b.depotRef(imported.Depot + "/asset.uasset"); !ok || grant.AllowExclusive {
		t.Fatalf("import must use its own exclusive grant: %+v %v", grant, ok)
	}
}

func TestP4BindingDoesNotAcceptAnImportedNamespaceParent(t *testing.T) {
	main := P4DepotData{Port: "p4:1666", Depot: "//game/main", AllowSubmit: true}
	parent := P4DepotData{Port: main.Port, Depot: "//engine", AllowSubmit: true, AllowExclusive: true}
	ws := &p4cache.Workspace{Port: main.Port, Stream: main.Depot, View: []string{
		main.Depot + "/... //client/...", "//engine/main/... //client/engine/...",
	}}
	d := &Daemon{}
	ref, accepted, err := d.matchP4Workspace(t.Context(), []P4DepotData{main, parent}, ws)
	if err != nil {
		t.Fatal(err)
	}
	b := &p4TaskBinding{ws: ws, ref: ref, depots: accepted}
	if inherited, ok := b.depotRef("//engine/main/file.cpp"); ok {
		t.Fatalf("a mapped import borrowed its parent's grants: %+v", inherited)
	}
}

func TestP4BindingPrefersTheClientStreamOverAnImportedDepot(t *testing.T) {
	main := P4DepotData{Port: "p4:1666", Depot: "//game/main", AllowSync: true}
	engine := P4DepotData{Port: main.Port, Depot: "//engine/long-import-path"}
	ws := &p4cache.Workspace{Port: main.Port, Stream: main.Depot, View: []string{
		main.Depot + "/... //client/...", engine.Depot + "/... //client/engine/...",
	}}
	depots := []P4DepotData{engine, main}
	ref, err := matchTaskDepot(depots, ws)
	if err != nil || ref.Depot != main.Depot || !ref.AllowSync {
		t.Fatalf("import displaced the main resource: %+v %v", ref, err)
	}
	d := &Daemon{}
	ref, accepted, err := d.matchP4Workspace(t.Context(), depots, ws)
	if err != nil || ref.Depot != main.Depot || len(accepted) != 2 {
		t.Fatalf("verified binding chose the import as its primary resource: %+v %v (%v)", ref, accepted, err)
	}
}
