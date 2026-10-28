package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func privateDirectoryRequest(method, projectID, resourceID, userID string, body any) *http.Request {
	req := newRequest(method, "/api/projects/"+projectID+"/resources", body)
	req.Header.Set("X-User-ID", userID)
	return testutil.WithURLParams(req, "id", projectID, "resourceId", resourceID)
}

func TestProjectLocalDirectoryClaimPrivacy(t *testing.T) {
	otherUser := dbfx.User(t, "P4 runtime peer", "p4-runtime-peer@example.test")
	dbfx.Member(t, testWorkspaceID, otherUser, "member")
	projectID := dbfx.Project(t, "Private claim directories")
	const alicePath = "/private/alice/p4-client"
	const bobPath = "/private/bob/p4-client"
	const legacyPath = "/private/unknown/p4-client"
	insertPrivateDirectory(t, projectID, testUserID, "alice-desktop", alicePath)
	insertPrivateDirectory(t, projectID, otherUser, "bob-desktop", bobPath)
	insertPrivateDirectory(t, projectID, "", "legacy-desktop", legacyPath)
	dbfx.Insert(t, "project_resource", testutil.Cols{
		"project_id": projectID, "workspace_id": testWorkspaceID, "resource_type": "perforce_depot",
		"resource_ref": `{"port":"p4:1666","depot":"//depot/game"}`, "created_by": otherUser,
	})
	for _, tc := range []struct {
		name, ownerID, wantPath string
		forbidden               []string
	}{
		{"alice runtime", testUserID, alicePath, []string{bobPath, legacyPath}},
		{"bob runtime", otherUser, bobPath, []string{alicePath, legacyPath}},
		{"ownerless runtime", "", "", []string{alicePath, bobPath, legacyPath}},
		{"depot-only lookup", "", "", []string{alicePath, bobPath, legacyPath}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := &db.AgentRuntime{}
			if tc.ownerID != "" {
				runtime.OwnerID = parseUUID(tc.ownerID)
			}
			if tc.name == "depot-only lookup" {
				runtime = nil
			}
			project, err := testHandler.resolveClaimProjectContext(context.Background(), parseUUID(projectID), parseUUID(testWorkspaceID), runtime)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(project.Resources)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantPath != "" && !strings.Contains(string(wire), tc.wantPath) {
				t.Error("runtime's own directory missing from claim context")
			}
			for _, path := range tc.forbidden {
				if strings.Contains(string(wire), path) {
					t.Errorf("claim context leaked %q", path)
				}
			}
			if len(project.P4Depots) != 1 || project.P4Depots[0].Depot != "//depot/game" {
				t.Errorf("shared depot context lost: %+v", project.P4Depots)
			}
		})
	}
}

func insertPrivateDirectory(t *testing.T, projectID, ownerID, daemonID, path string) string {
	t.Helper()
	ref, err := json.Marshal(map[string]string{"local_path": path, "daemon_id": daemonID, "execution_mode": "in_place"})
	if err != nil {
		t.Fatal(err)
	}
	cols := testutil.Cols{
		"project_id": projectID, "workspace_id": testWorkspaceID,
		"resource_type": "local_directory", "resource_ref": string(ref), "label": path,
	}
	if ownerID != "" {
		cols["created_by"] = ownerID
	}
	return dbfx.Insert(t, "project_resource", cols)
}

