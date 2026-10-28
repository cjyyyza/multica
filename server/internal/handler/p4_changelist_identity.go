package handler

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) beginP4ChangelistWrite(ctx context.Context, workspaceID pgtype.UUID) (pgx.Tx, *db.Queries, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	qtx := h.Queries.WithTx(tx)
	if _, err := qtx.LockP4ChangelistWorkspace(ctx, workspaceID); err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, err
	}
	return tx, qtx, nil
}

func (h *Handler) applyP4ChangelistStatus(ctx context.Context, workspaceID, id pgtype.UUID, daemonID string, update p4ChangelistStatusUpdate, submittedAt pgtype.Timestamptz) (db.P4Changelist, error) {
	tx, qtx, err := h.beginP4ChangelistWrite(ctx, workspaceID)
	if err != nil {
		return db.P4Changelist{}, err
	}
	defer tx.Rollback(ctx)
	row, err := qtx.GetP4ChangelistInWorkspace(ctx, db.GetP4ChangelistInWorkspaceParams{ID: id, WorkspaceID: workspaceID})
	if err != nil {
		return db.P4Changelist{}, err
	}
	if row.DaemonID != "" && row.DaemonID != daemonID {
		return db.P4Changelist{}, pgx.ErrNoRows
	}
	if row.PollGeneration != update.PollGeneration {
		return db.P4Changelist{}, pgx.ErrNoRows
	}
	key := strings.TrimSpace(update.ServerKey)
	if key == "" {
		key = row.ServerKey
	}
	original, number := update.OriginalNumber, update.Number
	if original <= 0 {
		original = row.OriginalNumber
	}
	if number <= 0 {
		number = row.Number
	}
	row, _, err = reconcileP4ChangelistIdentity(ctx, qtx, workspaceID, key, row.Port, number, original, row.ID)
	if err != nil {
		return db.P4Changelist{}, err
	}
	// A different daemon can already own the canonical row. Merging its
	// duplicate manual link must not transfer that ownership or overwrite its
	// observations; its normal poll will keep updating all the merged links.
	if (row.DaemonID == "" || row.DaemonID == daemonID) && row.PollGeneration == update.PollGeneration {
		row, err = qtx.UpdateP4ChangelistStatus(ctx, db.UpdateP4ChangelistStatusParams{
			ID: row.ID, WorkspaceID: workspaceID, Status: update.Status,
			Number: number, SubmittedAt: submittedAt, DaemonID: daemonID,
			PollGeneration: update.PollGeneration,
		})
		if err != nil {
			return db.P4Changelist{}, err
		}
		if state := strings.TrimSpace(update.SwarmReviewState); state != "" && row.SwarmReviewID.Valid {
			if err := qtx.UpdateP4ChangelistSwarmReviewState(ctx, db.UpdateP4ChangelistSwarmReviewStateParams{
				ID: row.ID, WorkspaceID: workspaceID, State: state,
			}); err != nil {
				return db.P4Changelist{}, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return db.P4Changelist{}, err
	}
	return row, nil
}

// A manual link initially knows a port and either a pending or submitted
// number. Adopt that row once Helix supplies its cluster/original number;
// if a canonical row exists, move all links before removing the placeholder.
func reconcileP4ChangelistIdentity(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, serverKey, port string, number, original int32, sourceID pgtype.UUID) (db.P4Changelist, bool, error) {
	rows, err := q.FindP4ChangelistIdentityRows(ctx, db.FindP4ChangelistIdentityRowsParams{
		WorkspaceID: workspaceID, ServerKey: serverKey, Port: port,
		Number: number, OriginalNumber: original, SourceID: sourceID,
	})
	if err != nil || len(rows) == 0 {
		return db.P4Changelist{}, false, err
	}
	canonical := rows[0]
	for _, duplicate := range rows[1:] {
		if err := q.MergeP4Changelist(ctx, db.MergeP4ChangelistParams{
			WorkspaceID: workspaceID, SourceID: duplicate.ID, TargetID: canonical.ID,
		}); err != nil {
			return db.P4Changelist{}, false, err
		}
	}
	canonical, err = q.SetP4ChangelistIdentity(ctx, db.SetP4ChangelistIdentityParams{
		ID: canonical.ID, WorkspaceID: workspaceID, ServerKey: serverKey, OriginalNumber: original,
	})
	return canonical, true, err
}
