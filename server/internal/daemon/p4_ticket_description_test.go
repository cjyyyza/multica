package daemon

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/p4depot"
)

func TestTaskChangeDescriptionKeepsBoundTicket(t *testing.T) {
	b := &p4TaskBinding{ticket: "123", issueKey: "MUL-1", title: "repair"}
	for _, description := range []string{"refs #999 another issue", "refs #TODO repair"} {
		t.Run(description, func(t *testing.T) {
			if got, err := taskChangeDescription(b, description); err == nil {
				t.Fatalf("accepted a description with a foreign or invalid ticket: %q", got)
			}
		})
	}
	for _, description := range []string{"repair", "", "refs #123 repair", "REFS  # 123 repair"} {
		got, err := taskChangeDescription(b, description)
		if err != nil || p4depot.TicketFromText(got) != b.ticket || !p4depot.DescriptionHasTaskTrailer(got, b.issueKey) {
			t.Fatalf("description = %q, error = %v", got, err)
		}
	}
}
