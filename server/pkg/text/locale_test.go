package text

import "testing"

func TestResolveLocale(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want Locale
	}{
		{name: "empty", raw: "", want: LocaleEn},
		{name: "blank", raw: "   ", want: LocaleEn},
		{name: "english", raw: "en", want: LocaleEn},
		{name: "english region", raw: "en-US", want: LocaleEn},
		{name: "simplified chinese", raw: "zh-Hans", want: LocaleZhHans},
		{name: "legacy chinese", raw: "zh", want: LocaleZhHans},
		{name: "chinese region", raw: "zh_CN", want: LocaleZhHans},
		{name: "chinese traditional falls back to the shipped chinese pack", raw: "zh-Hant", want: LocaleZhHans},
		{name: "japanese", raw: "ja", want: LocaleJa},
		{name: "japanese region", raw: "ja-JP", want: LocaleJa},
		{name: "korean", raw: "ko", want: LocaleKo},
		{name: "korean region", raw: "ko-KR", want: LocaleKo},
		{name: "french", raw: "fr", want: LocaleFr},
		{name: "french region", raw: "FR-fr", want: LocaleFr},
		{name: "case insensitive", raw: " ZH-HANS ", want: LocaleZhHans},
		{name: "unknown", raw: "de", want: LocaleEn},
		{name: "unknown region", raw: "de-DE", want: LocaleEn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveLocale(tc.raw); got != tc.want {
				t.Fatalf("ResolveLocale(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
