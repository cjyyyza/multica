package handler

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSwarmCardState(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"needsReview":   "open",
		"needsRevision": "open",
		"approved":      "open",
		"committed":     "merged",
		"rejected":      "closed",
		"archived":      "closed",
		"":              "open",
	}
	for in, want := range cases {
		if got := swarmCardState(in); got != want {
			t.Errorf("swarmCardState(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSwarmReviewTitle(t *testing.T) {
	t.Parallel()
	if got := swarmReviewTitle("", 12); got != "Review 12" {
		t.Fatalf("empty = %q", got)
	}
	if got := swarmReviewTitle("Fix the crash\nmore detail", 12); got != "Fix the crash" {
		t.Fatalf("multiline = %q", got)
	}
	for _, text := range []string{strings.Repeat("中", 67), strings.Repeat("中🙂a", 100), strings.Repeat("a", 201)} {
		got := swarmReviewTitle(text, 12)
		want := []rune(text)
		if len(want) > 200 {
			want = want[:200]
		}
		if !utf8.ValidString(got) || got != string(want) {
			t.Fatalf("title must preserve UTF-8 and cap at 200 characters: %q", got)
		}
	}
}

func TestSwarmHost(t *testing.T) {
	t.Parallel()
	if got := swarmHost("https://swarm.example.com/reviews"); got != "swarm.example.com" {
		t.Fatalf("host = %q", got)
	}
}
