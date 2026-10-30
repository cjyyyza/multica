package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestExternalRequirementCandidateRequiresCompatibleReader(t *testing.T) {
	enableRequirementFlowForTest(t)
	leader := dbfx.Agent(t, "Candidate compatibility leader", testRuntimeID)
	squad := dbfx.Squad(t, "Candidate compatibility squad", leader)
	detail := createRequirementDelegationForTest(t, requirementDelegationInput("candidate-compatibility", squad), "candidate-compatibility-create")
	for _, capability := range []string{"", "unrelated-capability", protocol.AppCapabilityRequirementCandidateProvenanceV1} {
		r := requirementRequest(testUserID, http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/requirements/"+detail.ID,
			nil, "", "id", testWorkspaceID, "requirementId", detail.ID)
		r.Header.Set("X-Client-Capabilities", capability)
		if capability != protocol.AppCapabilityRequirementCandidateProvenanceV1 {
			var upgrade struct {
				Code    string `json:"code"`
				Message string `json:"error"`
			}
			testutil.Call(t, testHandler.GetRequirementByID, r).Want(http.StatusUpgradeRequired).JSON(&upgrade)
			if upgrade.Code != "requirement_client_upgrade_required" || upgrade.Message == "" {
				t.Fatalf("legacy reader got no actionable upgrade response: %+v", upgrade)
			}
			continue
		}
		var read service.RequirementDetail
		testutil.Call(t, testHandler.GetRequirementByID, r).Want(http.StatusOK).JSON(&read)
		if read.LatestCandidate == nil || read.LatestCandidate.ID != detail.LatestCandidate.ID || read.LatestCandidate.CreatedByTaskID != nil {
			t.Fatalf("compatible reader lost the external candidate provenance: %+v", read.LatestCandidate)
		}
	}
}
