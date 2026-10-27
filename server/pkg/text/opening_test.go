package text

import "testing"

func TestOpeningCopyRenderFallsBackToTheFallbackLocale(t *testing.T) {
	copy := OpeningCopy{LocaleEn: "Hello %[1]s", LocaleFr: "Bonjour %[1]s"}
	if got, want := copy.Render(Locale("de"), "Multica"), "Hello Multica"; got != want {
		t.Fatalf("Render(unknown) = %q, want %q", got, want)
	}
	if got, want := copy.Render(LocaleFr, "Multica"), "Bonjour Multica"; got != want {
		t.Fatalf("Render(fr) = %q, want %q", got, want)
	}
}

func TestOpeningCopyRenderEscapesEveryArgument(t *testing.T) {
	copy := OpeningCopy{LocaleEn: "%[1]s and %[2]s"}
	got := copy.Render(LocaleEn, "**Ops**", "`prod`")
	if want := "\\*\\*Ops\\*\\* and \\`prod\\`"; got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
}

func TestOpeningCopyRenderSupportsReorderedArguments(t *testing.T) {
	copy := OpeningCopy{LocaleEn: "%[2]s in %[1]s"}
	if got, want := copy.Render(LocaleEn, "Venus", "Mika"), "Mika in Venus"; got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
}
