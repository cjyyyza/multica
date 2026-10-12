package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/issueproperty"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// maxActiveIssueProperties matches the handler catalog cap. Import creates
// missing definitions inside the issue transaction, so it has to enforce the
// same limit.
const maxActiveIssueProperties = 20

// prepareYixiezuoIssue maps the snapshot onto properties and dates and points
// the description at the fields that were not stored. Definitions are created
// in this transaction; value checks still run through validateIssueCreateProperties.
func (s *IssueService) prepareYixiezuoIssue(ctx context.Context, tx pgx.Tx, qtx *db.Queries, p *IssueCreateParams) error {
	members, err := loadYixiezuoMembers(ctx, qtx, p.WorkspaceID)
	if err != nil {
		return err
	}
	class := yixiezuo.Classify(*p.Yixiezuo, members)
	values, _, err := ensureYixiezuoProperties(ctx, tx, qtx, p.WorkspaceID, class.Properties)
	if err != nil {
		return err
	}
	if len(values) > 0 {
		if p.Properties == nil {
			p.Properties = values
		} else {
			for id, value := range values {
				p.Properties[id] = value
			}
		}
	}
	p.Description = pgtype.Text{String: p.Yixiezuo.MarkdownRetaining(class.Retained), Valid: true}
	if class.StartDate != "" {
		day, err := util.ParseCalendarDate(class.StartDate)
		if err != nil {
			return err
		}
		p.StartDate = day
	}
	if class.DueDate != "" {
		day, err := util.ParseCalendarDate(class.DueDate)
		if err != nil {
			return err
		}
		p.DueDate = day
	}
	return nil
}

// ApplyYixiezuoRefresh copies source-owned properties, yixiezuo_status, and
// start/due dates from next onto the imported issue. Title, description,
// Multica status, and assignee are left alone. A date column changes only
// when the previous or next snapshot actually carried that date.
func (s *IssueService) ApplyYixiezuoRefresh(ctx context.Context, tx pgx.Tx, workspaceID, issueID pgtype.UUID, previous, next yixiezuo.Snapshot) error {
	qtx := s.Queries.WithTx(tx)
	members, err := loadYixiezuoMembers(ctx, qtx, workspaceID)
	if err != nil {
		return err
	}
	before := yixiezuo.Classify(previous, members)
	after := yixiezuo.Classify(next, members)
	_, catalog, err := ensureYixiezuoProperties(ctx, tx, qtx, workspaceID, after.Properties)
	if err != nil {
		return err
	}

	keep := make(map[string]struct{}, len(after.Properties))
	var lockIDs []pgtype.UUID
	for _, property := range after.Properties {
		keep[strings.ToLower(property.Name)] = struct{}{}
		if def, ok := catalog[strings.ToLower(property.Name)]; ok {
			lockIDs = append(lockIDs, def.ID)
		}
	}
	var removed []db.IssueProperty
	for _, property := range before.Properties {
		key := strings.ToLower(property.Name)
		if _, ok := keep[key]; ok {
			continue
		}
		def, ok := catalog[key]
		if !ok {
			continue
		}
		removed = append(removed, def)
		lockIDs = append(lockIDs, def.ID)
	}
	if err := lockIssueProperties(ctx, tx, lockIDs); err != nil {
		return err
	}
	for _, def := range removed {
		if _, err := qtx.DeleteIssuePropertyValue(ctx, db.DeleteIssuePropertyValueParams{
			Key:         util.UUIDToString(def.ID),
			ID:          issueID,
			WorkspaceID: workspaceID,
		}); err != nil {
			return err
		}
	}
	for _, property := range after.Properties {
		def, ok := catalog[strings.ToLower(property.Name)]
		if !ok {
			return yixiezuoPropertyError(fmt.Sprintf("property %q was not created", property.Name))
		}
		canonical, err := canonicalIssuePropertyValue(ctx, qtx, workspaceID, def, property.Value)
		if err != nil {
			return err
		}
		if _, err := qtx.SetIssuePropertyValue(ctx, db.SetIssuePropertyValueParams{
			Key:         util.UUIDToString(def.ID),
			Value:       canonical,
			ID:          issueID,
			WorkspaceID: workspaceID,
		}); err != nil {
			return err
		}
	}
	if err := applyYixiezuoDates(ctx, tx, qtx, workspaceID, issueID, before, after); err != nil {
		return err
	}
	raw, err := json.Marshal(next.Status)
	if err != nil {
		return err
	}
	_, err = qtx.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		Key:         "yixiezuo_status",
		Value:       raw,
		ID:          issueID,
		WorkspaceID: workspaceID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return nil
}

