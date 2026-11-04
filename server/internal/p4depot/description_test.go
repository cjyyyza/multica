package p4depot

import "testing"

// A task created in Multica links its 易协作 ticket through the issue's
// source-link field. That field stores the issue URL, so the ticket has to be
// read out of the route rather than out of a `refs #` marker.
func TestTicketFromSourceURL(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"https://dj01.pm.netease.com/issues/110156":               "110156",
		"https://dj01.pm.netease.com/issues/110156/":              "110156",
		" https://dj01.pm.netease.com/issues/7?tab=comments#c1 ":  "7",
		"https://dj01.pm.netease.com/project/DJ01/issues/42":      "42",
		"https://dj01.pm.netease.com/v6/my?issue_id=109425":       "109425",
		"http://pm.internal.example/issues/000123":                "000123",
		"https://dj01.pm.netease.com/issues/DJ-7":                 "",
		"https://dj01.pm.netease.com/issues/110156/attachments/9": "",
		"https://dj01.pm.netease.com/issues/":                     "",
		"https://dj01.pm.netease.com/tasks/110156":                "",
		"file:///issues/110156":                                   "",
		"not a url":                                               "",
		"":                                                        "",
	}
	for raw, want := range cases {
		if got := TicketFromSourceURL(raw); got != want {
			t.Errorf("TicketFromSourceURL(%q) = %q, want %q", raw, got, want)
		}
	}
}
