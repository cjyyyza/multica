package handler

import (
	"encoding/json"
	"testing"
)

func TestPerforceDepotRefDropsRetiredActionSwitches(t *testing.T) {
	raw, err := validatePerforceDepotRef(json.RawMessage(`{"port":"p4:1666","depot":"//depot/main","allow_exclusive":true,"allow_submit":true,"allow_sync":true,"allow_unshelve":true,"no_auto_review":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var ref map[string]any
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"allow_exclusive", "allow_submit", "allow_sync", "allow_unshelve", "no_auto_review"} {
		if _, ok := ref[key]; ok {
			t.Fatalf("retired switch %s was persisted: %+v", key, ref)
		}
	}
}

func TestOverlayP4DepotDefaultsUsesExactCatalogMatch(t *testing.T) {
	project := []P4DepotData{
		{Port: "p4:1666", Depot: "//depot/main"},
		{Port: "p4:1666", Depot: "//depot/main", User: "project-user", Charset: "project-charset"},
		{Port: "p4:1666", Depot: "//depot/other"},
	}
	workspace := []P4DepotData{{Port: "p4:1666", Depot: "//depot/main", User: "catalog-user", Charset: "utf8", SwarmURL: "https://swarm.example"}}
	overlayP4DepotDefaults(project, workspace)
	if project[0].User != "catalog-user" || project[0].Charset != "utf8" || project[0].SwarmURL != "https://swarm.example" {
		t.Fatalf("catalog defaults missing: %+v", project[0])
	}
	if project[1].User != "project-user" || project[1].Charset != "project-charset" {
		t.Fatalf("explicit project values were replaced: %+v", project[1])
	}
	if project[2].User != "" || project[2].Charset != "" || project[2].SwarmURL != "" {
		t.Fatalf("unmatched depot inherited catalog metadata: %+v", project[2])
	}
}
