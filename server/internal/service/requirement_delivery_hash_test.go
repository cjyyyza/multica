package service

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestRequirementDeliveryRequestHashCoversIdentityAndSnapshot(t *testing.T) {
	requirementID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	occurrenceID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	recipientID := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	base := RequirementDeliveryIdentity{
		RequirementID: requirementID, Kind: "requirement_blocked", OccurrenceID: occurrenceID,
		RecipientUserID: recipientID, RequirementRevision: 4,
		IssueRevision: pgtype.Int8{Int64: 7, Valid: true},
		Snapshot:      []byte(`{"version":1,"title":"Fix login","state":"question"}`),
	}
	hash := RequirementDeliveryRequestHash(base)
	if hash == "" || hash != RequirementDeliveryRequestHash(base) {
		t.Fatal("request hash is not stable")
	}
	otherSnapshot := base
	otherSnapshot.Snapshot = []byte(`{"version":1,"title":"Fix signup","state":"question"}`)
	if hash == RequirementDeliveryRequestHash(otherSnapshot) {
		t.Fatal("request hash ignored snapshot content")
	}
	otherRevision := base
	otherRevision.RequirementRevision++
	if hash == RequirementDeliveryRequestHash(otherRevision) {
		t.Fatal("request hash ignored the requirement revision")
	}
	otherRecipient := base
	otherRecipient.RecipientUserID = pgtype.UUID{Bytes: [16]byte{9}, Valid: true}
	if hash == RequirementDeliveryRequestHash(otherRecipient) {
		t.Fatal("request hash ignored the recipient")
	}
}

// The snapshot is stored as jsonb, which reorders keys, rewrites whitespace and
// preserves number scale. The request hash must describe the same delivery
// before and after that round trip, or recovery reports a spurious conflict.
func TestRequirementDeliveryRequestHashSurvivesJSONBNormalization(t *testing.T) {
	identity := RequirementDeliveryIdentity{
		RequirementID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, Kind: "requirement_draft",
		OccurrenceID:    pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
		RecipientUserID: pgtype.UUID{Bytes: [16]byte{3}, Valid: true}, RequirementRevision: 4,
		Snapshot: []byte(`{"version":1,"title":"Fix login","state":"draft","entity_refs":{"issue_revision":1.0,"requirement_id":"r"}}`),
	}
	stored := identity
	stored.Snapshot = []byte(`{"state": "draft", "title": "Fix login", "version": 1, "entity_refs": {"issue_revision": 1.0, "requirement_id": "r"}}`)
	if RequirementDeliveryRequestHash(identity) != RequirementDeliveryRequestHash(stored) {
		t.Fatal("request hash changed after jsonb normalization")
	}

	// Large integers must stay exact instead of collapsing through float64.
	first, second := identity, identity
	first.Snapshot = []byte(`{"version":1,"entity_refs":{"issue_revision":9007199254740992}}`)
	second.Snapshot = []byte(`{"version":1,"entity_refs":{"issue_revision":9007199254740993}}`)
	if RequirementDeliveryRequestHash(first) == RequirementDeliveryRequestHash(second) {
		t.Fatal("request hash lost integer precision")
	}
}
