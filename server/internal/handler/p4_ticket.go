package handler

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/p4depot"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// resolveIssueP4Ticket finds the 易协作 ticket every changelist description on
// the shared depot must reference as `refs #<n>`. The agent never invents one:
// without a ticket the daemon refuses to open files, and a person has to put
// `refs #<n>` in the issue description.
//
// Sources, in order of authority:
//
//  1. The 易协作 import link. An imported issue IS a ticket, so its external_id
//     is the ticket and needs no marker in the text. A non-numeric external_id
//     is ignored rather than passed through: `refs #DJ-1` is not a ticket the
//     depot's submit trigger accepts.
//  2. `refs #<n>` in the title, then in the description — the manual path for
//     an issue created in Multica.
func (h *Handler) resolveIssueP4Ticket(ctx context.Context, issue db.Issue) string {
	imported, err := h.Queries.GetYixiezuoImportByIssue(ctx, db.GetYixiezuoImportByIssueParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
	})
	switch {
	case err == nil:
		if p4depot.ValidTicket(imported.ExternalID) {
			return imported.ExternalID
		}
	case !errors.Is(err, pgx.ErrNoRows):
		// A failed read is not "no ticket": fall through to the text sources
		// rather than reporting a ticket the issue may not have.
		slog.Warn("resolve p4 ticket: load 易协作 import failed",
			"issue_id", uuidToString(issue.ID), "error", err)
	}
	if ticket := p4depot.TicketFromText(issue.Title); ticket != "" {
		return ticket
	}
	return p4depot.TicketFromText(issue.Description.String)
}
