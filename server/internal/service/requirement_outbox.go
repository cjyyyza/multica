package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (s *RequirementService) requireFlow(ctx context.Context) error {
	if !featureflags.RequirementFlowEnabled(ctx, s.FeatureFlags) {
		return newRequirementError(http.StatusForbidden, "requirement_flow_disabled", "requirement flow is disabled")
	}
	return nil
}

type RequirementOutboxOptions struct {
	OccurrenceID    pgtype.UUID
	OperationID     pgtype.UUID
	IssueRevision   pgtype.Int8
	RecipientUserID pgtype.UUID
}

const requirementNotificationSnapshotVersion = 1

type requirementNotificationSnapshot struct {
	Version       int             `json:"version"`
	Title         string          `json:"title"`
	State         string          `json:"state"`
	Question      string          `json:"question,omitempty"`
	WorkspaceSlug string          `json:"workspace_slug"`
	EntityRefs    json.RawMessage `json:"entity_refs"`
}

// RequirementDeliveryIdentity is the stable business identity of one
// requirement notification. The request hash built from it prevents a retry
// from drifting to different content.
type RequirementDeliveryIdentity struct {
	RequirementID       pgtype.UUID
	Kind                string
	OccurrenceID        pgtype.UUID
	RecipientUserID     pgtype.UUID
	RequirementRevision int64
	IssueRevision       pgtype.Int8
	Snapshot            []byte
}

func notificationSnapshotVersion(payload []byte) int {
	var header struct {
		Version int `json:"version"`
	}
	_ = json.Unmarshal(payload, &header)
	return header.Version
}

func canonicalRequirementJSON(payload []byte) string {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return string(payload)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return string(payload)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return string(payload)
	}
	return string(canonical)
}

func buildRequirementNotificationSnapshot(ctx context.Context, q *db.Queries, row db.Requirement, event db.RequirementEvent, kind string, occurrenceID pgtype.UUID) ([]byte, error) {
	var source struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(row.SourceSnapshot, &source); err != nil {
		return nil, fmt.Errorf("decode requirement notification title: %w", err)
	}
	title := strings.TrimSpace(source.Title)
	if title == "" {
		title = row.ExternalID
	}
	workspace, err := q.GetWorkspace(ctx, row.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("load requirement notification workspace: %w", err)
	}
	state := "update"
	switch kind {
	case "requirement_draft":
		state = "draft"
	case "requirement_review":
		state = "in review"
	case "requirement_blocked":
		state = "question"
	default:
		if event.Action != "" {
			state = strings.ReplaceAll(event.Action, "_", " ")
		}
	}
	snapshot := requirementNotificationSnapshot{
		Version: requirementNotificationSnapshotVersion, Title: title, State: state,
		WorkspaceSlug: workspace.Slug, EntityRefs: append(json.RawMessage(nil), event.EntityRefs...),
	}
	if kind == "requirement_blocked" {
		question, err := q.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{ID: occurrenceID, WorkspaceID: row.WorkspaceID})
		if err != nil {
			return nil, fmt.Errorf("load requirement notification question: %w", err)
		}
		snapshot.Question = question.Content
	}
	return json.Marshal(snapshot)
}

// supersedeRequirementOutbox fences the command rows that still represent a
// pending notification. popo_bridge_command is the delivery ledger, so the
// business intent and its transport state are fenced in one update.
func supersedeRequirementOutbox(ctx context.Context, q *db.Queries, workspaceID, requirementID pgtype.UUID, kind string, occurrenceID pgtype.UUID) error {
	params := db.SupersedePendingRequirementPopoCommandsParams{WorkspaceID: workspaceID, RequirementID: requirementID}
	if kind != "" {
		params.Kind = pgtype.Text{String: kind, Valid: true}
	}
	if occurrenceID.Valid {
		params.OccurrenceID = occurrenceID
	}
	if _, err := q.SupersedePendingRequirementPopoCommands(ctx, params); err != nil {
		return err
	}
	return nil
}

func (s *RequirementService) appendRequirementOutbox(ctx context.Context, q *db.Queries, row db.Requirement, event db.RequirementEvent, kind string) error {
	return s.appendRequirementOutboxWithOptions(ctx, q, row, event, kind, RequirementOutboxOptions{})
}

