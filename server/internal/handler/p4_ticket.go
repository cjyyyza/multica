package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/p4depot"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// yixiezuoIDMetadataKey is the issue metadata key that records the 易协作
// ticket an issue came from. Imports, requirement approval and PM sync write
// it, and the issue UI renders it as the 易协作 source, so it is a structured
// ticket source in its own right — not just a copy of the import row.
const yixiezuoIDMetadataKey = "yixiezuo_id"

// resolveIssueP4Ticket finds the 易协作 ticket every changelist description on
// the shared depot must reference as `refs #<n>`. The agent never invents one:
// without a ticket the daemon refuses to open files, and a person has to link
// the ticket or put `refs #<n>` in the issue description.
//
// Sources, in order of authority:
//
//  1. The 易协作 import link. An imported issue IS a ticket, so its external_id
//     is the ticket and needs no marker in the text. A non-numeric external_id
//     is ignored rather than passed through: `refs #DJ-1` is not a ticket the
//     depot's submit trigger accepts.
//  2. Structured issue fields written when the task was linked to its source:
//     the `yixiezuo_id` metadata and the workspace's 易协作链接 or custom
//     易协作单子 property. A person can set them without editing the description.
//  3. `refs #<n>` in the title, then in the description — the manual path for
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
		// A failed read is not "no ticket": fall through to the structured
		// field and text sources rather than reporting a ticket the issue may
		// not have.
		slog.Warn("resolve p4 ticket: load 易协作 import failed",
			"issue_id", uuidToString(issue.ID), "error", err)
	}
	if ticket := ticketFromIssueMetadata(issue.Metadata); ticket != "" {
		return ticket
	}
	if ticket := h.ticketFromIssueProperties(ctx, issue); ticket != "" {
		return ticket
	}
	if ticket := p4depot.TicketFromText(issue.Title); ticket != "" {
		return ticket
	}
	return p4depot.TicketFromText(issue.Description.String)
}

// ticketFromIssueMetadata reads the source ticket recorded on the issue. Every
// server writer stores a JSON string, but metadata set through the CLI/API may
// arrive as a JSON number, so both primitive shapes are accepted.
func ticketFromIssueMetadata(raw []byte) string {
	var metadata map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &metadata) != nil {
		return ""
	}
	return ticketFromJSONScalar(metadata[yixiezuoIDMetadataKey])
}

// ticketFromIssueProperties reads the stable 易协作 properties from the issue.
// Imports populate 易协作链接 from the source URL; workspaces can also use the
// custom 易协作单子 field. Only values are read, so a missing definition or an
// empty value degrades to the next structured or text source.
func (h *Handler) ticketFromIssueProperties(ctx context.Context, issue db.Issue) string {
	if len(issue.Properties) == 0 {
		return ""
	}
	for _, name := range [...]string{yixiezuo.SourceLinkPropertyName, "易协作单子"} {
		value, err := h.Queries.GetIssuePropertyValueByName(ctx, db.GetIssuePropertyValueByNameParams{
			IssueID:     issue.ID,
			WorkspaceID: issue.WorkspaceID,
			Name:        name,
		})
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				slog.Warn("resolve p4 ticket: load 易协作 ticket property failed",
					"issue_id", uuidToString(issue.ID), "property_name", name, "error", err)
			}
			continue
		}
		for _, candidate := range propertyStrings(value) {
			candidate = strings.TrimSpace(candidate)
			if p4depot.ValidTicket(candidate) {
				return candidate
			}
			if ticket := p4depot.TicketFromSourceURL(candidate); ticket != "" {
				return ticket
			}
		}
	}
	return ""
}

// propertyStrings flattens the primitive shapes issue property values use: a
// string for scalar fields and an array of strings for multi-value fields.
// Numbers and booleans cannot name a ticket and are ignored.
func propertyStrings(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, propertyStrings(item)...)
		}
		return out
	}
	return nil
}

// ticketFromJSONScalar accepts the primitive JSON values issue metadata allows
// for the source key: a string (every server writer) or a number (metadata
// written through the CLI/API without quoting). Anything else is not a ticket.
func ticketFromJSONScalar(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&decoded) != nil {
		return ""
	}
	switch value := decoded.(type) {
	case string:
		if ticket := strings.TrimSpace(value); p4depot.ValidTicket(ticket) {
			return ticket
		}
	case json.Number:
		if ticket := value.String(); p4depot.ValidTicket(ticket) {
			return ticket
		}
	}
	return ""
}
