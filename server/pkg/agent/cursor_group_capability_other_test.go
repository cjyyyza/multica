//go:build !linux

package agent

import "testing"

func requireCursorBackgroundGroupSignals(t *testing.T) { t.Helper() }
