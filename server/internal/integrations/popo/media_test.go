package popo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeStaging struct {
	rows []db.PopoMediaStaging
}

func (f *fakeStaging) ListPopoMediaStagingByBridgeEvent(context.Context, db.ListPopoMediaStagingByBridgeEventParams) ([]db.PopoMediaStaging, error) {
	return f.rows, nil
}

type fakeMediaLedger struct {
	refuse bool
	n      int
}

func (f *fakeMediaLedger) RecordPendingMediaObject(context.Context, engine.RecordPendingMediaObjectParams) (bool, error) {
	f.n++
	return !f.refuse, nil
}

func testMediaMsg(t *testing.T, media ...BridgeMedia) channel.InboundMessage {
	t.Helper()
	msg, ok := inboundFromBridge("default", "11111111-1111-1111-1111-111111111111", BridgeInbound{
		EventID:        "evt-media",
		Sender:         BridgeSender{ID: "alice@corp.netease.com"},
		Chat:           BridgeChat{ID: "alice@corp.netease.com", Type: "p2p"},
		AddressedToBot: true,
		Text:           "see this",
		CommandText:    "see this",
		Media:          media,
	})
	if !ok {
		t.Fatal("inbound rejected")
	}
	return msg
}

func TestPopoMediaResolverHasMedia(t *testing.T) {
	r := NewMediaResolver(nil, nil, nil)
	with := testMediaMsg(t, BridgeMedia{Kind: "image", Filename: "a.png", MimeType: "image/png"})
	if !r.HasMedia(with) {
		t.Fatal("HasMedia=false for media descriptors")
	}
	without, ok := inboundFromBridge("default", "", BridgeInbound{
		EventID:        "evt-text",
		Sender:         BridgeSender{ID: "alice@corp.netease.com"},
		Chat:           BridgeChat{ID: "alice@corp.netease.com", Type: "p2p"},
		AddressedToBot: true,
		Text:           "hello",
		CommandText:    "hello",
	})
	if !ok || r.HasMedia(without) {
		t.Fatalf("HasMedia for text-only = %v ok=%v", r.HasMedia(without), ok)
	}
}

func TestPopoMediaResolverPromotesUploadedStaging(t *testing.T) {
	bridgeID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	sessionID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	chatMessageID := util.MustParseUUID("33333333-3333-3333-3333-333333333333")
	ws := util.MustParseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	instID := util.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	staging := &fakeStaging{rows: []db.PopoMediaStaging{{
		ID:         sessionID,
		BridgeID:   bridgeID,
		EventID:    "evt-media",
		MediaIndex: 0,
		Status:     MediaStagingUploaded,
		Filename:   "a.png",
		MimeType:   "image/png",
		SizeBytes:  4,
		StorageKey: pgtype.Text{String: "workspaces/ws/popo/staging/s", Valid: true},
		StorageUrl: pgtype.Text{String: "https://cdn.example/workspaces/ws/popo/staging/s", Valid: true},
	}}}
	ledger := &fakeMediaLedger{}
	r := NewMediaResolver(staging, ledger, nil)
	msg := testMediaMsg(t, BridgeMedia{Index: 0, Kind: "image", Filename: "a.png", MimeType: "image/png"})
	got := r.ResolveMedia(context.Background(), engine.ResolvedInstallation{
		ID: instID, WorkspaceID: ws,
	}, engine.ResolvedIdentity{UserID: instID}, pgtype.UUID{}, chatMessageID, msg)
	if len(got.MediaRefs) != 1 {
		t.Fatalf("refs=%d want 1 text=%q", len(got.MediaRefs), got.Text)
	}
	if got.MediaRefs[0].Filename != "a.png" || got.MediaRefs[0].Type != channel.MsgTypeImage {
		t.Fatalf("ref=%+v", got.MediaRefs[0])
	}
	if !strings.Contains(got.Text, "[Image]") || strings.Contains(got.Text, "failed") {
		t.Fatalf("text=%q", got.Text)
	}
	if ledger.n != 1 {
		t.Fatalf("intent rows=%d", ledger.n)
	}
}

