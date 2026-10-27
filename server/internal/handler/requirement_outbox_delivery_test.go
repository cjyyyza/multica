package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/popo"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type requirementPopoFixture struct {
	env          popoP2Env
	detail       service.RequirementDetail
	source       requirementSourceFixture
	operationID  string
	routeBinding string
}

func newRequirementPopoFixture(t *testing.T, externalID string) requirementPopoFixture {
	return newRequirementPopoFixtureWithParticipants(t, externalID, nil)
}

func newRequirementPopoFixtureWithParticipants(t *testing.T, externalID string, participants []service.RequirementParticipantInput) requirementPopoFixture {
	t.Helper()
	return newRequirementPopoFixtureWithSource(t, newRequirementSourceFixture(externalID), participants)
}

func newRequirementPopoFixtureWithSource(t *testing.T, source requirementSourceFixture, participants []service.RequirementParticipantInput) requirementPopoFixture {
	t.Helper()
	enableRequirementFlowForTest(t)
	if testPool == nil {
		t.Skip("database not available")
	}
	env := popoP2Setup(t)
	externalID := source.Source.ID
	_, agentID := newRequirementClaudePublicAgent(t, "Requirement outbox agent "+externalID)
	dbfx.Exec(t, `UPDATE channel_installation SET agent_id = $2 WHERE id = $1`, env.install, agentID)
	env.agentID = agentID
	sourceChatID := dbfx.ChatSession(t, agentID)
	channelChatID := "requirement-outbox-" + externalID
	routeBinding := dbfx.Insert(t, "channel_chat_session_binding", testutil.Cols{
		"chat_session_id": sourceChatID, "installation_id": env.install, "channel_type": "popo",
		"channel_chat_id": channelChatID, "chat_type": "group", "route_revision": 7,
	})
	operationID := dbfx.Insert(t, "yixiezuo_operation", testutil.Cols{
		"workspace_id": testWorkspaceID, "requested_by": testUserID, "kind": "preview", "state": "succeeded",
		"payload": requirementJSON(yixiezuo.OperationPayload{
			Source:  source.Source,
			Channel: &yixiezuo.ChannelScope{InstallationID: env.install, ChatID: channelChatID, ChatType: "group"},
		}),
		"result": source.Raw,
	})
	body := source.body(agentID)
	body["intakeOperationId"] = operationID
	if participants != nil {
		body["participants"] = participants
	}
	detail := createRequirementForTest(t, testUserID, body, "requirement-outbox-create-"+externalID)
	return requirementPopoFixture{env: env, detail: detail, source: source, operationID: operationID, routeBinding: routeBinding}
}

func requirementOutboxWorkerForTest() *popo.RequirementOutboxWorker {
	return popo.NewRequirementOutboxWorker(testHandler.Queries, testPool, "https://app.example.test", slog.Default())
}

