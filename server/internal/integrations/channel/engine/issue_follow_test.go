package engine

import "testing"

func TestParseFollowCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input      string
		kind       FollowCommandKind
		identifier string
		body       string
	}{
		{input: "/reply HAN-12 please continue", kind: FollowCommandReply, identifier: "HAN-12", body: "please continue"},
		{input: "/reply HAN-12\nmore detail", kind: FollowCommandReply, identifier: "HAN-12", body: "more detail"},
		{input: "/reply", kind: FollowCommandReply},
		{input: "/status HAN-3", kind: FollowCommandStatus, identifier: "HAN-3"},
		{input: "/stop HAN-3", kind: FollowCommandStop, identifier: "HAN-3"},
		{input: "/stop", kind: FollowCommandStop},
		{input: "  /stop MUL-9 leftover", kind: FollowCommandStop, identifier: "MUL-9"},
	}
	for _, tc := range tests {
		got, ok := ParseFollowCommand(tc.input)
		if !ok {
			t.Fatalf("ParseFollowCommand(%q) missing", tc.input)
		}
		if got.Kind != tc.kind || got.Identifier != tc.identifier || got.Body != tc.body {
			t.Fatalf("ParseFollowCommand(%q) = %+v", tc.input, got)
		}
	}
	if _, ok := ParseFollowCommand("/Reply HAN-1 hi"); ok {
		t.Fatal("mixed-case /Reply must not match")
	}
	if _, ok := ParseFollowCommand("please /reply HAN-1 later"); ok {
		t.Fatal("inline /reply must not match")
	}
	if _, ok := ParseFollowCommand("/issue HAN-1"); ok {
		t.Fatal("/issue is not a follow command")
	}
}
