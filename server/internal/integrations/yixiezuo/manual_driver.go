package yixiezuo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// ReadSnapshot only reads the selected issue; it never calls list_issues.
func (d *ExecDriver) ReadSnapshot(ctx context.Context, source Source) (Snapshot, error) {
	d.opts.GCPHost = source.Host
	id, err := strconv.Atoi(source.ID)
	if err != nil {
		return Snapshot{}, err
	}
	raw, err := d.toolCall(ctx, "get_issue_base", map[string]any{"id": id})
	if err != nil {
		return Snapshot{}, err
	}
	card, err := parseIssueBase(raw)
	if err != nil {
		return Snapshot{}, err
	}
	if card.ExternalID != source.ID {
		return Snapshot{}, fmt.Errorf("source returned a different issue")
	}
	var base struct {
		Fields []struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		} `json:"cf_fields"`
	}
	_ = json.Unmarshal(raw, &base)
	// getIssueForm, unlike get_issue_base, includes the edit lock_version.
	formRaw, err := d.toolCall(ctx, "getIssueForm", map[string]any{"issue": map[string]any{"id": id}, "expand": map[string]any{"include_field_options": false, "include_setting": false}})
	if err != nil {
		return Snapshot{}, fmt.Errorf("read source edit version: %w", err)
	}
	var form struct {
		Issue struct {
			ID          int    `json:"id"`
			LockVersion *int64 `json:"lock_version"`
			Subject     string `json:"subject"`
			Description string `json:"description"`
			StatusID    int    `json:"status_id"`
		} `json:"issue"`
	}
	if err = json.Unmarshal(formRaw, &form); err != nil || form.Issue.ID != id {
		return Snapshot{}, fmt.Errorf("invalid source edit form identity")
	}
	snapshot := Snapshot{Source: source, Title: form.Issue.Subject, Description: form.Issue.Description, Status: card.StatusName, Priority: card.Priority, LockVersion: form.Issue.LockVersion, Attachments: []Attachment{}, Comments: []Comment{}, Statuses: []Status{}, Warnings: []string{}, Fields: []SourceField{}}
	for _, field := range base.Fields {
		if value := decodeStringish(field.Value); value != "" {
			snapshot.Fields = append(snapshot.Fields, SourceField{Name: field.Name, Value: value})
		}
	}
	if !card.UpdatedAt.IsZero() {
		snapshot.UpdatedAt = card.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	raw, err = d.toolCall(ctx, "get_issue_journals", map[string]any{"id": id, "preview": 100, "show_children": false})
	if err != nil {
		return Snapshot{}, fmt.Errorf("read source comments: %w", err)
	}
	journals, err := sourceRows(raw, "journals", "list")
	if err != nil {
		return Snapshot{}, fmt.Errorf("read source comments: %w", err)
	}
	for _, row := range journals {
		content := rawString(row, "notes")
		if content == "" {
			continue
		}
		snapshot.Comments = append(snapshot.Comments, Comment{ID: rawString(row, "id"), Author: rawString(row, "author"), Content: content, CreatedAt: rawString(row, "created_on")})
	}
	var journalPage struct {
		HasMore bool `json:"has_more"`
	}
	_ = json.Unmarshal(raw, &journalPage)
	if journalPage.HasMore {
		snapshot.Warnings = append(snapshot.Warnings, "Only the latest 100 source history entries are included. Open the source for older comments.")
	}
	raw, err = d.toolCall(ctx, "get_issue_attachments", map[string]any{"id": id})
	if err != nil {
		return Snapshot{}, fmt.Errorf("read source attachments: %w", err)
	}
	attachments, err := sourceRows(raw, "attachments", "list")
	if err != nil {
		return Snapshot{}, fmt.Errorf("read source attachments: %w", err)
	}
	for _, row := range attachments {
		attachmentID := rawString(row, "id")
		name := rawString(row, "filename")
		if name == "" {
			name = rawString(row, "name")
		}
		if !numericID.MatchString(attachmentID) || name == "" {
			return Snapshot{}, fmt.Errorf("source attachment is missing its ID or filename")
		}
		snapshot.Attachments = append(snapshot.Attachments, Attachment{ID: attachmentID, Name: name, URL: "https://" + source.Host + "/attachments/" + attachmentID + "/" + url.PathEscape(name)})
	}
	// Inline screenshots are not included in get_issue_attachments on GCP.
	seen := map[string]bool{}
	for _, a := range snapshot.Attachments {
		seen[a.ID] = true
	}
	material := snapshot.Description
	for _, c := range snapshot.Comments {
		material += "\n" + c.Content
	}
	for _, match := range inlineAttachment.FindAllStringSubmatch(material, -1) {
		parsed, parseErr := url.Parse(match[0])
		if parseErr != nil || (parsed.IsAbs() && parsed.Hostname() != source.Host) {
			continue
		}
		if !seen[match[1]] {
			name, err := url.PathUnescape(match[2])
			if err != nil {
				return Snapshot{}, err
			}
			snapshot.Attachments = append(snapshot.Attachments, Attachment{ID: match[1], Name: name, URL: "https://" + source.Host + "/attachments/" + match[1] + "/" + url.PathEscape(name)})
			seen[match[1]] = true
		}
	}
	raw, err = d.toolCall(ctx, "getIssueFieldOptions", map[string]any{"issue": map[string]any{"id": id}, "core_fields": []any{map[string]any{"name": "STATUS_ID"}}, "include_permissions": true})
	if err != nil {
		snapshot.Warnings = append(snapshot.Warnings, "Status choices could not be loaded; status changes are unavailable.")
	} else {
		snapshot.Statuses = collectStatuses(raw)
		for _, status := range snapshot.Statuses {
			if status.ID == form.Issue.StatusID {
				snapshot.Status = status.Name
			}
		}
	}
	if snapshot.LockVersion == nil {
		snapshot.Warnings = append(snapshot.Warnings, "The source did not provide a lock_version; result publication is unavailable.")
	}
	snapshot.Digest = snapshot.Fingerprint()
	return snapshot, snapshot.Validate(source)
}

