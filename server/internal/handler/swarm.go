package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type upsertSwarmReviewRequest struct {
	ReviewID    int    `json:"review_id"`
	SwarmURL    string `json:"swarm_url"`
	Changelist  string `json:"changelist"`
	Description string `json:"description"`
	Author      string `json:"author"`
	State       string `json:"state"`
	HTMLURL     string `json:"html_url"`
}

// UpsertSwarmReviewForIssue records a Helix Swarm review and links it to this
// issue. Additional issues named in the review description (DJ01-7) in the
// same workspace are also linked, matching GitHub title/body auto-link.
func (h *Handler) UpsertSwarmReviewForIssue(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req upsertSwarmReviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ReviewID <= 0 {
		writeError(w, http.StatusBadRequest, "review_id must be a positive Swarm review id")
		return
	}
	swarmURL := strings.TrimRight(strings.TrimSpace(req.SwarmURL), "/")
	if swarmURL == "" {
		writeError(w, http.StatusBadRequest, "swarm_url is required")
		return
	}
	htmlURL := strings.TrimSpace(req.HTMLURL)
	if htmlURL == "" {
		htmlURL = swarmURL + "/reviews/" + strconv.Itoa(req.ReviewID)
	}
	desc := strings.TrimSpace(req.Description)
	title := swarmReviewTitle(desc, req.ReviewID)
	state := strings.TrimSpace(req.State)
	if state == "" {
		state = "needsReview"
	}

	review, err := h.Queries.UpsertSwarmReview(r.Context(), db.UpsertSwarmReviewParams{
		WorkspaceID:  issue.WorkspaceID,
		SwarmUrl:     swarmURL,
		ReviewNumber: int32(req.ReviewID),
		Changelist:   strings.TrimSpace(req.Changelist),
		Title:        title,
		Description:  desc,
		Author:       strings.TrimSpace(req.Author),
		State:        state,
		HtmlUrl:      htmlURL,
	})
	if err != nil {
		slog.Error("upsert swarm review", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save swarm review")
		return
	}

	closeIntent := closingIdentifierRe.MatchString(desc)
	linked := map[string]struct{}{uuidToString(issue.ID): {}}
	if err := h.Queries.LinkIssueSwarmReview(r.Context(), db.LinkIssueSwarmReviewParams{
		IssueID:       issue.ID,
		SwarmReviewID: review.ID,
		WorkspaceID:   issue.WorkspaceID,
		CloseIntent:   closeIntent,
	}); err != nil {
		slog.Error("link swarm review", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to link swarm review")
		return
	}
	h.linkSwarmReviewByIdentifiers(r.Context(), issue.WorkspaceID, review.ID, desc, closeIntent, linked)

	ids := make([]string, 0, len(linked))
	for id := range linked {
		ids = append(ids, id)
	}
	h.publish(protocol.EventPullRequestLinked, uuidToString(issue.WorkspaceID), "system", "", map[string]any{
		"pull_request":     swarmReviewToPRResponse(review),
		"linked_issue_ids": ids,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"pull_request":     swarmReviewToPRResponse(review),
		"linked_issue_ids": ids,
	})
}

func (h *Handler) linkSwarmReviewByIdentifiers(ctx context.Context, workspaceID, reviewID pgtype.UUID, description string, closeIntent bool, linked map[string]struct{}) {
	matches := identifierRe.FindAllString(description, 8)
	for _, match := range matches {
		issue, ok := h.resolveIssueByIdentifier(ctx, match, uuidToString(workspaceID))
		if !ok {
			continue
		}
		id := uuidToString(issue.ID)
		if _, exists := linked[id]; exists {
			continue
		}
		if err := h.Queries.LinkIssueSwarmReview(ctx, db.LinkIssueSwarmReviewParams{
			IssueID:       issue.ID,
			SwarmReviewID: reviewID,
			WorkspaceID:   workspaceID,
			CloseIntent:   closeIntent,
		}); err != nil {
			slog.Warn("link swarm review by identifier", "error", err, "identifier", match)
			continue
		}
		linked[id] = struct{}{}
	}
}

func (h *Handler) listSwarmReviewsForIssue(ctx context.Context, issue db.Issue) []GitHubPullRequestResponse {
	rows, err := h.Queries.ListSwarmReviewsByIssue(ctx, db.ListSwarmReviewsByIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Error("list swarm reviews", "error", err, "issue_id", uuidToString(issue.ID))
		return nil
	}
	out := make([]GitHubPullRequestResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, swarmReviewRowToPRResponse(row))
	}
	return out
}

func swarmReviewTitle(description string, reviewID int) string {
	line := strings.TrimSpace(description)
	if line == "" {
		return "Review " + strconv.Itoa(reviewID)
	}
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	if line == "" {
		return "Review " + strconv.Itoa(reviewID)
	}
	const max = 200
	runes := []rune(line)
	if len(runes) > max {
		return string(runes[:max])
	}
	return line
}

func swarmCardState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "committed":
		return "merged"
	case "rejected", "archived":
		return "closed"
	default:
		return "open"
	}
}

func swarmHost(swarmURL string) string {
	u, err := url.Parse(swarmURL)
	if err != nil || strings.TrimSpace(u.Host) == "" {
		return "swarm"
	}
	return u.Host
}

func swarmReviewToPRResponse(r db.SwarmReview) GitHubPullRequestResponse {
	changelist := strings.TrimSpace(r.Changelist)
	var branch *string
	if changelist != "" {
		b := "cl/" + changelist
		branch = &b
	}
	var author *string
	if r.Author != "" {
		author = &r.Author
	}
	return GitHubPullRequestResponse{
		ID:           uuidToString(r.ID),
		Provider:     "swarm",
		WorkspaceID:  uuidToString(r.WorkspaceID),
		RepoOwner:    "swarm",
		RepoName:     swarmHost(r.SwarmUrl),
		Number:       r.ReviewNumber,
		Title:        r.Title,
		State:        swarmCardState(r.State),
		HtmlURL:      r.HtmlUrl,
		Branch:       branch,
		AuthorLogin:  author,
		PRCreatedAt:  timestampToString(r.CreatedAt),
		PRUpdatedAt:  timestampToString(r.UpdatedAt),
		MergedAt:     swarmMergedAt(r.State, r.UpdatedAt),
		ClosedAt:     swarmClosedAt(r.State, r.UpdatedAt),
		Additions:    0,
		Deletions:    0,
		ChangedFiles: 0,
	}
}

func swarmReviewRowToPRResponse(r db.ListSwarmReviewsByIssueRow) GitHubPullRequestResponse {
	return swarmReviewToPRResponse(db.SwarmReview{
		ID:           r.ID,
		WorkspaceID:  r.WorkspaceID,
		SwarmUrl:     r.SwarmUrl,
		ReviewNumber: r.ReviewNumber,
		Changelist:   r.Changelist,
		Title:        r.Title,
		Description:  r.Description,
		Author:       r.Author,
		State:        r.State,
		HtmlUrl:      r.HtmlUrl,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
	})
}

func swarmMergedAt(state string, updated pgtype.Timestamptz) *string {
	if swarmCardState(state) != "merged" {
		return nil
	}
	return timestampToPtr(updated)
}

func swarmClosedAt(state string, updated pgtype.Timestamptz) *string {
	if swarmCardState(state) != "closed" {
		return nil
	}
	return timestampToPtr(updated)
}
