package notify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type senderFunc func(context.Context, Target, Notification) (Receipt, error)

func (f senderFunc) Send(ctx context.Context, target Target, notification Notification) (Receipt, error) {
	return f(ctx, target, notification)
}

func TestStoreReplaysReceiptAndRejectsChangedRequest(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	target := Target{RobotName: "DeepSeek Harness", Receiver: "8493291"}
	notification := Notification{Event: "question", Message: "中文问题"}
	calls := 0
	sender := senderFunc(func(context.Context, Target, Notification) (Receipt, error) {
		calls++
		return Receipt{MessageID: "sent-id", RobotName: target.RobotName, Receiver: target.Receiver}, nil
	})
	for i := 0; i < 2; i++ {
		receipt, err := store.Send(context.Background(), sender, target, notification, "task:question:1")
		if err != nil || receipt.MessageID != "sent-id" {
			t.Fatalf("replay %d: %+v %v", i, receipt, err)
		}
	}
	if calls != 1 {
		t.Fatalf("duplicate send count %d", calls)
	}
	if _, err := store.Send(context.Background(), sender, target, Notification{Message: "changed"}, "task:question:1"); err == nil {
		t.Fatal("changed body reused a key")
	}
	target.Receiver = "1234567"
	if _, err := store.Send(context.Background(), sender, target, notification, "task:question:1"); err == nil {
		t.Fatal("changed destination reused a key")
	}
	if calls != 1 {
		t.Fatal("key conflict sent a message")
	}
}

func TestStoreDoesNotRetryUnknownOrInterruptedSend(t *testing.T) {
	for _, state := range []string{"unknown", "sending"} {
		t.Run(state, func(t *testing.T) {
			store := Store{Dir: t.TempDir()}
			target := Target{RobotName: "DeepSeek Harness", Receiver: "8493291"}
			calls := 0
			sender := senderFunc(func(context.Context, Target, Notification) (Receipt, error) {
				calls++
				return Receipt{}, &SendError{Err: errors.New("response lost"), Unknown: true}
			})
			_, err := store.Send(context.Background(), sender, target, Notification{Message: "Question"}, "key")
			if err == nil {
				t.Fatal("lost response reported as sent")
			}
			if state == "sending" {
				path := filepath.Join(store.Dir, "delivery-"+digest("key")+".json")
				var delivery Delivery
				if _, err := readState(path, &delivery); err != nil {
					t.Fatal(err)
				}
				delivery.Status = "sending"
				if err := writeState(path, &delivery); err != nil {
					t.Fatal(err)
				}
			}
			_, err = store.Send(context.Background(), sender, target, Notification{Message: "Question"}, "key")
			var uncertain *SendError
			if !errors.As(err, &uncertain) || !uncertain.Unknown || calls != 1 {
				t.Fatalf("uncertain side effect was retried: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestStoreRetriesPreflightFailureWithSameIntent(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	target := Target{RobotName: "DeepSeek Harness", Receiver: "8493291"}
	calls := 0
	sender := senderFunc(func(context.Context, Target, Notification) (Receipt, error) {
		calls++
		if calls == 1 {
			return Receipt{}, errors.New("mention lookup unavailable")
		}
		return Receipt{MessageID: "receipt"}, nil
	})
	if _, err := store.Send(context.Background(), sender, target, Notification{Message: "Question"}, "key"); err == nil {
		t.Fatal("failure ignored")
	}
	if _, err := store.Send(context.Background(), sender, target, Notification{Message: "Question"}, "key"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("retry count %d", calls)
	}
}

func TestStoreSerializesConcurrentSenders(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	target := Target{RobotName: "DeepSeek Harness", Receiver: "8493291"}
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	sender := senderFunc(func(context.Context, Target, Notification) (Receipt, error) {
		close(started)
		<-release
		return Receipt{MessageID: "receipt"}, nil
	})
	go func() {
		_, err := store.Send(context.Background(), sender, target, Notification{Message: "body"}, "key")
		finished <- err
	}()
	<-started
	_, err := store.Send(context.Background(), sender, target, Notification{Message: "body"}, "key")
	close(release)
	if firstErr := <-finished; firstErr != nil {
		t.Fatal(firstErr)
	}
	if err == nil {
		t.Fatal("concurrent sender bypassed lock")
	}
	files, err := filepath.Glob(filepath.Join(store.Dir, ".notify-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("incomplete atomic writes: %v %v", files, err)
	}
	if _, err := os.Stat(filepath.Join(store.Dir, "delivery-"+digest("key")+".json")); err != nil {
		t.Fatal(err)
	}
}

func TestStoreConfirmsVerifiedReceiptWithoutResending(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	target := Target{RobotName: "DeepSeek Harness", Receiver: "8493291"}
	notification := Notification{Message: "Question"}
	calls := 0
	sender := senderFunc(func(context.Context, Target, Notification) (Receipt, error) {
		calls++
		return Receipt{}, &SendError{Err: errors.New("response lost"), Unknown: true}
	})
	if _, err := store.Send(context.Background(), sender, target, notification, "key"); err == nil {
		t.Fatal("expected unknown outcome")
	}
	receipt, err := store.Confirm("key", "verified-id")
	if err != nil || receipt.MessageID != "verified-id" || receipt.RobotName != target.RobotName {
		t.Fatalf("confirmation: %+v %v", receipt, err)
	}
	if _, err := store.Confirm("key", "different-id"); err == nil {
		t.Fatal("confirmed receipt was replaced")
	}
	if _, err := store.Send(context.Background(), sender, target, notification, "key"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("confirmation replay sent another message")
	}
	if _, err := store.Confirm("missing", "invented-id"); err == nil {
		t.Fatal("confirmation manufactured a delivery record")
	}
}
