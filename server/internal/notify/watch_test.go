package notify

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestWatchSurvivesRestartWithoutDuplicateNotices(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	target := Target{RobotName: "DeepSeek Harness", Receiver: "8493291"}
	source := Source{Kind: "requirement", ID: "req"}
	id := WatchID("https://api.multica.example", "ws", source, "/project/AGENTS.md", "team", target)
	var notices []Notification
	sender := senderFunc(func(_ context.Context, _ Target, n Notification) (Receipt, error) {
		notices = append(notices, n)
		return Receipt{MessageID: "receipt"}, nil
	})
	observations := []Snapshot{
		{Status: "clarifying"},
		{Status: "awaiting_approval", CandidateID: "c1"},
		{Status: "awaiting_approval", CandidateID: "c1", Title: "Edited title"},
		{Status: "awaiting_approval", CandidateID: "c2"},
		{Status: "executing"},
		{Status: "blocked", QuestionID: "q1", Question: "选择 A 还是 B？"},
		{Status: "blocked", QuestionID: "q1", Question: "选择 A 还是 B？", Title: "Edited title"},
		{Status: "blocked", QuestionID: "q2", Question: "何时发布？"},
		{Status: "executing"},
		{Status: "in_review"},
		{Status: "in_review", Title: "Edited title"},
		{Status: "executing"},
		{Status: "in_review"},
		{Status: "done"},
		{Status: "done"},
	}
	for i, snapshot := range observations {
		snapshot.ID, snapshot.Revision, snapshot.URL = "req", int64(i+1), "https://multica.example/team/chat?requirement=req"
		watcher := Watcher{Store: store, Sender: sender, Target: target, Source: source, Identity: id, Once: true, Interval: time.Second, Read: func(context.Context) (Snapshot, error) { return snapshot, nil }}
		if err := watcher.Run(context.Background()); err != nil {
			t.Fatalf("observation %d: %v", i, err)
		}
	}
	var events []string
	for _, notice := range notices {
		events = append(events, notice.Event)
	}
	want := []string{"requirement.awaiting_approval", "requirement.awaiting_approval", "requirement.blocked", "requirement.blocked", "requirement.in_review", "requirement.in_review", "requirement.done"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%v want=%v", events, want)
	}
	if notices[2].Message != "选择 A 还是 B？\n\n请被点名的参与者在 Multica 答复。" {
		t.Fatalf("actual question missing: %s", notices[2].Message)
	}
}

func TestWatchFreezesPendingPayloadAndReplaysReceipt(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	target := Target{RobotName: "DeepSeek Harness", Receiver: "8493291"}
	source := Source{Kind: "issue", ID: "issue"}
	snapshot := Snapshot{ID: "issue", Status: "in_review", Title: "Original title", URL: "https://multica.example/team/issues/MUL-1"}
	calls := 0
	watcher := Watcher{Store: store, Target: target, Source: source, Identity: "pending", Once: true, Interval: time.Second, Read: func(context.Context) (Snapshot, error) { return snapshot, nil }, Sender: senderFunc(func(_ context.Context, _ Target, n Notification) (Receipt, error) {
		calls++
		if calls == 1 {
			return Receipt{}, errors.New("POPO preflight unavailable")
		}
		if n.Title != "Original title" {
			t.Fatal("retry changed payload under the same idempotency key")
		}
		return Receipt{MessageID: "sent"}, nil
	})}
	if err := watcher.Run(context.Background()); err == nil {
		t.Fatal("send failure ignored")
	}
	var state WatchState
	if _, err := readState(watcher.Path(), &state); err != nil {
		t.Fatal(err)
	}
	pending := state.Pending
	if pending == nil {
		t.Fatal("failed delivery not retained")
	}
	snapshot.Title = "Later title"
	if err := watcher.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Simulate death after saving the receipt but before clearing the pending checkpoint.
	state.Pending = pending
	if err := writeState(watcher.Path(), &state); err != nil {
		t.Fatal(err)
	}
	if err := watcher.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("receipt replay sent another notification: %d", calls)
	}
}

func TestWatchStopsAtCancellationAndHonorsContext(t *testing.T) {
	for _, status := range []string{"cancelled", "executing"} {
		calls := 0
		ctx, cancel := context.WithCancel(context.Background())
		watcher := Watcher{Store: Store{Dir: t.TempDir()}, Target: Target{RobotName: "DeepSeek Harness", Receiver: "8493291"}, Source: Source{Kind: "issue", ID: "issue"}, Identity: "test", Interval: time.Second,
			Read: func(context.Context) (Snapshot, error) {
				calls++
				if status == "executing" {
					cancel()
				}
				return Snapshot{ID: "issue", Status: status}, nil
			},
			Sender: senderFunc(func(context.Context, Target, Notification) (Receipt, error) {
				t.Fatal("non-notifiable state sent a message")
				return Receipt{}, nil
			})}
		err := watcher.Run(ctx)
		cancel()
		if calls != 1 || status == "cancelled" && err != nil || status == "executing" && !errors.Is(err, context.Canceled) {
			t.Fatalf("status=%s calls=%d error=%v", status, calls, err)
		}
	}
}

func TestWatchReadRetryPolicy(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUpgradeRequired} {
		want := status == http.StatusTooManyRequests || status >= 500
		if got := retryableRead(&cli.HTTPError{StatusCode: status}); got != want {
			t.Fatalf("HTTP %d retry=%t", status, got)
		}
	}
	if retryableRead(errors.New("malformed response")) || retryableRead(context.Canceled) {
		t.Fatal("permanent failures retried")
	}
}
