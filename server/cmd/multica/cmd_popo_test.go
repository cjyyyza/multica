package main

import (
	"os"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/popo"
)

func TestPopoGatewayRemoved(t *testing.T) {
	err := runPopoGateway(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "nanobot.multica_bridge") {
		t.Fatalf("gateway error = %v", err)
	}
}

func TestRequireWindowsLocalHonorsOverride(t *testing.T) {
	t.Setenv(popo.AllowNonWindowsEnv, "1")
	if err := popo.RequireWindowsLocal(); err != nil {
		t.Fatalf("override should allow: %v", err)
	}
	_ = os.Unsetenv(popo.AllowNonWindowsEnv)
}
