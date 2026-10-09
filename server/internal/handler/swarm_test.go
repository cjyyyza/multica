package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
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

func TestSwarmReviewResponseKeepsRawState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw  string
		card string
	}{
		{"needsReview", "open"},
		{"pending", "open"},
		{"approved", "open"},
		{"needsRevision", "open"},
		{"committed", "merged"},
		{"rejected", "closed"},
		{"archived", "closed"},
	}
	for _, tc := range cases {
		got := swarmReviewToPRResponse(db.SwarmReview{
			SwarmUrl:     "https://swarm.example.com",
			ReviewNumber: 77,
			Changelist:   "1001",
			Title:        "Fix the crash",
			Author:       "alice",
			State:        tc.raw,
			HtmlUrl:      "https://swarm.example.com/reviews/77",
		})
		if got.Provider != "swarm" || got.State != tc.card {
			t.Fatalf("swarmReviewToPRResponse(%q) card = %q/%q", tc.raw, got.Provider, got.State)
		}
		if got.SwarmState == nil || *got.SwarmState != tc.raw {
			t.Fatalf("swarm_state(%q) = %v", tc.raw, got.SwarmState)
		}
		if got.Branch == nil || *got.Branch != "cl/1001" {
			t.Fatalf("branch = %v", got.Branch)
		}
	}

	raw, err := json.Marshal(swarmReviewToPRResponse(db.SwarmReview{
		State: "needsRevision", SwarmUrl: "https://swarm.example.com", HtmlUrl: "https://swarm.example.com/reviews/1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"swarm_state":"needsRevision"`) {
		t.Fatalf("json = %s", raw)
	}
	githubRaw, err := json.Marshal(GitHubPullRequestResponse{Provider: "github", State: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(githubRaw), "swarm_state") {
		t.Fatalf("github json includes swarm_state: %s", githubRaw)
	}
}

func TestSwarmHost(t *testing.T) {
	t.Parallel()
	if got := swarmHost("https://swarm.example.com/reviews"); got != "swarm.example.com" {
		t.Fatalf("host = %q", got)
	}
}
