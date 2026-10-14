package handler

import (
	"encoding/json"
	"testing"
)

func TestProjectP4ResourceDoesNotPersistActionGrants(t *testing.T) {
	raw, err := validatePerforceDepotRef(json.RawMessage(`{"port":"p4:1666","depot":"//depot/main","allow_exclusive":true,"allow_submit":true,"allow_sync":true,"allow_unshelve":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var ref map[string]any
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"allow_submit", "allow_sync", "allow_unshelve"} {
		if _, ok := ref[key]; ok {
			t.Errorf("project resource kept an ineffective permission: %s", raw)
		}
	}
	if ref["allow_exclusive"] != true {
		t.Fatal("project-owned exclusive-file setting was lost")
	}
}

func TestOverlayP4ActionSwitchesUsesWorkspaceSettings(t *testing.T) {
	project := []P4DepotData{{
		Port: "ssl:P4.example:1666", Depot: "//depot/game", Stream: "//depot/game/main",
		AllowSubmit: true,
	}}
	workspace := []P4DepotData{{
		Port: "ssl:p4.example:1666", Depot: "//depot/game", Stream: "//depot/game/main",
		AllowSync: true, AllowUnshelve: true,
	}}
	overlayP4ActionSwitches(project, workspace)
	if project[0].AllowSubmit || !project[0].AllowSync || !project[0].AllowUnshelve {
		t.Fatalf("switches = submit %v sync %v unshelve %v", project[0].AllowSubmit, project[0].AllowSync, project[0].AllowUnshelve)
	}
}

func TestOverlayP4ActionSwitchesCannotGrantProjectPermissions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		depot     P4DepotData
		workspace []P4DepotData
	}{
		{name: "no workspace catalog", depot: P4DepotData{Port: "p4:1666", Depot: "//depot/main"}},
		{name: "different depot", depot: P4DepotData{Port: "p4:1666", Depot: "//depot/other"}, workspace: []P4DepotData{{Port: "p4:1666", Depot: "//depot/main"}}},
		{name: "equivalent stream with different identity", depot: P4DepotData{Port: "p4:1666", Depot: "//depot", Stream: "//depot/main"}, workspace: []P4DepotData{{Port: "p4:1666", Depot: "//depot/main"}}},
		{name: "invalid workspace entry", depot: P4DepotData{Port: "p4:1666", Depot: "//depot/main"}, workspace: []P4DepotData{{Port: "https://p4:1666", Depot: "//depot/main", AllowSubmit: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.depot.AllowSubmit, tc.depot.AllowSync, tc.depot.AllowUnshelve = true, true, true
			project := []P4DepotData{tc.depot}
			overlayP4ActionSwitches(project, tc.workspace)
			if project[0].AllowSubmit || project[0].AllowSync || project[0].AllowUnshelve {
				t.Fatalf("project granted its own permissions: %+v", project[0])
			}
		})
	}
}