func TestPopoMediaResolverUnboundSenderDoesNotPromote(t *testing.T) {
	bridgeID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	staging := &fakeStaging{rows: []db.PopoMediaStaging{{
		BridgeID:   bridgeID,
		EventID:    "evt-media",
		MediaIndex: 0,
		Status:     MediaStagingUploaded,
		StorageKey: pgtype.Text{String: "k", Valid: true},
		StorageUrl: pgtype.Text{String: "https://cdn.example/k", Valid: true},
	}}}
	ledger := &fakeMediaLedger{}
	r := NewMediaResolver(staging, ledger, nil)
	msg := testMediaMsg(t, BridgeMedia{Kind: "image", Filename: "a.png"})
	got := r.ResolveMedia(context.Background(), engine.ResolvedInstallation{}, engine.ResolvedIdentity{}, pgtype.UUID{}, pgtype.UUID{}, msg)
	if len(got.MediaRefs) != 0 {
		t.Fatalf("unbound promoted %d refs", len(got.MediaRefs))
	}
	if !strings.Contains(got.Text, "failed") {
		t.Fatalf("unbound should leave a failure placeholder: %q", got.Text)
	}
	if ledger.n != 0 {
		t.Fatalf("unbound recorded %d intents", ledger.n)
	}
}

func TestPopoMediaResolverPartialStagingLeavesFailureVisible(t *testing.T) {
	bridgeID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	staging := &fakeStaging{rows: []db.PopoMediaStaging{{
		BridgeID:   bridgeID,
		EventID:    "evt-media",
		MediaIndex: 0,
		Status:     MediaStagingUploaded,
		Filename:   "a.png",
		StorageKey: pgtype.Text{String: "k0", Valid: true},
		StorageUrl: pgtype.Text{String: "https://cdn.example/k0", Valid: true},
	}}}
	r := NewMediaResolver(staging, &fakeMediaLedger{}, nil)
	msg := testMediaMsg(t,
		BridgeMedia{Index: 0, Kind: "image", Filename: "a.png"},
		BridgeMedia{Index: 1, Kind: "file", Filename: "notes.pdf"},
	)
	got := r.ResolveMedia(context.Background(), engine.ResolvedInstallation{
		ID: bridgeID, WorkspaceID: bridgeID,
	}, engine.ResolvedIdentity{UserID: bridgeID}, pgtype.UUID{}, bridgeID, msg)
	if len(got.MediaRefs) != 1 {
		t.Fatalf("refs=%d want 1", len(got.MediaRefs))
	}
	if !strings.Contains(got.Text, "[Image]") || !strings.Contains(got.Text, "[file: notes.pdf — failed]") {
		t.Fatalf("partial text=%q", got.Text)
	}
}

func TestPopoMediaResolverDoesNotWaitWithoutDeadline(t *testing.T) {
	r := NewMediaResolver(&fakeStaging{}, &fakeMediaLedger{}, nil).(*popoMediaResolver)
	msg := testMediaMsg(t, BridgeMedia{Kind: "image", Filename: "a.png"})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	got := r.ResolveMedia(ctx, engine.ResolvedInstallation{
		ID:          util.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"),
		WorkspaceID: util.MustParseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"),
	}, engine.ResolvedIdentity{UserID: util.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")}, pgtype.UUID{}, util.MustParseUUID("33333333-3333-3333-3333-333333333333"), msg)
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("ResolveMedia busy-waited for missing staging")
	}
	if len(got.MediaRefs) != 0 || !strings.Contains(got.Text, "failed") {
		t.Fatalf("missing staging = refs=%d text=%q", len(got.MediaRefs), got.Text)
	}
}

func TestClipOutboundText(t *testing.T) {
	short, overflow := clipOutboundText("hello", "https://app/x")
	if overflow || !strings.Contains(short, "hello") || !strings.Contains(short, "https://app/x") {
		t.Fatalf("short=%q overflow=%v", short, overflow)
	}
	long := strings.Repeat("x", MaxOutboundTextRunes+10)
	clipped, overflow := clipOutboundText(long, "https://app/x")
	if !overflow || utf8Count(clipped) <= MaxOutboundTextRunes {
		t.Fatalf("clipped runes=%d overflow=%v", utf8Count(clipped), overflow)
	}
	if !strings.Contains(clipped, "https://app/x") {
		t.Fatalf("missing link: %q", clipped)
	}
}

func utf8Count(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

func TestSendPayloadIncludesAttachments(t *testing.T) {
	raw, err := json.Marshal(SendPayload{
		RobotID:  "bot",
		ChatID:   "c",
		ChatType: "p2p",
		Text:     "hi",
		Attachments: []SendAttachment{{
			AttachmentID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
			Filename:     "a.png",
			MimeType:     "image/png",
			DownloadPath: "/api/popo/bridge/media/outbound/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "download_path") || !strings.Contains(string(raw), "a.png") {
		t.Fatalf("payload=%s", raw)
	}
}
