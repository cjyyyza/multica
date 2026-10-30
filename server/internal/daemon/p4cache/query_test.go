package p4cache

import (
	"errors"
	"testing"
)

func TestBoundedQueryWriterStopsAtLimit(t *testing.T) {
	cancelled := false
	w := &boundedQueryWriter{remaining: 4, cancel: func() { cancelled = true }}
	if n, err := w.Write([]byte("1234")); err != nil || n != 4 {
		t.Fatalf("first write: n=%d err=%v", n, err)
	}
	if n, err := w.Write([]byte("5")); !errors.Is(err, ErrQueryOutputTooLarge) || n != 0 {
		t.Fatalf("overflow write: n=%d err=%v", n, err)
	}
	if !cancelled {
		t.Fatal("overflow did not cancel the p4 process")
	}
	if w.Len() != 4 {
		t.Fatalf("buffer retained %d bytes, want 4", w.Len())
	}
}
