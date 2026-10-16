package yixiezuo

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PMReader deliberately exposes no bridge, publication, generic tool, or
// source mutation method. Every subprocess tool name is checked below.
type PMReader struct {
	driver   *ExecDriver
	scope    PMScope
	projects map[string]PMProject
	people   map[string]PMPerson
	trackers map[string]string
	filters  map[string]map[string]any
	issues   map[string]PMIssue
}

func NewPMReader(opts ExecOptions, scope PMScope) *PMReader {
	opts.GCPHost = scope.SourceHost
	return &PMReader{driver: NewExecDriver(opts), scope: scope, projects: map[string]PMProject{}, people: map[string]PMPerson{}, trackers: map[string]string{}, filters: map[string]map[string]any{}, issues: map[string]PMIssue{}}
}

func (d *PMReader) read(ctx context.Context, name string, args any) ([]byte, error) {
	switch name {
	case "get_filter_values", "searchUsers", "list_issues", "get_issue_base", "getIssueForm":
		return d.driver.toolCall(ctx, name, args)
	default:
		return nil, fmt.Errorf("PM source tool %q is not read-only and is not allowed", name)
	}
}

// Prepare resolves source IDs from the live instance instead of treating
// an email, display name, or BUG family name as an integer filter value.
func (d *PMReader) Prepare(ctx context.Context) error {
	raw, err := d.read(ctx, "get_filter_values", map[string]any{"fields": []string{"project_id"}, "context": "list"})
	if err != nil {
		return fmt.Errorf("read source projects: %w", err)
	}
	projects, err := pmFilterValues(raw, "project_id")
	if err != nil {
		return err
	}
	instanceScope := strings.EqualFold(d.scope.SourceProject, strings.Split(d.scope.SourceHost, ".")[0])
	for id, name := range projects {
		if instanceScope || id == d.scope.SourceProject || strings.EqualFold(name, d.scope.SourceProject) {
			d.projects[id] = PMProject{ID: id, Name: name}
		}
	}
	if len(d.projects) == 0 || (!instanceScope && len(d.projects) != 1) {
		return fmt.Errorf("source project is missing or ambiguous: %s", d.scope.SourceProject)
	}
	ownerIDs := []string{}
	for _, email := range d.scope.AssigneeEmails {
		raw, err := d.read(ctx, "searchUsers", map[string]string{"keyword": email})
		if err != nil {
			return fmt.Errorf("resolve source assignee %s: %w", email, err)
		}
		var people []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &people); err != nil {
			return fmt.Errorf("invalid source user search response")
		}
		matches := []PMPerson{}
		for _, row := range people {
			mail := rawString(row, "mail")
			if strings.EqualFold(email, mail) {
				matches = append(matches, PMPerson{ID: rawString(row, "id"), Name: rawString(row, "name"), Email: mail})
			}
		}
		if len(matches) != 1 || !numericID.MatchString(matches[0].ID) {
			return fmt.Errorf("source assignee email did not resolve uniquely: %s", email)
		}
		d.people[matches[0].ID] = matches[0]
		ownerIDs = append(ownerIDs, matches[0].ID)
	}
	for _, id := range sortedPMKeys(d.projects) {
		projectID, err := strconv.Atoi(id)
		if err != nil {
			return fmt.Errorf("source project id is not numeric")
		}
		raw, err := d.read(ctx, "get_filter_values", map[string]any{"project_id": projectID, "fields": []string{"tracker_id", "status_id"}, "context": "list"})
		if err != nil {
			return fmt.Errorf("read project %s filters: %w", id, err)
		}
		trackers, err := pmFilterValues(raw, "tracker_id")
		if err != nil {
			return err
		}
		statuses, err := pmFilterValues(raw, "status_id")
		if err != nil {
			return err
		}
		trackerIDs, excluded := []string{}, []string{}
		for trackerID, name := range trackers {
			d.trackers[trackerID] = name
			if PMBugTypeMatches(d.scope.IssueType, name) {
				trackerIDs = append(trackerIDs, trackerID)
			}
		}
		for statusID, name := range statuses {
			if PMExcludedStatus(name) {
				excluded = append(excluded, statusID)
			}
		}
		if len(trackerIDs) == 0 {
			continue
		}
		sort.Strings(trackerIDs)
		sort.Strings(excluded)
		filters := map[string]any{
			"assigned_to_id": map[string]any{"operator": "=", "values": ownerIDs},
			"tracker_id":     map[string]any{"operator": "=", "values": trackerIDs},
		}
		if len(excluded) > 0 {
			filters["status_id"] = map[string]any{"operator": "!", "values": excluded}
		}
		d.filters[id] = filters
	}
	if len(d.filters) == 0 {
		return fmt.Errorf("configured BUG type is not available in the source project scope")
	}
	return nil
}

