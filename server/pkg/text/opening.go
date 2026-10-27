package text

import "fmt"

// OpeningCopy is product-authored opening copy keyed by locale. Each value is
// a fmt format string; use explicit argument indexes (%[1]s, %[2]s) so a
// translation can reorder them.
//
// Render escapes every argument before substitution. Openings interpolate
// member- or source-authored text (workspace and agent names, requirement
// titles) into assistant content that chat renders as markdown, so escaping
// belongs in the renderer rather than at each call site: a new locale or a new
// caller cannot forget it.
type OpeningCopy map[Locale]string

// Render formats the copy for locale. A locale without an entry falls back to
// FallbackLocale, so callers never render a blank bubble; a missing fallback
// entry renders the empty string, which the map coverage tests forbid.
func (c OpeningCopy) Render(locale Locale, args ...string) string {
	template, ok := c[locale]
	if !ok {
		template = c[FallbackLocale]
	}
	escaped := make([]any, len(args))
	for i, arg := range args {
		escaped[i] = EscapeMarkdownInline(arg)
	}
	return fmt.Sprintf(template, escaped...)
}