var inlineAttachment = regexp.MustCompile(`(?:https?://[^/\s"'<>]+)?/attachments/([1-9][0-9]*)/([^\s"'<>?]+)`)

func sourceRows(raw []byte, keys ...string) ([]map[string]json.RawMessage, error) {
	var rows []map[string]json.RawMessage
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, err
		}
		return rows, nil
	}
	var box map[string]json.RawMessage
	if err := json.Unmarshal(raw, &box); err != nil {
		return nil, err
	}
	for _, key := range keys {
		if value, ok := box[key]; ok {
			if err := json.Unmarshal(value, &rows); err != nil {
				return nil, err
			}
			return rows, nil
		}
	}
	return nil, fmt.Errorf("unexpected response shape; source material was not imported")
}

func rawString(row map[string]json.RawMessage, key string) string {
	raw := row[key]
	if value := decodeStringish(raw); value != "" {
		return value
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return string(number)
	}
	return ""
}

func collectStatuses(raw []byte) []Status {
	// The field-options response is keyed by the requested core-field name.
	var root struct {
		CoreFields struct {
			StatusID struct {
				Options []map[string]json.RawMessage `json:"options"`
			} `json:"status_id"`
		} `json:"core_fields"`
	}
	_ = json.Unmarshal(raw, &root)
	rows := root.CoreFields.StatusID.Options
	result := []Status{}
	for _, row := range rows {
		if string(row["disabled"]) == "true" {
			continue
		}
		id, _ := strconv.Atoi(rawString(row, "id"))
		name := rawString(row, "name")
		if id > 0 && name != "" {
			result = append(result, Status{ID: id, Name: name})
		}
	}
	return result
}

func (d *ExecDriver) AttachmentDownloadURL(ctx context.Context, id string) (string, error) {
	number, err := strconv.Atoi(id)
	if err != nil {
		return "", err
	}
	raw, err := d.toolCall(ctx, "get_attachment_download_info", map[string]any{"id": number})
	if err != nil {
		return "", err
	}
	var row map[string]json.RawMessage
	if err = json.Unmarshal(raw, &row); err != nil {
		return "", err
	}
	value := rawString(row, "url")
	if value == "" {
		value = rawString(row, "download_url")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("source did not return an HTTPS attachment download URL")
	}
	return value, nil
}

func (d *ExecDriver) Execute(ctx context.Context, op Operation) Completion {
	done := Completion{LeaseToken: op.LeaseToken, State: "failed"}
	source, err := ParseSource(op.Payload.Source.URL)
	if err != nil || source != op.Payload.Source {
		done.Error = "Invalid source identity"
		return done
	}
	before, err := d.ReadSnapshot(ctx, source)
	if err != nil {
		done.Error = err.Error()
		return done
	}
	if op.Kind == "preview" || op.Kind == "refresh" {
		done.State = "succeeded"
		done.Snapshot = &before
		return done
	}
	if op.Kind != "publish" {
		done.Error = "Unsupported operation"
		return done
	}
	marker := "[Multica result " + op.ID + "]"
	for _, comment := range before.Comments {
		if strings.Contains(comment.Content, marker) {
			done.State = "succeeded"
			done.Snapshot = &before
			return done
		}
	}
	if before.Digest != op.Payload.ExpectedDigest {
		done.State = "conflict"
		done.Snapshot = &before
		done.Error = "The source changed. Refresh and review it before publishing."
		return done
	}
	if before.LockVersion == nil {
		done.Error = "The source did not provide lock_version; publication was refused."
		return done
	}
	fields := map[string]any{"lock_version": *before.LockVersion}
	if op.Payload.StatusName != "" {
		found := false
		for _, status := range before.Statuses {
			if status.Name == op.Payload.StatusName {
				fields["status_id"] = status.ID
				found = true
				break
			}
		}
		if !found {
			done.Error = "The requested status is not available in this issue's workflow."
			return done
		}
	}
	id, _ := strconv.Atoi(source.ID)
	raw, err := d.toolCall(ctx, "update_issue", map[string]any{"id": id, "issue": fields, "notes": op.Payload.Summary + "\n\n" + marker})
	if err != nil {
		done.State = "unknown"
		done.Error = "Publication could not be confirmed: " + err.Error()
		return done
	}
	var response struct {
		Conflicts []json.RawMessage `json:"conflicts"`
	}
	_ = json.Unmarshal(raw, &response)
	if len(response.Conflicts) > 0 {
		done.State = "conflict"
		done.Snapshot = &before
		done.Error = "The source rejected a concurrent update. Refresh before retrying."
		return done
	}
	after, err := d.ReadSnapshot(ctx, source)
	if err != nil {
		done.State = "unknown"
		done.Error = "Publication was sent but readback failed: " + err.Error()
		return done
	}
	found := false
	for _, comment := range after.Comments {
		if strings.Contains(comment.Content, marker) {
			found = true
			break
		}
	}
	if !found || (op.Payload.StatusName != "" && after.Status != op.Payload.StatusName) {
		done.State = "unknown"
		done.Error = "Readback did not confirm the result comment and requested status. Review the source before retrying."
		return done
	}
	done.State = "succeeded"
	done.Snapshot = &after
	return done
}