// Requirement POPO deliveries are popo_bridge_command rows; there is no shadow
// outbox row to compare against.
func requirementDeliveryCommandsForTest(t *testing.T, requirementID string) []db.PopoBridgeCommand {
	t.Helper()
	rows, err := testHandler.Queries.ListRequirementPopoCommands(context.Background(), db.ListRequirementPopoCommandsParams{
		WorkspaceID: parseUUID(testWorkspaceID), RequirementID: parseUUID(requirementID), PageLimit: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func requirementDeliveryCommandForKind(t *testing.T, requirementID, kind string) db.PopoBridgeCommand {
	t.Helper()
	var found []db.PopoBridgeCommand
	for _, row := range requirementDeliveryCommandsForTest(t, requirementID) {
		if row.RequirementKind.String == kind {
			found = append(found, row)
		}
	}
	if len(found) != 1 {
		t.Fatalf("delivery kind %s rows = %d, want 1", kind, len(found))
	}
	return found[0]
}

func requirementLatestDeliveryCommandForKind(t *testing.T, requirementID, kind string) db.PopoBridgeCommand {
	t.Helper()
	var found *db.PopoBridgeCommand
	for _, row := range requirementDeliveryCommandsForTest(t, requirementID) {
		if row.RequirementKind.String == kind {
			copy := row
			found = &copy
		}
	}
	if found == nil {
		t.Fatalf("no delivery kind %s", kind)
	}
	return *found
}

func requirementDeliveryCommandForRecipient(t *testing.T, requirementID, userID string) db.PopoBridgeCommand {
	t.Helper()
	for _, row := range requirementDeliveryCommandsForTest(t, requirementID) {
		if row.RecipientUserID.Valid && uuidToString(row.RecipientUserID) == userID {
			return row
		}
	}
	t.Fatalf("no delivery for recipient %s", userID)
	return db.PopoBridgeCommand{}
}

func requirementCommandCount(t *testing.T, requirementID string) int {
	t.Helper()
	return dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE requirement_id = $1`, requirementID)
}

func forceRequirementDeliveryRetry(t *testing.T, row db.PopoBridgeCommand) {
	t.Helper()
	dbfx.Exec(t, `UPDATE popo_bridge_command SET next_attempt_at = now() - interval '1 second',
		lease_id = NULL, lease_expires_at = NULL WHERE id = $1`, row.ID)
}

// enableRequirementReceiptAttempt simulates a bridge that negotiated the
// receipt_attempt_v1 capability.
func enableRequirementReceiptAttempt(t *testing.T, f requirementPopoFixture) {
	t.Helper()
	dbfx.Exec(t, `UPDATE popo_bridge SET capabilities_json = $2::jsonb WHERE id = $1`, f.env.bridgeID,
		`["inbound", "send", "receipt_attempt_v1"]`)
}

func decodeRequirementSendPayload(t *testing.T, command db.PopoBridgeCommand) popo.SendPayload {
	t.Helper()
	var payload popo.SendPayload
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func boolPtr(value bool) *bool { return &value }

func TestRequirementDeliveryPreparationReceiptAndStableIdentity(t *testing.T) {
	f := newRequirementPopoFixture(t, "98101")
	worker := requirementOutboxWorkerForTest()
	result, err := worker.RunOnce(context.Background(), 10)
	if err != nil || result.Accepted != 1 {
		t.Fatalf("first worker pass = %+v, %v", result, err)
	}
	delivery := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	if delivery.Status != "pending" || !delivery.BridgeID.Valid || !delivery.InstallationID.Valid ||
		!delivery.RecipientBindingID.Valid || !delivery.AcceptedAt.Valid || !delivery.PayloadHash.Valid ||
		delivery.Attempt != 1 || delivery.DeliveredAt.Valid || !delivery.NotSent {
		t.Fatalf("prepared delivery = %+v", delivery)
	}
	payload := decodeRequirementSendPayload(t, delivery)
	if payload.Occurrence == nil || payload.Occurrence.ID != f.operationID || payload.Occurrence.Kind != "requirement_draft" ||
		payload.Recipient == nil || payload.Recipient.ChannelUserID != f.env.sender ||
		payload.ChatID != f.env.sender || payload.RequirementRevision != 1 || payload.RequirementID != f.detail.ID {
		t.Fatalf("draft payload = %+v", payload)
	}
	if !strings.Contains(payload.Text, "https://app.example.test/handler-tests/chat?requirement="+f.detail.ID) {
		t.Fatalf("draft link = %q", payload.Text)
	}
	var operationState string
	dbfx.QueryRow(t, `SELECT state FROM yixiezuo_operation WHERE id = $1`, f.operationID).Scan(&operationState)
	if operationState != "succeeded" {
		t.Fatalf("worker executed business ACTION: %s", operationState)
	}
	detail := requirementDetailForTest(t, testUserID, testWorkspaceID, f.detail.ID)
	if len(detail.Deliveries) != 1 || detail.Deliveries[0].State != "accepted" || detail.Deliveries[0].TransportCommandID == nil ||
		detail.Deliveries[0].AttemptCount != 1 {
		t.Fatalf("accepted detail deliveries = %+v", detail.Deliveries)
	}
	for range 2 {
		result, err = worker.RunOnce(context.Background(), 10)
		if err != nil || result.Claimed != 0 || requirementCommandCount(t, f.detail.ID) != 1 {
			t.Fatalf("repeat worker pass = %+v, %v", result, err)
		}
	}

	receipt := popo.CommandReceipt{Status: "delivered", RemoteMessageID: "popo-requirement-98101"}
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID), receipt); err != nil {
		t.Fatal(err)
	}
	delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	if delivery.Status != "delivered" || !delivery.DeliveredAt.Valid || delivery.NotSent || delivery.NeedsReconcile || !delivery.EvidenceFinalized {
		t.Fatalf("delivered delivery = %+v", delivery)
	}
	firstDeliveredAt := delivery.DeliveredAt.Time
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID), receipt); err != nil {
		t.Fatal(err)
	}
	delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	if !delivery.DeliveredAt.Time.Equal(firstDeliveredAt) || requirementCommandCount(t, f.detail.ID) != 1 {
		t.Fatalf("duplicate receipt changed delivery = %+v", delivery)
	}
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID), popo.CommandReceipt{Status: "failed", Error: "stale"}); !errors.Is(err, popo.ErrReceiptConflict) {
		t.Fatalf("conflicting receipt = %v", err)
	}
	if got := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft").Status; got != "delivered" {
		t.Fatalf("conflicting receipt changed state to %s", got)
	}
}

func TestRequirementDeliveryCrashWindows(t *testing.T) {
	t.Run("prepare rollback and committed response loss", func(t *testing.T) {
		f := newRequirementPopoFixture(t, "98102")
		failing := popo.NewRequirementOutboxWorker(testHandler.Queries, requirementCommitStarter{pool: testPool, injectedErr: errors.New("prepare rollback")}, "https://app.example.test", slog.Default())
		if _, err := failing.RunOnce(context.Background(), 10); err == nil {
			t.Fatal("expected prepare commit failure")
		}
		delivery := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
		if delivery.Status != "pending" || delivery.BridgeID.Valid {
			t.Fatalf("prepare escaped rollback: %+v", delivery)
		}
		forceRequirementDeliveryRetry(t, delivery)
		worker := requirementOutboxWorkerForTest()
		if _, err := worker.RunOnce(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
		delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
		if delivery.Status != "pending" || !delivery.BridgeID.Valid || requirementCommandCount(t, f.detail.ID) != 1 {
			t.Fatalf("recovered prepare = %+v", delivery)
		}

		f = newRequirementPopoFixture(t, "98103")
		failing = popo.NewRequirementOutboxWorker(testHandler.Queries, requirementCommitStarter{pool: testPool, commit: true, injectedErr: errors.New("prepare response loss")}, "https://app.example.test", slog.Default())
		if _, err := failing.RunOnce(context.Background(), 10); err == nil {
			t.Fatal("expected committed prepare response loss")
		}
		delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
		if !delivery.BridgeID.Valid || requirementCommandCount(t, f.detail.ID) != 1 {
			t.Fatalf("committed prepare was not recoverable: %+v", delivery)
		}
		worker = requirementOutboxWorkerForTest()
		if result, err := worker.RunOnce(context.Background(), 10); err != nil || result.Claimed != 0 || requirementCommandCount(t, f.detail.ID) != 1 {
			t.Fatalf("response-loss retry duplicated delivery: %+v, %v", result, err)
		}
	})

	t.Run("receipt rollback and committed response loss", func(t *testing.T) {
		f := newRequirementPopoFixture(t, "98104")
		worker := requirementOutboxWorkerForTest()
		if _, err := worker.RunOnce(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
		delivery := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
		receipt := popo.CommandReceipt{Status: "delivered", RemoteMessageID: "receipt-rollback-98104"}
		failing := popo.NewBridgeService(testHandler.Queries, requirementCommitStarter{pool: testPool, injectedErr: errors.New("receipt rollback")})
		if _, err := failing.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID), receipt); err == nil {
			t.Fatal("expected receipt commit failure")
		}
		if got := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft").Status; got != "pending" {
			t.Fatalf("receipt escaped rollback into %s", got)
		}
		if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID), receipt); err != nil {
			t.Fatal(err)
		}
		if got := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft").Status; got != "delivered" {
			t.Fatalf("replayed receipt state = %s", got)
		}

		f = newRequirementPopoFixture(t, "98105")
		if _, err := worker.RunOnce(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
		delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
		receipt.RemoteMessageID = "receipt-response-loss-98105"
		failing = popo.NewBridgeService(testHandler.Queries, requirementCommitStarter{pool: testPool, commit: true, injectedErr: errors.New("receipt response loss")})
		if _, err := failing.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID), receipt); err == nil {
			t.Fatal("expected committed receipt response loss")
		}
		if got := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft").Status; got != "delivered" {
			t.Fatalf("committed receipt was lost: %s", got)
		}
		if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID), receipt); err != nil {
			t.Fatal(err)
		}
		if requirementCommandCount(t, f.detail.ID) != 1 {
			t.Fatal("receipt replay created another delivery")
		}
	})
}

func TestRequirementDeliveryMultiRecipientBacklogAndReplicas(t *testing.T) {
	secondUser := dbfx.User(t, "Requirement second recipient", "requirement-second-98106@example.test")
	dbfx.Member(t, testWorkspaceID, secondUser, "member")
	f := newRequirementPopoFixtureWithParticipants(t, "98106", []service.RequirementParticipantInput{
		{UserID: testUserID, Roles: []string{"requester", "approver"}},
		{UserID: secondUser, Roles: []string{"viewer"}},
	})
	worker := requirementOutboxWorkerForTest()
	result, err := worker.RunOnce(context.Background(), 10)
	if err != nil || result.Accepted != 1 || result.Deferred != 1 {
		t.Fatalf("multi-recipient first pass = %+v, %v", result, err)
	}
	first := requirementDeliveryCommandForRecipient(t, f.detail.ID, testUserID)
	second := requirementDeliveryCommandForRecipient(t, f.detail.ID, secondUser)
	if !first.BridgeID.Valid || second.BridgeID.Valid || !second.LastError.Valid ||
		dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE id = $1 AND next_attempt_at > now()`, second.ID) != 1 {
		t.Fatalf("recipient states = first bridge %v second bridge %v last_error %v", first.BridgeID.Valid, second.BridgeID.Valid, second.LastError.Valid)
	}
	secondDeliveryID := second.DeliveryID
	secondChat := "second-recipient-98106@corp.netease.com"
	dbfx.Insert(t, "channel_user_binding", testutil.Cols{
		"workspace_id": testWorkspaceID, "multica_user_id": secondUser, "installation_id": f.env.install,
		"channel_type": "popo", "channel_user_id": secondChat,
	})
	forceRequirementDeliveryRetry(t, second)

	workers := []*popo.RequirementOutboxWorker{requirementOutboxWorkerForTest(), requirementOutboxWorkerForTest()}
	var wg sync.WaitGroup
	errs := make(chan error, len(workers)*2)
	for _, replica := range workers {
		for range 2 {
			wg.Add(1)
			go func(w *popo.RequirementOutboxWorker) {
				defer wg.Done()
				_, err := w.RunOnce(context.Background(), 10)
				errs <- err
			}(replica)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := requirementCommandCount(t, f.detail.ID); got != 2 {
		t.Fatalf("replica deliveries = %d, want 2", got)
	}
	firstAfter := requirementDeliveryCommandForRecipient(t, f.detail.ID, testUserID)
	secondAfter := requirementDeliveryCommandForRecipient(t, f.detail.ID, secondUser)
	if firstAfter.DeliveryID != first.DeliveryID || secondAfter.DeliveryID != secondDeliveryID {
		t.Fatalf("stable identity changed: first=%+v second=%+v", firstAfter, secondAfter)
	}
	if !secondAfter.BridgeID.Valid || !secondAfter.RecipientBindingID.Valid || secondAfter.RecipientBindingID == firstAfter.RecipientBindingID {
		t.Fatalf("second recipient binding = %+v", secondAfter)
	}
	payload := decodeRequirementSendPayload(t, secondAfter)
	if payload.ChatID != secondChat || payload.Recipient.ChannelUserID != secondChat || payload.Recipient.UserID != secondUser {
		t.Fatalf("second recipient payload = %+v", payload)
	}
}

func TestRequirementDeliveryUnknownDoesNotResend(t *testing.T) {
	f := newRequirementPopoFixture(t, "98107")
	worker := requirementOutboxWorkerForTest()
	if _, err := worker.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	delivery := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID), popo.CommandReceipt{Status: "unknown", Error: "send outcome unavailable"}); err != nil {
		t.Fatal(err)
	}
	delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	if delivery.Status != "unknown" || !delivery.UnknownAt.Valid || !delivery.NeedsReconcile || delivery.NotSent {
		t.Fatalf("unknown delivery = %+v", delivery)
	}
	forceRequirementDeliveryRetry(t, delivery)
	for range 3 {
		result, err := worker.RunOnce(context.Background(), 10)
		if err != nil || result.Claimed != 0 {
			t.Fatalf("unknown worker pass = %+v, %v", result, err)
		}
	}
	delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	if delivery.Status != "unknown" || delivery.Attempt != 1 || requirementCommandCount(t, f.detail.ID) != 1 {
		t.Fatalf("unknown was re-sent: %+v", delivery)
	}
}

func TestRequirementDeliveryConfirmedFailedRetryEvidence(t *testing.T) {
	f := newRequirementPopoFixture(t, "98108")
	worker := requirementOutboxWorkerForTest()
	if _, err := worker.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	delivery := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	// Leasing marks the command as handed to the bridge; a failure without
	// explicit not-sent evidence after that point is unknown, not retryable.
	if _, err := testHandler.PopoBridge.LeaseCommands(context.Background(), parseUUID(f.env.bridgeID), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID),
		popo.CommandReceipt{Status: "failed", Error: "no evidence"}); err != nil {
		t.Fatal(err)
	}
	delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	if delivery.Status != "unknown" || delivery.NotSent || !delivery.NeedsReconcile {
		t.Fatalf("unproven failure = %+v", delivery)
	}
	// The next attempt needs a fresh delivery row.
	f = newRequirementPopoFixture(t, "98118")
	if _, err := worker.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	receipt := popo.CommandReceipt{
		Status: "failed", Error: "confirmed not delivered",
		NotSent: boolPtr(true), Retryable: boolPtr(true), EvidenceFinalized: boolPtr(true),
	}
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID), receipt); err != nil {
		t.Fatal(err)
	}
	delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	if delivery.Status != "failed" || !delivery.NotSent || !delivery.Retryable || !delivery.EvidenceFinalized || delivery.Attempt != 1 {
		t.Fatalf("confirmed failed delivery = %+v", delivery)
	}
	// Without the attempt capability the row stays failed and is not retried.
	if _, err := worker.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if got := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft"); got.Status != "failed" || got.Attempt != 1 {
		t.Fatalf("legacy bridge command was re-sent: %+v", got)
	}
	enableRequirementReceiptAttempt(t, f)
	recovered, err := worker.RecoverConfirmedFailedRequirementDelivery(context.Background(), testHandler.Queries, parseUUID(testWorkspaceID), delivery.DeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != "pending" || recovered.Attempt != 2 || recovered.DeliveryID != delivery.DeliveryID ||
		recovered.DispatchedAt.Valid || !recovered.NotSent || recovered.EvidenceFinalized {
		t.Fatalf("recovered delivery = %+v", recovered)
	}
	if requirementCommandCount(t, f.detail.ID) != 1 {
		t.Fatal("confirmed-failed recovery created a new delivery")
	}
	oldAttempt := int32(1)
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID),
		popo.CommandReceipt{Status: "failed", Attempt: &oldAttempt}); !errors.Is(err, popo.ErrReceiptConflict) {
		t.Fatalf("stale receipt = %v", err)
	}
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID),
		popo.CommandReceipt{Status: "delivered", RemoteMessageID: "legacy-attempt-missing"}); !errors.Is(err, popo.ErrInvalidReceipt) {
		t.Fatalf("missing attempt receipt = %v", err)
	}
	newAttempt := int32(2)
	delivered := popo.CommandReceipt{Status: "delivered", RemoteMessageID: "delivered-attempt-2", Attempt: &newAttempt}
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID), delivered); err != nil {
		t.Fatal(err)
	}
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID), delivered); err != nil {
		t.Fatal(err)
	}
	if got := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft").Status; got != "delivered" {
		t.Fatalf("second-attempt receipt state = %s", got)
	}
	var remote string
	dbfx.QueryRow(t, `SELECT remote_message_id FROM popo_bridge_command WHERE id = $1`, delivery.ID).Scan(&remote)
	if remote != "delivered-attempt-2" {
		t.Fatalf("remote message id = %q", remote)
	}
}

