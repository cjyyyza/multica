package p4cache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
)

const (
	ActionChanges     = "changes"
	ActionFstat       = "fstat"
	ActionFiles       = "files"
	ActionDirs        = "dirs"
	ActionGrep        = "grep"
	ActionPrint       = "print"
	ActionDiff        = "diff"
	ActionDiff2       = "diff2"
	ActionAnnotate    = "annotate"
	ActionWhere       = "where"
	ActionHave        = "have"
	ActionSizes       = "sizes"
	ActionIntegrated  = "integrated"
	ActionInterchange = "interchanges"
	ActionResolved    = "resolved"
)

const (
	// QueryDefaultMax is applied when `changes` omits -m. A client view on a
	// UE depot otherwise lists years of history into the agent's context.
	QueryDefaultMax = 50
	// QueryMaxMax is the hard cap for an explicit -m on a forwarded read.
	QueryMaxMax = 200
	// QueryOutputMax refuses a read whose stdout would flood the task. The
	// bytes are not returned truncated: a cut record can still name another depot.
	QueryOutputMax = 1 << 20
)

var ErrQueryOutputTooLarge = errors.New("p4 output exceeds the 1 MiB limit")

// RunRead runs one already-authorized read in the bound client. ztag is the
// global -ztag, which p4 only accepts before the verb. args are the command's
// own flags and filespecs; nothing here adds a write flag.
func (c *Cache) RunRead(ctx context.Context, ws *Workspace, verb string, args []string, ztag bool) (string, string, error) {
	if c == nil || ws == nil {
		return "", "", fmt.Errorf("perforce workspace is required")
	}
	base := ws.baseArgs()
	if ztag {
		base = ws.tagArgs()
	}
	full := append(append(base, verb), args...)
	cmd, cancel, err := c.commandAt(ctx, ws.Dir, full...)
	if err != nil {
		return "", "", err
	}
	defer cancel()
	stdout := &boundedQueryWriter{remaining: QueryOutputMax, cancel: func() { _ = cmd.Cancel() }}
	stderr := &boundedQueryWriter{remaining: QueryOutputMax, cancel: func() { _ = cmd.Cancel() }}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	if stdout.exceeded || stderr.exceeded {
		return "", "", ErrQueryOutputTooLarge
	}
	return stdout.String(), stderr.String(), err
}

type boundedQueryWriter struct {
	bytes.Buffer
	remaining int
	exceeded  bool
	cancel    func()
}

func (w *boundedQueryWriter) Write(p []byte) (int, error) {
	if w.exceeded {
		return 0, ErrQueryOutputTooLarge
	}
	if len(p) > w.remaining {
		w.exceeded = true
		if w.cancel != nil {
			w.cancel()
		}
		return 0, ErrQueryOutputTooLarge
	}
	w.remaining -= len(p)
	return w.Buffer.Write(p)
}

// ExitCode reports a p4 process exit. ok is false when err is not an exit
// (missing binary, timeout): that must not be presented as p4's own stderr.
func ExitCode(err error) (int, bool) {
	if err == nil {
		return 0, true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), true
	}
	return 0, false
}
