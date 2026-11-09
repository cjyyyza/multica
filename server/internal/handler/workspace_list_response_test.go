package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// TestListWorkspacesCarriesP4DepotsAndPinnedAt keeps the fork's P4 depot
// project field on the list projection now that the upstream member pin shares
// the same query. Either column missing breaks a different client: the P4
// workspace settings, or pinned workspace ordering.
func TestListWorkspacesCarriesP4DepotsAndPinnedAt(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	dbfx.Exec(t, `UPDATE workspace SET p4_depots = $1::jsonb WHERE id = $2`,
		`[{"port":"ssl:perforce.example.com:1666","depot":"//depot/game"}]`, testWorkspaceID)
	dbfx.Exec(t, `UPDATE member SET pinned_at = now() WHERE workspace_id = $1 AND user_id = $2`,
		testWorkspaceID, testUserID)
	t.Cleanup(func() {
		dbfx.Exec(t, `UPDATE workspace SET p4_depots = '[]'::jsonb WHERE id = $1`, testWorkspaceID)
		dbfx.Exec(t, `UPDATE member SET pinned_at = NULL WHERE workspace_id = $1 AND user_id = $2`,
			testWorkspaceID, testUserID)
	})

	var got []struct {
		ID       string        `json:"id"`
		P4Depots []P4DepotData `json:"p4_depots"`
		PinnedAt *string       `json:"pinned_at"`
	}
	testutil.Call(t, testHandler.ListWorkspaces, newRequest(http.MethodGet, "/api/workspaces", nil)).
		Want(http.StatusOK).JSON(&got)

	for _, workspace := range got {
		if workspace.ID != testWorkspaceID {
			continue
		}
		if len(workspace.P4Depots) != 1 || workspace.P4Depots[0].Depot != "//depot/game" {
			t.Fatalf("list p4_depots = %+v, want the workspace's depot", workspace.P4Depots)
		}
		if workspace.PinnedAt == nil {
			t.Fatal("list pinned_at = nil, want the member's pin")
		}
		return
	}
	t.Fatalf("workspace %s missing from list", testWorkspaceID)
}
