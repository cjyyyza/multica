package popo

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

type CreateMediaSessionParams struct {
	EventID   string
	Index     int
	Filename  string
	MimeType  string
	SizeBytes int64
	Kind      string
	RobotID   string
}

func (s *BridgeService) CreateMediaSession(ctx context.Context, bridge db.PopoBridge, p CreateMediaSessionParams) (db.PopoMediaStaging, error) {
	eventID := strings.TrimSpace(p.EventID)
	if eventID == "" {
		return db.PopoMediaStaging{}, ErrMissingEventID
	}
	kind, ok := canonicalMediaKind(p.Kind)
	if !ok {
		return db.PopoMediaStaging{}, ErrMediaInvalid
	}
	if p.Index < 0 {
		return db.PopoMediaStaging{}, ErrMediaInvalid
	}
	if p.SizeBytes > MaxInboundMediaBytes {
		return db.PopoMediaStaging{}, ErrMediaTooLarge
	}
	if p.SizeBytes < 0 {
		return db.PopoMediaStaging{}, ErrMediaInvalid
	}
	filename := cleanMediaFilename(p.Filename)
	if filename == "" {
		filename = fmt.Sprintf("popo-%s-%d", kind, p.Index+1)
	}
	mimeType := strings.TrimSpace(p.MimeType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	var installationID pgtype.UUID
	robotID := strings.TrimSpace(p.RobotID)
	if robotID != "" {
		normalized, err := normalizeRobotID(robotID)
		if err != nil {
			return db.PopoMediaStaging{}, err
		}
		robotID = normalized
		inst, err := s.q.GetChannelInstallationByAppID(ctx, db.GetChannelInstallationByAppIDParams{
			ChannelType: string(TypePopo),
			AppID:       robotID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return db.PopoMediaStaging{}, ErrInstallationWrong
			}
			return db.PopoMediaStaging{}, err
		}
		if inst.WorkspaceID != bridge.WorkspaceID || inst.Status != "active" {
			return db.PopoMediaStaging{}, ErrInstallationWrong
		}
		installationID = inst.ID
	}

	expiresAt := pgtype.Timestamptz{Time: s.now().Add(MediaSessionTTL), Valid: true}
	existing, err := s.q.GetPopoMediaStagingByEventIndex(ctx, db.GetPopoMediaStagingByEventIndexParams{
		BridgeID:   bridge.ID,
		EventID:    eventID,
		MediaIndex: int32(p.Index),
	})
	if err == nil {
		if existing.Status == MediaStagingUploaded && existing.ExpiresAt.Valid && existing.ExpiresAt.Time.After(s.now()) {
			return existing, nil
		}
		return s.q.ResetPopoMediaStaging(ctx, db.ResetPopoMediaStagingParams{
			ID:             existing.ID,
			BridgeID:       bridge.ID,
			InstallationID: installationID,
			RobotID:        robotID,
			Filename:       filename,
			MimeType:       mimeType,
			SizeBytes:      p.SizeBytes,
			Kind:           kind,
			ExpiresAt:      expiresAt,
		})
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.PopoMediaStaging{}, err
	}

	row, err := s.q.InsertPopoMediaStaging(ctx, db.InsertPopoMediaStagingParams{
		ID:             dbid.NewV7(),
		WorkspaceID:    bridge.WorkspaceID,
		BridgeID:       bridge.ID,
		InstallationID: installationID,
		RobotID:        robotID,
		EventID:        eventID,
		MediaIndex:     int32(p.Index),
		Filename:       filename,
		MimeType:       mimeType,
		SizeBytes:      p.SizeBytes,
		Kind:           kind,
		ExpiresAt:      expiresAt,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return s.q.GetPopoMediaStagingByEventIndex(ctx, db.GetPopoMediaStagingByEventIndexParams{
				BridgeID:   bridge.ID,
				EventID:    eventID,
				MediaIndex: int32(p.Index),
			})
		}
		return db.PopoMediaStaging{}, err
	}
	return row, nil
}

func (s *BridgeService) PutMediaSession(ctx context.Context, bridge db.PopoBridge, sessionID pgtype.UUID, data []byte, contentType string) (db.PopoMediaStaging, error) {
	if s.storage == nil || s.ledger == nil {
		return db.PopoMediaStaging{}, ErrMediaStorageUnavailable
	}
	if int64(len(data)) > MaxInboundMediaBytes {
		return db.PopoMediaStaging{}, ErrMediaTooLarge
	}
	if len(data) == 0 {
		return db.PopoMediaStaging{}, ErrMediaInvalid
	}
	row, err := s.q.GetPopoMediaStagingForBridge(ctx, db.GetPopoMediaStagingForBridgeParams{
		ID:       sessionID,
		BridgeID: bridge.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.PopoMediaStaging{}, ErrMediaSessionNotFound
		}
		return db.PopoMediaStaging{}, err
	}
	if !row.ExpiresAt.Valid || !row.ExpiresAt.Time.After(s.now()) {
		return db.PopoMediaStaging{}, ErrMediaSessionExpired
	}
	if row.Status == MediaStagingUploaded {
		return row, nil
	}
	if row.Status != MediaStagingPending {
		return db.PopoMediaStaging{}, ErrMediaSessionExpired
	}
	if !contentTypeMatches(row.MimeType, contentType) {
		return db.PopoMediaStaging{}, ErrMediaContentTypeMismatch
	}

	key := stagingObjectKey(row.WorkspaceID, row.ID)
	link := s.storage.ObjectURL(key)
	owned, err := s.ledger.RecordPendingMediaObject(ctx, engine.RecordPendingMediaObjectParams{
		StorageKey:     key,
		WorkspaceID:    row.WorkspaceID,
		ChatMessageID:  row.ID,
		StorageURL:     link,
		InstallationID: row.InstallationID,
	})
	if err != nil {
		return db.PopoMediaStaging{}, fmt.Errorf("record media intent: %w", err)
	}
	if !owned {
		return db.PopoMediaStaging{}, errors.New("media key owned by reconciler")
	}
	if _, err := s.storage.Upload(ctx, key, data, row.MimeType, row.Filename); err != nil {
		_, _ = s.q.MarkPopoMediaStagingFailed(ctx, db.MarkPopoMediaStagingFailedParams{
			ID:       row.ID,
			BridgeID: bridge.ID,
			Error:    pgtype.Text{String: "upload failed", Valid: true},
		})
		return db.PopoMediaStaging{}, fmt.Errorf("upload staged media: %w", err)
	}
	uploaded, err := s.q.MarkPopoMediaStagingUploaded(ctx, db.MarkPopoMediaStagingUploadedParams{
		ID:         row.ID,
		BridgeID:   bridge.ID,
		StorageKey: pgtype.Text{String: key, Valid: true},
		StorageUrl: pgtype.Text{String: link, Valid: true},
		SizeBytes:  int64(len(data)),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.PopoMediaStaging{}, ErrMediaSessionExpired
		}
		return db.PopoMediaStaging{}, err
	}
	return uploaded, nil
}

func (s *BridgeService) GetOutboundMedia(ctx context.Context, bridge db.PopoBridge, attachmentID pgtype.UUID) (db.Attachment, error) {
	grant, err := s.q.GetPopoOutboundMediaGrant(ctx, db.GetPopoOutboundMediaGrantParams{
		BridgeID:     bridge.ID,
		AttachmentID: attachmentID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Attachment{}, ErrOutboundMediaDenied
		}
		return db.Attachment{}, err
	}
	if grant.WorkspaceID != bridge.WorkspaceID {
		return db.Attachment{}, ErrOutboundMediaDenied
	}
	att, err := s.q.GetAttachmentByIDOnly(ctx, attachmentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Attachment{}, ErrOutboundMediaDenied
		}
		return db.Attachment{}, err
	}
	if att.WorkspaceID != grant.WorkspaceID || att.ID != grant.AttachmentID {
		return db.Attachment{}, ErrOutboundMediaDenied
	}
	return att, nil
}

func canonicalMediaKind(kind string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case MediaKindImage, MediaKindFile, MediaKindAudio, MediaKindVideo:
		return strings.ToLower(strings.TrimSpace(kind)), true
	default:
		return "", false
	}
}

func contentTypeMatches(declared, got string) bool {
	declared = stripContentType(declared)
	got = stripContentType(got)
	if got == "" || got == "application/octet-stream" {
		return true
	}
	if declared == "" {
		return true
	}
	return strings.EqualFold(declared, got)
}

func stripContentType(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	if i := strings.IndexByte(v, ';'); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}

func outboundDownloadPath(attachmentID pgtype.UUID) string {
	return OutboundMediaPath + util.UUIDToString(attachmentID)
}

func sendAttachmentFrom(id pgtype.UUID, filename, mimeType string) SendAttachment {
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	if filename == "" {
		filename = path.Base(util.UUIDToString(id))
	}
	return SendAttachment{
		AttachmentID: util.UUIDToString(id),
		Filename:     filename,
		MimeType:     mimeType,
		DownloadPath: outboundDownloadPath(id),
	}
}
