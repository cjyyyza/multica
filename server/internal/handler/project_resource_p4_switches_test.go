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

func TestOverlayP4MetadataUsesOnlyExactCatalogDefaults(t *testing.T) {
	workspace := []P4DepotData{{Port: "p4:1666", Depot: "//depot/main", User: "catalog-user", Charset: "utf8", SwarmURL: "https://swarm.example", AllowSubmit: true}}
	project := []P4DepotData{
		{Port: "p4:1666", Depot: "//depot/main"},
		{Port: "p4:1666", Depot: "//depot/main", User: "project-user", Charset: "utf8-bom", SwarmURL: "https://project.example"},
		{Port: "p4:1666", Depot: "//depot/main/sub"},
	}
	overlayP4ActionSwitches(project, workspace)
	if project[0].User != "catalog-user" || project[0].Charset != "utf8" || project[0].SwarmURL != "https://swarm.example" || !project[0].AllowSubmit {
		t.Fatalf("exact catalog defaults lost: %+v", project[0])
	}
	if project[1].User != "project-user" || project[1].Charset != "utf8-bom" || project[1].SwarmURL != "https://swarm.example" {
		t.Fatalf("explicit project metadata overwritten: %+v", project[1])
	}
	if project[2].User != "" || project[2].Charset != "" || project[2].SwarmURL != "" || project[2].AllowSubmit {
		t.Fatalf("parent catalog entry granted a child scope: %+v", project[2])
	}
}

func TestProjectP4SwarmOriginCannotOverrideCatalog(t *testing.T) {
	for _, catalog := range [][]P4DepotData{
		{{Port: "p4:1666", Depot: "//depot/main", SwarmURL: "https://trusted.example"}},
		{{Port: "p4:1666", Depot: "//depot/other", SwarmURL: "https://trusted.example"}},
		nil,
	} {
		raw, err := validatePerforceDepotRef(json.RawMessage(`{"port":"p4:1666","depot":"//depot/main","swarm_url":"https://attacker.example"}`))
		if err != nil {
			t.Fatal(err)
		}
		var depot P4DepotData
		if err := json.Unmarshal(raw, &depot); err != nil {
			t.Fatal(err)
		}
		project := []P4DepotData{depot}
		overlayP4ActionSwitches(project, catalog)
		want := ""
		if len(catalog) > 0 && catalog[0].Depot == depot.Depot {
			want = catalog[0].SwarmURL
		}
		if project[0].SwarmURL != want {
			t.Fatalf("untrusted origin survived catalog overlay: %+v", project[0])
		}
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
