// Package p4cache syncs Perforce depots into a task working directory.
// It creates a unique P4 client per task and uses host credentials
// (P4USER, P4TICKETS, P4CONFIG). It does not prefetch depots at register time.
package p4cache

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/p4depot"
)

const p4Timeout = 10 * time.Minute

// SyncParams is the input to a one-shot `p4 client` + `p4 sync`.
type SyncParams struct {
	WorkspaceID string
	TaskID      string
	WorkDir     string
	Ref         p4depot.Ref
	Fresh       bool
}

// SyncResult is the daemon /p4/sync response.
type SyncResult struct {
	Path       string `json:"path"`
	Client     string `json:"client"`
	Changelist string `json:"changelist,omitempty"`
}

// Cache runs the p4 CLI. P4Path defaults to "p4" on PATH.
type Cache struct {
	P4Path string
	Logger *slog.Logger
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

// Sync creates a task-scoped P4 client and syncs the depot into workdir/p4/….
func (c *Cache) Sync(ctx context.Context, params SyncParams) (*SyncResult, error) {
	ref, err := p4depot.Normalize(params.Ref)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(params.WorkDir) == "" {
		return nil, fmt.Errorf("workdir is required")
	}
	if strings.TrimSpace(params.TaskID) == "" {
		return nil, fmt.Errorf("task_id is required")
	}

	user := ref.User
	if user == "" {
		user = strings.TrimSpace(os.Getenv("P4USER"))
	}
	if user == "" {
		user, err = c.whoami(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("P4USER is not set and p4 info did not report a user (run `p4 login` on this machine): %w", err)
		}
	}

	client := p4depot.ClientName(params.WorkspaceID, params.TaskID, ref)
	root := filepath.Join(params.WorkDir, "p4", clientRelDir(ref))
	if params.Fresh {
		if err := os.RemoveAll(root); err != nil {
			return nil, fmt.Errorf("clear existing Perforce checkout: %w", err)
		}
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create Perforce checkout directory: %w", err)
	}

	spec := buildClientSpec(client, user, root, ref)
	if err := c.runClientIn(ctx, ref, user, spec); err != nil {
		return nil, err
	}

	syncArg := p4depot.SyncPath(ref.Depot, ref.Changelist)
	if err := c.run(ctx, ref, user, client, "sync", syncArg); err != nil {
		return nil, err
	}

	return &SyncResult{
		Path:       root,
		Client:     client,
		Changelist: ref.Changelist,
	}, nil
}

func clientRelDir(ref p4depot.Ref) string {
	raw := strings.TrimSpace(ref.Stream)
	if raw == "" {
		raw = strings.TrimSpace(ref.Depot)
	}
	raw = strings.TrimPrefix(raw, "//")
	raw = strings.ReplaceAll(raw, "/", "_")
	raw = strings.ReplaceAll(raw, "...", "")
	raw = strings.Trim(raw, "._-")
	if raw == "" {
		raw = "depot"
	}
	return raw
}

func buildClientSpec(client, user, root string, ref p4depot.Ref) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Client: %s\n", client)
	fmt.Fprintf(&b, "Owner: %s\n", user)
	fmt.Fprintf(&b, "Description: Multica task client\n")
	fmt.Fprintf(&b, "Root: %s\n", root)
	fmt.Fprintf(&b, "Options: noallwrite noclobber nocompress unlocked nomodtime normdir\n")
	fmt.Fprintf(&b, "LineEnd: local\n")
	if ref.Stream != "" {
		fmt.Fprintf(&b, "Stream: %s\n", ref.Stream)
		return b.String()
	}
	depotSide, clientRel := p4depot.ViewMaps(ref.Depot)
	fmt.Fprintf(&b, "View:\n\t%s //%s/%s\n", depotSide, client, clientRel)
	return b.String()
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

func (c *Cache) runClientIn(ctx context.Context, ref p4depot.Ref, user, spec string) error {
	args := c.baseArgs(ref, user, "")
	args = append(args, "client", "-i")
	cmd, cancel, err := c.command(ctx, args...)
	if err != nil {
		return err
	}
	defer cancel()
	cmd.Stdin = strings.NewReader(spec)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("p4 client -i: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (c *Cache) run(ctx context.Context, ref p4depot.Ref, user, client string, args ...string) error {
	full := append(c.baseArgs(ref, user, client), args...)
	cmd, cancel, err := c.command(ctx, full...)
	if err != nil {
		return err
	}
	defer cancel()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("p4 %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (c *Cache) output(ctx context.Context, ref p4depot.Ref, user, client string, args ...string) ([]byte, error) {
	full := append(c.baseArgs(ref, user, client), args...)
	cmd, cancel, err := c.command(ctx, full...)
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

func (c *Cache) command(ctx context.Context, args ...string) (*exec.Cmd, context.CancelFunc, error) {
	bin := c.p4bin()
	if filepath.IsAbs(bin) {
		if _, err := os.Stat(bin); err != nil {
			return nil, func() {}, fmt.Errorf("p4 is not installed on this machine (install the Helix p4 CLI and run `p4 login`): %w", err)
		}
	} else if _, err := exec.LookPath(bin); err != nil {
		return nil, func() {}, fmt.Errorf("p4 is not installed on this machine (install the Helix p4 CLI and run `p4 login`): %w", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, p4Timeout)
	cmd := exec.CommandContext(runCtx, c.p4bin(), args...)
	cmd.Dir = filepath.VolumeName(os.TempDir()) + string(os.PathSeparator)
	cmd.Env = os.Environ()
	c.logger().Debug("p4 command", "args", args)
	return cmd, cancel, nil
}
