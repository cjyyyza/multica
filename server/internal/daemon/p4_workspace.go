package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

// p4TaskBinding is the human-owned Helix client a task edits in place.
// change is filled lazily on the first open, or reused from an earlier run
// on the same issue.
type p4TaskBinding struct {
	mu              sync.Mutex
	ws              *p4cache.Workspace
	ref             p4depot.Ref
	ticket          string
	issueKey, title string
	description     string
	change          string
	sidecars        []string
	defaultSnapshot []string
	release         func()
	taskID          string
	closed          bool
}

func (b *p4TaskBinding) changelist() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.change
}

func (b *p4TaskBinding) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// envSpec is the brief's view of the workspace. Change is set only when an
// earlier run left a pending changelist; a changelist created later is
// visible through `multica p4 status`.
func (b *p4TaskBinding) envSpec() *execenv.P4WorkspaceForEnv {
	b.mu.Lock()
	defer b.mu.Unlock()
	return &execenv.P4WorkspaceForEnv{
		Port:           b.ws.Port,
		User:           b.ws.User,
		Client:         b.ws.Client,
		Root:           b.ws.Root,
		Stream:         b.ws.Stream,
		Ticket:         b.ticket,
		Change:         b.change,
		AllowExclusive: b.ref.AllowExclusive,
	}
}

func (d *Daemon) rememberP4Binding(b *p4TaskBinding) {
	d.p4BindingsMu.Lock()
	defer d.p4BindingsMu.Unlock()
	if d.p4Bindings == nil {
		d.p4Bindings = map[string]*p4TaskBinding{}
	}
	d.p4Bindings[b.taskID] = b
}

func (d *Daemon) forgetP4Binding(taskID string) {
	d.p4BindingsMu.Lock()
	defer d.p4BindingsMu.Unlock()
	delete(d.p4Bindings, taskID)
}

func (d *Daemon) p4Binding(taskID string) *p4TaskBinding {
	d.p4BindingsMu.Lock()
	defer d.p4BindingsMu.Unlock()
	if d.p4Bindings == nil {
		return nil
	}
	return d.p4Bindings[taskID]
}

// bindP4Workspace resolves the Helix client under the task's local directory
// and holds it until finishP4Workspace. A task that lists Perforce depots but
// has no usable workspace fails here: falling through would let the agent
// write a read-only tree with no changelist.
func (d *Daemon) bindP4Workspace(ctx context.Context, task Task, a *localDirectoryAssignment, provider string) (*p4TaskBinding, error) {
	if a == nil || a.UsesWorktree() || len(task.P4Depots) == 0 {
		return nil, nil
	}
	if d.p4Cache == nil {
		return nil, fmt.Errorf("perforce is not initialized")
	}
	if d.p4ClientLocks == nil {
		return nil, fmt.Errorf("perforce client locks are not initialized")
	}
	ws, err := d.p4Cache.DetectWorkspace(ctx, a.AbsPath)
	if err != nil {
		return nil, fmt.Errorf("this task has Perforce depots, but no usable workspace was detected in %s: %w", a.AbsPath, err)
	}
	ref, err := matchTaskDepot(task.P4Depots, ws)
	if err != nil {
		return nil, err
	}
	release, err := d.p4ClientLocks.Acquire(ctx, ws.Port+"|"+ws.Client, task.ID, nil)
	if err != nil {
		return nil, fmt.Errorf("wait for Perforce client %s: %w", ws.Client, err)
	}
	binding := &p4TaskBinding{
		ws:       ws,
		ref:      ref,
		ticket:   strings.TrimSpace(task.P4Ticket),
		issueKey: strings.TrimSpace(task.IssueIdentifier),
		title:    strings.TrimSpace(task.ThreadName),
		taskID:   task.ID,
		release:  release,
	}
	if err := d.rejectTrackedSidecars(ctx, binding, a.AbsPath, provider); err != nil {
		release()
		return nil, err
	}
	opened, err := d.p4Cache.OpenedInChange(ctx, ws, "default")
	if err != nil {
		release()
		return nil, fmt.Errorf("snapshot the default changelist in %s: %w", ws.Client, err)
	}
	for _, file := range opened {
		binding.defaultSnapshot = append(binding.defaultSnapshot, file.DepotFile)
	}
	if binding.issueKey != "" {
		change, findErr := d.p4Cache.FindTaskChange(ctx, ws, binding.issueKey)
		if findErr != nil {
			release()
			return nil, findErr
		}
		binding.change = change
		if change != "" {
			binding.description = p4depot.ChangeDescription(binding.ticket, binding.title, binding.issueKey)
		}
	}
	d.rememberP4Binding(binding)
	return binding, nil
}

// rejectTrackedSidecars fails the task when a file Multica is about to write
// (the runtime brief, its scratch directories, or the provider skills) is
// already in the depot. Writing those in a project subdirectory edits the
// game, so the local directory has to be the workspace root.
func (d *Daemon) rejectTrackedSidecars(ctx context.Context, b *p4TaskBinding, workDir, provider string) error {
	candidates := sidecarCandidates(workDir, provider)
	b.sidecars = candidates
	states, err := d.p4Cache.FileStates(ctx, b.ws, sidecarFstatPatterns(candidates))
	if err != nil {
		return fmt.Errorf("check whether Multica's own files are in the depot: %w", err)
	}
	if len(states) == 0 {
		return nil
	}
	tracked := make([]string, 0, len(states))
	for _, state := range states {
		tracked = append(tracked, state.DepotFile)
	}
	return fmt.Errorf("%s is tracked in the depot; point the local directory resource at the workspace root (e.g. D:\\DJ)", strings.Join(tracked, ", "))
}

