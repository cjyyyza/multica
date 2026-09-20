package popo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const stagingPollInterval = 100 * time.Millisecond

type stagingReader interface {
	ListPopoMediaStagingByBridgeEvent(ctx context.Context, arg db.ListPopoMediaStagingByBridgeEventParams) ([]db.PopoMediaStaging, error)
}

type popoMediaResolver struct {
	q      stagingReader
	ledger engine.MediaIntentLedger
	logger *slog.Logger
	poll   time.Duration
}

var _ engine.MediaResolver = (*popoMediaResolver)(nil)

func NewMediaResolver(q stagingReader, ledger engine.MediaIntentLedger, logger *slog.Logger) engine.MediaResolver {
	if logger == nil {
		logger = slog.Default()
	}
	return &popoMediaResolver{q: q, ledger: ledger, logger: logger, poll: stagingPollInterval}
}

func (r *popoMediaResolver) HasMedia(msg channel.InboundMessage) bool {
	raw, err := decodePopoRaw(msg)
	return err == nil && len(raw.Media) > 0
}

func (r *popoMediaResolver) ResolveMedia(ctx context.Context, inst engine.ResolvedInstallation, sender engine.ResolvedIdentity, _ pgtype.UUID, chatMessageID pgtype.UUID, msg channel.InboundMessage) channel.InboundMessage {
	raw, err := decodePopoRaw(msg)
	if err != nil || len(raw.Media) == 0 {
		return msg
	}
	if !sender.UserID.Valid {
		return rebuildMediaText(msg, raw, nil)
	}
	if r.q == nil || r.ledger == nil {
		r.logger.Warn("popo media resolve skipped: dependency missing", "message_id", msg.MessageID)
		return rebuildMediaText(msg, raw, nil)
	}
	bridgeID, err := util.ParseUUID(raw.BridgeID)
	if err != nil || !bridgeID.Valid {
		return rebuildMediaText(msg, raw, nil)
	}

	byIndex := r.loadStaging(ctx, bridgeID, strings.TrimSpace(msg.EventID), raw.Media)
	promoted := make(map[int]channel.MediaRef, len(raw.Media))
	for i, item := range raw.Media {
		if err := ctx.Err(); err != nil {
			r.logger.Warn("popo media budget spent",
				"installation_id", util.UUIDToString(inst.ID),
				"message_id", msg.MessageID,
				"done", i,
				"total", len(raw.Media),
				"error", err)
			break
		}
		row, ok := byIndex[item.Index]
		if !ok || row.Status != MediaStagingUploaded || !row.StorageKey.Valid || row.StorageKey.String == "" {
			continue
		}
		ref, err := r.promoteOne(ctx, inst, chatMessageID, raw.Media, i, item, row)
		if err != nil {
			r.logger.Warn("popo media promote failed",
				"installation_id", util.UUIDToString(inst.ID),
				"message_id", msg.MessageID,
				"index", item.Index,
				"error", err)
			continue
		}
		promoted[item.Index] = ref
	}
	return rebuildMediaText(msg, raw, promoted)
}

func (r *popoMediaResolver) loadStaging(ctx context.Context, bridgeID pgtype.UUID, eventID string, media []BridgeMedia) map[int]db.PopoMediaStaging {
	wanted := make(map[int]struct{}, len(media))
	for _, item := range media {
		wanted[item.Index] = struct{}{}
	}
	poll := r.poll
	if poll <= 0 {
		poll = stagingPollInterval
	}
	for {
		rows, err := r.q.ListPopoMediaStagingByBridgeEvent(ctx, db.ListPopoMediaStagingByBridgeEventParams{
			BridgeID: bridgeID,
			EventID:  eventID,
		})
		byIndex := make(map[int]db.PopoMediaStaging, len(rows))
		if err == nil {
			for _, row := range rows {
				byIndex[int(row.MediaIndex)] = row
			}
		}
		pending := false
		ready := true
		for idx := range wanted {
			row, ok := byIndex[idx]
			if !ok {
				ready = false
				continue
			}
			switch row.Status {
			case MediaStagingUploaded, MediaStagingFailed:
			case MediaStagingPending:
				pending = true
				ready = false
			default:
				ready = false
			}
		}
		if ready || !pending {
			return byIndex
		}
		if _, hasDeadline := ctx.Deadline(); !hasDeadline || ctx.Err() != nil {
			return byIndex
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return byIndex
		case <-timer.C:
		}
	}
}

func (r *popoMediaResolver) promoteOne(ctx context.Context, inst engine.ResolvedInstallation, chatMessageID pgtype.UUID, media []BridgeMedia, i int, item BridgeMedia, row db.PopoMediaStaging) (channel.MediaRef, error) {
	key := row.StorageKey.String
	link := row.StorageUrl.String
	if link == "" {
		link = key
	}
	owned, err := r.ledger.RecordPendingMediaObject(ctx, engine.RecordPendingMediaObjectParams{
		StorageKey:     key,
		WorkspaceID:    inst.WorkspaceID,
		ChatMessageID:  chatMessageID,
		StorageURL:     link,
		InstallationID: inst.ID,
	})
	if err != nil {
		return channel.MediaRef{}, fmt.Errorf("record media intent: %w", err)
	}
	if !owned {
		return channel.MediaRef{}, errors.New("media key owned by reconciler")
	}
	kind := parseMediaKind(item.Kind)
	filename := item.Filename
	if filename == "" {
		filename = row.Filename
	}
	mimeType := item.MimeType
	if mimeType == "" {
		mimeType = row.MimeType
	}
	size := row.SizeBytes
	if size == 0 {
		size = item.SizeBytes
	}
	return channel.MediaRef{
		Type:              kind,
		StorageKey:        key,
		StorageURL:        link,
		Filename:          filename,
		MimeType:          mimeType,
		SizeBytes:         size,
		InlinePlaceholder: mediaPlaceholder(kind),
		InlineIndex:       placeholderOccurrence(media, i),
	}, nil
}

func placeholderOccurrence(media []BridgeMedia, i int) int {
	if i < 0 || i >= len(media) {
		return 0
	}
	want := mediaPlaceholder(parseMediaKind(media[i].Kind))
	n := 0
	for j := 0; j < i; j++ {
		if mediaPlaceholder(parseMediaKind(media[j].Kind)) == want {
			n++
		}
	}
	return n
}

func rebuildMediaText(msg channel.InboundMessage, raw popoRawEvent, promoted map[int]channel.MediaRef) channel.InboundMessage {
	lines := make([]string, 0, len(raw.Media)+1)
	if body := strings.TrimSpace(raw.Body); body != "" {
		lines = append(lines, body)
	}
	successCount := map[string]int{}
	var refs []channel.MediaRef
	for _, item := range raw.Media {
		kind := parseMediaKind(item.Kind)
		ph := mediaPlaceholder(kind)
		if ref, ok := promoted[item.Index]; ok {
			ref.InlinePlaceholder = ph
			ref.InlineIndex = successCount[ph]
			successCount[ph]++
			refs = append(refs, ref)
			lines = append(lines, ph)
			continue
		}
		lines = append(lines, mediaFailurePlaceholder(item))
	}
	msg.Text = strings.Join(lines, "\n")
	msg.MediaRefs = refs
	return msg
}

func stagingObjectKey(workspaceID, sessionID pgtype.UUID) string {
	return path.Join(
		"workspaces",
		util.UUIDToString(workspaceID),
		"popo",
		"staging",
		util.UUIDToString(sessionID),
	)
}
