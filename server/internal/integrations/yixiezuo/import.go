package yixiezuo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Source struct {
	Host string `json:"host"`
	ID   string `json:"id"`
	URL  string `json:"url"`
}

type Attachment struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	DownloadURL string `json:"download_url,omitempty"`
	LocalID     string `json:"local_id,omitempty"`
	LocalURL    string `json:"local_url,omitempty"`
}

type Comment struct {
	ID        string `json:"id"`
	Author    string `json:"author"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

type Status struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type SourceField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Snapshot is immutable import material. Remote state never overwrites an issue.
type Snapshot struct {
	Source      Source        `json:"source"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Status      string        `json:"status"`
	Priority    string        `json:"priority"`
	UpdatedAt   string        `json:"updated_at"`
	LockVersion *int64        `json:"lock_version"`
	Attachments []Attachment  `json:"attachments"`
	Comments    []Comment     `json:"comments"`
	Statuses    []Status      `json:"statuses"`
	Warnings    []string      `json:"warnings"`
	Digest      string        `json:"digest"`
	Fields      []SourceField `json:"fields"`
}

type OperationPayload struct {
	Source         Source        `json:"source"`
	ExpectedDigest string        `json:"expected_digest,omitempty"`
	Revision       int64         `json:"revision,omitempty"`
	StatusName     string        `json:"status_name,omitempty"`
	Summary        string        `json:"summary,omitempty"`
	IssueURL       string        `json:"issue_url,omitempty"`
	Channel        *ChannelScope `json:"channel,omitempty"`
}

// ChannelScope binds a review to the authenticated member's originating chat.
type ChannelScope struct {
	InstallationID string `json:"installation_id"`
	ChatID         string `json:"chat_id"`
	ChatType       string `json:"chat_type"`
}

type Operation struct {
	ID         string           `json:"id"`
	Kind       string           `json:"kind"`
	State      string           `json:"state"`
	LeaseToken string           `json:"lease_token,omitempty"`
	Payload    OperationPayload `json:"payload"`
	Snapshot   *Snapshot        `json:"snapshot"`
	Error      string           `json:"error"`
}

type Completion struct {
	LeaseToken string    `json:"lease_token"`
	State      string    `json:"state"`
	Snapshot   *Snapshot `json:"snapshot"`
	Error      string    `json:"error"`
}

var issuePath = regexp.MustCompile(`/(?:issues|issue)/(\d+)(?:/|$)`)
var numericID = regexp.MustCompile(`^[1-9][0-9]*$`)

func ParseSource(raw string) (Source, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || !strings.HasSuffix(strings.ToLower(u.Hostname()), ".pm.netease.com") {
		return Source{}, fmt.Errorf("use an HTTPS 易协作 issue URL on a *.pm.netease.com host")
	}
	id := ""
	if match := issuePath.FindStringSubmatch(u.Path); len(match) == 2 {
		id = match[1]
	}
	for _, key := range []string{"issue_id", "issueId"} {
		if value := u.Query().Get(key); value != "" {
			if id != "" && id != value {
				return Source{}, fmt.Errorf("issue URL contains conflicting IDs")
			}
			id = value
		}
	}
	if !numericID.MatchString(id) {
		return Source{}, fmt.Errorf("the URL must identify one 易协作 issue")
	}
	host := strings.ToLower(u.Hostname())
	return Source{Host: host, ID: id, URL: "https://" + host + "/issues/" + id}, nil
}

func (s *Snapshot) Validate(source Source) error {
	if s.Source != source || strings.TrimSpace(s.Title) == "" {
		return fmt.Errorf("source identity or title does not match the requested issue")
	}
	if len(s.Title) > 4096 || len(s.Description) > 2*1024*1024 || len(s.Comments) > 1000 || len(s.Attachments) > 1000 {
		return fmt.Errorf("source material exceeds import limits")
	}
	for _, a := range s.Attachments {
		u, err := url.Parse(a.URL)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" {
			return fmt.Errorf("invalid source attachment URL")
		}
	}
	return nil
}

func (s Snapshot) Fingerprint() string {
	// Catalog/warnings are not issue content and can change independently.
	s.Digest = ""
	s.Statuses = nil
	s.Warnings = nil
	s.Attachments = append([]Attachment(nil), s.Attachments...)
	for i := range s.Attachments {
		s.Attachments[i].DownloadURL = ""
		s.Attachments[i].LocalID = ""
		s.Attachments[i].LocalURL = ""
	}
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s Snapshot) Markdown() string {
	var b strings.Builder
	description := s.Description
	for _, a := range s.Attachments {
		if a.LocalURL != "" {
			description = strings.ReplaceAll(description, a.URL, a.LocalURL)
			description = strings.ReplaceAll(description, "/attachments/"+a.ID+"/"+url.PathEscape(a.Name), a.LocalURL)
		}
	}
	fmt.Fprintf(&b, "[易协作 #%s](%s) · %s\n\n%s", s.Source.ID, s.Source.URL, s.Status, description)
	for _, field := range s.Fields {
		fmt.Fprintf(&b, "\n\n%s: %s", field.Name, field.Value)
	}
	if len(s.Attachments) > 0 {
		b.WriteString("\n\n## Source attachments\n")
		for _, a := range s.Attachments {
			link := a.URL
			if a.LocalURL != "" {
				link = a.LocalURL
			}
			fmt.Fprintf(&b, "\n- [%s](%s)", strings.NewReplacer("[", "\\[", "]", "\\]").Replace(a.Name), link)
		}
	}
	if len(s.Comments) > 0 {
		b.WriteString("\n\n## Source comments\n")
		for _, c := range s.Comments {
			fmt.Fprintf(&b, "\n### %s · %s\n\n%s\n", c.Author, c.CreatedAt, c.Content)
		}
	}
	return b.String()
}

// ParseLocalTime interprets timestamps without offsets in the source timezone.
func ParseLocalTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if value, err := time.Parse(layout, raw); err == nil {
			return value, nil
		}
	}
	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", time.DateOnly} {
		if value, err := time.ParseInLocation(layout, raw, zone); err == nil {
			return value, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparsed source timestamp %q", raw)
}
