// Package text holds the text rendering shared by product-authored chat copy:
// markdown escaping, locale resolution, and localized opening builders.
//
// It depends only on the standard library, so both the handler and the service
// layers can import it without adding a dependency edge between them.
package text

import "strings"

// markdownInlineEscaper neutralizes the inline markdown metacharacters in
// member- or source-authored text before it is interpolated into rendered copy.
// Chat renders assistant content as markdown, so a workspace called `**Ops**`
// would otherwise bold part of the sentence, and one containing a backtick
// would open a code span that swallows the rest of the line.
//
// Backslash goes first so it never double-escapes an escape this adds. Only
// inline constructs are covered: callers interpolate this text mid-sentence,
// never at the start of a line, so block markers like `#` and `-` are inert.
var markdownInlineEscaper = strings.NewReplacer(
	`\`, `\\`,
	"`", "\\`",
	`*`, `\*`,
	`_`, `\_`,
	`~`, `\~`,
	`[`, `\[`,
	`]`, `\]`,
	`<`, `\<`,
)

// EscapeMarkdownInline escapes the inline markdown metacharacters in s.
func EscapeMarkdownInline(s string) string {
	return markdownInlineEscaper.Replace(s)
}
