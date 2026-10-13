package p4depot

import (
	"regexp"
	"strings"
)

// refsTicket matches the `refs #<number>` marker every submitted changelist
// description on the shared depot must carry. The number is the 易协作 ticket.
var refsTicket = regexp.MustCompile(`(?i)\brefs\s*#\s*(\d+)\b`)

// TicketFromText returns the first `refs #<number>` ticket in text, or "".
func TicketFromText(text string) string {
	if m := refsTicket.FindStringSubmatch(text); len(m) == 2 {
		return m[1]
	}
	return ""
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

// TaskTrailerPrefix starts the line that ties a changelist to its Multica
// issue, so a later run on the same issue can find and reuse it.
const TaskTrailerPrefix = "Multica: "

// ChangeDescription builds a task changelist description:
//
//	refs #<ticket> <title>
//
//	Multica: <issueKey>
//
// An empty issueKey omits the trailer.
func ChangeDescription(ticket, title, issueKey string) string {
	title = strings.Join(strings.Fields(title), " ")
	var b strings.Builder
	b.WriteString("refs #")
	b.WriteString(strings.TrimSpace(ticket))
	if title != "" {
		b.WriteString(" ")
		b.WriteString(title)
	}
	b.WriteString("\n")
	if key := strings.TrimSpace(issueKey); key != "" {
		b.WriteString("\n")
		b.WriteString(TaskTrailerPrefix)
		b.WriteString(key)
		b.WriteString("\n")
	}
	return b.String()
}

// DescriptionHasTaskTrailer reports whether desc carries the trailer for issueKey.
func DescriptionHasTaskTrailer(desc, issueKey string) bool {
	key := strings.TrimSpace(issueKey)
	if key == "" {
		return false
	}
	for _, line := range strings.Split(desc, "\n") {
		if strings.EqualFold(strings.TrimSpace(line), TaskTrailerPrefix+key) {
			return true
		}
	}
	return false
}
