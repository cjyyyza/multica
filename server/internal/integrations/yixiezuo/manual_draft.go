package yixiezuo

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"strconv"
	"strings"
)

// DraftInput keeps the creation intent immutable after Source resolves to the new issue.
type DraftInput struct {
	Template Source `json:"template"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	AgentID  string `json:"agent_id"`
}

func (in DraftInput) Validate() error {
	template, err := ParseSource(in.Template.URL)
	if err != nil || template != in.Template {
		return fmt.Errorf("a valid template issue URL is required")
	}
	if strings.TrimSpace(in.Title) == "" || len([]rune(in.Title)) > 255 || strings.TrimSpace(in.Body) == "" || len(in.Body) > 20000 {
		return fmt.Errorf("draft title and idea are required (title: 255 characters; idea: 20000 bytes)")
	}
	return nil
}

func DraftMarker(operationID string) string { return "[Multica draft " + operationID + "]" }

func ValidateDraftSnapshot(operationID string, in DraftInput, snapshot Snapshot) error {
	source, err := ParseSource(snapshot.Source.URL)
	if err != nil || source != snapshot.Source || source.Host != in.Template.Host || source.ID == in.Template.ID {
		return fmt.Errorf("draft readback must identify a new issue on the selected source host")
	}
	if err := snapshot.Validate(source); err != nil {
		return err
	}
	if snapshot.Title != in.Title || !strings.Contains(snapshot.Description, DraftMarker(operationID)) || !strings.Contains(snapshot.Description, "待澄清") {
		return fmt.Errorf("draft readback does not contain the expected title and creation marker")
	}
	if snapshot.LockVersion == nil || *snapshot.LockVersion < 0 {
		return fmt.Errorf("draft readback did not provide an edit version")
	}
	return nil
}

type draftForm struct {
	Issue  map[string]any `json:"issue"`
	Fields []struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Type         string `json:"type"`
		Required     bool   `json:"required"`
		Readonly     bool   `json:"readonly"`
		DefaultValue any    `json:"default_value"`
		Options      []struct {
			ID       any    `json:"id"`
			Name     string `json:"name"`
			Disabled bool   `json:"disabled"`
		} `json:"options"`
	} `json:"fields"`
}

func draftValuePresent(value any) bool {
	if value == nil {
		return false
	}
	switch value := value.(type) {
	case string:
		return strings.TrimSpace(value) != ""
	case []any:
		return len(value) > 0
	}
	return true
}

func draftCustom(issue map[string]any) map[string]any {
	values, _ := issue["custom_field_values"].(map[string]any)
	return values
}

func buildDraftFields(op Operation, template, initial draftForm) (map[string]any, error) {
	in := op.Payload.Draft
	fields := map[string]any{
		"project_id": template.Issue["project_id"], "tracker_id": template.Issue["tracker_id"],
		"tracker_category": template.Issue["tracker_category"], "subject": in.Title,
		"description": "<p>待澄清</p><p>" + strings.ReplaceAll(html.EscapeString(in.Body), "\n", "<br>") + "</p><p>" + DraftMarker(op.ID) + "</p>",
	}
	custom := map[string]any{}
	for _, field := range initial.Fields {
		if !field.Required {
			continue
		}
		value := fields[field.ID]
		if !draftValuePresent(value) {
			value = initial.Issue[field.ID]
			if field.Type == "CUSTOM" {
				value = draftCustom(initial.Issue)[field.ID]
			}
		}
		if !draftValuePresent(value) {
			value = field.DefaultValue
		}
		if !draftValuePresent(value) && field.ID == "assigned_to_id" && !field.Readonly {
			author, ok := initial.Issue["author_id"].(float64)
			if !ok || author <= 0 {
				return nil, fmt.Errorf("the new source form did not identify the current user for draft assignment")
			}
			value = author
		}
		if !draftValuePresent(value) && field.ID != "status_id" {
			value = template.Issue[field.ID]
			if field.Type == "CUSTOM" {
				value = draftCustom(template.Issue)[field.ID]
			}
		}
		if !draftValuePresent(value) {
			return nil, fmt.Errorf("draft requires a value for %s (%s); configure the source form or template before retrying", field.Name, field.ID)
		}
		if len(field.Options) > 0 {
			valid := false
			for _, option := range field.Options {
				if !option.Disabled && (fmt.Sprint(value) == fmt.Sprint(option.ID) || fmt.Sprint(value) == option.Name) {
					valid = true
				}
			}
			if !valid {
				return nil, fmt.Errorf("draft value for %s (%s) is not an available form option", field.Name, field.ID)
			}
		}
		if field.Type == "CUSTOM" {
			if !numericID.MatchString(field.ID) {
				return nil, fmt.Errorf("invalid required custom field %s", field.ID)
			}
			custom[field.ID] = value
			continue
		}
		switch field.ID {
		case "project_id", "tracker_id", "subject", "description", "status_id", "assigned_to_id", "fixed_version_id", "priority_id", "start_date", "due_date", "estimated_hours":
			fields[field.ID] = value
		default:
			return nil, fmt.Errorf("unsupported required draft field %s", field.ID)
		}
	}
	if !draftValuePresent(fields["status_id"]) {
		return nil, fmt.Errorf("the new issue form did not provide an initial status")
	}
	if len(custom) > 0 {
		fields["custom_field_values"] = custom
	}
	return fields, nil
}

func (d *ExecDriver) createDraft(ctx context.Context, op Operation) Completion {
	done := Completion{LeaseToken: op.LeaseToken, State: "failed"}
	if op.State != "running" || op.LeaseToken == "" || op.ID == "" || op.Payload.Draft == nil {
		done.Error = "Draft creation requires a running, leased creation intent"
		return done
	}
	in := *op.Payload.Draft
	if err := in.Validate(); err != nil {
		done.Error = err.Error()
		return done
	}
	d.opts.GCPHost = in.Template.Host
	id, _ := strconv.Atoi(in.Template.ID)
	readForm := func(issue map[string]any) (draftForm, error) {
		raw, err := d.toolCall(ctx, "getIssueForm", map[string]any{"issue": issue, "expand": map[string]any{"include_field_options": true, "include_setting": false}})
		var form draftForm
		if err == nil {
			err = json.Unmarshal(raw, &form)
		}
		return form, err
	}
	template, err := readForm(map[string]any{"id": id})
	if err == nil && template.Issue["id"] != float64(id) {
		err = fmt.Errorf("template form identity does not match")
	}
	if err == nil {
		project, projectOK := template.Issue["project_id"].(float64)
		tracker, trackerOK := template.Issue["tracker_id"].(float64)
		category := template.Issue["tracker_category"]
		if !projectOK || !trackerOK || project <= 0 || tracker <= 0 || (category != "Task" && category != "Requirement") {
			err = fmt.Errorf("template form did not provide a project and issue type")
		}
	}
	if err != nil {
		done.Error = "Read draft template: " + err.Error()
		return done
	}
	initial, err := readForm(map[string]any{"project_id": template.Issue["project_id"], "tracker_id": template.Issue["tracker_id"], "tracker_category": template.Issue["tracker_category"]})
	if err != nil {
		done.Error = "Read new issue form: " + err.Error()
		return done
	}
	fields, err := buildDraftFields(op, template, initial)
	if err != nil {
		done.Error = err.Error()
		return done
	}
	// Never retry this mutation, including when its response cannot be decoded.
	raw, err := d.toolCall(ctx, "bulkCreateIssue", map[string]any{"issues": []any{fields}})
	done.State = "unknown"
	if err != nil {
		done.Error = "Draft creation could not be confirmed. Do not create it again; inspect the source for " + DraftMarker(op.ID) + ": " + err.Error()
		return done
	}
	var response []struct {
		ID     int  `json:"id"`
		Error  bool `json:"error"`
		Errors []struct {
			Message string `json:"message"`
			Fields  []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"fields"`
		} `json:"errors"`
	}
	decodeErr := json.Unmarshal(raw, &response)
	if decodeErr == nil && len(response) == 1 && (response[0].Error || len(response[0].Errors) > 0) {
		done.State = "failed"
		var failures []string
		for _, failure := range response[0].Errors {
			for _, field := range failure.Fields {
				failures = append(failures, field.Name+" ("+field.ID+"): "+failure.Message)
			}
			if len(failure.Fields) == 0 {
				failures = append(failures, failure.Message)
			}
		}
		done.Error = "Source rejected draft creation: " + strings.Join(failures, "; ")
		return done
	}
	if decodeErr != nil || len(response) != 1 || response[0].ID <= 0 {
		reply := []rune(string(raw))
		if len(reply) > 2000 {
			reply = reply[:2000]
		}
		done.Error = "Draft creation returned no single issue ID. Inspect the source for " + DraftMarker(op.ID) + ". Source response: " + string(reply)
		return done
	}
	source, err := ParseSource("https://" + in.Template.Host + "/issues/" + strconv.Itoa(response[0].ID))
	if err != nil {
		done.Error = err.Error()
		return done
	}
	snapshot, err := d.ReadSnapshot(ctx, source)
	if err == nil {
		err = ValidateDraftSnapshot(op.ID, in, snapshot)
	}
	if err != nil {
		done.Error = "Draft was created at " + source.URL + " but readback could not be confirmed: " + err.Error()
		return done
	}
	done.State, done.Snapshot = "succeeded", &snapshot
	return done
}
