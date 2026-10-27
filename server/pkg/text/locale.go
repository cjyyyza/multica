package text

import "strings"

// Locale names a language that has product-authored copy in this package.
//
// The set mirrors SUPPORTED_LOCALES in packages/core/i18n/types.ts and
// supportedLanguages in server/internal/handler/auth.go: every language a user
// may persist in user.language has an entry here. Keep all three lists in sync
// when adding a locale.
type Locale string

const (
	LocaleEn     Locale = "en"
	LocaleZhHans Locale = "zh-Hans"
	LocaleJa     Locale = "ja"
	LocaleKo     Locale = "ko"
	LocaleFr     Locale = "fr"

	// FallbackLocale is the copy an empty or unrecognized profile language
	// gets. English is the repository's source language, so a missing choice
	// renders something a reader can act on instead of a blank bubble.
	FallbackLocale = LocaleEn
)

// ResolveLocale maps a raw user.language value onto a copy Locale.
//
// Writes are validated against the five supported values, but rows can still
// carry legacy or client-supplied spellings ("zh", "zh_CN", "fr-FR"), so match
// the primary subtag case-insensitively. Any zh variant gets the Simplified
// Chinese pack because that is the only Chinese copy the product ships. An
// empty or unrecognized value is not evidence of a language: fall back to
// English rather than reusing a deployment or request default.
func ResolveLocale(raw string) Locale {
	primary := strings.ToLower(strings.TrimSpace(raw))
	if index := strings.IndexAny(primary, "-_"); index >= 0 {
		primary = primary[:index]
	}
	switch primary {
	case "en":
		return LocaleEn
	case "zh":
		return LocaleZhHans
	case "ja":
		return LocaleJa
	case "ko":
		return LocaleKo
	case "fr":
		return LocaleFr
	default:
		return FallbackLocale
	}
}
