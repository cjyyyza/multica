package p4depot

import (
	"fmt"
	"strings"
	"unicode"
)

// ViewSource parses both quoted paths and the exclusion/overlay/ditto markers.
// A client view selects mappings; callers still authorize each resolved file.
func ViewSource(line string) (string, bool, error) {
	var fields []string
	var field strings.Builder
	quoted := false
	for _, ch := range strings.TrimSpace(line) {
		switch {
		case ch == '"':
			quoted = !quoted
		case unicode.IsSpace(ch) && !quoted:
			if field.Len() > 0 {
				fields = append(fields, field.String())
				field.Reset()
			}
		default:
			field.WriteRune(ch)
		}
	}
	if field.Len() > 0 {
		fields = append(fields, field.String())
	}
	if quoted || len(fields) != 2 {
		return "", false, fmt.Errorf("invalid Perforce view mapping %q", line)
	}
	source := fields[0]
	excluded := strings.HasPrefix(source, "-")
	if strings.ContainsAny(source[:1], "+-&") {
		source = source[1:]
	}
	if !strings.HasPrefix(source, "//") || !strings.HasPrefix(fields[1], "//") {
		return "", false, fmt.Errorf("invalid Perforce view mapping %q", line)
	}
	return source, excluded, nil
}

func ViewOverlaps(scope, source string) bool {
	scope = strings.TrimSuffix(strings.TrimRight(scope, "/"), "/...")
	if !strings.HasPrefix(scope, "//") || !strings.HasPrefix(source, "//") {
		return false
	}
	// Consume the scope through the view pattern. Once its directory prefix
	// is matched, any remaining pattern can select a file inside that scope.
	states := make([]bool, len(source)+1)
	states[0] = true
	closure := func() {
		for i := 0; i < len(source); i++ {
			if states[i] {
				if strings.HasPrefix(source[i:], "...") {
					states[i+3] = true
				} else if source[i] == '*' {
					states[i+1] = true
				}
			}
		}
	}
	closure()
	for offset, ch := range []byte(scope + "/") {
		if offset == len(scope) && states[len(source)] {
			return true
		}
		next := make([]bool, len(states))
		for i := 0; i < len(source); i++ {
			if !states[i] {
				continue
			}
			switch {
			case strings.HasPrefix(source[i:], "..."):
				next[i] = true
			case source[i] == '*':
				if ch != '/' {
					next[i] = true
				}
			case source[i] == ch:
				next[i+1] = true
			}
		}
		states = next
		closure()
	}
	for _, reachable := range states {
		if reachable {
			return true
		}
	}
	return source == scope
}

// ViewLiteralPrefix preserves partial path components before either wildcard.
func ViewLiteralPrefix(source string) string {
	end := len(source)
	if i := strings.IndexByte(source, '*'); i >= 0 {
		end = i
	}
	if i := strings.Index(source, "..."); i >= 0 && i < end {
		end = i
	}
	return source[:end]
}
