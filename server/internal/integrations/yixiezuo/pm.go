package yixiezuo

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

// PMScope describes a read-only source query and its Multica destination.
type PMScope struct {
	SourceHost         string            `json:"source_host"`
	SourceProject      string            `json:"source_project"`
	AssigneeEmails     []string          `json:"assignee_emails"`
	AssigneeMap        map[string]string `json:"assignee_map,omitempty"`
	IssueType          string            `json:"issue_type"`
	ProjectID          string            `json:"project_id"`
	TriageMention      string            `json:"triage_mention"`
	CategoryPropertyID string            `json:"category_property_id,omitempty"`
}

type PMPerson struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type PMProject struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Identifier string `json:"identifier"`
}

type PMIssue struct {
	Source                   Source    `json:"source"`
	Title                    string    `json:"title"`
	Description              string    `json:"description"`
	Status                   string    `json:"status"`
	Priority                 string    `json:"priority"`
	Assignee                 PMPerson  `json:"assignee"`
	Project                  PMProject `json:"project"`
	IssueType                string    `json:"issue_type"`
	UpdatedAt                string    `json:"updated_at"`
	UpdatedAtMinutePrecision bool      `json:"updated_at_minute_precision,omitempty"`
	ObservedAt               time.Time `json:"observed_at"`
}

type PMChange struct {
	Field string `json:"field"`
	Old   string `json:"old"`
	New   string `json:"new"`
}

type PMResult struct {
	ExternalID     string     `json:"external_id"`
	IssueID        string     `json:"issue_id"`
	Identifier     string     `json:"identifier"`
	URL            string     `json:"url"`
	Assignee       string     `json:"assignee"`
	SourceAssignee string     `json:"source_assignee"`
	State          string     `json:"state"`
	Changes        []PMChange `json:"changes"`
	Warnings       []string   `json:"warnings"`
	Error          string     `json:"error"`
}

type PMTriageResult struct {
	IssueID    string `json:"issue_id"`
	Identifier string `json:"identifier"`
	CommentID  string `json:"comment_id"`
	State      string `json:"state"`
	ReasonCode string `json:"reason_code"`
	Error      string `json:"error"`
}

type PMSourceRequest struct {
	Scope PMScope `json:"scope"`
	Issue PMIssue `json:"issue"`
}

var pmMention = regexp.MustCompile(`^\[@[^\r\n]+\]\(mention://agent/([0-9a-fA-F-]{36})\)$`)
var externalMarker = regexp.MustCompile(`(?m)^[ \t]*外部单号[：:][ \t]*([1-9][0-9]*)[ \t]*\r?$`)
var sourceLinkMarker = regexp.MustCompile(`(?m)^来源链接[：:][ \t]*(\S+)[ \t]*$`)

func (s *PMScope) Normalize() error {
	s.SourceHost = strings.ToLower(strings.TrimSpace(s.SourceHost))
	if source, err := ParseSource("https://" + s.SourceHost + "/issues/1"); err != nil || source.Host != s.SourceHost {
		return fmt.Errorf("source_host must identify a *.pm.netease.com instance")
	}
	s.SourceProject = strings.TrimSpace(s.SourceProject)
	if s.SourceProject == "" || len(s.SourceProject) > 128 {
		return fmt.Errorf("source_project is required (project id or identifier)")
	}
	if len(s.AssigneeEmails) == 0 || len(s.AssigneeEmails) > 100 {
		return fmt.Errorf("assignee_emails must contain 1-100 exact source email addresses")
	}
	for i, email := range s.AssigneeEmails {
		email = strings.ToLower(strings.TrimSpace(email))
		address, err := mail.ParseAddress(email)
		if err != nil || address.Address != email {
			return fmt.Errorf("invalid assignee email")
		}
		s.AssigneeEmails[i] = email
	}
	if s.IssueType = strings.TrimSpace(s.IssueType); s.IssueType == "" {
		s.IssueType = "BUG"
	}
	if _, err := s.TriageAgentID(); err != nil {
		return err
	}
	normalizedMap := map[string]string{}
	for email, id := range s.AssigneeMap {
		email = strings.ToLower(strings.TrimSpace(email))
		address, err := mail.ParseAddress(email)
		if err != nil || address.Address != email {
			return fmt.Errorf("invalid assignee_map email")
		}
		if prior, exists := normalizedMap[email]; exists && prior != id {
			return fmt.Errorf("conflicting assignee mappings")
		}
		normalizedMap[email] = id
	}
	s.AssigneeMap = normalizedMap
	return nil
}

