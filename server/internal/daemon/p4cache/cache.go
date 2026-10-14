// Package p4cache runs the host p4 CLI for a task.
// It uses host credentials (P4USER, P4TICKETS, P4CONFIG) and does not create a
// Helix client or sync a depot into the task directory: that download belongs
// to the person's existing workspace.
package p4cache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/p4depot"
)

// defaultP4Timeout is the fallback when no timeout is configured on the Cache.
// The daemon sets Timeout from MULTICA_P4_TIMEOUT; tests and the zero value
// keep this.
const defaultP4Timeout = 10 * time.Minute

// ErrTaskClientNotCreated is returned for every attempt to create a
// task-scoped Helix client and sync a depot into the task directory.
// Creating that client runs `p4 sync`, which downloads the depot.
var ErrTaskClientNotCreated = errors.New("Multica does not create a task Perforce client or sync a depot into the task directory; that would download the depot. Attach an in_place local_directory that is your existing Helix workspace")

// Cache runs the p4 CLI. P4Path defaults to "p4" on PATH.
type Cache struct {
	P4Path     string
	Logger     *slog.Logger
	HTTPClient *http.Client
	// Timeout bounds one p4 invocation; zero falls back to defaultP4Timeout.
	Timeout time.Duration
}

func (c *Cache) p4bin() string {
	if c != nil && strings.TrimSpace(c.P4Path) != "" {
		return c.P4Path
	}
	return "p4"
}

func (c *Cache) logger() *slog.Logger {
	if c != nil && c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

func (c *Cache) timeout() time.Duration {
	if c != nil && c.Timeout > 0 {
		return c.Timeout
	}
	return defaultP4Timeout
}

func (c *Cache) httpClient() *http.Client {
	client := http.DefaultClient
	if c != nil && c.HTTPClient != nil {
		client = c.HTTPClient
	}
	// Never follow a redirect with the host ticket, even to a subdomain.
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}

func (c *Cache) resolveUser(ctx context.Context, ref p4depot.Ref) (string, error) {
	user := ref.User
	if user == "" {
		user = strings.TrimSpace(os.Getenv("P4USER"))
	}
	if user != "" {
		return user, nil
	}
	user, err := c.whoami(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("P4USER is not set and p4 info did not report a user (run `p4 login` on this machine): %w", err)
	}
	return user, nil
}

func (c *Cache) whoami(ctx context.Context, ref p4depot.Ref) (string, error) {
	out, err := c.output(ctx, ref, "", "", "info")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "User name:"); ok {
			user := strings.TrimSpace(rest)
			if user != "" {
				return user, nil
			}
		}
	}
	return "", fmt.Errorf("p4 info did not include User name")
}

func (c *Cache) output(ctx context.Context, ref p4depot.Ref, user, client string, args ...string) ([]byte, error) {
	return c.outputAt(ctx, "", ref, user, client, args...)
}

func (c *Cache) outputAt(ctx context.Context, dir string, ref p4depot.Ref, user, client string, args ...string) ([]byte, error) {
	full := append(c.baseArgs(ref, user, client), args...)
	cmd, cancel, err := c.commandAt(ctx, dir, full...)
	if err != nil {
		return nil, err
	}
	defer cancel()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("p4 %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (c *Cache) baseArgs(ref p4depot.Ref, user, client string) []string {
	args := []string{"-p", ref.Port}
	if user != "" {
		args = append(args, "-u", user)
	}
	if client != "" {
		args = append(args, "-c", client)
	}
	if ref.Charset != "" {
		args = append(args, "-C", ref.Charset)
	}
	return args
}

func (c *Cache) commandAt(ctx context.Context, dir string, args ...string) (*exec.Cmd, context.CancelFunc, error) {
	bin := c.p4bin()
	if filepath.IsAbs(bin) {
		if _, err := os.Stat(bin); err != nil {
			return nil, func() {}, fmt.Errorf("p4 is not installed on this machine (install the Helix p4 CLI and run `p4 login`): %w", err)
		}
	} else if _, err := exec.LookPath(bin); err != nil {
		return nil, func() {}, fmt.Errorf("p4 is not installed on this machine (install the Helix p4 CLI and run `p4 login`): %w", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, c.timeout())
	cmd := exec.CommandContext(runCtx, c.p4bin(), args...)
	cmd.Env = os.Environ()
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
		// p4 finds P4CONFIG (p4config.txt) by walking up from $PWD, not from
		// the process working directory, and exec does not set PWD. Without
		// it a workspace's own config is ignored and the registry P4CLIENT —
		// on a developer machine routinely a client bound to another host —
		// wins silently.
		cmd.Env = append(cmd.Env, "PWD="+dir)
	} else {
		cmd.Dir = filepath.VolumeName(os.TempDir()) + string(os.PathSeparator)
	}
	c.logger().Debug("p4 command", "args", args)
	return cmd, cancel, nil
}