func (d *PMReader) Discover(ctx context.Context) ([]Source, error) {
	all := []Source{}
	seen := map[string]bool{}
	for _, project := range sortedPMKeys(d.filters) {
		projectID, _ := strconv.Atoi(project)
		totalPages := 1
		for page := 1; page <= totalPages; page++ {
			if page > 10000 {
				return all, fmt.Errorf("source pagination exceeds the scan limit; query scope is too broad")
			}
			observed := time.Now().UTC()
			raw, err := d.read(ctx, "list_issues", map[string]any{"project_id": projectID, "set_filter": 1, "page": page,
				"per_page": 100, "sort": "id:asc", "context": "list", "filter_mode": "simple", "filters": d.filters[project], "c": []string{"id", "subject", "status", "priority", "assigned_to", "project", "tracker", "updated_on"}})
			if err != nil {
				return all, fmt.Errorf("source project %s page %d: %w", project, page, err)
			}
			var result struct {
				List           []map[string]json.RawMessage `json:"list"`
				TotalPage      *int                         `json:"total_page"`
				Page           *int                         `json:"page"`
				InvalidFilters json.RawMessage              `json:"invalid_filter_fields"`
			}
			if err := json.Unmarshal(raw, &result); err != nil || result.List == nil || result.TotalPage == nil || result.Page == nil || *result.TotalPage < 0 || (*result.TotalPage == 0 && len(result.List) != 0) {
				return all, fmt.Errorf("malformed source issue page %d", page)
			}
			if *result.Page != page {
				return all, fmt.Errorf("source returned a different page than requested")
			}
			if raw := strings.TrimSpace(string(result.InvalidFilters)); raw != "" && raw != "null" && raw != "[]" && raw != "{}" {
				return all, fmt.Errorf("source rejected one or more configured query filters")
			}
			if page == 1 {
				totalPages = *result.TotalPage
				if totalPages == 0 {
					totalPages = 1
				}
			} else if *result.TotalPage != totalPages {
				return all, fmt.Errorf("source pagination changed during the scan; retry next cycle")
			}
			for _, row := range result.List {
				id := rawString(row, "id")
				if !numericID.MatchString(id) {
					return all, fmt.Errorf("source issue number is missing or invalid on page %d", page)
				}
				if seen[id] {
					continue
				}
				seen[id] = true
				source, err := ParseSource("https://" + d.scope.SourceHost + "/issues/" + id)
				if err != nil {
					return all, err
				}
				all = append(all, source)
				// List rows contain the seven required fields in one coherent
				// response. Reuse them; only sources absent from discovery need
				// individual reads for closure reconciliation.
				var rowCard rawCard
				rowJSON, marshalErr := json.Marshal(row)
				if marshalErr != nil {
					return all, marshalErr
				}
				if json.Unmarshal(rowJSON, &rowCard) == nil {
					card, cardErr := cardFromListRow(rowCard)
					if cardErr != nil {
						return all, cardErr
					}
					var owner, projectData map[string]json.RawMessage
					if err := json.Unmarshal(row["assigned_to"], &owner); err != nil {
						return all, fmt.Errorf("source %s assignee is malformed", id)
					}
					if err := json.Unmarshal(row["project"], &projectData); err != nil {
						return all, fmt.Errorf("source %s project is malformed", id)
					}
					item := PMIssue{Source: source, Title: card.Title, Status: card.StatusName, Priority: card.Priority,
						Assignee:  PMPerson{ID: rawString(owner, "id"), Name: rawString(owner, "value"), Email: rawString(owner, "mail")},
						Project:   PMProject{ID: rawString(row, "project_id"), Name: rawString(projectData, "value")},
						IssueType: decodeNamed(row["tracker"]), ObservedAt: observed}
					if item.Assignee.Email == "" {
						item.Assignee.Email = d.people[item.Assignee.ID].Email
					}
					if !card.UpdatedAt.IsZero() {
						item.UpdatedAt = card.UpdatedAt.UTC().Format(time.RFC3339Nano)
						item.UpdatedAtMinutePrecision = len(decodeStringish(row["updated_on"])) == len("2006-01-02 15:04")
					}
					if err := item.Validate(d.scope.SourceHost); err != nil {
						return all, fmt.Errorf("source %s: %w", id, err)
					}
					d.issues[id] = item
				}
			}
		}
	}
	return all, nil
}

