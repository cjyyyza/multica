package yixiezuo

import (
	"testing"
	"time"
)

func TestMapIncomingStatusDefaultAndCatalog(t *testing.T) {
	t.Parallel()
	if got := MapIncomingStatus("开发中", nil, nil, nil, "todo"); got != "in_progress" {
		t.Fatalf("default map: got %q", got)
	}
	if got := MapIncomingStatus("in_review", nil, []string{"in_review"}, []string{"In Review"}, "todo"); got != "in_review" {
		t.Fatalf("catalog key: got %q", got)
	}
	if got := MapIncomingStatus("验收通过", nil, []string{"approved"}, []string{"验收通过"}, "todo"); got != "approved" {
		t.Fatalf("catalog name: got %q", got)
	}
	if got := MapIncomingStatus("未知列", nil, []string{"todo"}, []string{"Todo"}, "todo"); got != "todo" {
		t.Fatalf("unknown keeps fallback: got %q", got)
	}
	if got := MapIncomingStatus("进行中", map[string]string{"进行中": "blocked"}, nil, nil, "todo"); got != "blocked" {
		t.Fatalf("configured overlay: got %q", got)
	}
}

func TestMapOutgoingStatusRoundTrip(t *testing.T) {
	t.Parallel()
	if got := MapOutgoingStatus("in_progress", nil); got != "开发中" {
		t.Fatalf("outgoing default: got %q", got)
	}
	if got := MapOutgoingStatus("blocked", map[string]string{"卡住": "blocked"}); got != "卡住" {
		t.Fatalf("outgoing configured: got %q", got)
	}
	if got := MapOutgoingStatus("custom_qa", nil); got != "custom_qa" {
		t.Fatalf("unmapped key passthrough: got %q", got)
	}
	if got := ResolveOutgoingStatusName("in_progress", nil, []string{"进行中", "新建"}); got != "进行中" {
		t.Fatalf("catalog alias: got %q", got)
	}
}

func TestPreferRemoteLastWriteWins(t *testing.T) {
	t.Parallel()
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if !PreferRemote(older, newer, false) {
		t.Fatal("clean local must accept remote")
	}
	if PreferRemote(older, newer, true) {
		t.Fatal("dirty newer local must keep local")
	}
	if !PreferRemote(newer, older, true) {
		t.Fatal("newer remote must win over dirty local")
	}
	if !PreferRemote(newer, newer, true) {
		t.Fatal("tie prefers remote so both sides converge")
	}
}

func TestPriorityMaps(t *testing.T) {
	t.Parallel()
	if got := MapIncomingPriority("高", "medium"); got != "high" {
		t.Fatalf("incoming: got %q", got)
	}
	if got := MapIncomingPriority("mystery", "medium"); got != "medium" {
		t.Fatalf("unknown incoming: got %q", got)
	}
	if got := MapOutgoingPriority("urgent"); got != "紧急" {
		t.Fatalf("outgoing: got %q", got)
	}
}