func TestRequirementDeliveryTerminalReceiptProjectionAndLateEvidence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		externalID string
		status     string
		wantState  string
	}{
		{name: "queue expiry", externalID: "98115", status: "expired", wantState: "failed"},
		{name: "business supersession", externalID: "98116", status: "superseded", wantState: "superseded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRequirementPopoFixture(t, tc.externalID)
			worker := requirementOutboxWorkerForTest()
			if _, err := worker.RunOnce(context.Background(), 10); err != nil {
				t.Fatal(err)
			}
			delivery := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
			if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID),
				popo.CommandReceipt{Status: tc.status, Error: tc.name}); err != nil {
				t.Fatal(err)
			}
			delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
			if delivery.Status != tc.status || delivery.DeliveredAt.Valid || !delivery.NotSent {
				t.Fatalf("%s delivery = %+v", tc.status, delivery)
			}
			detail := requirementDetailForTest(t, testUserID, testWorkspaceID, f.detail.ID)
			if len(detail.Deliveries) != 1 || detail.Deliveries[0].State != tc.wantState {
				t.Fatalf("%s RequirementDelivery projection = %+v", tc.status, detail.Deliveries)
			}
			if tc.status == "superseded" && !delivery.SupersededAt.Valid {
				t.Fatal("superseded receipt did not retain superseded_at")
			}
			forceRequirementDeliveryRetry(t, delivery)
			result, err := worker.RunOnce(context.Background(), 10)
			if err != nil || result.Claimed != 0 {
				t.Fatalf("%s worker pass = %+v, %v", tc.status, result, err)
			}
			delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
			if delivery.Status != tc.status || delivery.Attempt != 1 || requirementCommandCount(t, f.detail.ID) != 1 {
				t.Fatalf("%s was automatically resent: %+v", tc.status, delivery)
			}
			if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), delivery.ID, parseUUID(f.env.bridgeID), popo.CommandReceipt{
				Status: "delivered", RemoteMessageID: "late-delivered-" + tc.externalID,
			}); err != nil {
				t.Fatal(err)
			}
			delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
			if delivery.Status != "delivered" || !delivery.DeliveredAt.Valid {
				t.Fatalf("late evidence after %s = %+v", tc.status, delivery)
			}
			if tc.status == "superseded" && !delivery.SupersededAt.Valid {
				t.Fatal("late delivered erased the business supersession timestamp")
			}
		})
	}
}

