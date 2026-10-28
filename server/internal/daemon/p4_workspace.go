package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
	"github.com/multica-ai/multica/server/internal/util"
)

// p4TaskBinding is the human-owned Helix client a task edits in place.
// change is filled lazily on the first open, or reused from an earlier run
// on the same issue.
type p4TaskBinding struct {
	opMu                sync.Mutex // Serialize writes and close-out on this client.
	mu                  sync.Mutex
	ws                  *p4cache.Workspace
	ref                 p4depot.Ref
	depots              []p4depot.Ref
	ticket              string
	issueKey, title     string
	description         string
	change              string
	submitted           bool
	submission          *p4cache.FinishResult
	sidecars            []string
	defaultSnapshot     []string
	release             func()
	taskID              string
	closed              bool
	readOnly            bool
	changeTouched       bool
	initialOpened       map[string]p4OpenedFileSnapshot
	descriptionReady    bool
	changeReported      bool
	registrationPending bool
	touchedPaths        map[string]bool
	workspaceID         string
}

type p4OpenedFileSnapshot struct {
	path, action, fileType string
	exists                 bool
	digest                 [sha256.Size]byte
}

type p4SnapshotReader struct {
	ctx context.Context
	io.Reader
}

func (r p4SnapshotReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func (b *p4TaskBinding) touchChange() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.changeTouched = true
}

// Snapshot only this reused CL's opens, never the entire workspace. Content
// hashes also catch direct edits that preserve file size and modification time.
func (d *Daemon) snapshotP4Change(ctx context.Context, b *p4TaskBinding) (map[string]p4OpenedFileSnapshot, error) {
	files, err := d.p4Cache.OpenedInChange(ctx, b.ws, b.change)
	if err != nil {
		return nil, err
	}
	snapshot := make(map[string]p4OpenedFileSnapshot, len(files))
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path, err := d.p4Cache.LocalPath(ctx, b.ws, file.DepotFile)
		if err != nil {
			return nil, err
		}
		path, err = b.ws.ResolveLocalPath(path)
		if err != nil {
			return nil, err
		}
		state := p4OpenedFileSnapshot{path: path, action: file.Action, fileType: file.HeadType}
		f, err := os.Open(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("snapshot changelist file %s: %w", path, err)
		}
		if err == nil {
			h := sha256.New()
			_, readErr := io.Copy(h, p4SnapshotReader{ctx, f})
			closeErr := f.Close()
			if err := errors.Join(readErr, closeErr); err != nil {
				return nil, fmt.Errorf("snapshot changelist file %s: %w", path, err)
			}
			state.exists = true
			copy(state.digest[:], h.Sum(nil))
		}
		snapshot[file.DepotFile] = state
	}
	return snapshot, nil
}

func changedP4OpenedFiles(before, after map[string]p4OpenedFileSnapshot) []string {
	var files []string
	for file, state := range after {
		if initial, ok := before[file]; !ok || initial != state {
			files = append(files, file)
		}
	}
	files = append(files, removedP4OpenedFiles(before, after)...)
	sort.Strings(files)
	return files
}

func removedP4OpenedFiles(before, after map[string]p4OpenedFileSnapshot) []string {
	var files []string
	for file := range before {
		if _, ok := after[file]; !ok {
			files = append(files, file)
		}
	}
	sort.Strings(files)
	return files
}

func p4WorkspacePathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

func (b *p4TaskBinding) touchPaths(paths []string) {
	if b.touchedPaths == nil {
		b.touchedPaths = make(map[string]bool)
	}
	for _, path := range paths {
		b.touchedPaths[p4WorkspacePathKey(path)] = true
	}
}

// Only this run's file commands authorize cleanup. A human can open another
// file in the same CL while the task runs, so absence from the snapshot alone
// is not sufficient authority to revert it.
func (d *Daemon) revertableP4Files(ctx context.Context, b *p4TaskBinding) ([]string, error) {
	files := []string{}
	if len(b.touchedPaths) == 0 {
		return files, nil
	}
	opened, err := d.p4Cache.OpenedInChange(ctx, b.ws, b.change)
	if err != nil {
		return nil, err
	}
	for _, file := range opened {
		if _, protected := b.initialOpened[file.DepotFile]; protected {
			continue
		}
		path, err := d.p4Cache.LocalPath(ctx, b.ws, file.DepotFile)
		if err != nil {
			return nil, err
		}
		path, err = b.ws.ResolveLocalPath(path)
		if err != nil {
			return nil, err
		}
		if b.touchedPaths[p4WorkspacePathKey(path)] {
			files = append(files, file.DepotFile)
		}
	}
	return files, nil
}

