package yixiezuo

import "testing"

func TestResolveRequirementTemplateURL(t *testing.T) {
	const custom = "https://dj01.pm.netease.com/issues/12"
	cases := []struct {
		name, configured, override, want string
	}{
		{name: "default", want: DefaultRequirementTemplateURL},
		{name: "saved robot template", configured: custom, want: custom},
		{name: "command url wins", configured: custom, override: "https://dj01.pm.netease.com/issues/99", want: "https://dj01.pm.netease.com/issues/99"},
		{name: "blank override keeps saved", configured: "  " + custom + "  ", override: "  ", want: custom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveRequirementTemplateURL(tc.configured, tc.override); got != tc.want {
				t.Fatalf("template = %q, want %q", got, tc.want)
			}
		})
	}
	if _, err := ParseSource(DefaultRequirementTemplateURL); err != nil {
		t.Fatal(err)
	}
}