// sidecarFstatPatterns turns sidecar paths into fstat arguments. fstat on a
// bare directory answers "no such file(s)" even when the depot tracks files
// under it, so each candidate is also queried as <path>/.... A candidate that
// is a file on disk is left alone; one that is a directory or does not exist
// yet (.multica, .agent_context) gets the wildcard. A name-based test cannot
// tell them apart: ".multica" has an extension as far as filepath.Ext goes.
func sidecarFstatPatterns(candidates []string) []string {
	patterns := make([]string, 0, 2*len(candidates))
	for _, path := range candidates {
		patterns = append(patterns, path)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			continue
		}
		patterns = append(patterns, filepath.Join(path, "..."))
	}
	return patterns
}

func sidecarCandidates(workDir, provider string) []string {
	var paths []string
	if config := execenv.RuntimeConfigPath(workDir, provider); config != "" {
		paths = append(paths, config)
	}
	paths = append(paths,
		filepath.Join(workDir, ".multica"),
		filepath.Join(workDir, ".agent_context"),
	)
	if skills := execenv.SkillsDirPath(workDir, provider); skills != "" {
		paths = append(paths, skills)
	}
	return paths
}

type p4FinalizeError struct{ err error }

func (e *p4FinalizeError) Error() string { return e.err.Error() }
func (e *p4FinalizeError) Unwrap() error { return e.err }

func (d *Daemon) finishP4Workspace(ctx context.Context, task Task, b *p4TaskBinding) error {
	if b == nil {
		return nil
	}
	defer func() {
		if b.release != nil {
			b.release()
			b.release = nil
		}
		d.forgetP4Binding(b.taskID)
	}()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	change := b.change
	b.mu.Unlock()
	if change == "" {
		return nil
	}
	result, finishErr := d.p4Cache.FinishChange(ctx, b.ws, change)
	if finishErr != nil {
		if d.logger != nil {
			d.logger.Error("p4 workspace finish failed", "task_id", task.ID, "change", change, "error", finishErr)
		}
	}
	if result == nil {
		result = &p4cache.FinishResult{Change: change}
	}
	report := d.changelistReport(ctx, b, result)
	if result.Shelved && !b.ref.NoAutoReview {
		d.attachSwarmReview(ctx, b, &report)
	}
	reportErr := d.reportTaskP4Changelist(ctx, task, report)
	if err := errors.Join(finishErr, reportErr); err != nil {
		return fmt.Errorf("Perforce changelist %s delivery failed: %w", change, err)
	}
	return nil
}

func (d *Daemon) p4CommandTimeout() time.Duration {
	if d != nil && d.cfg.P4Timeout > 0 {
		return d.cfg.P4Timeout
	}
	return DefaultP4Timeout
}

// matchTaskDepot picks the configured depot this client actually maps.
// The longest covering path wins, so //DJ01_Proj/Dev beats //DJ01_Proj.
func matchTaskDepot(depots []P4DepotData, ws *p4cache.Workspace) (p4depot.Ref, error) {
	var best p4depot.Ref
	bestLen := -1
	consider := func(ref p4depot.Ref, target string) {
		if !strings.EqualFold(ref.Port, ws.Port) || !depotCovers(ref, target) {
			return
		}
		if n := len(depotScopePath(ref)); n > bestLen {
			best = ref
			bestLen = n
		}
	}
	for _, depot := range depots {
		ref, err := p4depot.Normalize(p4depot.Ref(depot))
		if err != nil {
			continue
		}
		if ws.Stream != "" {
			consider(ref, ws.Stream)
			continue
		}
		for _, mapping := range ws.View {
			left, _, _ := strings.Cut(mapping, " ")
			consider(ref, strings.TrimSuffix(left, "/..."))
		}
	}
	if bestLen < 0 {
		where := ws.Stream
		if where == "" {
			where = "no stream"
		}
		return p4depot.Ref{}, fmt.Errorf("Perforce client %s (%s) is not under a depot configured for this task", ws.Client, where)
	}
	return best, nil
}

func depotScopePath(ref p4depot.Ref) string {
	scope := strings.TrimSpace(ref.Depot)
	if strings.TrimSpace(ref.Stream) != "" {
		scope = strings.TrimSpace(ref.Stream)
	}
	return strings.TrimSuffix(scope, "/...")
}

func depotCovers(ref p4depot.Ref, target string) bool {
	scope := depotScopePath(ref)
	target = strings.TrimSuffix(strings.TrimSpace(target), "/...")
	if scope == "" || target == "" {
		return false
	}
	return target == scope || strings.HasPrefix(target, scope+"/")
}

func (b *p4TaskBinding) isSidecar(path string) bool {
	for _, root := range b.sidecars {
		if pathEqualOrInside(root, path) {
			return true
		}
	}
	return false
}

func pathEqualOrInside(root, path string) bool {
	root = strings.TrimRight(filepath.Clean(root), string(filepath.Separator))
	path = filepath.Clean(path)
	if strings.EqualFold(root, path) {
		return true
	}
	prefix := root + string(filepath.Separator)
	return len(path) > len(prefix) && strings.EqualFold(path[:len(prefix)], prefix)
}
