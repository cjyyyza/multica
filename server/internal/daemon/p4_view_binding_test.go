package daemon

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
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
		{"virtual stream maps configured files", "//streams/virtual", `//depot/Team/... //client/...`, true},
		{"unrelated stream", "//streams/other", `//other/... //client/...`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := matchTaskDepot([]P4DepotData{{Port: "p4:1666", Depot: "//depot/Team"}}, &p4cache.Workspace{Port: "p4:1666", Stream: tc.stream, View: []string{tc.mapping}})
			if (err == nil) != tc.allowed {
				t.Fatalf("binding error=%v allowed=%v", err, tc.allowed)
			}
		})
	}
}

func TestP4BindingPrefersTheClientStreamOverAnImportedDepot(t *testing.T) {
	main := P4DepotData{Port: "p4:1666", Depot: "//game/main"}
	engine := P4DepotData{Port: main.Port, Depot: "//engine/long-import-path"}
	ws := &p4cache.Workspace{Port: main.Port, Stream: main.Depot, View: []string{
		main.Depot + "/... //client/...", engine.Depot + "/... //client/engine/...",
	}}
	depots := []P4DepotData{engine, main}
	ref, err := matchTaskDepot(depots, ws)
	if err != nil || ref.Depot != main.Depot {
		t.Fatalf("import displaced the main resource: %+v %v", ref, err)
	}
	d := &Daemon{}
	ref, accepted, err := d.matchP4Workspace(t.Context(), depots, ws)
	if err != nil || ref.Depot != main.Depot || len(accepted) != 2 {
		t.Fatalf("verified binding chose the import: %+v %v (%v)", ref, accepted, err)
	}
}