func (s PMScope) TriageAgentID() (string, error) {
	if strings.Count(s.TriageMention, "mention://") != 1 {
		return "", fmt.Errorf("triage_mention must contain exactly one agent mention")
	}
	match := pmMention.FindStringSubmatch(s.TriageMention)
	if len(match) != 2 {
		return "", fmt.Errorf("triage_mention must be the complete supplied [@label](mention://agent/UUID) link")
	}
	return match[1], nil
}

func PMCheckDescriptionSource(body string, expected Source) error {
	for _, marker := range sourceLinkMarker.FindAllStringSubmatch(body, -1) {
		source, err := ParseSource(marker[1])
		if err != nil || source.Host != expected.Host || source.ID != expected.ID {
			return fmt.Errorf("description source link identifies a different or invalid source issue")
		}
	}
	return nil
}

func (s PMScope) Includes(issue PMIssue) bool {
	if issue.Source.Host != s.SourceHost || !PMBugTypeMatches(s.IssueType, issue.IssueType) {
		return false
	}
	project := strings.EqualFold(s.SourceProject, issue.Project.ID) || strings.EqualFold(s.SourceProject, issue.Project.Identifier) || strings.EqualFold(s.SourceProject, issue.Project.Name)
	if strings.EqualFold(s.SourceProject, strings.Split(s.SourceHost, ".")[0]) && issue.Project.ID != "" {
		project = true
	}
	if !project {
		return false
	}
	for _, email := range s.AssigneeEmails {
		if strings.EqualFold(email, issue.Assignee.Email) {
			return true
		}
	}
	return false
}

func PMBugTypeMatches(want, actual string) bool {
	if strings.EqualFold(want, actual) {
		return true
	}
	return strings.EqualFold(want, "BUG") && strings.HasPrefix(strings.ToUpper(actual), "BUG-")
}

func PMExcludedStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "已关闭", "已取消", "已解决", "交付测试":
		return true
	default:
		return false
	}
}

func PMTerminalStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "已关闭":
		return "done"
	case "已取消":
		return "cancelled"
	default:
		return ""
	}
}

func (i PMIssue) Validate(host string) error {
	source, err := ParseSource(i.Source.URL)
	if err != nil || source != i.Source || source.Host != host {
		return fmt.Errorf("source identity does not match the configured instance")
	}
	if strings.TrimSpace(i.Title) == "" || len(i.Title) > 4096 || len(i.Description) > 2*1024*1024 {
		return fmt.Errorf("source title is missing or source material exceeds import limits")
	}
	if strings.TrimSpace(i.Status) == "" || i.ObservedAt.IsZero() {
		return fmt.Errorf("source status or observation time is missing")
	}
	if i.UpdatedAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, i.UpdatedAt); err != nil {
			return fmt.Errorf("invalid source updated_at")
		}
	}
	return nil
}

func PMDescription(issue PMIssue) string {
	return fmt.Sprintf("外部单号：%s\n来源链接：%s\n易协作状态：%s\n易协作优先级：%s\n\n%s",
		issue.Source.ID, issue.Source.URL, PMDisplay(issue.Status), PMDisplay(issue.Priority), issue.Description)
}

// PMAppendMarker operates on the locked current description, never a CLI copy.
func PMAppendMarker(body, externalID string) (string, error) {
	markers := externalMarker.FindAllStringSubmatch(body, -1)
	for _, marker := range markers {
		if marker[1] != externalID {
			return "", fmt.Errorf("description identifies a different external issue (%s)", marker[1])
		}
	}
	if len(markers) > 0 {
		return body, nil
	}
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + "\n外部单号：" + externalID, nil
}

func PMExternalID(body string) (string, error) {
	markers := externalMarker.FindAllStringSubmatch(body, -1)
	id := ""
	for _, marker := range markers {
		if id != "" && id != marker[1] {
			return "", fmt.Errorf("description contains conflicting external issue numbers")
		}
		id = marker[1]
	}
	return id, nil
}

func PMDisplay(value string) string {
	if strings.TrimSpace(value) == "" {
		return "缺失"
	}
	return value
}
