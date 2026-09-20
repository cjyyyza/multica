package yixiezuo

import "testing"

func TestParseCommandPreservesExplicitConfirmationBoundary(t *testing.T) {
	for _, tc := range []struct {
		text, action                 string
		confirmed, recognized, valid bool
	}{
		{"/yixiezuo preview https://dj01.pm.netease.com/issues/7", "preview", false, true, true},
		{"/yixiezuo import op --project project --confirm", "import", true, true, true},
		{"/yixiezuo import op", "import", false, true, true},
		{"/yixiezuo publish MUL-7 --status Ready for QA\nVerified in the editor.", "publish", false, true, true},
		{"/yixiezuo confirm review", "confirm", false, true, true},
		{"/yixiezuo publish MUL-7", "publish", false, true, false},
		{"/yixiezuo import op --yes", "import", false, true, false},
		{"/yixiezuo import op --confirm --confirm", "import", true, true, false},
		{"Please read this:\n/yixiezuo confirm review", "", false, false, true},
		{"/yixiezuo-other preview url", "", false, false, true},
	} {
		t.Run(tc.text, func(t *testing.T) {
			got, recognized, err := ParseCommand(tc.text)
			if recognized != tc.recognized || (err == nil) != tc.valid || (recognized && (got.Action != tc.action || got.Confirmed != tc.confirmed)) {
				t.Fatalf("command=%+v recognized=%v err=%v", got, recognized, err)
			}
		})
	}
}
