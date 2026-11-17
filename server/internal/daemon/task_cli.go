package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func configureTaskCLIEnvironment(env map[string]string) error {
	selfBin, err := resolveSelfExecutable()
	if err != nil {
		return fmt.Errorf("resolve task Multica CLI: %w", err)
	}
	// Login shell startup files can reorder PATH after the provider starts.
	// Keep an explicit executable for the commands in the runtime brief.
	env["MULTICA_CLI_PATH"] = selfBin
	env["PATH"] = filepath.Dir(selfBin) + string(os.PathListSeparator) + os.Getenv("PATH")
	// Agent backends drop inherited MULTICA_* variables. Forward only the
	// deployment settings needed by comment notifications alongside task context.
	for _, key := range []string{"MULTICA_NOTIFY_CONFIG", "MULTICA_POPO_CLI"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			env[key] = value
		}
	}
	return nil
}