func (b *p4TaskBinding) changelist() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.submission != nil {
		return b.submission.Change
	}
	return b.change
}

func (b *p4TaskBinding) isSubmitted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.submitted
}

func (b *p4TaskBinding) markSubmitted() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.submitted = true
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
		AllowSubmit:    b.ref.AllowSubmit,
		AllowSync:      b.ref.AllowSync,
		AllowUnshelve:  b.ref.AllowUnshelve,
		ReadOnly:       b.readOnly,
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
	// Detection and alias verification can block on network calls before the
	// client-wait and snapshot phases start their own lease protection.
	stopProbeLease := func() {}
	if d.client != nil {
		log := d.logger
		if log == nil {
			log = slog.Default()
		}
		stopProbeLease = d.startTaskPrepareLeaseExtender(ctx, task, log)
	}
	defer stopProbeLease()
	ws, err := d.p4Cache.DetectWorkspace(ctx, a.AbsPath)
	if err != nil {
		if !projectExplicitlyTargetsP4(task) && errors.Is(err, p4cache.ErrWorkspaceNotConfigured) {
			if d.logger != nil {
				d.logger.Info("workspace Perforce catalog does not apply to this local directory", "path", a.AbsPath, "reason", err)
			}
			return nil, nil
		}
		return nil, fmt.Errorf("this task has Perforce depots, but no usable workspace was detected in %s: %w", a.AbsPath, err)
	}
	ref, depots, err := d.matchP4Workspace(ctx, task.P4Depots, ws)
	if err != nil {
		return nil, err
	}
	binding := &p4TaskBinding{
		ws:          ws,
		ref:         ref,
		depots:      depots,
		ticket:      strings.TrimSpace(task.P4Ticket),
		issueKey:    strings.TrimSpace(task.IssueIdentifier),
		title:       strings.TrimSpace(task.ThreadName),
		taskID:      task.ID,
		workspaceID: task.WorkspaceID,
		readOnly:    task.ChatSessionID != "" || task.IssueID == "",
	}
	if binding.readOnly {
		if err := d.rejectTrackedSidecars(ctx, binding, a.AbsPath); err != nil {
			return nil, err
		}
		d.rememberP4Binding(binding)
		return binding, nil
	}
	if d.p4ClientLocks == nil {
		return nil, fmt.Errorf("perforce client locks are not initialized")
	}
	stopProbeLease()
	release, err := d.acquireP4ClientLock(ctx, task, ws)
	if err != nil {
		return nil, fmt.Errorf("wait for Perforce client %s: %w", ws.Client, err)
	}
	binding.release = release
	// Existing opened UE assets can be large; hashing them must not let the
	// dispatched task's lease expire after the client wait has ended.
	if d.client != nil {
		log := d.logger
		if log == nil {
			log = slog.Default()
		}
		stopLease := d.startTaskPrepareLeaseExtender(ctx, task, log)
		defer stopLease()
	}
	if err := d.rejectTrackedSidecars(ctx, binding, a.AbsPath); err != nil {
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
			spec, err := d.p4Cache.ReadPendingChange(ctx, ws, change)
			if err != nil {
				release()
				return nil, err
			}
			binding.description = spec.Description
			binding.initialOpened, err = d.snapshotP4Change(ctx, binding)
			if err != nil {
				release()
				return nil, fmt.Errorf("snapshot reused changelist %s: %w", change, err)
			}
		}
	}
	if binding.change != "" {
		if err := d.registerPendingP4Change(ctx, binding); err != nil {
			release()
			return nil, err
		}
	}
	d.rememberP4Binding(binding)
	return binding, nil
}