func (d *PMReader) ReadIssue(ctx context.Context, source Source) (PMIssue, error) {
	item := PMIssue{Source: source, ObservedAt: time.Now().UTC()}
	if source.Host != d.scope.SourceHost {
		return item, fmt.Errorf("source belongs to a different instance")
	}
	if cached, ok := d.issues[source.ID]; ok {
		return cached, nil
	}
	id, err := strconv.Atoi(source.ID)
	if err != nil {
		return item, err
	}
	raw, err := d.read(ctx, "get_issue_base", map[string]any{"id": id})
	if err != nil {
		return item, err
	}
	card, err := parseIssueBase(raw)
	if err != nil || card.ExternalID != source.ID {
		return item, fmt.Errorf("source returned a different or malformed issue")
	}
	item.Title, item.Description, item.Status, item.Priority = card.Title, card.Description, card.StatusName, card.Priority
	if !card.UpdatedAt.IsZero() {
		item.UpdatedAt = card.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	var base struct {
		Base       map[string]json.RawMessage `json:"base"`
		CoreFields []json.RawMessage          `json:"core_fields"`
	}
	if err := json.Unmarshal(raw, &base); err != nil {
		return item, err
	}
	values := map[string]json.RawMessage{}
	for _, group := range base.CoreFields {
		var fields []struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(group, &fields); err != nil {
			return item, fmt.Errorf("malformed source core fields")
		}
		for _, field := range fields {
			values[field.Key] = field.Value
		}
	}
	formRaw, err := d.read(ctx, "getIssueForm", map[string]any{"issue": map[string]any{"id": id}, "expand": map[string]any{"include_field_options": false, "include_setting": false}})
	if err != nil {
		return item, err
	}
	var form struct {
		Issue map[string]json.RawMessage `json:"issue"`
	}
	if err := json.Unmarshal(formRaw, &form); err != nil || rawString(form.Issue, "id") != source.ID {
		return item, fmt.Errorf("source edit form identity does not match")
	}
	if title := rawString(form.Issue, "subject"); title != "" {
		item.Title = title
	}
	if body, ok := form.Issue["description"]; ok {
		item.Description = decodeStringish(body)
	}
	ownerID, projectID, trackerID := rawString(form.Issue, "assigned_to_id"), rawString(form.Issue, "project_id"), rawString(form.Issue, "tracker_id")
	item.Assignee = d.people[ownerID]
	item.Assignee.ID = ownerID
	if item.Assignee.Name == "" {
		item.Assignee.Name = decodeNamed(values["assigned_to_id"])
		if item.Assignee.Name == "" {
			item.Assignee.Name = decodeNamed(base.Base["assigned_to"])
		}
	}
	item.Project = d.projects[projectID]
	item.Project.ID = projectID
	if item.Project.Name == "" {
		item.Project.Name = rawString(base.Base, "project_name")
		if item.Project.Name == "" {
			item.Project.Name = decodeNamed(values["project_id"])
		}
	}
	item.IssueType = d.trackers[trackerID]
	if item.IssueType == "" {
		item.IssueType = decodeNamed(base.Base["tracker"])
		if item.IssueType == "" {
			item.IssueType = decodeNamed(values["tracker_id"])
		}
	}
	// Closure decisions use a coherent observation. The form supplies IDs,
	// while the base supplies status names; reject edits between those reads.
	latest, err := d.read(ctx, "get_issue_base", map[string]any{"id": id})
	if err != nil {
		return item, err
	}
	latestCard, err := parseIssueBase(latest)
	sameTime := latestCard.UpdatedAt.Equal(card.UpdatedAt)
	latestCard.UpdatedAt = card.UpdatedAt
	var latestBase struct {
		Base map[string]json.RawMessage `json:"base"`
	}
	if err != nil || json.Unmarshal(latest, &latestBase) != nil || !sameTime || latestCard != card ||
		rawString(base.Base, "project_id") != rawString(latestBase.Base, "project_id") ||
		rawString(base.Base, "tracker_id") != rawString(latestBase.Base, "tracker_id") ||
		decodeNamed(base.Base["assigned_to"]) != decodeNamed(latestBase.Base["assigned_to"]) ||
		(item.Title != card.Title) || (item.Description != card.Description) {
		return item, fmt.Errorf("source changed during reconciliation reads; retry next cycle")
	}
	if err := item.Validate(d.scope.SourceHost); err != nil {
		return item, err
	}
	return item, nil
}

func pmFilterValues(raw []byte, field string) (map[string]string, error) {
	var root map[string]struct {
		Values [][]json.RawMessage `json:"values"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("malformed source filter catalog")
	}
	entry, exists := root[field]
	if !exists || entry.Values == nil {
		return nil, fmt.Errorf("source filter catalog is missing %s", field)
	}
	values := map[string]string{}
	for _, tuple := range entry.Values {
		if len(tuple) < 2 {
			return nil, fmt.Errorf("malformed %s filter option", field)
		}
		name, id := decodeStringish(tuple[0]), decodeStringish(tuple[1])
		if id == "" {
			var n json.Number
			if json.Unmarshal(tuple[1], &n) == nil {
				id = n.String()
			}
		}
		if field == "project_id" && id == "mine" {
			continue
		}
		if field == "project_id" {
			name = strings.TrimLeft(name, "- ")
		}
		if !numericID.MatchString(id) {
			return nil, fmt.Errorf("invalid %s filter id", field)
		}
		if prior, exists := values[id]; exists && prior != name {
			return nil, fmt.Errorf("ambiguous %s filter id", field)
		}
		values[id] = name
	}
	return values, nil
}

func sortedPMKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
