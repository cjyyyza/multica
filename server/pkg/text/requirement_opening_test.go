package text

import (
	"strings"
	"testing"
)

// Every locale the product can render a requirement opening in must have copy,
// and each entry must be that locale's own text. A missing entry would leave a
// blank bubble as the first message of the requirement chat.
func TestBuildRequirementOpeningCoversEverySupportedLocale(t *testing.T) {
	cases := []struct {
		locale Locale
		label  string
	}{
		{LocaleEn, "Requirement: title"},
		{LocaleZhHans, "需求：title"},
		{LocaleJa, "要件：title"},
		{LocaleKo, "요구사항: title"},
		{LocaleFr, "Exigence : title"},
	}
	if got, want := len(requirementOpeningCopy), len(cases); got != want {
		t.Fatalf("requirement opening copy covers %d locales, want %d", got, want)
	}
	for _, tc := range cases {
		t.Run(string(tc.locale), func(t *testing.T) {
			opening := BuildRequirementOpening(tc.locale, "title")
			if strings.TrimSpace(opening) == "" {
				t.Fatalf("locale %q has no opening", tc.locale)
			}
			if strings.Contains(opening, "%!") {
				t.Fatalf("locale %q has a broken format verb: %s", tc.locale, opening)
			}
			if !strings.Contains(opening, tc.label) {
				t.Fatalf("locale %q opening does not read as %q:\n%s", tc.locale, tc.label, opening)
			}
		})
	}
}

// The title comes from an external source snapshot and chat renders assistant
// content as markdown, so every inline metacharacter has to be escaped before
// it reaches the transcript.
func TestBuildRequirementOpeningEscapesTheSourceTitle(t *testing.T) {
	title := "交付 *v2* _draft_ `prod` [beta] <ops> ~done~ C:\\temp"
	opening := BuildRequirementOpening(LocaleEn, title)

	wantEscaped := "交付 \\*v2\\* \\_draft\\_ \\`prod\\` \\[beta\\] \\<ops> \\~done\\~ C:\\\\temp"
	if !strings.Contains(opening, wantEscaped) {
		t.Fatalf("opening does not contain the escaped title %q:\n%s", wantEscaped, opening)
	}
	for _, raw := range []string{"*v2*", "_draft_", "`prod`", "[beta]", "~done~"} {
		if strings.Contains(opening, raw) {
			t.Fatalf("raw markdown %q survived unescaped in:\n%s", raw, opening)
		}
	}
}

func TestBuildRequirementOpeningPreventsSourceTitleBlockInjection(t *testing.T) {
	cases := []struct {
		name      string
		title     string
		wantTitle string
	}{
		{
			name:      "LF",
			title:     "real title\n## fake section\n> fake quote\n- fake item",
			wantTitle: "real title ## fake section > fake quote - fake item",
		},
		{
			name:      "CRLF",
			title:     "real title\r\n## fake section\r\n> fake quote\r\n- fake item",
			wantTitle: "real title ## fake section > fake quote - fake item",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opening := BuildRequirementOpening(LocaleEn, tc.title)

			if strings.Contains(opening, "\r") {
				t.Fatalf("opening retains a carriage return from the source title: %q", opening)
			}
			for _, block := range []string{"## fake section", "> fake quote", "- fake item"} {
				if strings.Contains(opening, "\n"+block) {
					t.Fatalf("source title created markdown block %q:\n%s", block, opening)
				}
			}
			firstLine, _, _ := strings.Cut(opening, "\n")
			if want := "Requirement: " + tc.wantTitle; firstLine != want {
				t.Fatalf("opening first line = %q, want %q", firstLine, want)
			}
		})
	}
}

func TestBuildRequirementOpeningFallsBackToEnglishForUnknownLocales(t *testing.T) {
	want := BuildRequirementOpening(LocaleEn, "title")
	for _, locale := range []Locale{"", Locale("de"), Locale("de-DE")} {
		if got := BuildRequirementOpening(ResolveLocale(string(locale)), "title"); got != want {
			t.Fatalf("locale %q opening = %q, want english %q", locale, got, want)
		}
	}
}