func (d *Daemon) acquireP4ClientLock(ctx context.Context, task Task, ws *p4cache.Workspace) (func(), error) {
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	log := d.logger
	if log == nil {
		log = slog.Default()
	}
	waiting := false
	var stopLease func()
	defer func() {
		if waiting {
			d.resourceWaitTasks.Add(-1)
		}
		if stopLease != nil {
			stopLease()
		}
	}()
	onWait := func(holder string) {
		if waiting {
			return
		}
		waiting = true
		d.resourceWaitTasks.Add(1)
		log.Info("perforce client waiting", "client", ws.Client, "holder", holder)
		if d.client == nil {
			return
		}
		reason := "Perforce client " + ws.Client
		if err := d.client.MarkTaskWaitingLocalDirectory(waitCtx, task.ID, reason); err != nil {
			log.Warn("mark perforce client waiting failed", "error", err)
		}
		stopLease = d.startTaskPrepareLeaseExtender(waitCtx, task, log)
		interval := d.cancelPollInterval
		if interval <= 0 {
			interval = 5 * time.Second
		}
		cancelled := d.watchTaskCancellation(waitCtx, task.ID, interval, log)
		go func() {
			select {
			case <-cancelled:
				cancel()
			case <-waitCtx.Done():
			}
		}()
	}
	server := ws.ServerCluster
	if !p4depot.ClusterKey(server, ws.Port) {
		server = strings.TrimPrefix(p4depot.EndpointKey(ws.Port), "tcp:")
	}
	releaseClient, err := d.p4ClientLocks.Acquire(waitCtx, server+"|"+ws.Client, task.ID, onWait)
	if err != nil {
		return nil, err
	}
	root, err := util.ResolveSymlinks(ws.Root)
	if err != nil {
		releaseClient()
		return nil, err
	}
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
	}
	releaseRoot, err := d.p4ClientLocks.Acquire(waitCtx, "root:"+root, task.ID, onWait)
	if err != nil {
		releaseClient()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { releaseRoot(); releaseClient() }) }, nil
}

