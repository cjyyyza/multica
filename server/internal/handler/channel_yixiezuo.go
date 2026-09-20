package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var _ engine.MemberCommandHandler = (*Handler)(nil)

func sourceCommandKey(inst engine.ResolvedInstallation, actor pgtype.UUID, messageID string) string {
	sum := sha256.Sum256([]byte(uuidToString(inst.ID) + "\n" + uuidToString(actor) + "\n" + messageID))
	return "channel:" + hex.EncodeToString(sum[:])
}

func (h *Handler) HandleMemberCommand(ctx context.Context, inst engine.ResolvedInstallation, identity engine.ResolvedIdentity, msg channel.InboundMessage) (engine.Result, bool, error) {
	command, handled, parseErr := yixiezuo.ParseCommand(msg.CommandText)
	if !handled {
		return engine.Result{}, false, nil
	}
	result := engine.Result{Outcome: engine.OutcomeIssueFollow, InstallationID: inst.ID, Sender: msg.Source.SenderID}
	if parseErr != nil {
		result.ReplyText = parseErr.Error()
		return result, true, nil
	}
	if !inst.ID.Valid || !inst.WorkspaceID.Valid || !identity.UserID.Valid || strings.TrimSpace(msg.MessageID) == "" || strings.TrimSpace(msg.Source.ChatID) == "" {
		result.ReplyText = "A bound workspace member and a durable message identity are required."
		return result, true, nil
	}
	key := sourceCommandKey(inst, identity.UserID, msg.MessageID)
	result.ReplyKey = "source_ack:" + key
	scope := &yixiezuo.ChannelScope{InstallationID: uuidToString(inst.ID), ChatID: msg.Source.ChatID, ChatType: string(msg.Source.ChatType)}
	err := h.runSourceCommand(ctx, inst, identity.UserID, scope, key, command, &result)
	var action *yixiezuoActionError
	if errors.As(err, &action) {
		result.ReplyText = action.message
		return result, true, nil
	}
	if err != nil {
		return engine.Result{}, true, err
	}
	return result, true, nil
}

func sourceUUID(raw, label string) (pgtype.UUID, error) {
	id, err := util.ParseUUID(raw)
	if err != nil || !id.Valid {
		return id, sourceError(400, "Invalid "+label+". Copy the ID from the preceding response.")
	}
	return id, nil
}

