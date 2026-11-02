package handler

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRecoveryOwnerIsExcludedFromTriggerNotices(t *testing.T) {
	owner := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	other := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	ownerID := uuidToString(owner)
	otherID := uuidToString(other)
	targets := []commentMentionTarget{
		{
			TargetType: "agent", TargetID: ownerID,
			unusable: &blockedRuntimeNotice{agent: db.Agent{ID: owner}},
		},
		{
			TargetType: "agent", TargetID: ownerID,
			Status: DispatchBlocked, ReasonCode: ReasonInvocationNotAllowed,
		},
		{
			TargetType: "squad", TargetID: "squad-owned", ExecAgentID: ownerID,
			unusable: &blockedRuntimeNotice{agent: db.Agent{ID: owner}},
		},
		{
			TargetType: "agent", TargetID: otherID,
			Status: DispatchBlocked, ReasonCode: ReasonInvocationNotAllowed,
		},
		{
			TargetType: "agent", TargetID: otherID, ExecAgentID: otherID,
			unusable: &blockedRuntimeNotice{agent: db.Agent{ID: other}},
		},
	}

	noticeTargets := commentTargetsWithoutRecoveryOwner(targets, owner, true)
	if len(noticeTargets) != 2 {
		t.Fatalf("notice targets = %+v, want the two non-owner targets", noticeTargets)
	}
	for _, target := range noticeTargets {
		if commentTargetIsRecoveryOwner(target, owner, ownerID) {
			t.Fatalf("recovery owner remained in notice targets: %+v", target)
		}
	}
	outcomes := commentTriggerOutcomes(noticeTargets, nil)
	denied := 0
	for _, outcome := range outcomes {
		if outcome.Status == DispatchBlocked && outcome.ReasonCode == ReasonInvocationNotAllowed {
			denied++
			if outcome.TargetID != otherID {
				t.Fatalf("invocation notice target = %s, want %s", outcome.TargetID, otherID)
			}
		}
	}
	if denied != 1 {
		t.Fatalf("invocation notices = %d, want the other agent only", denied)
	}
	var runtimeNotices int
	for _, target := range noticeTargets {
		if target.unusable != nil {
			runtimeNotices++
			if target.unusable.agent.ID == owner {
				t.Fatal("recovery owner runtime refusal reached notices")
			}
		}
	}
	if runtimeNotices != 1 {
		t.Fatalf("runtime notices = %d, want the other agent only", runtimeNotices)
	}

	if got := commentTargetsWithoutRecoveryOwner(targets, owner, false); len(got) != len(targets) {
		t.Fatalf("unowned comment dropped targets: %d", len(got))
	}
}
