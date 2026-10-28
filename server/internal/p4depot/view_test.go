package p4depot

import "testing"

func TestViewOverlapsKeepsWildcardNamespaceAndSuffix(t *testing.T) {
	for _, tc := range []struct {
		scope, source string
		want          bool
	}{
		{"//depot/Other", "//depot/Secret*/...", false},
		{"//depot/Secret", "//depot/Secret*/...", true},
		{"//depot/SecretTeam", "//depot/Secret*/...", true},
		{"//depot/SecretTeam/Public", "//depot/Secret*/Private/...", false},
		{"//depot/SecretTeam/Private", "//depot/Secret*/Private/...", true},
		{"//depot/Other", "//depot/.../Secret*", true},
		{"//depot/Team", "//depot/Team/*.cpp", true},
		{"//depot/Team/file.cpp", "//depot/Team/*.cpp", true},
		{"//depot/Team/Sub", "//depot/Team/*.cpp", false},
		{"//depot/Team", "//depot/...", true},
		{"//depot/Team/...", "//depot/Team/Space Dir/...", true},
		{"//depot/Team2", "//depot/Team/...", false},
	} {
		t.Run(tc.scope+" in "+tc.source, func(t *testing.T) {
			if got := ViewOverlaps(tc.scope, tc.source); got != tc.want {
				t.Fatalf("ViewOverlaps = %v, want %v", got, tc.want)
			}
		})
	}
}