func (h *Handler) sourceChatOperation(ctx context.Context, ws, actor pgtype.UUID, rawID string, scope *yixiezuo.ChannelScope) (db.YixiezuoOperation, yixiezuo.OperationPayload, error) {
	id, err := sourceUUID(rawID, "operation ID")
	if err != nil {
		return db.YixiezuoOperation{}, yixiezuo.OperationPayload{}, err
	}
	if err = h.Queries.ExpireYixiezuoOperations(ctx, ws); err != nil {
		return db.YixiezuoOperation{}, yixiezuo.OperationPayload{}, err
	}
	row, err := h.Queries.GetYixiezuoOperation(ctx, db.GetYixiezuoOperationParams{WorkspaceID: ws, RequestedBy: actor, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, yixiezuo.OperationPayload{}, sourceError(404, "Operation not found for this member and workspace.")
	}
	if err != nil {
		return row, yixiezuo.OperationPayload{}, err
	}
	var payload yixiezuo.OperationPayload
	if json.Unmarshal(row.Payload, &payload) != nil {
		return row, payload, sourceError(500, "Invalid stored source operation.")
	}
	if !sourceScopeMatches(payload.Channel, scope) {
		return row, payload, sourceError(403, "Use the same robot and chat where this preview or review was requested.")
	}
	return row, payload, nil
}

func (h *Handler) sourceResultIssue(ctx context.Context, result *engine.Result, issue db.Issue) {
	result.IssueID, result.IssueNumber, result.IssueTitle, result.IssueStatus = issue.ID, issue.Number, issue.Title, issue.Status
	if ws, err := h.Queries.GetWorkspace(ctx, issue.WorkspaceID); err == nil {
		result.IssueIdentifier = service.IssueIdentifier(issuePrefixForWorkspace(ws), issue.Number)
		result.IssueWorkspaceSlug = ws.Slug
	}
}

func sourceOwnedByBot(issue db.Issue, agent pgtype.UUID) bool {
	return !issue.AssigneeID.Valid || (issue.AssigneeType.String == "agent" && issue.AssigneeID == agent)
}

func sourceOperationReply(row db.YixiezuoOperation) string {
	text := fmt.Sprintf("Source operation %s: %s.\n/yixiezuo show %s", row.Kind, row.State, uuidToString(row.ID))
	if row.Error != "" {
		text += "\n" + row.Error
	}
	return text
}

func sourcePreviewReply(row db.YixiezuoOperation) string {
	if row.State != "succeeded" {
		return sourceOperationReply(row)
	}
	var snapshot yixiezuo.Snapshot
	if json.Unmarshal(row.Result, &snapshot) != nil {
		return "The source material could not be read. Request another preview."
	}
	material := []rune(snapshot.Markdown())
	if len(material) > 3000 {
		material = append(material[:3000], []rune("\n[Preview truncated; open the source link to review all material.]")...)
	}
	text := string(material)
	for _, warning := range snapshot.Warnings {
		text += "\n" + warning
	}
	if row.Kind == "preview" {
		text += "\n\nAfter reviewing, confirm import (the task stays unassigned):\n/yixiezuo import " + uuidToString(row.ID) + " --confirm [--project <project-uuid>]"
	}
	return text
}

func (h *Handler) runSourceCommand(ctx context.Context, inst engine.ResolvedInstallation, actor pgtype.UUID, scope *yixiezuo.ChannelScope, key string, command yixiezuo.Command, result *engine.Result) error {
	ws := inst.WorkspaceID
	switch command.Action {
	case "help":
		result.ReplyText = yixiezuo.CommandHelp + "\nKeep multica yixiezuo bridge running with your own Multica account and workspace. Importing never starts an agent."
		return nil
	case "preview":
		row, err := h.previewYixiezuo(ctx, ws, actor, command.Target, key, scope)
		if err != nil {
			return err
		}
		result.ReplyText = sourceOperationReply(row) + "\nThe local bridge reads this one issue. Review its material before importing."
		return nil
	case "show":
		row, payload, err := h.sourceChatOperation(ctx, ws, actor, command.Target, scope)
		if err != nil {
			return err
		}
		if row.Kind == "review" {
			result.ReplyText, err = h.sourceReviewReply(ctx, row, payload)
		} else if row.Kind == "preview" || row.Kind == "refresh" {
			result.ReplyText = sourcePreviewReply(row)
		} else {
			result.ReplyText = sourceOperationReply(row)
		}
		return err
	case "import":
		if !command.Confirmed {
			return sourceError(400, "Review the source material, select the intended project, then repeat the import command with --confirm.")
		}
		row, _, err := h.sourceChatOperation(ctx, ws, actor, command.Target, scope)
		if err != nil {
			return err
		}
		project := pgtype.UUID{}
		if command.ProjectID != "" {
			project, err = sourceUUID(command.ProjectID, "project ID")
			if err != nil {
				return err
			}
		}
		issue, existing, err := h.importYixiezuo(ctx, ws, actor, row.ID, project, service.IssueCreateOpts{
			ChannelIdempotency: &service.ChannelIdempotencyKey{InstallationID: inst.ID, ChannelType: "popo", MessageID: key, Kind: "source_import"},
			ChannelSource:      &service.ChannelIssueSource{InstallationID: inst.ID, ChannelType: "popo", ChatID: scope.ChatID, ChatType: scope.ChatType},
		})
		if err != nil {
			return err
		}
		h.sourceResultIssue(ctx, result, issue)
		if existing {
			result.ReplyText = "Already imported as " + result.IssueIdentifier + ". Existing edits, assignment and notification route are preserved."
		} else {
			result.ReplyText = "Imported as " + result.IssueIdentifier + ". The task is unassigned; assign it in Multica when ready."
		}
		result.ReplyText += "\n/status " + result.IssueIdentifier + "\n/reply " + result.IssueIdentifier + " <comment>"
		return nil
	case "refresh", "publish":
		resolved, err := h.ResolveIssue(ctx, ws, command.Target)
		if errors.Is(err, pgx.ErrNoRows) {
			return sourceError(404, "Task not found in this workspace.")
		}
		if err != nil {
			return err
		}
		if !sourceOwnedByBot(resolved.Issue, inst.AgentID) {
			return sourceError(403, "This task is assigned to another agent or member. Use its current owner to control it.")
		}
		h.sourceResultIssue(ctx, result, resolved.Issue)
		kind := command.Action
		if kind == "publish" {
			kind = "review"
		}
		row, err := h.enqueueYixiezuo(ctx, ws, actor, resolved.Issue.ID, kind, yixiezuoIssueRequest{Summary: command.Summary, StatusName: command.StatusName}, key, scope)
		if err != nil {
			return err
		}
		if kind == "review" {
			var payload yixiezuo.OperationPayload
			if err = json.Unmarshal(row.Payload, &payload); err != nil {
				return err
			}
			result.ReplyText, err = h.sourceReviewReply(ctx, row, payload)
		} else {
			result.ReplyText = sourceOperationReply(row) + "\nOnly the source snapshot will change; local task fields are preserved."
		}
		return err
	case "confirm":
		review, payload, err := h.sourceChatOperation(ctx, ws, actor, command.Target, scope)
		if err != nil {
			return err
		}
		if review.Kind != "review" || !review.IssueID.Valid {
			return sourceError(400, "This ID is not a result review. Use /yixiezuo publish to prepare one.")
		}
		resolved, err := h.ResolveIssue(ctx, ws, uuidToString(review.IssueID))
		if err != nil {
			return sourceError(404, "The reviewed task is no longer available.")
		}
		if !sourceOwnedByBot(resolved.Issue, inst.AgentID) {
			return sourceError(403, "The task has been reassigned. Review it with its current owner.")
		}
		confirmKey := "confirm:" + uuidToString(review.ID)
		if time.Since(review.CreatedAt.Time) > 15*time.Minute {
			if _, exists, err := existingSourceRequest(ctx, h.Queries, ws, actor, confirmKey); err != nil {
				return err
			} else if !exists {
				return sourceError(409, "This review expired. Prepare a new result review before confirming.")
			}
		}
		row, err := h.enqueueYixiezuo(ctx, ws, actor, review.IssueID, "publish", yixiezuoIssueRequest{Summary: payload.Summary, StatusName: payload.StatusName, Revision: payload.Revision, SourceDigest: payload.ExpectedDigest, Confirmed: true}, confirmKey, scope)
		if err != nil {
			return err
		}
		h.sourceResultIssue(ctx, result, resolved.Issue)
		result.ReplyText = sourceOperationReply(row) + "\nSource readback determines the final result."
		return nil
	}
	return sourceError(400, yixiezuo.CommandHelp)
}

func (h *Handler) sourceReviewReply(ctx context.Context, row db.YixiezuoOperation, payload yixiezuo.OperationPayload) (string, error) {
	prior, exists, err := existingSourceRequest(ctx, h.Queries, row.WorkspaceID, row.RequestedBy, "confirm:"+uuidToString(row.ID))
	if err != nil {
		return "", err
	}
	if exists {
		return "This review was already confirmed.\n" + sourceOperationReply(prior), nil
	}
	if time.Since(row.CreatedAt.Time) > 15*time.Minute {
		return "This review expired. Prepare and review the current result again before confirming.", nil
	}
	status := payload.StatusName
	if status == "" {
		status = "Keep the current source status"
	}
	return fmt.Sprintf("Review result for %s\nSource status: %s\n\n%s\n\nNo external write has been queued. After checking the result and evidence, confirm within 15 minutes in this same chat:\n/yixiezuo confirm %s\nAny newer task or source revision requires a new review.", payload.Source.URL, status, payload.Summary, uuidToString(row.ID)), nil
}
