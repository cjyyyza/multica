package notify

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
)

type Source struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func (s Source) Validate() error {
	if s.Kind != "requirement" && s.Kind != "issue" {
		return errors.New("notification source must be a requirement or issue")
	}
	if s.ID == "" || strings.ContainsAny(s.ID, "/\\?#\r\n") || s.ID == "." || s.ID == ".." {
		return errors.New("invalid notification source ID")
	}
	return nil
}

type Snapshot struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Revision    int64  `json:"revision"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	QuestionID  string `json:"questionId,omitempty"`
	Question    string `json:"question,omitempty"`
	CandidateID string `json:"candidateId,omitempty"`
}

func (s Snapshot) terminal() bool { return s.Status == "done" || s.Status == "cancelled" }

// signal ignores ordinary edits while retaining distinct questions and approval candidates.
func (s Snapshot) signal() string {
	switch s.Status {
	case "blocked":
		return digest([]string{s.ID, s.Status, s.QuestionID, s.Question})
	case "awaiting_approval":
		return digest([]string{s.ID, s.Status, s.CandidateID})
	default:
		return digest([]string{s.ID, s.Status})
	}
}

type Reader struct {
	Client *cli.APIClient
	Source Source
	AppURL string
	slug   string
}

func ValidateAppURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.Scheme != "https" && parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("app URL must be an absolute HTTP(S) base URL without credentials, query or fragment")
	}
	return nil
}

type issueDetail struct {
	ID                      string  `json:"id"`
	WorkspaceID             string  `json:"workspace_id"`
	Identifier              string  `json:"identifier"`
	Title                   string  `json:"title"`
	Status                  string  `json:"status"`
	StatusCategory          string  `json:"status_category"`
	Revision                int64   `json:"revision"`
	RequirementID           *string `json:"requirement_id"`
	RequirementBindingKnown *bool   `json:"requirement_binding_known"`
}

func knownStatus(status string) bool {
	switch status {
	case "backlog", "todo", "in_progress", "blocked", "in_review", "done", "cancelled":
		return true
	}
	return false
}

func (r *Reader) Read(ctx context.Context) (Snapshot, error) {
	var snapshot Snapshot
	if err := r.Source.Validate(); err != nil {
		return snapshot, err
	}
	if err := ValidateAppURL(r.AppURL); err != nil {
		return snapshot, err
	}
	if r.Client == nil || r.Client.WorkspaceID == "" {
		return snapshot, errors.New("notification reader needs a workspace client")
	}
	ctx, cancel := cli.APIContext(ctx)
	defer cancel()
	if r.slug == "" {
		var workspace struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		}
		if err := r.Client.GetJSON(ctx, "/api/workspaces/"+url.PathEscape(r.Client.WorkspaceID), &workspace); err != nil {
			return snapshot, err
		}
		if workspace.ID != r.Client.WorkspaceID || workspace.Slug == "" || strings.ContainsAny(workspace.Slug, "/\\?#") {
			return snapshot, errors.New("malformed notification workspace response")
		}
		r.slug = workspace.Slug
	}
	base := strings.TrimRight(r.AppURL, "/") + "/" + url.PathEscape(r.slug)
	if r.Source.Kind == "issue" {
		var issue issueDetail
		if err := r.Client.GetJSON(ctx, "/api/issues/"+url.PathEscape(r.Source.ID), &issue); err != nil {
			return snapshot, err
		}
		if issue.ID == "" || issue.WorkspaceID != r.Client.WorkspaceID || issue.Identifier == "" || issue.Title == "" || issue.Revision < 1 {
			return snapshot, errors.New("malformed notification issue response")
		}
		if issue.RequirementID != nil && *issue.RequirementID != "" {
			return snapshot, fmt.Errorf("issue belongs to requirement %s; watch --requirement instead to keep one notification stream", *issue.RequirementID)
		}
		if issue.RequirementBindingKnown == nil || !*issue.RequirementBindingKnown {
			return snapshot, errors.New("issue requirement binding is unknown; upgrade or repair the server before watching this issue")
		}
		status := issue.StatusCategory
		if status == "" {
			status = issue.Status
		}
		if !knownStatus(status) {
			return snapshot, fmt.Errorf("unknown issue lifecycle category %q", status)
		}
		return Snapshot{ID: issue.ID, Status: status, Revision: issue.Revision, Title: issue.Identifier + ": " + issue.Title, URL: base + "/issues/" + url.PathEscape(issue.Identifier)}, nil
	}
	var requirement struct {
		ID             string `json:"id"`
		WorkspaceID    string `json:"workspaceId"`
		State          string `json:"state"`
		Revision       int64  `json:"revision"`
		IssueDeleted   bool   `json:"issueDeleted"`
		SourceSnapshot struct {
			Title string `json:"title"`
		} `json:"sourceSnapshot"`
		Issue           *issueDetail `json:"issue"`
		LatestCandidate *struct {
			ID string `json:"id"`
		} `json:"latestCandidate"`
		BlockedQuestion *struct {
			CommentID string `json:"commentId"`
			Content   string `json:"content"`
		} `json:"blockedQuestion"`
	}
	path := "/api/workspaces/" + url.PathEscape(r.Client.WorkspaceID) + "/requirements/" + url.PathEscape(r.Source.ID)
	if err := r.Client.GetJSON(ctx, path, &requirement); err != nil {
		return snapshot, err
	}
	if requirement.ID != r.Source.ID || requirement.WorkspaceID != r.Client.WorkspaceID || requirement.Revision < 1 {
		return snapshot, errors.New("malformed notification requirement response")
	}
	if requirement.IssueDeleted {
		return snapshot, errors.New("requirement issue was deleted; notification watch stopped")
	}
	switch requirement.State {
	case "intake_pending", "clarifying", "awaiting_approval", "executing", "blocked", "in_review", "done", "cancelled":
	default:
		return snapshot, fmt.Errorf("unknown requirement lifecycle state %q", requirement.State)
	}
	snapshot = Snapshot{ID: requirement.ID, Status: requirement.State, Revision: requirement.Revision, Title: requirement.SourceSnapshot.Title, URL: base + "/chat?requirement=" + url.QueryEscape(requirement.ID)}
	if requirement.Issue != nil {
		snapshot.Title = requirement.Issue.Identifier + ": " + requirement.Issue.Title
	}
	if strings.TrimSpace(snapshot.Title) == "" {
		snapshot.Title = "Requirement " + requirement.ID
	}
	if requirement.LatestCandidate != nil {
		snapshot.CandidateID = requirement.LatestCandidate.ID
	}
	if requirement.BlockedQuestion != nil {
		snapshot.QuestionID, snapshot.Question = requirement.BlockedQuestion.CommentID, requirement.BlockedQuestion.Content
		if snapshot.QuestionID == "" || strings.TrimSpace(snapshot.Question) == "" {
			return Snapshot{}, errors.New("malformed requirement blocked question")
		}
	}
	return snapshot, nil
}
