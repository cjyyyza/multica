package popo

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	requirementOutboxLease = 30 * time.Second
	// requirementOutboxMaxTransportTries is the first send plus four
	// evidence-confirmed not-sent retries.
	requirementOutboxMaxTransportTries = 5
	// requirementNotificationSnapshotVersion is the only snapshot format the
	// worker accepts; older rows carry no frozen business content.
	requirementNotificationSnapshotVersion = 1
)

var (
	errRequirementStale       = errors.New("requirement occurrence is superseded")
	errRequirementBacklog     = errors.New("requirement recipient binding is not established")
	errRequirementTransport   = errors.New("requirement recipient transport is unavailable")
	errRequirementIdentity    = errors.New("requirement delivery identity conflict")
	errRequirementRetryLimits = errors.New("requirement confirmed-failed retry budget exhausted")
)

// RequirementOutboxWorker prepares requirement notifications and keeps the
// delivery ledger reconciled. popo_bridge_command is the only state store: a
// row is created with the business intent, gains its route once a recipient
// binding exists, and records the bridge receipt in place.
type RequirementOutboxWorker struct {
	q      *db.Queries
	tx     engine.TxStarter
	appURL string
	logger *slog.Logger
	now    func() time.Time
	tick   time.Duration
	batch  int32
}

func NewRequirementOutboxWorker(q *db.Queries, tx engine.TxStarter, appURL string, logger *slog.Logger) *RequirementOutboxWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &RequirementOutboxWorker{q: q, tx: tx, appURL: strings.TrimRight(appURL, "/"), logger: logger, now: time.Now, tick: 2 * time.Second, batch: 32}
}

type RequirementOutboxRunResult struct {
	Claimed        int
	Reconciled     int
	RecoveryFailed int
	Accepted       int
	Delivered      int
	Failed         int
	Unknown        int
	Superseded     int
	Deferred       int
	Conflict       int
}

