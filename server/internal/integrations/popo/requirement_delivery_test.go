package popo

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestConfirmedNotSentFailureRequiresEvidenceAfterDispatch(t *testing.T) {
	dispatched := db.PopoBridgeCommand{DispatchedAt: pgtype.Timestamptz{Valid: true}}
	notDispatched := db.PopoBridgeCommand{}
	confirmed := true
	partial := true
	for _, tc := range []struct {
		name    string
		row     db.PopoBridgeCommand
		receipt CommandReceipt
		remote  string
		want    bool
	}{
		{name: "prepared but never dispatched", row: notDispatched, want: true},
		{name: "dispatched without extended evidence", row: dispatched, want: false},
		{name: "dispatched with not-sent evidence", row: dispatched, receipt: CommandReceipt{
			NotSent: &confirmed, EvidenceFinalized: &confirmed,
		}, want: true},
		{name: "dispatched with unverified evidence", row: dispatched, receipt: CommandReceipt{
			NotSent: &confirmed,
		}, want: false},
		{name: "remote message id proves delivery", row: notDispatched, remote: "msg-1", want: false},
		{name: "partial delivery proves delivery", row: notDispatched, receipt: CommandReceipt{
			PartialDelivery: &partial,
		}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := confirmedNotSentFailure(tc.row, tc.receipt, tc.remote); got != tc.want {
				t.Fatalf("confirmedNotSentFailure = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRequirementPayloadHashIsStableAndPrefixed(t *testing.T) {
	payload := []byte(`{"text":"hello","chat_id":"u1"}`)
	first := requirementPayloadHash(payload)
	if first != requirementPayloadHash(append([]byte(nil), payload...)) {
		t.Fatal("payload hash is not stable for equal bytes")
	}
	if first == requirementPayloadHash([]byte(`{"text":"hello","chat_id":"u2"}`)) {
		t.Fatal("payload hash ignored content drift")
	}
	if len(first) != len("sha256:")+64 || first[:len("sha256:")] != "sha256:" {
		t.Fatalf("payload hash = %q, want sha256:<64 hex>", first)
	}
}

// The prepared payload is stored as jsonb, so the bytes read back are key
// ordered and number normalized. The content identity must not change.
func TestRequirementPayloadHashSurvivesJSONBNormalization(t *testing.T) {
	written := []byte(`{"text":"hello","chat_id":"u1","requirement_revision":1,"recipient":{"user_id":"u","binding_id":"b"}}`)
	stored := []byte(`{"chat_id": "u1", "recipient": {"binding_id": "b", "user_id": "u"}, "requirement_revision": 1.0, "text": "hello"}`)
	if requirementPayloadHash(written) != requirementPayloadHash(stored) {
		t.Fatal("payload hash changed after jsonb normalization")
	}
}
