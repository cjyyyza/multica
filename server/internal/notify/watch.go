package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/cli"
)

type Pending struct {
	Key          string       `json:"key"`
	Notification Notification `json:"notification"`
}

type WatchState struct {
	Version   int      `json:"version"`
	Source    Source   `json:"source"`
	Status    string   `json:"status"`
	Signal    string   `json:"signal"`
	Sequence  int64    `json:"sequence"`
	Pending   *Pending `json:"pending,omitempty"`
	UpdatedAt string   `json:"updatedAt"`
}

type Watcher struct {
	Store  Store
	Sender Sender
	Target Target
	Source Source
	// Identity includes server, workspace, resource, config file and named target.
	Identity string
	Read     func(context.Context) (Snapshot, error)
	Interval time.Duration
	Once     bool
	Out      io.Writer
	Ready    func() error
}

func WatchID(server, workspace string, source Source, configPath, targetName string, target Target) string {
	return digest([]any{server, workspace, source, configPath, targetName, target})
}

func (w *Watcher) Path() string { return filepath.Join(w.Store.Dir, "watch-"+w.Identity+".json") }

func (w *Watcher) Run(ctx context.Context) error {
	if w.Interval < time.Second {
		return errors.New("watch interval must be at least one second")
	}
	if w.Identity == "" || w.Read == nil || w.Sender == nil {
		return errors.New("notification watcher is missing its identity, reader or sender")
	}
	if err := w.Source.Validate(); err != nil {
		return err
	}
	if err := w.Target.Validate(); err != nil {
		return err
	}
	if err := w.Store.prepare(); err != nil {
		return err
	}
	lock, err := lockFile(w.Path() + ".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	var state WatchState
	exists, err := readState(w.Path(), &state)
	if err != nil {
		return err
	}
	if exists && (state.Version != 1 || state.Source != w.Source) {
		return errors.New("invalid notification watch checkpoint")
	}
	if !exists {
		state = WatchState{Version: 1, Source: w.Source}
	}
	if w.Ready != nil {
		if err := w.Ready(); err != nil {
			return err
		}
	}
	for {
		snapshot, err := w.Read(ctx)
		if err != nil {
			// Reads have no external side effect. A live watch tolerates temporary outages.
			if w.Once || !retryableRead(err) {
				return err
			}
			if w.Out != nil {
				fmt.Fprintln(w.Out, "Multica read temporarily failed; retrying on the next poll")
			}
		} else {
			if err := w.step(ctx, &state, snapshot); err != nil {
				return err
			}
			if w.Once || snapshot.terminal() {
				return nil
			}
		}
		timer := time.NewTimer(w.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (w *Watcher) step(ctx context.Context, state *WatchState, snapshot Snapshot) error {
	if snapshot.ID == "" || snapshot.Status == "" {
		return errors.New("invalid notification snapshot")
	}
	signal := snapshot.signal()
	if state.Signal != signal {
		state.Sequence++
		state.Status, state.Signal, state.Pending = snapshot.Status, signal, nil
		if notification, ok := snapshot.notification(w.Source.Kind); ok {
			state.Pending = &Pending{Key: fmt.Sprintf("watch:%s:%d", w.Identity, state.Sequence), Notification: notification}
		}
		state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := writeState(w.Path(), state); err != nil {
			return err
		}
	}
	if state.Pending == nil {
		return nil
	}
	receipt, err := w.Store.Send(ctx, w.Sender, w.Target, state.Pending.Notification, state.Pending.Key)
	if err != nil {
		return err
	}
	if w.Out != nil {
		fmt.Fprintf(w.Out, "POPO %s: message %s\n", snapshot.Status, receipt.MessageID)
	}
	state.Pending = nil
	return writeState(w.Path(), state)
}

func (s Snapshot) notification(kind string) (Notification, bool) {
	var message string
	switch s.Status {
	case "awaiting_approval":
		message = "方案已就绪，请在 Multica 查看并批准。"
	case "blocked":
		message = "任务遇到阻塞，请在 Multica 查看并处理。"
		if s.Question != "" {
			message = s.Question + "\n\n请被点名的参与者在 Multica 答复。"
		}
	case "in_review":
		message = "执行结果已提交，请在 Multica 验收。"
	case "done":
		message = "任务已完成，请在 Multica 查看结果。"
	default:
		return Notification{}, false
	}
	// Lifecycle notices link to the full source; generic sends reject oversized text.
	message = preview(message, 2000)
	return Notification{Event: kind + "." + s.Status, Title: preview(s.Title, 250), Message: message, URL: s.URL}, true
}

func preview(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit]) + "\n…（内容较长，请打开 Multica 查看全文）"
}

func retryableRead(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var httpErr *cli.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == 429 || httpErr.StatusCode >= 500
	}
	var networkErr net.Error
	return errors.As(err, &networkErr) || errors.Is(err, io.ErrUnexpectedEOF)
}
