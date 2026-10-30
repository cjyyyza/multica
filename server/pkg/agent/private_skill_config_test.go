package agent

import "testing"

func TestPrivateSkillConfigUsesRuntimeVersionContract(t *testing.T) {
	for _, test := range []struct {
		provider, version string
		want              bool
	}{
		{"opencode", "1.18.31", true}, {"opencode", "opencode v2.2.0", false},
		{"codearts", "1.0.0", false}, {"deveco", "1.0.0", true}, {"cursor", "1.0.0", false},
	} {
		if got := SupportsPrivateSkillConfig(test.provider, test.version, true); got != test.want {
			t.Fatalf("%s %s native config-directory support = %v, want %v", test.provider, test.version, got, test.want)
		}
	}
}