func TestRequirementDeliveryLeaseAndDispatchExpiry(t *testing.T) {
	t.Run("lease expiry after dispatch is unknown", func(t *testing.T) {
		f := newRequirementPopoFixture(t, "98121")
		worker := requirementOutboxWorkerForTest()
		if _, err := worker.RunOnce(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
		delivery := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
		if _, err := testHandler.PopoBridge.LeaseCommands(context.Background(), parseUUID(f.env.bridgeID), 0); err != nil {
			t.Fatal(err)
		}
		dbfx.Exec(t, `UPDATE popo_bridge_command SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, delivery.ID)
		if _, _, err := worker.Reconcile(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
		delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
		if delivery.Status != "unknown" || delivery.NotSent || !delivery.NeedsReconcile || !delivery.UnknownAt.Valid ||
			delivery.LastError.String != "POPO command lease expired before receipt" {
			t.Fatalf("expired lease delivery = %+v", delivery)
		}
		result, err := worker.RunOnce(context.Background(), 10)
		if err != nil || result.Claimed != 0 {
			t.Fatalf("expired lease was reclaimed: %+v, %v", result, err)
		}
	})

	t.Run("expiry before dispatch is a final failure", func(t *testing.T) {
		f := newRequirementPopoFixture(t, "98122")
		worker := requirementOutboxWorkerForTest()
		if _, err := worker.RunOnce(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
		delivery := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
		dbfx.Exec(t, `UPDATE popo_bridge_command SET expires_at = now() - interval '1 second', dispatched_at = NULL WHERE id = $1`, delivery.ID)
		if _, _, err := worker.Reconcile(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
		delivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
		if delivery.Status != "failed" || !delivery.NotSent || delivery.Retryable || !delivery.EvidenceFinalized ||
			delivery.LastError.String != "POPO command expired before dispatch" {
			t.Fatalf("expired before dispatch delivery = %+v", delivery)
		}
	})
}

func TestRequirementDeliveryCancelFencesQueuedCommand(t *testing.T) {
	f := newRequirementPopoFixture(t, "98109")
	worker := requirementOutboxWorkerForTest()
	if _, err := worker.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	draft := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	var cancelled service.RequirementDetail
	testutil.Call(t, testHandler.CancelRequirement, requirementActionRequest(testUserID, "cancel", f.detail.ID,
		service.CancelRequirementRequest{ExpectedRevision: ptr(int64(1))}, "requirement-outbox-cancel-98109")).Want(http.StatusOK).JSON(&cancelled)
	draft = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	if draft.Status != "superseded" || !draft.SupersededAt.Valid {
		t.Fatalf("cancel did not fence draft: %+v", draft)
	}
	allowed, err := testHandler.PopoBridge.CanDeliver(context.Background(), draft.ID, parseUUID(f.env.bridgeID))
	if err != nil || allowed {
		t.Fatalf("cancelled command authorize = %v, %v", allowed, err)
	}
	leased, err := testHandler.PopoBridge.LeaseCommands(context.Background(), parseUUID(f.env.bridgeID), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range leased {
		if command.ID == draft.ID {
			t.Fatal("cancelled command was leased")
		}
	}
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), draft.ID, parseUUID(f.env.bridgeID),
		popo.CommandReceipt{Status: "cancelled", Error: "authorize denied"}); err != nil {
		t.Fatal(err)
	}
	draft = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	if draft.Status != "superseded" {
		t.Fatalf("cancelled receipt changed business state: %+v", draft)
	}
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), draft.ID, parseUUID(f.env.bridgeID), popo.CommandReceipt{
		Status: "delivered", RemoteMessageID: "late-delivered-after-cancel",
	}); err != nil {
		t.Fatal(err)
	}
	draft = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	if draft.Status != "delivered" || !draft.SupersededAt.Valid || !draft.DeliveredAt.Valid {
		t.Fatalf("late receipt did not preserve transport truth: %+v", draft)
	}
	if _, err := worker.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM popo_bridge_command WHERE requirement_id = $1 AND requirement_kind = 'requirement_status'`, f.detail.ID); got != 0 {
		t.Fatalf("cancellation queued %d routine notifications", got)
	}
}

func TestRequirementDeliveryBusinessExpiryKeepsSuperseded(t *testing.T) {
	f := newRequirementPopoFixture(t, "98117")
	worker := requirementOutboxWorkerForTest()
	if _, err := worker.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	draft := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	var cancelled service.RequirementDetail
	testutil.Call(t, testHandler.CancelRequirement, requirementActionRequest(testUserID, "cancel", f.detail.ID,
		service.CancelRequirementRequest{ExpectedRevision: ptr(int64(1))}, "requirement-outbox-expiry-98117")).Want(http.StatusOK).JSON(&cancelled)
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), draft.ID, parseUUID(f.env.bridgeID),
		popo.CommandReceipt{Status: "expired", Error: "business occurrence expired"}); err != nil {
		t.Fatal(err)
	}
	draft = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	if draft.Status != "superseded" || !draft.SupersededAt.Valid {
		t.Fatalf("business expiry = %+v", draft)
	}
	forceRequirementDeliveryRetry(t, draft)
	if _, _, err := worker.Reconcile(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if got := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft"); got.Status != "superseded" {
		t.Fatalf("business expiry was retried: %+v", got)
	}
	if _, err := testHandler.PopoBridge.RecordReceipt(context.Background(), draft.ID, parseUUID(f.env.bridgeID), popo.CommandReceipt{
		Status: "delivered", RemoteMessageID: "late-delivered-after-business-expiry",
	}); err != nil {
		t.Fatal(err)
	}
	draft = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
	if draft.Status != "delivered" || !draft.SupersededAt.Valid || !draft.DeliveredAt.Valid {
		t.Fatalf("late evidence after business expiry = %+v", draft)
	}
}

func TestRequirementDeliveryRecipientAndTransportRevocation(t *testing.T) {
	assertFenced := func(t *testing.T, f requirementPopoFixture) {
		t.Helper()
		delivery := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
		if delivery.Status != "cancelled" && delivery.Status != "failed" {
			t.Fatalf("revocation state = %s", delivery.Status)
		}
		allowed, err := testHandler.PopoBridge.CanDeliver(context.Background(), delivery.ID, parseUUID(f.env.bridgeID))
		if err != nil || allowed {
			t.Fatalf("revoked command authorize = %v, %v", allowed, err)
		}
		forceRequirementDeliveryRetry(t, delivery)
		worker := requirementOutboxWorkerForTest()
		result, err := worker.RunOnce(context.Background(), 10)
		if err != nil || result.Accepted != 0 || result.Claimed != 0 || requirementCommandCount(t, f.detail.ID) != 1 {
			t.Fatalf("revoked delivery recovered unsafely: %+v, %v", result, err)
		}
	}

	t.Run("recipient binding removed", func(t *testing.T) {
		f := newRequirementPopoFixture(t, "98111")
		worker := requirementOutboxWorkerForTest()
		if _, err := worker.RunOnce(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
		delivery := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_draft")
		dbfx.Exec(t, `DELETE FROM channel_user_binding WHERE installation_id = $1 AND multica_user_id = $2`, f.env.install, testUserID)
		allowed, err := testHandler.PopoBridge.CanDeliver(context.Background(), delivery.ID, parseUUID(f.env.bridgeID))
		if err != nil || allowed {
			t.Fatalf("removed recipient authorize = %v, %v", allowed, err)
		}
		assertFenced(t, f)
	})

	t.Run("installation revoked", func(t *testing.T) {
		f := newRequirementPopoFixture(t, "98112")
		worker := requirementOutboxWorkerForTest()
		if _, err := worker.RunOnce(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
		if err := testHandler.PopoInstall.Revoke(context.Background(), parseUUID(f.env.install)); err != nil {
			t.Fatal(err)
		}
		assertFenced(t, f)
	})

	t.Run("bridge revoked", func(t *testing.T) {
		f := newRequirementPopoFixture(t, "98113")
		worker := requirementOutboxWorkerForTest()
		if _, err := worker.RunOnce(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
		if err := testHandler.PopoBridge.Revoke(context.Background(), parseUUID(f.env.bridgeID), parseUUID(testWorkspaceID), parseUUID(testUserID)); err != nil {
			t.Fatal(err)
		}
		assertFenced(t, f)
	})
}

func TestRequirementDeliveryBlockedReviewAcceptOccurrencesAndFence(t *testing.T) {
	f := newRequirementPopoFixture(t, "98110")
	opened := openRequirementForTest(t, testUserID, f.detail.ID, 1, "requirement-outbox-open-98110")
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, opened.PendingTask.TaskID)
	submitted := submitCandidateForTest(t, f.env.agentID, opened.PendingTask.TaskID, f.detail.ID, opened.Session.ID,
		validCandidateRequest(t, opened.Requirement, 2), "requirement-outbox-candidate-98110")
	f.detail = submitted
	approved, _ := approveRequirementForTest(t, requirementApproveFixture{
		Detail: f.detail, Opened: opened, AgentID: f.env.agentID, Source: f.source,
		OperationID: f.operationID, InstallationID: f.env.install, BindingID: f.routeBinding,
	}, "requirement-outbox-approve-98110")
	issueResponse := approveIssueForTest(t, approved)
	dbfx.Cleanup(t, `DELETE FROM issue WHERE origin_type = 'requirement_chat' AND origin_id = $1`, opened.Session.ID)
	dbfx.Cleanup(t, `DELETE FROM yixiezuo_import WHERE workspace_id = $1 AND source_host = $2 AND external_id = $3`, testWorkspaceID, f.source.Source.Host, f.source.Source.ID)
	dbfx.Cleanup(t, `DELETE FROM channel_issue_source WHERE installation_id = $1`, f.env.install)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE requirement_id = $1 OR issue_id IN (SELECT id FROM issue WHERE origin_type = 'requirement_chat' AND origin_id = $2)`, f.detail.ID, opened.Session.ID)
	dbfx.Cleanup(t, `DELETE FROM yixiezuo_operation WHERE issue_id IN (SELECT id FROM issue WHERE origin_type = 'requirement_chat' AND origin_id = $1)`, opened.Session.ID)

	initialTaskID := approved.Requirement.Run
	var run AgentTaskResponse
	if err := json.Unmarshal(initialTaskID, &run); err != nil {
		t.Fatal(err)
	}
	issue, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issueResponse.ID))
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, run.ID)
	var blocked struct {
		QuestionComment struct {
			ID string `json:"id"`
		} `json:"question_comment"`
	}
	blockFixture := requirementReviewFixture{
		requirementApproveFixture: requirementApproveFixture{Detail: f.detail, Opened: opened, AgentID: f.env.agentID, Source: f.source},
		Issue:                     issue,
	}
	testutil.Call(t, testHandler.BlockRequirement, requirementBlockRequest(blockFixture,
		run.ID, "Need acceptance detail", testUserID, issueResponse.Revision, "requirement-outbox-block-98110")).Want(http.StatusOK).JSON(&blocked)
	blockedDelivery := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_blocked")
	if blockedDelivery.OccurrenceID != parseUUID(blocked.QuestionComment.ID) {
		t.Fatalf("blocked occurrence = %+v", blockedDelivery)
	}
	worker := requirementOutboxWorkerForTest()
	if _, err := worker.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	blockedDelivery = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_blocked")
	blockedPayload := decodeRequirementSendPayload(t, blockedDelivery)
	if blockedPayload.CommentID != blocked.QuestionComment.ID || blockedPayload.TaskID != run.ID || !strings.Contains(blockedPayload.Text, "Need acceptance detail") {
		t.Fatalf("blocked payload = %+v", blockedPayload)
	}
	issue, err = testHandler.Queries.GetIssue(context.Background(), parseUUID(issueResponse.ID))
	if err != nil {
		t.Fatal(err)
	}
	// Answering the blocking question resumes execution, then the run delivers
	// the issue to review: both are explicit requirement actions.
	issue, err = service.UpdateIssueStatusWithRequirementGate(context.Background(), testHandler.Queries, db.UpdateIssueStatusParams{
		ID: issue.ID, WorkspaceID: issue.WorkspaceID, Status: "in_progress",
		RequirementAction: service.RequirementIssueAction(service.RequirementIssueActionWakeupResume),
	})
	if err != nil {
		t.Fatal(err)
	}
	issue, err = service.UpdateIssueStatusWithRequirementGate(context.Background(), testHandler.Queries, db.UpdateIssueStatusParams{
		ID: issue.ID, WorkspaceID: issue.WorkspaceID, Status: "in_review",
		RequirementAction: service.RequirementIssueAction(service.RequirementIssueActionTaskComplete),
	})
	if err != nil {
		t.Fatal(err)
	}
	review := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_review")
	var reviewAction string
	dbfx.QueryRow(t, `SELECT action FROM requirement_event WHERE id = $1`, review.OccurrenceID).Scan(&reviewAction)
	if reviewAction != "in_review" || review.OccurrenceID == parseUUID(f.operationID) {
		t.Fatalf("review occurrence = %+v action %s", review, reviewAction)
	}
	if got := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_blocked").Status; got != "superseded" {
		t.Fatalf("review did not supersede blocked intent: %s", got)
	}
	if _, err := worker.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	review = requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_review")
	initialReview := review
	blockFixture.Issue = issue
	rejectRequirementForTest(t, blockFixture, "Acceptance complete", "requirement-outbox-reject-98110")
	if got := requirementDeliveryCommandForKind(t, f.detail.ID, "requirement_review").Status; got != "superseded" {
		t.Fatalf("leaving review did not supersede review intent: %s", got)
	}
	allowed, err := testHandler.PopoBridge.CanDeliver(context.Background(), review.ID, parseUUID(f.env.bridgeID))
	if err != nil || allowed {
		t.Fatalf("left-review command authorize = %v, %v", allowed, err)
	}
	issue, err = service.UpdateIssueStatusWithRequirementGate(context.Background(), testHandler.Queries, db.UpdateIssueStatusParams{
		ID: issue.ID, WorkspaceID: issue.WorkspaceID, Status: "in_review",
		RequirementAction: service.RequirementIssueAction(service.RequirementIssueActionTaskComplete),
	})
	if err != nil {
		t.Fatal(err)
	}
	review = requirementLatestDeliveryCommandForKind(t, f.detail.ID, "requirement_review")
	if review.OccurrenceID == initialReview.OccurrenceID {
		t.Fatal("re-entering review reused the old occurrence")
	}
	if _, err := worker.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	review = requirementLatestDeliveryCommandForKind(t, f.detail.ID, "requirement_review")
	if !review.BridgeID.Valid {
		t.Fatalf("re-entered review was not prepared: %+v", review)
	}

	var accepted service.RequirementDetail
	testutil.Call(t, testHandler.AcceptRequirement, requirementActionRequest(testUserID, "accept", f.detail.ID,
		service.AcceptRequirementRequest{ExpectedIssueRevision: issue.Revision}, "requirement-outbox-accept-98110")).Want(http.StatusOK).JSON(&accepted)
	var operationID, operationState string
	dbfx.QueryRow(t, `SELECT id::text, state FROM yixiezuo_operation WHERE issue_id = $1 AND kind = 'publish'`, issue.ID).Scan(&operationID, &operationState)
	status := requirementLatestDeliveryCommandForKind(t, f.detail.ID, "requirement_status")
	var acceptedAction string
	dbfx.QueryRow(t, `SELECT action FROM requirement_event WHERE id = $1`, status.OccurrenceID).Scan(&acceptedAction)
	if acceptedAction != "accepted" || status.OccurrenceID == parseUUID(operationID) ||
		!status.OperationID.Valid || uuidToString(status.OperationID) != operationID {
		t.Fatalf("accept occurrence/operation = %+v action %s", status, acceptedAction)
	}
	if got := requirementLatestDeliveryCommandForKind(t, f.detail.ID, "requirement_review").Status; got != "superseded" {
		t.Fatalf("accept did not supersede review: %s", got)
	}
	allowed, err = testHandler.PopoBridge.CanDeliver(context.Background(), review.ID, parseUUID(f.env.bridgeID))
	if err != nil || allowed {
		t.Fatalf("old review authorize = %v, %v", allowed, err)
	}
	if _, err := worker.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	status = requirementLatestDeliveryCommandForKind(t, f.detail.ID, "requirement_status")
	if !status.BridgeID.Valid || status.Attempt != 1 {
		t.Fatalf("accept POPO delivery = %+v", status)
	}
	dbfx.QueryRow(t, `SELECT state FROM yixiezuo_operation WHERE id = $1`, operationID).Scan(&operationState)
	if operationState != "pending" {
		t.Fatalf("POPO worker completed yixiezuo operation: %s", operationState)
	}
}