// Only Multica's private marker/resource directories belong to the daemon.
// Repository rules and provider skills stay under normal Perforce ownership.
func (d *Daemon) rejectTrackedSidecars(ctx context.Context, b *p4TaskBinding, workDir string) error {
	candidates := []string{filepath.Join(workDir, ".multica"), filepath.Join(workDir, ".agent_context")}
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
	return fmt.Errorf("Multica private paths are tracked in the depot: %s; keep .multica and .agent_context untracked before running in %s", strings.Join(tracked, ", "), workDir)
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

type p4FinalizeError struct{ err error }

func (e *p4FinalizeError) Error() string { return e.err.Error() }
func (e *p4FinalizeError) Unwrap() error { return e.err }

func (d *Daemon) finishP4Workspace(ctx context.Context, task Task, b *p4TaskBinding) error {
	if b == nil {
		return nil
	}
	b.opMu.Lock()
	defer b.opMu.Unlock()
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
	submitted := b.submitted
	b.mu.Unlock()
	if b.readOnly || change == "" {
		return nil
	}
	if err := d.registerPendingP4Change(ctx, b); err != nil {
		return err
	}
	var (
		result    *p4cache.FinishResult
		finishErr error
	)
	preserveOpens := false
	var changedFiles []string
	if !submitted && b.initialOpened != nil {
		current, err := d.snapshotP4Change(ctx, b)
		if err != nil {
			return fmt.Errorf("inspect reused changelist %s before close-out: %w", change, err)
		}
		changedFiles = changedP4OpenedFiles(b.initialOpened, current)
		if removed := removedP4OpenedFiles(b.initialOpened, current); len(removed) > 0 {
			preserveOpens = true
			finishErr = fmt.Errorf("pre-existing opens were removed from changelist %s: %s", change, strings.Join(removed, ", "))
			result = &p4cache.FinishResult{Change: change}
		} else if len(changedFiles) == 0 {
			// Metadata-only operations and failed/no-op file commands do not
			// authorize replacing the human's pre-existing open files.
			preserveOpens = true
			shelf, err := d.p4Cache.ReadShelf(ctx, b.ws, change)
			if err != nil {
				return err
			}
			files := shelf.Files
			result = &p4cache.FinishResult{Change: change, Files: files, Shelved: shelf.HasContents()}
			names := make([]string, 0, len(current))
			shelvedPaths := make(map[string]bool, len(files))
			for _, file := range files {
				shelvedPaths[file.DepotFile] = true
			}
			for file := range current {
				names = append(names, file)
			}
			sort.Strings(names)
			for _, file := range names {
				if !shelvedPaths[file] {
					state := current[file]
					result.Files = append(result.Files, p4cache.FileState{DepotFile: file, LocalPath: state.path, Action: state.action, HeadType: state.fileType})
				}
			}
		}
	}
	if submitted {
		result, finishErr = d.submittedP4Change(ctx, b)
	} else if !preserveOpens {
		files := changedFiles
		if files == nil {
			opened, err := d.p4Cache.OpenedInChange(ctx, b.ws, change)
			if err != nil {
				return err
			}
			for _, file := range opened {
				files = append(files, file.DepotFile)
			}
		}
		if err := d.validateP4CloseoutFiles(ctx, b, files); err != nil {
			return err
		}
		shelf, err := d.p4Cache.ReadShelf(ctx, b.ws, change)
		if err != nil {
			return err
		}
		if rej := b.rejectDepotFiles("shelve", shelf.Files); rej != nil {
			return fmt.Errorf("%s: %s", rej.Code, rej.Message)
		}
		revertFiles, err := d.revertableP4Files(ctx, b)
		if err != nil {
			return err
		}
		result, finishErr = d.p4Cache.FinishChange(ctx, b.ws, change, p4cache.FinishChangeOptions{Files: changedFiles, RevertFiles: revertFiles})
	}
	if finishErr != nil && d.logger != nil {
		d.logger.Error("p4 workspace finish failed", "task_id", task.ID, "change", change, "error", finishErr)
	}
	if result == nil {
		// Keep an unverified submission eligible for polling rather than
		// permanently recording an invented terminal receipt.
		result = &p4cache.FinishResult{Change: change}
	}
	report := d.changelistReport(ctx, b, result)
	if result.Shelved && !preserveOpens && !b.ref.NoAutoReview {
		d.attachSwarmReview(ctx, b, &report)
	}
	reportErr := d.reportTaskP4Changelist(ctx, task, report)
	if err := errors.Join(finishErr, reportErr); err != nil {
		return fmt.Errorf("Perforce changelist %s delivery failed: %w", change, err)
	}
	return nil
}

func (d *Daemon) submittedP4Change(ctx context.Context, b *p4TaskBinding) (*p4cache.FinishResult, error) {
	b.mu.Lock()
	result, original := b.submission, b.change
	b.mu.Unlock()
	if result != nil {
		return result, nil
	}
	result, err := d.p4Cache.SubmittedChange(ctx, b.ws, original)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.submission = result
	b.mu.Unlock()
	return result, nil
}

func (d *Daemon) p4CommandTimeout() time.Duration {
	if d != nil && d.cfg.P4Timeout > 0 {
		return d.cfg.P4Timeout
	}
	return DefaultP4Timeout
}

// matchTaskDepot picks the configured depot this client actually maps.
// The client stream takes priority over imports. Within that class the
// most specific mapped resource wins. An ancestor alone grants no stream access.
func matchTaskDepot(depots []P4DepotData, ws *p4cache.Workspace) (p4depot.Ref, error) {
	var best p4depot.Ref
	bestLen := -1
	bestStream := false
	for _, depot := range depots {
		ref, err := p4depot.Normalize(p4depot.Ref(depot))
		if err != nil || !p4depot.SameEndpoint(ref.Port, ws.Port) {
			continue
		}
		mapped, streamMatch := depotMatchesWorkspace(ref, ws)
		if mapped && ((streamMatch && !bestStream) || (streamMatch == bestStream && len(depotScopePath(ref)) > bestLen)) {
			best, bestLen, bestStream = ref, len(depotScopePath(ref)), streamMatch
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

func depotMatchesWorkspace(ref p4depot.Ref, ws *p4cache.Workspace) (bool, bool) {
	scope := depotScopePath(ref)
	stream := strings.TrimSuffix(strings.TrimSpace(ws.Stream), "/...")
	if stream != "" && scope == stream {
		return true, true
	}
	if stream != "" && depotCovers(ref, stream) {
		return false, false
	}
	for _, mapping := range ws.View {
		source, excluded, err := p4depot.ViewSource(mapping)
		if err != nil || excluded || !p4depot.ViewOverlaps(scope, source) {
			continue
		}
		prefix := strings.TrimRight(p4depot.ViewLiteralPrefix(source), "/")
		if stream != "" && strings.HasPrefix(prefix, scope+"/") {
			// Generated stream views name mapped namespaces, including imports.
			// Their parents cannot lend grants to those namespaces.
			continue
		}
		return true, false
	}
	return false, false
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
		inside, err := p4cache.PathWithin(root, path)
		if err != nil || inside {
			// An unresolvable alias cannot prove this is a business file.
			return true
		}
	}
	return false
}
