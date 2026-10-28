package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestProjectLocalDirectoryRebindReplacesOnlyOwnerlessRows(t *testing.T) {
	for _, samePath := range []bool{false, true} {
		name := "new path"
		if samePath {
			name = "same ref"
		}
		t.Run(name, func(t *testing.T) {
			projectID := dbfx.Project(t, "Ownerless directory rebind")
			legacyID := insertPrivateDirectory(t, projectID, "", "rebind-desktop", "/legacy/secret")
			otherID := insertPrivateDirectory(t, projectID, "", "other-desktop", "/legacy/other")
			path := "/fresh/user-supplied"
			if samePath {
				path = "/legacy/secret"
			}
			var created ProjectResourceResponse
			resp := testutil.Call(t, testHandler.CreateProjectResource, privateDirectoryRequest(http.MethodPost, projectID, "", testUserID, map[string]any{
				"resource_type": "local_directory", "resource_ref": map[string]any{"local_path": path, "daemon_id": "rebind-desktop", "execution_mode": "in_place"},
			})).Want(http.StatusCreated)
			resp.JSON(&created)
			if created.ID == legacyID || created.CreatedBy == nil || *created.CreatedBy != testUserID || created.Label != nil {
				t.Fatalf("rebind inherited legacy identity or metadata: %+v", created)
			}
			if !samePath && strings.Contains(resp.Text(), "/legacy/secret") {
				t.Fatal("rebind leaked the legacy path")
			}
			var oldCount, otherCount int
			dbfx.QueryRow(t, "SELECT count(*) FROM project_resource WHERE id=$1", legacyID).Scan(&oldCount)
			dbfx.QueryRow(t, "SELECT count(*) FROM project_resource WHERE id=$1", otherID).Scan(&otherCount)
			if oldCount != 0 || otherCount != 1 {
				t.Fatalf("old=%d unrelated=%d", oldCount, otherCount)
			}
			testutil.Call(t, testHandler.CreateProjectResource, privateDirectoryRequest(http.MethodPost, projectID, "", testUserID, map[string]any{
				"resource_type": "local_directory", "resource_ref": map[string]any{"local_path": "/another/path", "daemon_id": "rebind-desktop", "execution_mode": "in_place"},
			})).Want(http.StatusConflict)
		})
	}
}

func TestProjectLocalDirectoryRebindDoesNotReplaceOwnedBinding(t *testing.T) {
	otherUser := dbfx.User(t, "Rebind peer", "rebind-peer@example.test")
	dbfx.Member(t, testWorkspaceID, otherUser, "member")
	projectID := dbfx.Project(t, "Owned binding rebind protection")
	ownedID := insertPrivateDirectory(t, projectID, otherUser, "shared-desktop", "/private/peer")
	legacyID := insertPrivateDirectory(t, projectID, "", "shared-desktop", "/legacy/unknown")
	testutil.Call(t, testHandler.CreateProjectResource, privateDirectoryRequest(http.MethodPost, projectID, "", testUserID, map[string]any{
		"resource_type": "local_directory", "resource_ref": map[string]any{"local_path": "/fresh/path", "daemon_id": "shared-desktop", "execution_mode": "in_place"},
	})).Want(http.StatusConflict)
	var count int
	dbfx.QueryRow(t, "SELECT count(*) FROM project_resource WHERE id=ANY($1::uuid[])", []string{ownedID, legacyID}).Scan(&count)
	if count != 2 {
		t.Fatalf("failed rebind changed existing records: %d", count)
	}
}

func TestProjectLocalDirectoryConcurrentRebindHasOneWinner(t *testing.T) {
	projectID := dbfx.Project(t, "Concurrent directory rebind")
	insertPrivateDirectory(t, projectID, "", "concurrent-desktop", "/legacy/unknown")
	start := make(chan struct{})
	statuses := make(chan int, 2)
	for _, path := range []string{"/fresh/one", "/fresh/two"} {
		go func() {
			<-start
			body, _ := json.Marshal(map[string]any{"resource_type": "local_directory", "resource_ref": map[string]any{
				"local_path": path, "daemon_id": "concurrent-desktop", "execution_mode": "in_place",
			}})
			req := privateDirectoryRequest(http.MethodPost, projectID, "", testUserID, json.RawMessage(body))
			statuses <- testutil.Call(t, testHandler.CreateProjectResource, req).Code
		}()
	}
	close(start)
	first, second := <-statuses, <-statuses
	if !((first == http.StatusCreated && second == http.StatusConflict) || (second == http.StatusCreated && first == http.StatusConflict)) {
		t.Fatalf("concurrent statuses=%d,%d", first, second)
	}
	var count int
	dbfx.QueryRow(t, "SELECT count(*) FROM project_resource WHERE project_id=$1", projectID).Scan(&count)
	if count != 1 {
		t.Fatalf("concurrent rebind left %d records", count)
	}
}
