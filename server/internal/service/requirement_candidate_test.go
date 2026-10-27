package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func candidateSnapshotFixture(agentID, projectID string) map[string]any {
	return map[string]any{
		"goal":               "  Ship requirement flow  ",
		"nonGoals":           []string{"No approve or issue creation"},
		"scope":              []string{"z first", "a second"},
		"constraints":        []string{"Keep existing task policy"},
		"acceptanceCriteria": []string{"One chat and one plan task"},
		"projectId":          projectID,
		"agentId":            agentID,
	}
}

func TestCanonicalRequirementCandidateSnapshot(t *testing.T) {
	agentID := "9f26f085-fc1b-4f26-9f26-000000002001"
	projectID := "9f26f085-fc1b-4f26-9f26-000000002002"
	requirement := db.Requirement{AgentID: requirementTestUUID(t, agentID), ProjectID: requirementTestUUID(t, projectID)}
	raw := requirementJSONForTest(candidateSnapshotFixture(agentID, projectID))
	snapshot, encoded, digest, err := CanonicalRequirementCandidateSnapshot(raw, requirement)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Goal != "Ship requirement flow" || snapshot.AgentID != agentID || snapshot.ProjectID == nil || *snapshot.ProjectID != projectID {
		t.Fatalf("canonical snapshot = %+v", snapshot)
	}
	wantEncoded := fmt.Sprintf(`{"acceptanceCriteria":["One chat and one plan task"],"agentId":%q,"constraints":["Keep existing task policy"],"goal":"Ship requirement flow","nonGoals":["No approve or issue creation"],"projectId":%q,"scope":["z first","a second"]}`, agentID, projectID)
	wantDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(wantEncoded)))
	if string(encoded) != wantEncoded || digest != wantDigest {
		t.Fatalf("canonical encoding/digest = %s %s, want %s %s", encoded, digest, wantEncoded, wantDigest)
	}

	for name, mutate := range map[string]func(map[string]any){
		"unknown field":      func(body map[string]any) { body["unexpected"] = true },
		"missing nonGoals":   func(body map[string]any) { delete(body, "nonGoals") },
		"empty scope":        func(body map[string]any) { body["scope"] = []string{} },
		"empty constraint":   func(body map[string]any) { body["constraints"] = []string{"  "} },
		"duplicate criteria": func(body map[string]any) { body["acceptanceCriteria"] = []string{"same", "same"} },
		"wrong agent":        func(body map[string]any) { body["agentId"] = "9f26f085-fc1b-4f26-9f26-000000002099" },
		"wrong project":      func(body map[string]any) { body["projectId"] = "9f26f085-fc1b-4f26-9f26-000000002098" },
	} {
		t.Run(name, func(t *testing.T) {
			body := candidateSnapshotFixture(agentID, projectID)
			mutate(body)
			if _, _, _, err := CanonicalRequirementCandidateSnapshot(requirementJSONForTest(body), requirement); err == nil {
				t.Fatal("invalid candidate was accepted")
			}
		})
	}
	if _, _, _, err := CanonicalRequirementCandidateSnapshot(json.RawMessage(`{"goal":"one"}{"goal":"two"}`), requirement); err == nil {
		t.Fatal("multiple JSON values were accepted")
	}
}

func TestCanonicalRequirementCandidateSnapshotAttachments(t *testing.T) {
	agentID := "9f26f085-fc1b-4f26-9f26-000000002003"
	projectID := "9f26f085-fc1b-4f26-9f26-000000002004"
	requirement := db.Requirement{AgentID: requirementTestUUID(t, agentID), ProjectID: requirementTestUUID(t, projectID)}
	missing := candidateSnapshotFixture(agentID, projectID)
	_, missingEncoded, missingDigest, err := CanonicalRequirementCandidateSnapshot(requirementJSONForTest(missing), requirement)
	if err != nil {
		t.Fatal(err)
	}
	withEmpty := candidateSnapshotFixture(agentID, projectID)
	withEmpty["attachmentIds"] = []string{}
	_, emptyEncoded, emptyDigest, err := CanonicalRequirementCandidateSnapshot(requirementJSONForTest(withEmpty), requirement)
	if err != nil {
		t.Fatal(err)
	}
	if string(emptyEncoded) != string(missingEncoded) || emptyDigest != missingDigest {
		t.Fatalf("empty attachmentIds changed canonical bytes/digest: %s %s != %s %s", emptyEncoded, emptyDigest, missingEncoded, missingDigest)
	}

	selected := candidateSnapshotFixture(agentID, projectID)
	selected["attachmentIds"] = []string{" 101 ", "102"}
	snapshot, encoded, digest, err := CanonicalRequirementCandidateSnapshot(requirementJSONForTest(selected), requirement)
	if err != nil {
		t.Fatal(err)
	}
	wantEncoded := fmt.Sprintf(`{"acceptanceCriteria":["One chat and one plan task"],"agentId":%q,"attachmentIds":["101","102"],"constraints":["Keep existing task policy"],"goal":"Ship requirement flow","nonGoals":["No approve or issue creation"],"projectId":%q,"scope":["z first","a second"]}`, agentID, projectID)
	if string(encoded) != wantEncoded || digest != fmt.Sprintf("%x", sha256.Sum256([]byte(wantEncoded))) {
		t.Fatalf("attachment canonical encoding/digest = %s %s", encoded, digest)
	}
	if len(snapshot.AttachmentIDs) != 2 || snapshot.AttachmentIDs[0] != "101" || snapshot.AttachmentIDs[1] != "102" {
		t.Fatalf("canonical attachment IDs = %+v", snapshot.AttachmentIDs)
	}
	for name, ids := range map[string][]string{
		"duplicate":            {"101", "101"},
		"duplicate after trim": {" 101 ", "101"},
		"empty":                {" "},
	} {
		t.Run(name, func(t *testing.T) {
			body := candidateSnapshotFixture(agentID, projectID)
			body["attachmentIds"] = ids
			if _, _, _, err := CanonicalRequirementCandidateSnapshot(requirementJSONForTest(body), requirement); err == nil {
				t.Fatal("invalid attachment IDs were accepted")
			}
		})
	}
}

func requirementJSONForTest(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
