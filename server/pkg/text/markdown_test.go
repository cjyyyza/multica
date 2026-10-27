package text

import "testing"

func TestEscapeMarkdownInlineEscapesEveryInlineMetacharacter(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "plain", in: "plain text 你好", want: "plain text 你好"},
		{name: "emphasis", in: "*bold*", want: `\*bold\*`},
		{name: "underscore", in: "_em_", want: `\_em\_`},
		{name: "code span", in: "`code`", want: "\\`code\\`"},
		{name: "link brackets", in: "[link]", want: `\[link\]`},
		{name: "angle brackets", in: "<tag>", want: `\<tag>`},
		{name: "strikethrough", in: "~strike~", want: `\~strike\~`},
		{name: "backslash", in: `a\b`, want: `a\\b`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EscapeMarkdownInline(tc.in); got != tc.want {
				t.Fatalf("EscapeMarkdownInline(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The escaper must not double-escape its own output: a name containing a
// literal backslash stays one backslash to the reader.
func TestEscapeMarkdownInlineEscapesBackslashesFirst(t *testing.T) {
	if got := EscapeMarkdownInline(`a\*b`); got != `a\\\*b` {
		t.Fatalf("EscapeMarkdownInline(`a\\*b`) = %q, want %q", got, `a\\\*b`)
	}
}