func (s *RequirementService) appendRequirementOutboxWithOptions(ctx context.Context, q *db.Queries, row db.Requirement, event db.RequirementEvent, kind string, options RequirementOutboxOptions) error {
	return appendRequirementOutboxWithOptions(ctx, q, row, event, kind, options)
}

// appendRequirementOutboxWithOptions needs no service state, so callers that
// only hold queries can record deliveries without a service allocation.
func appendRequirementOutboxWithOptions(ctx context.Context, q *db.Queries, row db.Requirement, event db.RequirementEvent, kind string, options RequirementOutboxOptions) error {
	switch kind {
	case "requirement_draft", "requirement_status", "requirement_review", "requirement_blocked":
	default:
		return fmt.Errorf("unknown requirement outbox kind %q", kind)
	}
	if kind == "requirement_review" && event.Action != "in_review" {
		return errors.New("requirement review outbox requires an in_review event")
	}
	occurrenceID := options.OccurrenceID
	if !occurrenceID.Valid {
		occurrenceID = event.ID
	}
	operationID := options.OperationID
	if kind == "requirement_draft" {
		if !row.IntakeOperationID.Valid {
			return errors.New("requirement draft outbox has no intake operation")
		}
		occurrenceID = row.IntakeOperationID
		operationID = row.IntakeOperationID
	}
	if (kind == "requirement_status" || kind == "requirement_review") && occurrenceID != event.ID {
		return errors.New("requirement event outbox occurrence must be the requirement event id")
	}
	if err := supersedeRequirementOutbox(ctx, q, row.WorkspaceID, row.ID, kind, pgtype.UUID{}); err != nil {
		return fmt.Errorf("supersede requirement outbox: %w", err)
	}
	if kind == "requirement_status" && (row.IntakeState != "intake_pending" || row.IssueID.Valid) {
		if err := supersedeRequirementOutbox(ctx, q, row.WorkspaceID, row.ID, "requirement_draft", pgtype.UUID{}); err != nil {
			return fmt.Errorf("supersede requirement draft outbox: %w", err)
		}
	}
	if kind == "requirement_status" {
		switch event.Action {
		case "created", "candidate_submitted", "accepted":
		default:
			return nil
		}
	}
	snapshot, err := buildRequirementNotificationSnapshot(ctx, q, row, event, kind, occurrenceID)
	if err != nil {
		return err
	}
	participants, err := q.ListRequirementParticipants(ctx, db.ListRequirementParticipantsParams{
		WorkspaceID: row.WorkspaceID, RequirementID: row.ID,
	})
	if err != nil {
		return fmt.Errorf("list requirement outbox recipients: %w", err)
	}
	seen := make(map[string]bool, len(participants))
	for _, participant := range participants {
		if options.RecipientUserID.Valid && participant.UserID != options.RecipientUserID {
			continue
		}
		recipientID := util.UUIDToString(participant.UserID)
		if seen[recipientID] {
			continue
		}
		seen[recipientID] = true
		sourceKey := fmt.Sprintf("requirement:%s:%s:%s", util.UUIDToString(row.ID), kind, util.UUIDToString(occurrenceID))
		deliveryUUID := uuid.NewMD5(uuid.Nil, []byte(sourceKey+"\n"+recipientID))
		requestHash := RequirementDeliveryRequestHash(RequirementDeliveryIdentity{
			RequirementID: row.ID, Kind: kind, OccurrenceID: occurrenceID, RecipientUserID: participant.UserID,
			RequirementRevision: row.Revision, IssueRevision: options.IssueRevision, Snapshot: snapshot,
		})
		_, err = q.CreateRequirementPopoCommand(ctx, db.CreateRequirementPopoCommandParams{
			WorkspaceID: row.WorkspaceID, RequirementID: row.ID, RequirementKind: pgtype.Text{String: kind, Valid: true},
			OccurrenceID: occurrenceID, OperationID: operationID, RecipientUserID: participant.UserID,
			DeliveryID:      pgtype.UUID{Bytes: [16]byte(deliveryUUID), Valid: true},
			SnapshotPayload: snapshot, RequirementRevision: pgtype.Int8{Int64: row.Revision, Valid: true},
			IssueRevision: options.IssueRevision,
			RequestHash:   pgtype.Text{String: requestHash, Valid: true},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("create requirement outbox: %w", err)
		}
	}
	return nil
}

func syncRequirementOutboxForIssueTransition(ctx context.Context, q *db.Queries, previous, updated db.Issue, sourceTaskID pgtype.UUID) error {
	if previous.ID != updated.ID || previous.Status == updated.Status {
		return nil
	}
	row, err := q.GetRequirementByIssue(ctx, db.GetRequirementByIssueParams{WorkspaceID: updated.WorkspaceID, IssueID: updated.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if previous.Status == "blocked" || updated.Status == "blocked" || updated.Status == "in_review" {
		if err := supersedeRequirementOutbox(ctx, q, updated.WorkspaceID, row.ID, "requirement_blocked", pgtype.UUID{}); err != nil {
			return err
		}
	}
	if previous.Status == "in_review" || updated.Status == "in_review" {
		if err := supersedeRequirementOutbox(ctx, q, updated.WorkspaceID, row.ID, "requirement_review", pgtype.UUID{}); err != nil {
			return err
		}
	}
	if updated.Status != "in_review" {
		return nil
	}
	actor := RequirementActor{WorkspaceID: updated.WorkspaceID}
	actorType := "system"
	if sourceTaskID.Valid {
		task, taskErr := q.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: sourceTaskID, WorkspaceID: updated.WorkspaceID})
		if taskErr == nil {
			actor.UserID = task.AgentID
			actorType = "agent"
		} else if !errors.Is(taskErr, pgx.ErrNoRows) {
			return taskErr
		}
	}
	refs := map[string]any{
		"requirement_id": util.UUIDToString(row.ID), "issue_id": util.UUIDToString(updated.ID), "issue_revision": updated.Revision,
	}
	if sourceTaskID.Valid {
		refs["task_id"] = util.UUIDToString(sourceTaskID)
	}
	event, err := appendRequirementEvent(ctx, q, actor, actorType, row, "in_review", previous.Status, "in_review", refs, "")
	if err != nil {
		return err
	}
	return appendRequirementOutboxWithOptions(ctx, q, row, event, "requirement_review", RequirementOutboxOptions{
		IssueRevision: pgtype.Int8{Int64: updated.Revision, Valid: true},
	})
}

// RequirementDeliveryRequestHash is the stable deduplication coordinate of one
// requirement notification. It is not an authentication secret.
func RequirementDeliveryRequestHash(row RequirementDeliveryIdentity) string {
	issueRevision := ""
	if row.IssueRevision.Valid {
		issueRevision = strconv.FormatInt(row.IssueRevision.Int64, 10)
	}
	recipientID := ""
	if row.RecipientUserID.Valid {
		recipientID = util.UUIDToString(row.RecipientUserID)
	}
	parts := []string{
		util.UUIDToString(row.RequirementID), row.Kind, util.UUIDToString(row.OccurrenceID), recipientID,
		strconv.FormatInt(row.RequirementRevision, 10), issueRevision,
	}
	if notificationSnapshotVersion(row.Snapshot) == requirementNotificationSnapshotVersion {
		parts = append(parts, canonicalRequirementJSON(row.Snapshot))
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(digest[:])
}

// RequirementDeliveryIdentityFromCommand projects the ledger row back onto the
// identity used for request-hash validation.
func RequirementDeliveryIdentityFromCommand(cmd db.PopoBridgeCommand) RequirementDeliveryIdentity {
	return RequirementDeliveryIdentity{
		RequirementID:       cmd.RequirementID,
		Kind:                cmd.RequirementKind.String,
		OccurrenceID:        cmd.OccurrenceID,
		RecipientUserID:     cmd.RecipientUserID,
		RequirementRevision: cmd.RequirementRevision.Int64,
		IssueRevision:       cmd.IssueRevision,
		Snapshot:            cmd.SnapshotPayload,
	}
}
