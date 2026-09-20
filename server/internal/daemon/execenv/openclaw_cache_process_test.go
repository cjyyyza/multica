package execenv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"
)

const cacheProcessMarker = "--openclaw-cache-worker"

func TestOpenclawDiscoveryCacheProcessHelper(t *testing.T) {
	index := slices.Index(os.Args, cacheProcessMarker)
	if index < 0 {
		return
	}
	if len(os.Args) != index+4 {
		t.Fatal("invalid cache worker arguments")
	}
	cache, bin, config := os.Args[index+1], os.Args[index+2], os.Args[index+3]
	for i := 0; i < 20; i++ {
		if err := storeOpenclawDiscoveryCache(cache, bin, config, []any{map[string]any{"id": "worker"}}, false, time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, ok := loadOpenclawDiscoveryCache(cache, bin, time.Now()); !ok {
			t.Fatal("process observed an incomplete cache")
		}
	}
}

func TestOpenclawDiscoveryCacheConcurrentProcesses(t *testing.T) {
	f := newOpenclawCacheFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() {
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOpenclawDiscoveryCacheProcessHelper$", "--", cacheProcessMarker, f.cachePath(), f.bin, f.configPath)
			output, err := cmd.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("cache worker: %w: %s", err, output)
			}
			results <- err
		}()
	}
	for i := 0; i < 3; i++ {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
}
