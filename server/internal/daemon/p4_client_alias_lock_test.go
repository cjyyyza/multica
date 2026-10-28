package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
)

func TestP4ClientLocksCoverServerAliasesAndSharedRoots(t *testing.T) {
	for _, sharedClient := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared root", true: "shared client via alias"}[sharedClient], func(t *testing.T) {
			root := t.TempDir()
			first := &p4cache.Workspace{Port: "ssl:first:1666", Client: "client", ServerCluster: "cluster", Root: root}
			second := &p4cache.Workspace{Port: "ssl:alias:1666", Client: "another", ServerCluster: "cluster", Root: root}
			if sharedClient {
				second.Client = "client"
				second.Root = t.TempDir()
			}
			d := &Daemon{p4ClientLocks: NewLocalPathLocker()}
			release, err := d.acquireP4ClientLock(t.Context(), Task{ID: "first"}, first)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
			defer cancel()
			other, err := d.acquireP4ClientLock(ctx, Task{ID: "second"}, second)
			if other != nil {
				other()
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("conflicting workspace did not wait: %v", err)
			}
		})
	}
}
