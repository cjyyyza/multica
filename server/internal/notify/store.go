package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Sender interface {
	// Send reports ambiguous external side effects with SendError.Unknown.
	Send(context.Context, Target, Notification) (Receipt, error)
}

// Store records intent before the external side effect. POPO has no idempotency key.
type Store struct{ Dir string }

type Delivery struct {
	Version      int          `json:"version"`
	Key          string       `json:"key"`
	RequestHash  string       `json:"requestHash"`
	Status       string       `json:"status"`
	Target       Target       `json:"target"`
	Notification Notification `json:"notification"`
	Receipt      *Receipt     `json:"receipt,omitempty"`
	UpdatedAt    string       `json:"updatedAt"`
}

func digest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (s Store) prepare() error {
	if s.Dir == "" {
		return errors.New("notification state directory is required")
	}
	return os.MkdirAll(s.Dir, 0700)
}

func (s Store) Send(ctx context.Context, sender Sender, target Target, notification Notification, key string) (Receipt, error) {
	if err := target.Validate(); err != nil {
		return Receipt{}, err
	}
	if _, err := notification.Text(); err != nil {
		return Receipt{}, err
	}
	requestHash := digest(struct {
		Target       Target
		Notification Notification
	}{target, notification})
	if key == "" {
		key = requestHash
	}
	if err := s.prepare(); err != nil {
		return Receipt{}, err
	}
	path := filepath.Join(s.Dir, "delivery-"+digest(key)+".json")
	lock, err := lockFile(path + ".lock")
	if err != nil {
		return Receipt{}, err
	}
	defer lock.Close()
	var delivery Delivery
	exists, err := readState(path, &delivery)
	if err != nil {
		return Receipt{}, err
	}
	if exists {
		if delivery.Version != 1 || delivery.Key != key || delivery.RequestHash != requestHash {
			return Receipt{}, fmt.Errorf("notification key %q already belongs to a different request", key)
		}
		switch delivery.Status {
		case "sent":
			if delivery.Receipt == nil || delivery.Receipt.MessageID == "" {
				return Receipt{}, errors.New("notification receipt is corrupt")
			}
			return *delivery.Receipt, nil
		case "sending", "unknown":
			return Receipt{}, &SendError{Err: fmt.Errorf("notification %q has an unknown delivery outcome; verify POPO before resending (record: %s)", key, path), Unknown: true}
		case "failed":
		default:
			return Receipt{}, fmt.Errorf("invalid notification delivery state in %s", path)
		}
	}
	delivery = Delivery{Version: 1, Key: key, RequestHash: requestHash, Status: "sending", Target: target, Notification: notification}
	if err := writeState(path, &delivery); err != nil {
		return Receipt{}, err
	}
	receipt, sendErr := sender.Send(ctx, target, notification)
	if sendErr == nil && receipt.MessageID == "" {
		sendErr = &SendError{Err: errors.New("sender returned no message receipt"), Unknown: true}
	}
	if sendErr != nil {
		delivery.Status = "failed"
		var uncertain *SendError
		if errors.As(sendErr, &uncertain) && uncertain.Unknown {
			delivery.Status = "unknown"
		}
		if err := writeState(path, &delivery); err != nil {
			return Receipt{}, &SendError{Err: fmt.Errorf("send failed and delivery state could not be saved: %w", err), Unknown: true}
		}
		if delivery.Status == "unknown" {
			return Receipt{}, &SendError{Err: fmt.Errorf("%w; verify POPO before resending (record: %s)", sendErr, path), Unknown: true}
		}
		return Receipt{}, sendErr
	}
	delivery.Status, delivery.Receipt = "sent", &receipt
	if err := writeState(path, &delivery); err != nil {
		return Receipt{}, &SendError{Err: fmt.Errorf("POPO returned message %s but saving its receipt failed: %w", receipt.MessageID, err), Unknown: true}
	}
	return receipt, nil
}

// Confirm records an operator-verified POPO receipt without sending another message.
func (s Store) Confirm(key, messageID string) (Receipt, error) {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(messageID) == "" || strings.ContainsAny(messageID, "\r\n") {
		return Receipt{}, errors.New("a notification key and verified POPO message ID are required")
	}
	if err := s.prepare(); err != nil {
		return Receipt{}, err
	}
	path := filepath.Join(s.Dir, "delivery-"+digest(key)+".json")
	lock, err := lockFile(path + ".lock")
	if err != nil {
		return Receipt{}, err
	}
	defer lock.Close()
	var delivery Delivery
	exists, err := readState(path, &delivery)
	if err != nil {
		return Receipt{}, err
	}
	if !exists || delivery.Version != 1 || delivery.Key != key {
		return Receipt{}, errors.New("notification delivery record not found or invalid")
	}
	if delivery.Status == "sent" && delivery.Receipt != nil && delivery.Receipt.MessageID == messageID {
		return *delivery.Receipt, nil
	}
	if delivery.Status != "unknown" && delivery.Status != "sending" {
		return Receipt{}, errors.New("only an unconfirmed delivery can be confirmed; existing receipts cannot be replaced")
	}
	receipt := Receipt{MessageID: messageID, RobotName: delivery.Target.RobotName, Receiver: delivery.Target.Receiver, AtUIDs: delivery.Target.AtUIDs}
	delivery.Status, delivery.Receipt = "sent", &receipt
	if err := writeState(path, &delivery); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func readState(path string, value any) (bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, value); err != nil {
		return false, fmt.Errorf("invalid notification state %s: %w", path, err)
	}
	return true, nil
}

func writeState(path string, value any) error {
	if delivery, ok := value.(*Delivery); ok {
		delivery.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".notify-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
