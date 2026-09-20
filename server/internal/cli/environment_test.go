package cli

import (
	"os"
	"testing"

	"github.com/multica-ai/multica/server/internal/testenv"
)

func TestMain(m *testing.M) {
	os.Exit(testenv.RunIsolated(m.Run))
}