func loadYixiezuoMembers(ctx context.Context, qtx *db.Queries, workspaceID pgtype.UUID) (yixiezuo.MemberIDs, error) {
	rows, err := qtx.ListMembersWithUser(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	members := make(yixiezuo.MemberIDs, len(rows))
	for _, row := range rows {
		if row.UserName == "" {
			continue
		}
		members[row.UserName] = append(members[row.UserName], util.UUIDToString(row.UserID))
	}
	return members, nil
}

func ensureYixiezuoProperties(ctx context.Context, tx pgx.Tx, qtx *db.Queries, workspaceID pgtype.UUID, properties []yixiezuo.MappedProperty) (map[pgtype.UUID]json.RawMessage, map[string]db.IssueProperty, error) {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "props:"+util.UUIDToString(workspaceID)); err != nil {
		return nil, nil, fmt.Errorf("lock issue property catalog: %w", err)
	}
	catalog, err := listYixiezuoProperties(ctx, qtx, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	active, err := qtx.CountActiveIssueProperties(ctx, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	var needed int64
	seen := map[string]struct{}{}
	for _, property := range properties {
		key := strings.ToLower(property.Name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if _, ok := catalog[key]; !ok {
			needed++
		}
	}
	if active+needed > maxActiveIssueProperties {
		return nil, nil, yixiezuoPropertyError(fmt.Sprintf("a workspace cannot have more than %d active properties; archive unused ones first", maxActiveIssueProperties))
	}

	values := make(map[pgtype.UUID]json.RawMessage, len(properties))
	for _, property := range properties {
		key := strings.ToLower(property.Name)
		def, ok := catalog[key]
		if !ok {
			created, err := qtx.CreateIssueProperty(ctx, db.CreateIssuePropertyParams{
				WorkspaceID: workspaceID,
				Name:        property.Name,
				Type:        property.Type,
				Description: "",
				Icon:        "",
				Config:      []byte(`{}`),
			})
			if err != nil {
				if !isUniqueViolation(err) {
					return nil, nil, err
				}
				catalog, err = listYixiezuoProperties(ctx, qtx, workspaceID)
				if err != nil {
					return nil, nil, err
				}
				def, ok = catalog[key]
				if !ok {
					return nil, nil, fmt.Errorf("property %q disappeared during create", property.Name)
				}
			} else {
				def = created
				catalog[key] = created
			}
		}
		if def.ArchivedAt.Valid {
			return nil, nil, yixiezuoPropertyError(fmt.Sprintf("property %q is archived and cannot receive new values", def.Name))
		}
		if def.Type != property.Type {
			return nil, nil, yixiezuoPropertyError(fmt.Sprintf("property %q already exists as %s", def.Name, def.Type))
		}
		values[def.ID] = property.Value
	}
	return values, catalog, nil
}

func listYixiezuoProperties(ctx context.Context, qtx *db.Queries, workspaceID pgtype.UUID) (map[string]db.IssueProperty, error) {
	rows, err := qtx.ListIssueProperties(ctx, db.ListIssuePropertiesParams{WorkspaceID: workspaceID, IncludeArchived: true})
	if err != nil {
		return nil, err
	}
	catalog := make(map[string]db.IssueProperty, len(rows))
	for _, row := range rows {
		catalog[strings.ToLower(row.Name)] = db.IssueProperty{
			ID:          row.ID,
			WorkspaceID: row.WorkspaceID,
			Name:        row.Name,
			Type:        row.Type,
			Description: row.Description,
			Config:      row.Config,
			Position:    row.Position,
			ArchivedAt:  row.ArchivedAt,
			CreatedAt:   row.CreatedAt,
			UpdatedAt:   row.UpdatedAt,
			Icon:        row.Icon,
		}
	}
	return catalog, nil
}

func canonicalIssuePropertyValue(ctx context.Context, qtx *db.Queries, workspaceID pgtype.UUID, def db.IssueProperty, raw json.RawMessage) ([]byte, error) {
	value, err := issueproperty.ValidateValue(def, raw)
	if err != nil {
		return nil, &IssuePropertyValidationError{PropertyID: util.UUIDToString(def.ID), Message: err.Error()}
	}
	if !issueproperty.IsActor(def.Type) {
		return value, nil
	}
	refs, err := issueproperty.ActorRefsInValue(def.Type, value)
	if err != nil {
		return nil, &IssuePropertyValidationError{PropertyID: util.UUIDToString(def.ID), Message: err.Error()}
	}
	for _, ref := range refs {
		actorID, parseErr := util.ParseUUID(ref.ID)
		if parseErr != nil {
			return nil, &IssuePropertyValidationError{PropertyID: util.UUIDToString(def.ID), Message: fmt.Sprintf("actor id in %q must be a UUID", ref)}
		}
		if _, lookupErr := qtx.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
			UserID: actorID, WorkspaceID: workspaceID,
		}); lookupErr != nil {
			if errors.Is(lookupErr, pgx.ErrNoRows) {
				return nil, &IssuePropertyValidationError{PropertyID: util.UUIDToString(def.ID), Message: fmt.Sprintf("%q does not refer to a member of this workspace", ref)}
			}
			return nil, fmt.Errorf("resolve issue property actor: %w", lookupErr)
		}
	}
	return value, nil
}

func applyYixiezuoDates(ctx context.Context, tx pgx.Tx, qtx *db.Queries, workspaceID, issueID pgtype.UUID, before, after yixiezuo.Classification) error {
	issue, err := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{WorkspaceID: workspaceID, ID: issueID})
	if err != nil {
		return err
	}
	start, startChanged, err := reconcileYixiezuoDate(after.StartDate, before.StartDate, issue.StartDate)
	if err != nil {
		return err
	}
	due, dueChanged, err := reconcileYixiezuoDate(after.DueDate, before.DueDate, issue.DueDate)
	if err != nil {
		return err
	}
	if !startChanged && !dueChanged {
		return nil
	}
	_, err = tx.Exec(ctx, `
		UPDATE issue
		SET start_date = $3,
		    due_date = $4,
		    revision = revision + 1,
		    last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now()),
		    updated_at = now()
		WHERE id = $1
		  AND workspace_id = $2
		  AND (start_date IS DISTINCT FROM $3::date OR due_date IS DISTINCT FROM $4::date)
	`, issueID, workspaceID, start, due)
	return err
}

func reconcileYixiezuoDate(nextDay, prevDay string, current pgtype.Date) (pgtype.Date, bool, error) {
	if nextDay != "" {
		day, err := util.ParseCalendarDate(nextDay)
		if err != nil {
			return pgtype.Date{}, false, err
		}
		return day, !sameCalendarDay(current, day), nil
	}
	if prevDay != "" && current.Valid {
		return pgtype.Date{}, true, nil
	}
	return current, false, nil
}

func sameCalendarDay(left, right pgtype.Date) bool {
	if left.Valid != right.Valid {
		return false
	}
	if !left.Valid {
		return true
	}
	return left.Time.Format("2006-01-02") == right.Time.Format("2006-01-02")
}

func lockIssueProperties(ctx context.Context, tx pgx.Tx, ids []pgtype.UUID) error {
	sort.Slice(ids, func(left, right int) bool {
		return util.UUIDToString(ids[left]) < util.UUIDToString(ids[right])
	})
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		key := util.UUIDToString(id)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "prop:"+key); err != nil {
			return fmt.Errorf("lock issue property: %w", err)
		}
	}
	return nil
}

func yixiezuoPropertyError(message string) error {
	return &IssuePropertyValidationError{Message: message}
}