func TestProjectLocalDirectoryPrivacy(t *testing.T) {
	otherUser := dbfx.User(t, "P4 directory peer", "p4-directory-peer@example.test")
	dbfx.Member(t, testWorkspaceID, otherUser, "member")
	projectID := dbfx.Project(t, "Private P4 directories")
	const ownPath = "/private/alice/p4"
	const peerPath = "/private/bob/p4"
	const legacyPath = "/private/unknown/p4"
	ownID := insertPrivateDirectory(t, projectID, testUserID, "alice-desktop", ownPath)
	// Same daemon, different user: machine identity is not directory ownership.
	peerID := insertPrivateDirectory(t, projectID, otherUser, "alice-desktop", peerPath)
	otherMachineID := insertPrivateDirectory(t, projectID, testUserID, "alice-laptop", "/private/alice/laptop")
	legacyID := insertPrivateDirectory(t, projectID, "", "legacy-desktop", legacyPath)
	depotID := dbfx.Insert(t, "project_resource", testutil.Cols{
		"project_id": projectID, "workspace_id": testWorkspaceID, "resource_type": "perforce_depot",
		"resource_ref": `{"port":"p4:1666","depot":"//depot/game"}`, "created_by": otherUser,
	})

	for _, tc := range []struct {
		name, userID string
		wantIDs      []string
		hiddenPaths  []string
	}{
		{"alice", testUserID, []string{ownID, otherMachineID, depotID}, []string{peerPath, legacyPath}},
		{"bob", otherUser, []string{peerID, depotID}, []string{ownPath, "/private/alice/laptop", legacyPath}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := testutil.Call(t, testHandler.ListProjectResources, privateDirectoryRequest("GET", projectID, "", tc.userID, nil)).Want(http.StatusOK)
			var list struct {
				Resources []ProjectResourceResponse `json:"resources"`
				Total     int                       `json:"total"`
			}
			resp.JSON(&list)
			if list.Total != len(tc.wantIDs) || len(list.Resources) != len(tc.wantIDs) {
				t.Errorf("visible resources = %d/%d, want %d", list.Total, len(list.Resources), len(tc.wantIDs))
			}
			for _, id := range tc.wantIDs {
				found := false
				for _, resource := range list.Resources {
					found = found || resource.ID == id
				}
				if !found {
					t.Errorf("visible resource %s missing", id)
				}
			}
			for _, path := range tc.hiddenPaths {
				if strings.Contains(resp.Text(), path) {
					t.Errorf("list leaked another user's path %q", path)
				}
			}
		})
	}

	for _, target := range []struct{ resourceID, userID string }{
		{peerID, testUserID}, {ownID, otherUser}, {legacyID, testUserID}, {legacyID, otherUser},
	} {
		for _, method := range []string{"PATCH", "DELETE"} {
			t.Run(method+"/"+target.resourceID+"/"+target.userID, func(t *testing.T) {
				handler := testHandler.UpdateProjectResource
				if method == "DELETE" {
					handler = testHandler.DeleteProjectResource
				}
				resp := testutil.Call(t, handler, privateDirectoryRequest(method, projectID, target.resourceID, target.userID, map[string]any{"label": "changed"})).Want(http.StatusNotFound)
				if strings.Contains(resp.Text(), "/private/") {
					t.Fatal("rejected mutation leaked a local path")
				}
				var label string
				dbfx.QueryRow(t, "SELECT label FROM project_resource WHERE id=$1", target.resourceID).Scan(&label)
				if label == "changed" {
					t.Fatal("rejected mutation changed another user's binding")
				}
			})
		}
	}
	testutil.Call(t, testHandler.UpdateProjectResource, privateDirectoryRequest("PATCH", projectID, otherMachineID, testUserID, map[string]any{"label": "Alice P4"})).Want(http.StatusOK)
	testutil.Call(t, testHandler.DeleteProjectResource, privateDirectoryRequest("DELETE", projectID, ownID, testUserID, nil)).Want(http.StatusNoContent)
	testutil.Call(t, testHandler.ListProjectResources, privateDirectoryRequest("GET", projectID, "", "", nil)).Want(http.StatusUnauthorized)
}

func TestProjectLocalDirectoryEventsDoNotBroadcastPaths(t *testing.T) {
	h := *testHandler
	h.Bus = events.New()
	var delivered []events.Event
	h.Bus.SubscribeAll(func(event events.Event) { delivered = append(delivered, event) })
	projectID := dbfx.Project(t, "Private directory broadcasts")
	const path = "/private/alice/secret-p4-client"
	var created ProjectResourceResponse
	testutil.Call(t, h.CreateProjectResource, privateDirectoryRequest("POST", projectID, "", testUserID, map[string]any{
		"resource_type": "local_directory", "resource_ref": map[string]any{"local_path": path, "daemon_id": "private-events", "execution_mode": "in_place"}, "label": path,
	})).Want(http.StatusCreated).JSON(&created)
	dbfx.Cleanup(t, "DELETE FROM project_resource WHERE id=$1", created.ID)
	if created.CreatedBy == nil || *created.CreatedBy != testUserID || !strings.Contains(string(created.ResourceRef), path) {
		t.Fatalf("creator must receive their own binding: %+v", created)
	}
	testutil.Call(t, h.UpdateProjectResource, privateDirectoryRequest("PATCH", projectID, created.ID, testUserID, map[string]any{"label": path + "/renamed"})).Want(http.StatusOK)
	// Bundled project creation has its own resource-event publishing path.
	var bundled struct {
		ProjectResponse
		Resources []ProjectResourceResponse `json:"resources"`
	}
	testutil.Call(t, h.CreateProject, newRequest("POST", "/api/projects", map[string]any{
		"title": "Bundled private directory", "resources": []map[string]any{{
			"resource_type": "local_directory", "resource_ref": map[string]any{"local_path": path, "daemon_id": "bundled-private-events"}, "label": path,
		}},
	})).Want(http.StatusCreated).JSON(&bundled)
	dbfx.Cleanup(t, "DELETE FROM project WHERE id=$1", bundled.ID)
	for _, resource := range bundled.Resources {
		dbfx.Cleanup(t, "DELETE FROM project_resource WHERE id=$1", resource.ID)
	}
	resourceEvents := 0
	for _, event := range delivered {
		payload, err := json.Marshal(event.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(payload), path) || strings.Contains(string(payload), "local_path") {
			t.Errorf("workspace event %s leaked a private path: %s", event.Type, payload)
		}
		if event.Type != protocol.EventProjectResourceCreated && event.Type != protocol.EventProjectResourceUpdated {
			continue
		}
		resourceEvents++
		var ids struct {
			ProjectID  string `json:"project_id"`
			ResourceID string `json:"resource_id"`
		}
		if err := json.Unmarshal(payload, &ids); err != nil || ids.ProjectID == "" || ids.ResourceID == "" {
			t.Errorf("resource event lacks safe invalidation identifiers: %s", payload)
		}
	}
	if resourceEvents != 3 {
		t.Fatalf("resource events = %d, want create/update/bundled create", resourceEvents)
	}
}