func (w *RequirementOutboxWorker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	ticker := time.NewTicker(w.tick)
	defer ticker.Stop()
	for {
		if _, err := w.RunOnce(ctx, w.batch); err != nil && ctx.Err() == nil {
			w.logger.WarnContext(ctx, "requirement POPO delivery pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *RequirementOutboxWorker) RunOnce(ctx context.Context, limit int32) (RequirementOutboxRunResult, error) {
	var result RequirementOutboxRunResult
	if w == nil || w.q == nil || w.tx == nil || limit <= 0 {
		return result, nil
	}
	reconciled, recoveryFailed, reconcileErr := w.Reconcile(ctx, limit)
	result.Reconciled, result.RecoveryFailed = reconciled, recoveryFailed
	var errs []error
	if reconcileErr != nil {
		w.logger.WarnContext(ctx, "requirement POPO delivery reconcile reported errors",
			"error", reconcileErr, "recovery_failed", recoveryFailed)
		errs = append(errs, reconcileErr)
	}
	rows, err := w.q.ClaimRequirementPopoCommands(ctx, db.ClaimRequirementPopoCommandsParams{
		LeaseExpiresAt: pgtype.Timestamptz{Time: w.now().Add(requirementOutboxLease), Valid: true},
		PageLimit:      limit,
	})
	if err != nil {
		return result, errors.Join(append(errs, err)...)
	}
	result.Claimed = len(rows)
	for _, row := range rows {
		outcome, processErr := w.processClaim(ctx, row)
		switch outcome {
		case "accepted":
			result.Accepted++
		case "delivered":
			result.Delivered++
		case "failed":
			result.Failed++
		case "unknown":
			result.Unknown++
		case "superseded":
			result.Superseded++
		case "deferred":
			result.Deferred++
		case "conflict":
			result.Conflict++
		}
		if processErr != nil {
			deferErr := w.retryClaim(ctx, row, processErr)
			errs = append(errs, errors.Join(processErr, deferErr))
		}
	}
	return result, errors.Join(errs...)
}

// Reconcile applies the two time-based terminal rules: a leased command whose
// lease expired after dispatch becomes unknown (never pending), and a prepared
// command that expired before any bridge leased it becomes a final not-sent
// failure. Both are idempotent so repeated passes converge.
func (w *RequirementOutboxWorker) Reconcile(ctx context.Context, limit int32) (reconciled int, recoveryFailed int, err error) {
	if w == nil || w.q == nil || limit <= 0 {
		return 0, 0, nil
	}
	if err := w.q.ExpireRequirementPopoCommandLeases(ctx); err != nil {
		return 0, 1, fmt.Errorf("expire requirement command leases: %w", err)
	}
	if err := w.q.ExpireUndispatchedRequirementPopoCommands(ctx); err != nil {
		return 0, 1, fmt.Errorf("expire undispatched requirement commands: %w", err)
	}
	return 0, 0, nil
}

func requirementOutboxBackoff(attempt int32) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := 2 * time.Second
	for n := int32(1); n < attempt && delay < time.Minute; n++ {
		delay *= 2
	}
	if delay > time.Minute {
		delay = time.Minute
	}
	return delay
}

func (w *RequirementOutboxWorker) retryClaim(ctx context.Context, row db.PopoBridgeCommand, cause error) error {
	if !row.LeaseID.Valid {
		return nil
	}
	_, err := w.q.DeferRequirementPopoCommand(ctx, db.DeferRequirementPopoCommandParams{
		ID: row.ID, WorkspaceID: row.WorkspaceID, LeaseID: row.LeaseID,
		NextAttemptAt: pgtype.Timestamptz{Time: w.now().Add(requirementOutboxBackoff(row.Attempt + 1)), Valid: true},
		LastError:     pgtype.Text{String: cause.Error(), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

func (w *RequirementOutboxWorker) processClaim(ctx context.Context, claimed db.PopoBridgeCommand) (string, error) {
	tx, err := w.tx.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	qtx := w.q.WithTx(tx)

	row, err := qtx.LockClaimedRequirementPopoCommand(ctx, db.LockClaimedRequirementPopoCommandParams{
		ID: claimed.ID, WorkspaceID: claimed.WorkspaceID, LeaseID: claimed.LeaseID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	prep, err := w.prepareRoute(ctx, qtx, row)
	if err != nil {
		return w.handlePreparationError(ctx, qtx, tx, row, err)
	}
	if err := validateRequirementOccurrence(ctx, qtx, row, prep.requirement); err != nil {
		return w.finishPreparationWithError(ctx, qtx, tx, row, err)
	}
	payload, err := w.buildPayload(ctx, qtx, row, prep)
	if err != nil {
		return w.finishPreparationWithError(ctx, qtx, tx, row, err)
	}
	prepared, err := qtx.PrepareRequirementPopoCommand(ctx, db.PrepareRequirementPopoCommandParams{
		ID: row.ID, WorkspaceID: row.WorkspaceID, LeaseID: row.LeaseID,
		BridgeID: prep.bridge.ID, InstallationID: prep.installation.ID, RecipientBindingID: prep.binding.ID,
		Payload: payload, PayloadHash: pgtype.Text{String: requirementPayloadHash(payload), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	_ = prepared
	return "accepted", tx.Commit(ctx)
}

func (w *RequirementOutboxWorker) handlePreparationError(ctx context.Context, qtx *db.Queries, tx pgx.Tx, row db.PopoBridgeCommand, cause error) (string, error) {
	if errors.Is(cause, errRequirementBacklog) && !row.BridgeID.Valid {
		if _, err := qtx.DeferRequirementPopoCommand(ctx, db.DeferRequirementPopoCommandParams{
			ID: row.ID, WorkspaceID: row.WorkspaceID, LeaseID: row.LeaseID,
			NextAttemptAt: pgtype.Timestamptz{Time: w.now().Add(requirementOutboxBackoff(row.Attempt + 1)), Valid: true},
			LastError:     pgtype.Text{String: cause.Error(), Valid: true},
		}); err != nil {
			return "", err
		}
		return "deferred", tx.Commit(ctx)
	}
	return w.finishPreparationWithError(ctx, qtx, tx, row, cause)
}

func (w *RequirementOutboxWorker) finishPreparationWithError(ctx context.Context, qtx *db.Queries, tx pgx.Tx, row db.PopoBridgeCommand, cause error) (string, error) {
	outcome := "failed"
	switch {
	case errors.Is(cause, errRequirementStale):
		outcome = "superseded"
		if _, err := qtx.SupersedeClaimedRequirementPopoCommand(ctx, db.SupersedeClaimedRequirementPopoCommandParams{
			ID: row.ID, WorkspaceID: row.WorkspaceID, LeaseID: row.LeaseID,
		}); err != nil {
			return "", err
		}
	case errors.Is(cause, errRequirementIdentity):
		outcome = "conflict"
		if _, err := qtx.MarkClaimedRequirementPopoCommandConflict(ctx, db.MarkClaimedRequirementPopoCommandConflictParams{
			ID: row.ID, WorkspaceID: row.WorkspaceID, LeaseID: row.LeaseID,
			LastError: pgtype.Text{String: cause.Error(), Valid: true},
		}); err != nil {
			return "", err
		}
	default:
		if _, err := qtx.FailClaimedRequirementPopoCommand(ctx, db.FailClaimedRequirementPopoCommandParams{
			ID: row.ID, WorkspaceID: row.WorkspaceID, LeaseID: row.LeaseID,
			LastError: pgtype.Text{String: cause.Error(), Valid: true},
		}); err != nil {
			return "", err
		}
	}
	return outcome, tx.Commit(ctx)
}

type requirementDeliveryPreparation struct {
	requirement     db.Requirement
	recipientUserID pgtype.UUID
	binding         db.ChannelUserBinding
	installation    db.ChannelInstallation
	bridge          db.PopoBridge
}

func (w *RequirementOutboxWorker) prepareRoute(ctx context.Context, q *db.Queries, row db.PopoBridgeCommand) (requirementDeliveryPreparation, error) {
	var prep requirementDeliveryPreparation
	if !row.RequirementID.Valid || !row.RecipientUserID.Valid || !row.RequirementKind.Valid {
		return prep, errRequirementIdentity
	}
	requirement, err := q.GetRequirement(ctx, db.GetRequirementParams{ID: row.RequirementID, WorkspaceID: row.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return prep, errRequirementStale
	}
	if err != nil {
		return prep, err
	}
	prep.requirement = requirement
	prep.recipientUserID = row.RecipientUserID
	roles, err := q.ListRequirementParticipantRoles(ctx, db.ListRequirementParticipantRolesParams{
		WorkspaceID: row.WorkspaceID, RequirementID: row.RequirementID, UserID: row.RecipientUserID,
	})
	if err != nil {
		return prep, err
	}
	if len(roles) == 0 {
		return prep, errRequirementStale
	}
	if _, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID: row.RecipientUserID, WorkspaceID: row.WorkspaceID,
	}); errors.Is(err, pgx.ErrNoRows) {
		return prep, errRequirementStale
	} else if err != nil {
		return prep, err
	}

	preferredInstallation, err := requirementSourceInstallation(ctx, q, requirement)
	if err != nil {
		return prep, err
	}
	if row.InstallationID.Valid {
		preferredInstallation = row.InstallationID
	}
	binding, err := q.ResolveRequirementRecipientBinding(ctx, db.ResolveRequirementRecipientBindingParams{
		WorkspaceID: row.WorkspaceID, RecipientUserID: row.RecipientUserID, AgentID: requirement.AgentID, InstallationID: preferredInstallation,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return prep, errRequirementBacklog
	}
	if err != nil {
		return prep, err
	}
	if row.RecipientBindingID.Valid && row.RecipientBindingID != binding.ID {
		return prep, errRequirementTransport
	}
	if strings.TrimSpace(binding.ChannelUserID) == "" {
		return prep, errRequirementIdentity
	}
	installation, err := q.GetChannelInstallationInWorkspace(ctx, db.GetChannelInstallationInWorkspaceParams{
		ID: binding.InstallationID, WorkspaceID: row.WorkspaceID, ChannelType: string(TypePopo),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return prep, errRequirementTransport
	}
	if err != nil {
		return prep, err
	}
	if installation.Status != "active" || installation.AgentID != requirement.AgentID {
		return prep, errRequirementTransport
	}
	config := DecodePublicConfig(installation.Config)
	bridgeID, err := util.ParseUUID(config.BridgeID)
	if err != nil {
		return prep, errRequirementTransport
	}
	bridge, err := q.GetPopoBridgeInWorkspace(ctx, db.GetPopoBridgeInWorkspaceParams{ID: bridgeID, WorkspaceID: row.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return prep, errRequirementTransport
	}
	if err != nil {
		return prep, err
	}
	if bridge.Status != BridgeStatusActive {
		return prep, errRequirementTransport
	}
	if row.BridgeID.Valid && (row.InstallationID != installation.ID || row.BridgeID != bridge.ID) {
		return prep, errRequirementIdentity
	}
	prep.binding, prep.installation, prep.bridge = binding, installation, bridge
	return prep, nil
}

func requirementSourceInstallation(ctx context.Context, q *db.Queries, row db.Requirement) (pgtype.UUID, error) {
	if !row.IntakeOperationID.Valid {
		return pgtype.UUID{}, nil
	}
	operation, err := q.GetYixiezuoOperation(ctx, db.GetYixiezuoOperationParams{
		WorkspaceID: row.WorkspaceID, RequestedBy: row.RequesterID, ID: row.IntakeOperationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, errRequirementStale
	}
	if err != nil {
		return pgtype.UUID{}, err
	}
	var payload struct {
		Channel *struct {
			InstallationID string `json:"installation_id"`
		} `json:"channel"`
	}
	if err := json.Unmarshal(operation.Payload, &payload); err != nil {
		return pgtype.UUID{}, errRequirementIdentity
	}
	if payload.Channel == nil || strings.TrimSpace(payload.Channel.InstallationID) == "" {
		return pgtype.UUID{}, nil
	}
	id, err := util.ParseUUID(payload.Channel.InstallationID)
	if err != nil {
		return pgtype.UUID{}, errRequirementIdentity
	}
	return id, nil
}

func requirementPayloadSnapshotVersion(payload []byte) int {
	var header struct {
		Version int `json:"version"`
	}
	_ = json.Unmarshal(payload, &header)
	return header.Version
}

// requirementPayloadHash is the immutable content identity of the encoded send
// payload. A retry reuses this payload and must never compute a different hash.
// The payload is stored as jsonb, so the bytes read back differ from the bytes
// written (key order, number spelling). Hashing the canonical JSON form keeps
// the identity stable across that round trip.
func requirementPayloadHash(payload []byte) string {
	canonical := payload
	if value, err := DecodeCanonicalJSON(payload); err == nil {
		if encoded, err := CanonicalJSON(value); err == nil {
			canonical = []byte(encoded)
		}
	}
	digest := sha256.Sum256(canonical)
	return fmt.Sprintf("sha256:%x", digest)
}

func validateRequirementRevision(ctx context.Context, q *db.Queries, command db.PopoBridgeCommand, row db.Requirement) error {
	if command.SupersededAt.Valid || !command.RequirementRevision.Valid || row.Revision < command.RequirementRevision.Int64 {
		return errRequirementStale
	}
	if row.Revision == command.RequirementRevision.Int64 {
		return nil
	}
	participantChanges, err := q.CountRequirementParticipantRevisionEvents(ctx, db.CountRequirementParticipantRevisionEventsParams{
		WorkspaceID: row.WorkspaceID, RequirementID: row.ID,
		AfterRevision: command.RequirementRevision.Int64, CurrentRevision: row.Revision,
	})
	if err != nil {
		return err
	}
	if row.Revision-command.RequirementRevision.Int64 != participantChanges {
		return errRequirementStale
	}
	return nil
}

func validateRequirementOccurrence(ctx context.Context, q *db.Queries, command db.PopoBridgeCommand, row db.Requirement) error {
	if requirementPayloadSnapshotVersion(command.SnapshotPayload) == requirementNotificationSnapshotVersion {
		expected := service.RequirementDeliveryRequestHash(service.RequirementDeliveryIdentityFromCommand(command))
		if !command.RequestHash.Valid || command.RequestHash.String != expected {
			return errRequirementIdentity
		}
	}
	if err := validateRequirementRevision(ctx, q, command, row); err != nil {
		return err
	}
	currentState := row.IntakeState
	if row.CancelledAt.Valid {
		currentState = "cancelled"
	}
	var issue db.Issue
	if row.IssueID.Valid {
		var err error
		issue, err = q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: row.IssueID, WorkspaceID: row.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return errRequirementStale
		}
		if err != nil {
			return err
		}
		if command.IssueRevision.Valid && issue.Revision != command.IssueRevision.Int64 {
			return errRequirementStale
		}
		if !row.CancelledAt.Valid {
			currentState, err = requirementIssueState(ctx, q, issue)
			if err != nil {
				return err
			}
		}
	} else if command.IssueRevision.Valid {
		return errRequirementStale
	}

	switch command.RequirementKind.String {
	case "requirement_draft":
		if !row.IntakeOperationID.Valid || row.IntakeOperationID != command.OccurrenceID || row.IssueID.Valid || row.IntakeState != "intake_pending" || row.CancelledAt.Valid {
			return errRequirementStale
		}
		return nil
	case "requirement_blocked":
		if row.CancelledAt.Valid || currentState != "blocked" {
			return errRequirementStale
		}
		question, err := q.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{ID: command.OccurrenceID, WorkspaceID: row.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return errRequirementStale
		}
		if err != nil {
			return err
		}
		if question.DeletedAt.Valid || question.IssueID != row.IssueID {
			return errRequirementStale
		}
		active, err := q.HasActiveRequirementIssueWakeup(ctx, db.HasActiveRequirementIssueWakeupParams{
			WorkspaceID: row.WorkspaceID, RequirementID: row.ID, ParentCommentID: command.OccurrenceID,
		})
		if err != nil {
			return err
		}
		if !active {
			return errRequirementStale
		}
		return nil
	case "requirement_status", "requirement_review":
		event, err := q.GetRequirementEvent(ctx, db.GetRequirementEventParams{ID: command.OccurrenceID, WorkspaceID: row.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return errRequirementStale
		}
		if err != nil {
			return err
		}
		if event.RequirementID != row.ID {
			return errRequirementIdentity
		}
		if command.RequirementKind.String == "requirement_review" && (event.Action != "in_review" || event.ToState.String != "in_review") {
			return errRequirementStale
		}
		if command.RequirementKind.String == "requirement_status" && event.Action == "in_review" {
			return errRequirementStale
		}
		if event.ToState.Valid && event.ToState.String != "" && currentState != event.ToState.String {
			return errRequirementStale
		}
		return nil
	default:
		return errRequirementIdentity
	}
}

func requirementIssueState(ctx context.Context, q *db.Queries, issue db.Issue) (string, error) {
	switch issue.Status {
	case "backlog", "todo", "in_progress":
		return "executing", nil
	case "blocked", "in_review", "done", "cancelled":
		return issue.Status, nil
	}
	status, err := q.GetIssueStatusEntryByKey(ctx, db.GetIssueStatusEntryByKeyParams{WorkspaceID: issue.WorkspaceID, Key: issue.Status})
	if err != nil {
		return "", err
	}
	switch status.Category {
	case "unstarted", "started":
		return "executing", nil
	case "done":
		return "done", nil
	case "closed":
		return "cancelled", nil
	default:
		return "", fmt.Errorf("unknown requirement issue status category %q", status.Category)
	}
}

func (w *RequirementOutboxWorker) buildPayload(ctx context.Context, q *db.Queries, command db.PopoBridgeCommand, prep requirementDeliveryPreparation) ([]byte, error) {
	if requirementPayloadSnapshotVersion(command.SnapshotPayload) != requirementNotificationSnapshotVersion {
		return nil, errRequirementIdentity
	}
	var snapshot struct {
		Title         string          `json:"title"`
		State         string          `json:"state"`
		Question      string          `json:"question"`
		WorkspaceSlug string          `json:"workspace_slug"`
		EntityRefs    json.RawMessage `json:"entity_refs"`
	}
	if err := json.Unmarshal(command.SnapshotPayload, &snapshot); err != nil {
		return nil, errRequirementIdentity
	}
	refs := map[string]any{}
	if len(snapshot.EntityRefs) > 0 {
		if err := json.Unmarshal(snapshot.EntityRefs, &refs); err != nil {
			return nil, errRequirementIdentity
		}
	}
	workspaceSlug := snapshot.WorkspaceSlug
	if workspaceSlug == "" {
		workspace, err := q.GetWorkspace(ctx, command.WorkspaceID)
		if err != nil {
			return nil, err
		}
		workspaceSlug = workspace.Slug
	}
	title := strings.TrimSpace(snapshot.Title)
	if title == "" {
		title = prep.requirement.ExternalID
	}
	if command.RequirementKind.String == "requirement_blocked" {
		refs["comment_id"] = uuidString(command.OccurrenceID)
	}
	link := w.appURL + "/" + url.PathEscape(workspaceSlug) + "/chat?requirement=" + url.QueryEscape(uuidString(prep.requirement.ID))
	text := "Requirement " + snapshot.State + ": " + title + "\n" + link
	if command.RequirementKind.String == "requirement_blocked" {
		text = "Requirement question: " + title + "\n\n" + snapshot.Question + "\n\n" + link
	}
	issueRef, _ := refs["issue_id"].(string)
	commentRef, _ := refs["comment_id"].(string)
	taskRef, _ := refs["task_id"].(string)
	issueID, _ := util.ParseUUID(issueRef)
	commentID, _ := util.ParseUUID(commentRef)
	taskID, _ := util.ParseUUID(taskRef)
	if prep.requirement.IssueID.Valid && !issueID.Valid {
		issueID = prep.requirement.IssueID
	}
	payload := SendPayload{
		RobotID: DecodePublicConfig(prep.installation.Config).RobotID, ChatID: prep.binding.ChannelUserID, ChatType: "p2p", Text: text,
		IssueID: uuidString(issueID), CommentID: uuidString(commentID), TaskID: uuidString(taskID),
		OutboundKind: command.RequirementKind.String, SourceKey: requirementOutboxSourceKey(command),
		RequirementID: uuidString(prep.requirement.ID), RequirementRevision: command.RequirementRevision.Int64,
		Occurrence: &RequirementOccurrence{Kind: command.RequirementKind.String, ID: uuidString(command.OccurrenceID)},
		Recipient:  &RequirementRecipient{UserID: uuidString(prep.binding.MulticaUserID), BindingID: uuidString(prep.binding.ID), ChannelUserID: prep.binding.ChannelUserID},
	}
	if command.IssueRevision.Valid {
		payload.IssueRevision = command.IssueRevision.Int64
	}
	if command.RequestHash.Valid {
		payload.RequestHash = command.RequestHash.String
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func requirementOutboxSourceKey(row db.PopoBridgeCommand) string {
	return fmt.Sprintf("requirement:%s:%s:%s", uuidString(row.RequirementID), row.RequirementKind.String, uuidString(row.OccurrenceID))
}

func validateRequirementCommandIdentity(command db.PopoBridgeCommand, prep requirementDeliveryPreparation) error {
	if command.Type != CommandTypeSend || !command.DeliveryID.Valid || command.WorkspaceID != prep.requirement.WorkspaceID {
		return errRequirementIdentity
	}
	var payload SendPayload
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		return errRequirementIdentity
	}
	if payload.RequirementID != uuidString(command.RequirementID) || payload.Occurrence == nil ||
		payload.Occurrence.Kind != command.RequirementKind.String || payload.Occurrence.ID != uuidString(command.OccurrenceID) ||
		payload.Recipient == nil || payload.Recipient.UserID != uuidString(prep.recipientUserID) ||
		payload.Recipient.BindingID != uuidString(prep.binding.ID) || payload.SourceKey != requirementOutboxSourceKey(command) {
		return errRequirementIdentity
	}
	issueRevision := int64(0)
	if command.IssueRevision.Valid {
		issueRevision = command.IssueRevision.Int64
	}
	if payload.RequirementRevision != command.RequirementRevision.Int64 || payload.IssueRevision != issueRevision {
		return errRequirementIdentity
	}
	if command.RequestHash.Valid && payload.RequestHash != command.RequestHash.String {
		return errRequirementIdentity
	}
	if !command.PayloadHash.Valid || command.PayloadHash.String != requirementPayloadHash(command.Payload) {
		return errRequirementIdentity
	}
	return nil
}

// validateCommand re-checks the business occurrence and the resolved route
// before a bridge may deliver a requirement command.
func (w *RequirementOutboxWorker) validateCommand(ctx context.Context, q *db.Queries, command db.PopoBridgeCommand) error {
	if !command.RequirementID.Valid {
		return nil
	}
	if command.Status != CommandStatusPending && command.Status != CommandStatusLeased {
		return errRequirementStale
	}
	if command.SupersededAt.Valid {
		return errRequirementStale
	}
	prep, err := w.prepareRoute(ctx, q, command)
	if err != nil {
		return err
	}
	if err := validateRequirementOccurrence(ctx, q, command, prep.requirement); err != nil {
		return err
	}
	return validateRequirementCommandIdentity(command, prep)
}

// fenceRequirementCommand is the bridge delivery gate's entry point. That gate
// owns no worker clock, so the fence runs on the wall clock.
func fenceRequirementCommand(ctx context.Context, q *db.Queries, command db.PopoBridgeCommand, cause error) (db.PopoBridgeCommand, error) {
	status := CommandStatusFailed
	if errors.Is(cause, errRequirementStale) {
		status = CommandStatusSuperseded
	}
	row, err := q.FenceRequirementPopoCommand(ctx, db.FenceRequirementPopoCommandParams{
		ID: command.ID, WorkspaceID: command.WorkspaceID, Status: status,
		LastError: pgtype.Text{String: cause.Error(), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return command, nil
	}
	return row, err
}
