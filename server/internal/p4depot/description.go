package p4depot

import (
	"net/url"
	"regexp"
	"strings"
)

// refsTicket recognizes an optional 易协作 traceability marker in issue text.
var refsTicket = regexp.MustCompile(`(?i)\brefs\s*#\s*(\d+)\b`)

// TicketFromText returns the first `refs #<number>` ticket in text, or "".
func TicketFromText(text string) string {
	if m := refsTicket.FindStringSubmatch(text); len(m) == 2 {
		return m[1]
	}
	return ""
}

// TicketFromSourceURL extracts the ticket a 易协作 issue URL points at, e.g.
// "https://dj01.pm.netease.com/issues/110156" -> "110156". The v6 route uses
// "issue_110156", while links such as "/v6/my?issue_id=110156" carry the same
// ticket in the query instead.
//
// A path must be the issue route itself (`.../issues/<number>`); a link to
// something under the issue, such as an attachment, returns "" rather than the
// attachment's number. The host is deliberately not matched: the 易协作
// deployment host is workspace-specific, and this value only reaches the
// resolver from the issue's own source-link field, not from arbitrary text.
func TicketFromSourceURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	path := strings.TrimRight(parsed.Path, "/")
	const issueRoute = "/issues/"
	start := strings.LastIndex(path, issueRoute)
	if start < 0 {
		ticket := parsed.Query().Get("issue_id")
		if !ValidTicket(ticket) {
			return ""
		}
		return ticket
	}
	ticket := path[start+len(issueRoute):]
	if strings.Contains(ticket, "/") {
		return ""
	}
	ticket = strings.TrimPrefix(ticket, "issue_")
	if !ValidTicket(ticket) {
		return ""
	}
	return ticket
}

// ValidTicket reports whether s is a bare ticket number.
func ValidTicket(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
