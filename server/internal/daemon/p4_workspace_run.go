package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

var p4DescriptionTicketMarker = regexp.MustCompile(`(?i)\brefs\s*#`)

// p4Warning is one non-fatal observation attached to a workspace-mode run
// or status response. kind is one of not_head, opened_elsewhere,
// exclusive_held, default_changelist_files, unexpected_opens.
type p4Warning struct {
	Kind  string   `json:"kind"`
	Files []string `json:"files"`
}

// p4Rejection is the 409 body for a workspace-mode invariant. code is one of
// the values in the changelist-workflow contract.
type p4Rejection struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Files   []string `json:"files,omitempty"`
	Holders []string `json:"holders,omitempty"`
}

type p4WorkspaceResult struct {
	p4cache.MutateResult
	Warnings    []p4Warning               `json:"warnings,omitempty"`
	Files       []p4OpenedFile            `json:"files,omitempty"`
	Revisions   []p4cache.FilelogRevision `json:"revisions,omitempty"`
	Description string                    `json:"description,omitempty"`
	// Stderr and ExitCode forward a read-only p4 invocation. A non-zero exit
	// is still HTTP 200: the text is the result, not a transport failure.
	Stderr   string `json:"stderr,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
}

// p4OpenedFile is one file from this task's changelist. JSON is snake_case;
// `multica p4 opened -F` addresses the same fields with p4 -ztag names.
type p4OpenedFile struct {
	DepotFile  string `json:"depot_file"`
	ClientFile string `json:"client_file,omitempty"`
	Action     string `json:"action"`
	Change     string `json:"change"`
	Type       string `json:"type,omitempty"`
}

const (
	p4CodeTicketRequired      = "ticket_required"
	p4CodeOpenedByYou         = "opened_by_you"
	p4CodeExclusiveLocked     = "exclusive_locked"
	p4CodeExclusiveNotAllowed = "exclusive_not_allowed"
	p4CodeOutsideTaskChange   = "outside_task_change"
	p4CodeSidecarPath         = "sidecar_path"
	p4CodeActionNotAllowed    = "action_not_allowed"
	p4CodeNotInWorkspace      = "not_in_workspace"
)

func rejectP4(code, message string, files, holders []string) *p4Rejection {
	return &p4Rejection{Code: code, Message: message, Files: files, Holders: holders}
}

// runWorkspaceP4 applies one agent action to the bound client. Every write
// lands in the task changelist; the person's own opens are refused.
func (d *Daemon) runWorkspaceP4(ctx context.Context, b *p4TaskBinding, req p4MutateRequest, cwd string) (*p4WorkspaceResult, *p4Rejection, error) {
	b.opMu.Lock()
	defer b.opMu.Unlock()
	if b.isClosed() {
		return nil, rejectP4(p4CodeActionNotAllowed, "this task's Perforce workspace is already closing", nil, nil), nil
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if b.readOnly && !p4ReadOnlyAction(action) {
		return nil, rejectP4(p4CodeActionNotAllowed, "chat and tasks without an issue may only read the Perforce workspace", nil, nil), nil
	}
	if b.isSubmitted() && !p4ReadOnlyAction(action) && action != "sync" {
		return nil, rejectP4(p4CodeActionNotAllowed, "this task's changelist has already been submitted", nil, nil), nil
	}
	switch action {
	case "sync":
		if req.Changelist != "" || req.Fresh {
			return nil, rejectP4(p4CodeActionNotAllowed, "workspace sync does not support -c/--changelist or --fresh; pass individual files, or omit them to sync the whole client view", nil, nil), nil
		}
	case p4cache.ActionFilelog:
		// Native `p4 filelog -c` means "revisions in that changelist", which is
		// not this field. A number here would also look like a write target.
		if strings.TrimSpace(req.Changelist) != "" {
			return nil, rejectP4(p4CodeActionNotAllowed, "filelog does not take a changelist; describe that number with `multica p4 describe -c <number>`", nil, nil), nil
		}
	case p4cache.ActionDescribe:
		// A number selects a read, including history outside this task's CL.
	default:
		if p4QueryAction(action) {
			// -c here is the Multica write target, not `p4 changes -c` / `p4 fstat -c`.
			// Those flags travel in Args, after the verb.
			if strings.TrimSpace(req.Changelist) != "" {
				return nil, rejectP4(p4CodeActionNotAllowed, "pass p4 flags after the verb; -c/--changelist is not a client or a write target", nil, nil), nil
			}
			break
		}
		if rej := b.rejectForeignChangelist(req.Changelist); rej != nil {
			return nil, rej, nil
		}
	}
	if b.registrationPending && !p4ReadOnlyAction(action) {
		if err := d.registerPendingP4Change(ctx, b); err != nil {
			return nil, nil, err
		}
	}
	switch action {
	case "submit":
		if !b.ref.AllowSubmit {
			return nil, rejectP4(p4CodeActionNotAllowed, p4cache.ErrSubmitDisabled.Error(), nil, nil), nil
		}
		return d.workspaceSubmit(ctx, b)
	case "sync":
		if !b.ref.AllowSync {
			return nil, rejectP4(p4CodeActionNotAllowed, "workspace mode does not sync; a person syncs this workspace", nil, nil), nil
		}
		return d.workspaceSync(ctx, b, cwd, req.Files)
	case p4cache.ActionUnshelve:
		if !b.ref.AllowUnshelve {
			return nil, rejectP4(p4CodeActionNotAllowed, "workspace mode does not unshelve; a person syncs this workspace", nil, nil), nil
		}
		return d.workspaceUnshelve(ctx, b)
	case p4cache.ActionEdit, p4cache.ActionAdd, p4cache.ActionDelete, p4cache.ActionMove,
		p4cache.ActionReopen, p4cache.ActionRevert, p4cache.ActionReconcile, p4cache.ActionResolve,
		p4cache.ActionShelve, p4cache.ActionChange, p4cache.ActionOpened, p4cache.ActionDescribe,
		p4cache.ActionFilelog:
	default:
		if !p4QueryAction(action) {
			return nil, rejectP4(p4CodeActionNotAllowed, fmt.Sprintf("perforce action %q is not available in workspace mode", req.Action), nil, nil), nil
		}
	}
	if p4QueryAction(action) {
		req.Action = action
		return d.workspaceQuery(ctx, b, cwd, req)
	}
	if actionOpensFiles(action) && strings.TrimSpace(b.ticket) == "" {
		return nil, rejectP4(p4CodeTicketRequired, "this task has no 易协作 ticket; ask a person to add refs #<number> to the task description before opening files", nil, nil), nil
	}

	switch action {
	case p4cache.ActionOpened:
		return d.workspaceOpened(ctx, b)
	case p4cache.ActionDescribe:
		return d.workspaceDescribe(ctx, b, req.Changelist)
	case p4cache.ActionFilelog:
		return d.workspaceFilelog(ctx, b, cwd, req)
	case p4cache.ActionChange:
		return d.workspaceRetitle(ctx, b, req.Description)
	case p4cache.ActionShelve:
		return d.workspaceShelve(ctx, b)
	}
	if action == p4cache.ActionRevert && req.RevertUnchanged && len(req.Files) == 0 {
		files, err := d.revertableP4Files(ctx, b)
		if err != nil {
			return nil, nil, err
		}
		if len(files) == 0 {
			return workspaceVerbResult(b, action, b.changelist(), "No unchanged files opened by this run."), nil, nil
		}
		req.Files = files
	}

	var resolveFlags []string
	previewOnly := action == p4cache.ActionReconcile && req.Preview
	if action == p4cache.ActionResolve {
		flags, files, preview, rejection := classifyResolveArgs(req.Args)
		if rejection != nil {
			return nil, rejection, nil
		}
		resolveFlags = flags
		req.Files = files
		previewOnly = preview
	}

	files, dest, rej := b.resolveWorkspacePaths(cwd, req.Files, req.Dest)
	if rej != nil {
		return nil, rej, nil
	}
	var localTargets []string
	for _, path := range append(append([]string{}, files...), dest) {
		if path != "" && !strings.HasPrefix(path, "//") {
			localTargets = append(localTargets, path)
		}
	}
	if rej := b.rejectSidecars(action, localTargets); rej != nil {
		return nil, rej, nil
	}
	// Depot syntax is an alias for a local path, including files not in the
	// depot yet. fstat cannot reveal those aliases for add/reconcile.
	for i, path := range files {
		resolved, rejection, err := d.workspaceLocalPath(ctx, b, path)
		if rejection != nil || err != nil {
			return nil, rejection, err
		}
		files[i] = resolved
	}
	if dest != "" {
		var err error
		dest, rej, err = d.workspaceLocalPath(ctx, b, dest)
		if rej != nil || err != nil {
			return nil, rej, err
		}
	}
	if action == p4cache.ActionReconcile && len(files) == 0 {
		return nil, rejectP4(p4CodeActionNotAllowed, "reconcile requires explicit file paths in workspace mode and never scans the whole client", nil, nil), nil
	}
	if action == p4cache.ActionRevert && len(files) == 0 && !req.RevertUnchanged {
		return nil, rejectP4(p4CodeActionNotAllowed, "revert requires files, or --unchanged to revert unchanged files in the task changelist", nil, nil), nil
	}
	if action == p4cache.ActionMove && (len(files) != 1 || dest == "") {
		return nil, nil, fmt.Errorf("move requires a source file and --dest")
	}
	if (action == p4cache.ActionEdit || action == p4cache.ActionAdd || action == p4cache.ActionDelete || action == p4cache.ActionReopen) && len(files) == 0 {
		return nil, nil, fmt.Errorf("%s requires at least one file", action)
	}

	targets := append([]string{}, files...)
	if dest != "" {
		targets = append(targets, dest)
	}
	if rej := b.rejectSidecars(action, targets); rej != nil {
		return nil, rej, nil
	}
	states, err := d.p4Cache.FileStates(ctx, b.ws, targets)
	if err != nil {
		return nil, nil, err
	}
	if rej := b.rejectDepotFiles(action, states); rej != nil {
		return nil, rej, nil
	}
	if rej := b.rejectFileStates(action, states); rej != nil {
		return nil, rej, nil
	}
	if action == p4cache.ActionRevert {
		for _, path := range files {
			if !b.touchedPaths[p4WorkspacePathKey(path)] {
				return nil, rejectP4(p4CodeOpenedByYou, "file was not opened by this run", []string{path}, nil), nil
			}
		}
	}
	warnings := workspaceFileWarnings(states, b.ref.AllowExclusive)

	change := b.changelist()
	if actionNeedsChange(action) && !previewOnly {
		change, err = d.ensureTaskChange(ctx, b)
		if err != nil {
			return nil, nil, err
		}
	} else if change == "" {
		return nil, rejectP4(p4CodeActionNotAllowed, "this task has no changelist yet; open a file first", nil, nil), nil
	}

	var flags []string
	runFiles := files
	switch action {
	case p4cache.ActionRevert:
		if req.RevertUnchanged {
			flags = append(flags, "-a")
		}
	case p4cache.ActionReconcile:
		flags = append(flags, "-aed")
		if req.Preview {
			flags = append(flags, "-n")
		}
	case p4cache.ActionResolve:
		flags = append(flags, resolveFlags...)
	case p4cache.ActionMove:
		runFiles = []string{files[0], dest}
	}
	if !previewOnly {
		// P4 can apply some files before returning an error.
		b.touchChange()
		if actionOpensFiles(action) {
			b.touchPaths(targets)
		}
	}
	output, err := d.p4Cache.RunInChange(ctx, b.ws, change, action, runFiles, flags...)
	if err != nil {
		return nil, nil, err
	}
	return &p4WorkspaceResult{
		MutateResult: p4cache.MutateResult{
			Path:       b.ws.Dir,
			Client:     b.ws.Client,
			Action:     action,
			Changelist: change,
			Output:     output,
		},
		Warnings: warnings,
	}, nil, nil
}

func (d *Daemon) workspaceSubmit(ctx context.Context, b *p4TaskBinding) (*p4WorkspaceResult, *p4Rejection, error) {
	change := b.changelist()
	if change == "" {
		return nil, rejectP4(p4CodeActionNotAllowed, "this task has no changelist yet; open a file first", nil, nil), nil
	}
	validate := func() (*p4Rejection, error) {
		spec, err := d.p4Cache.ReadPendingChange(ctx, b.ws, change)
		if err != nil {
			return nil, err
		}
		if spec.Stream != "" {
			return rejectP4(p4CodeActionNotAllowed, "this changelist contains an open stream specification; a person must move or submit it before the agent can submit files", nil, nil), nil
		}
		desc := spec.Description
		if !p4depot.ValidTicket(b.ticket) || p4depot.TicketFromText(desc) != b.ticket {
			return rejectP4(p4CodeTicketRequired, "the changelist description must still reference this task's ticket refs #"+b.ticket, nil, nil), nil
		}
		if b.issueKey != "" && !p4depot.DescriptionHasTaskTrailer(desc, b.issueKey) {
			return rejectP4(p4CodeOutsideTaskChange, "the changelist description no longer identifies this task's issue", nil, nil), nil
		}
		b.mu.Lock()
		b.description = desc
		b.mu.Unlock()
		return nil, nil
	}
	if rej, err := validate(); rej != nil || err != nil {
		return nil, rej, err
	}
	opened, err := d.p4Cache.OpenedInChange(ctx, b.ws, change)
	if err != nil {
		return nil, nil, err
	}
	current := make(map[string]p4OpenedFileSnapshot, len(opened))
	for _, file := range opened {
		current[file.DepotFile] = p4OpenedFileSnapshot{}
	}
	if removed := removedP4OpenedFiles(b.initialOpened, current); len(removed) > 0 {
		return nil, rejectP4(p4CodeOpenedByYou, "pre-existing opens were removed; restore them before submitting", removed, nil), nil
	}
	shelf, err := d.p4Cache.ReadShelf(ctx, b.ws, change)
	if err != nil {
		return nil, nil, err
	}
	if shelf.Stream != "" {
		return nil, rejectP4(p4CodeActionNotAllowed, "this changelist contains a stream specification; a person must move or submit it before the agent can submit files", nil, nil), nil
	}
	if rej := b.rejectDepotFiles("submit", append(append([]p4cache.FileState{}, opened...), shelf.Files...)); rej != nil {
		return nil, rej, nil
	}
	b.touchChange()
	revertFiles, err := d.revertableP4Files(ctx, b)
	if err != nil {
		return nil, nil, err
	}
	if len(revertFiles) > 0 {
		if _, err := d.p4Cache.RunInChange(ctx, b.ws, change, "revert", revertFiles, "-a"); err != nil {
			return nil, nil, err
		}
	}
	opened, err = d.p4Cache.OpenedInChange(ctx, b.ws, change)
	if err != nil {
		return nil, nil, err
	}
	if len(opened) == 0 {
		return nil, rejectP4(p4CodeActionNotAllowed, "this task's changelist has no modified files to submit", nil, nil), nil
	}
	shelf, err = d.p4Cache.ReadShelf(ctx, b.ws, change)
	if err != nil {
		return nil, nil, err
	}
	if shelf.Stream != "" {
		return nil, rejectP4(p4CodeActionNotAllowed, "this changelist contains a stream specification; a person must move or submit it before the agent can submit files", nil, nil), nil
	}
	openPaths := make(map[string]bool, len(opened))
	for _, file := range opened {
		openPaths[file.DepotFile] = true
	}
	for _, file := range shelf.Files {
		if !openPaths[file.DepotFile] {
			unchanged, err := d.p4Cache.ShelfFileMatchesHave(ctx, b.ws, change, file)
			if err != nil {
				return nil, nil, err
			}
			if unchanged {
				continue
			}
			return nil, rejectP4(p4CodeActionNotAllowed, "the shelf contains files not open in this task changelist; restore or explicitly remove them before submitting", []string{file.DepotFile}, nil), nil
		}
	}
	// Re-read after cleanup: a person can edit the description in P4V while
	// the task is running. Never rewrite their description to authorize submit.
	if rej, err := validate(); rej != nil || err != nil {
		return nil, rej, err
	}
	if err := d.p4Cache.DeleteShelf(ctx, b.ws, change); err != nil {
		return nil, nil, err
	}
	output, err := d.p4Cache.RunInChange(ctx, b.ws, change, "submit", nil)
	if err != nil {
		return nil, nil, err
	}
	b.markSubmitted()
	// Once the write succeeded, a failed read must never replay submit.
	receipt, err := d.submittedP4Change(ctx, b)
	if err != nil {
		return nil, nil, fmt.Errorf("changelist %s was submitted, but its receipt could not be read: %w", change, err)
	}
	return workspaceVerbResult(b, "submit", receipt.Change, output), nil, nil
}

func (d *Daemon) workspaceUnshelve(ctx context.Context, b *p4TaskBinding) (*p4WorkspaceResult, *p4Rejection, error) {
	change := b.changelist()
	if change == "" {
		return nil, rejectP4(p4CodeActionNotAllowed, "this task has no changelist yet; open a file first", nil, nil), nil
	}
	// -s and -c are the same task changelist. Another shelf would land
	// someone else's files in this workspace.
	shelf, err := d.p4Cache.ReadShelf(ctx, b.ws, change)
	if err != nil {
		return nil, nil, err
	}
	if rej := b.rejectDepotFiles("unshelve", shelf.Files); rej != nil {
		return nil, rej, nil
	}
	for _, file := range shelf.Files {
		path, rej, err := d.workspaceLocalPath(ctx, b, file.DepotFile)
		if rej != nil || err != nil {
			return nil, rej, err
		}
		b.touchPaths([]string{path})
	}
	b.touchChange()
	output, err := d.p4Cache.RunInChange(ctx, b.ws, change, "unshelve", nil, "-s", change, "-Af")
	if err != nil {
		return nil, nil, err
	}
	return workspaceVerbResult(b, p4cache.ActionUnshelve, change, output), nil, nil
}

func (d *Daemon) workspaceSync(ctx context.Context, b *p4TaskBinding, cwd string, files []string) (*p4WorkspaceResult, *p4Rejection, error) {
	paths, _, rej := b.resolveWorkspacePaths(cwd, files, "")
	if rej != nil {
		return nil, rej, nil
	}
	// Empty means the full client view. Named arguments are individual files
	// in an authorized depot, same as every other workspace path. Directories
	// and wildcards stay rejected: a path/... sync is a person's choice.
	if len(files) > 0 {
		if len(paths) == 0 {
			return nil, rejectP4(p4CodeActionNotAllowed, "sync file arguments were empty; omit them to sync the whole client view", nil, nil), nil
		}
		for i, path := range paths {
			resolved, rejection, err := d.workspaceLocalPath(ctx, b, path)
			if rejection != nil || err != nil {
				return nil, rejection, err
			}
			paths[i] = resolved
		}
	}
	output, err := d.p4Cache.SyncClient(ctx, b.ws, paths)
	if err != nil {
		return nil, nil, err
	}
	return workspaceVerbResult(b, "sync", b.changelist(), output), nil, nil
}

func workspaceVerbResult(b *p4TaskBinding, action, change, output string) *p4WorkspaceResult {
	return &p4WorkspaceResult{
		MutateResult: p4cache.MutateResult{
			Path:       b.ws.Dir,
			Client:     b.ws.Client,
			Action:     action,
			Changelist: change,
			Output:     output,
		},
	}
}

func (d *Daemon) workspaceLocalPath(ctx context.Context, b *p4TaskBinding, path string) (string, *p4Rejection, error) {
	depot, local, err := d.p4Cache.MappedFile(ctx, b.ws, path)
	if err != nil {
		return "", nil, err
	}
	if _, allowed := b.depotRef(depot); !allowed {
		return "", rejectP4(p4CodeNotInWorkspace, "mapped file is outside this task's authorized depots", []string{depot}, nil), nil
	}
	paths, rej := b.resolveOneList(b.ws.Dir, []string{local})
	if rej != nil {
		return "", rej, nil
	}
	path = paths[0]
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return "", rejectP4(p4CodeActionNotAllowed, "workspace mode requires individual files, not directories", []string{path}, nil), nil
	}
	return path, nil, nil
}

func (b *p4TaskBinding) rejectForeignChangelist(requested string) *p4Rejection {
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == b.changelist() {
		return nil
	}
	// An explicit number before the task changelist exists, or a different
	// one after, would write into someone else's changelist.
	if b.changelist() == "" || requested != b.changelist() {
		return rejectP4(p4CodeOutsideTaskChange, "changelist must be this task's changelist", []string{requested}, nil)
	}
	return nil
}

func classifyResolveArgs(args []string) ([]string, []string, bool, *p4Rejection) {
	policy := p4ReadPolicy{rejectFlags: map[string]bool{"-c": true, "-So": true}}
	_, flags, files, rej := classifyReadArgs(policy, args)
	if rej != nil {
		return nil, nil, false, rej
	}
	automatic := false
	preview := false
	for _, flag := range flags {
		switch flag.name {
		case "-as", "-am", "-af", "-at", "-ay":
			automatic = true
		case "-n", "-N":
			preview = true
		case "-f", "-K", "-o", "-t", "-v",
			"-Aa", "-Ab", "-Ac", "-Ad", "-Am", "-At", "-AQ",
			"-db", "-dw", "-dl", "-dn", "-ds", "-dc", "-du":
		default:
			if !resolveContextDiffFlag(flag.name) {
				return nil, nil, false, rejectP4(p4CodeActionNotAllowed, "resolve flag is not in the allowed non-interactive set", []string{flag.name}, nil)
			}
		}
	}
	if !automatic && !preview {
		return nil, nil, false, rejectP4(p4CodeActionNotAllowed, "resolve requires an automatic mode (-as, -am, -af, -at, or -ay), or -n/-N for a preview", nil, nil)
	}
	return rebuildReadArgs(flags, nil), files, preview, nil
}

func resolveContextDiffFlag(flag string) bool {
	for _, prefix := range []string{"-dc", "-du"} {
		if suffix, ok := strings.CutPrefix(flag, prefix); ok && suffix != "" {
			for _, r := range suffix {
				if r < '0' || r > '9' {
					return false
				}
			}
			return true
		}
	}
	return false
}

func actionOpensFiles(action string) bool {
	switch action {
	case p4cache.ActionEdit, p4cache.ActionAdd, p4cache.ActionDelete, p4cache.ActionMove,
		p4cache.ActionReopen, p4cache.ActionReconcile, p4cache.ActionChange:
		return true
	default:
		return false
	}
}

func actionNeedsChange(action string) bool {
	return actionOpensFiles(action)
}

func (d *Daemon) ensureTaskChange(ctx context.Context, b *p4TaskBinding) (string, error) {
	b.mu.Lock()
	change, err := d.ensureTaskChangeLocked(ctx, b)
	b.mu.Unlock()
	if err != nil {
		return "", err
	}
	if err := d.registerPendingP4Change(ctx, b); err != nil {
		return "", err
	}
	return change, nil
}

func (d *Daemon) ensureTaskChangeLocked(ctx context.Context, b *p4TaskBinding) (string, error) {
	if b.change != "" {
		if b.initialOpened != nil && !b.descriptionReady {
			if err := d.prepareReusedP4Description(ctx, b); err != nil {
				return "", err
			}
		}
		return b.change, nil
	}
	if strings.TrimSpace(b.ticket) == "" {
		return "", fmt.Errorf("this task has no 易协作 ticket")
	}
	if b.issueKey != "" {
		existing, err := d.p4Cache.FindTaskChange(ctx, b.ws, b.issueKey)
		if err != nil {
			return "", err
		}
		if existing != "" {
			b.change = existing
			spec, err := d.p4Cache.ReadPendingChange(ctx, b.ws, existing)
			if err != nil {
				return "", err
			}
			b.description = spec.Description
			b.initialOpened, err = d.snapshotP4Change(ctx, b)
			if err != nil {
				return "", err
			}
			if err := d.prepareReusedP4Description(ctx, b); err != nil {
				return "", err
			}
			return existing, nil
		}
	}
	desc := p4depot.ChangeDescription(b.ticket, b.title, b.issueKey)
	b.changeTouched = true
	change, err := d.p4Cache.CreateTaskChange(ctx, b.ws, desc)
	if err != nil {
		return "", err
	}
	b.change = change
	b.description = desc
	b.descriptionReady = true
	return change, nil
}

// Called with b.mu held. Repair only a ticket that was already absent when
// binding; never overwrite human edits or a conflicting task association.
func (d *Daemon) prepareReusedP4Description(ctx context.Context, b *p4TaskBinding) error {
	spec, err := d.p4Cache.ReadPendingChange(ctx, b.ws, b.change)
	if err != nil {
		return err
	}
	desc := spec.Description
	if !p4depot.ValidTicket(b.ticket) {
		return fmt.Errorf("this task has no valid bound ticket")
	}
	if !p4depot.DescriptionHasTaskTrailer(desc, b.issueKey) {
		return fmt.Errorf("the changelist no longer identifies this task's issue")
	}
	for _, line := range strings.Split(desc, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), strings.ToLower(p4depot.TaskTrailerPrefix)) &&
			!strings.EqualFold(line, p4depot.TaskTrailerPrefix+b.issueKey) {
			return fmt.Errorf("the changelist identifies another issue")
		}
	}
	ticket := p4depot.TicketFromText(desc)
	if ticket != "" && ticket != b.ticket {
		return fmt.Errorf("the changelist references another ticket refs #%s", ticket)
	}
	if ticket == "" {
		if strings.TrimSpace(desc) != strings.TrimSpace(b.description) || p4DescriptionTicketMarker.MatchString(desc) {
			return fmt.Errorf("the changelist ticket is missing or was edited; ask a person to restore refs #%s", b.ticket)
		}
		desc = "refs #" + b.ticket + "\n\n" + desc
		b.changeTouched = true
		if err := d.p4Cache.SetChangeDescription(ctx, b.ws, b.change, desc); err != nil {
			return err
		}
	}
	b.description = desc
	b.descriptionReady = true
	return nil
}

func (b *p4TaskBinding) resolveWorkspacePaths(cwd string, files []string, dest string) ([]string, string, *p4Rejection) {
	resolved, rej := b.resolveOneList(cwd, files)
	if rej != nil {
		return nil, "", rej
	}
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return resolved, "", nil
	}
	resolvedDest, rej := b.resolveOneList(cwd, []string{dest})
	if rej != nil {
		return nil, "", rej
	}
	if len(resolvedDest) != 1 {
		return nil, "", rejectP4(p4CodeNotInWorkspace, "destination is outside the workspace", []string{dest}, nil)
	}
	return resolved, resolvedDest[0], nil
}

func (b *p4TaskBinding) resolveOneList(cwd string, files []string) ([]string, *p4Rejection) {
	if cwd == "" {
		cwd = b.ws.Dir
	}
	out := make([]string, 0, len(files))
	var outside []string
	for _, file := range files {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		if strings.Contains(file, "...") || strings.ContainsAny(file, "*@#") {
			return nil, rejectP4(p4CodeActionNotAllowed, "workspace mode requires individual file paths without wildcards or revision specifiers", []string{file}, nil)
		}
		if strings.HasPrefix(file, "//") {
			out = append(out, file)
			continue
		}
		abs := file
		if !filepath.IsAbs(file) {
			if filepath.VolumeName(file) != "" || os.IsPathSeparator(file[0]) || !filepath.IsAbs(cwd) {
				outside = append(outside, file)
				continue
			}
			// Join/Abs clean away link/.. before the filesystem can resolve it.
			abs = strings.TrimRight(cwd, string(filepath.Separator)) + string(filepath.Separator) + file
		}
		resolved, err := b.ws.ResolveLocalPath(abs)
		if err != nil {
			outside = append(outside, file)
			continue
		}
		out = append(out, resolved)
	}
	if len(outside) > 0 {
		return nil, rejectP4(p4CodeNotInWorkspace, "path is outside the Perforce client root "+b.ws.Root, outside, nil)
	}
	return out, nil
}

func (b *p4TaskBinding) rejectSidecars(action string, paths []string) *p4Rejection {
	switch action {
	case p4cache.ActionEdit, p4cache.ActionAdd, p4cache.ActionDelete, p4cache.ActionMove,
		p4cache.ActionReconcile, p4cache.ActionResolve, p4cache.ActionReopen:
	default:
		return nil
	}
	var hits []string
	for _, path := range paths {
		if b.isSidecar(path) {
			hits = append(hits, path)
		}
	}
	if len(hits) == 0 {
		return nil
	}
	return rejectP4(p4CodeSidecarPath, "Multica writes this path itself; it cannot be opened into the task changelist", hits, nil)
}

func (b *p4TaskBinding) rejectFileStates(action string, states []p4cache.FileState) *p4Rejection {
	taskChange := b.changelist()
	var openedFiles []string
	var openedChange string
	var lockedFiles, lockedHolders []string
	var exclusiveFiles []string
	var outside []string
	for _, state := range states {
		if state.LocalPath != "" && b.isSidecar(state.LocalPath) {
			return rejectP4(p4CodeSidecarPath, "Multica writes this path itself; it cannot be opened into the task changelist", []string{state.DepotFile}, nil)
		}
		switch action {
		case p4cache.ActionRevert:
			if _, protected := b.initialOpened[state.DepotFile]; protected {
				return rejectP4(p4CodeOpenedByYou, "file was already open before this run", []string{state.DepotFile}, nil)
			}
			if state.OpenChange != taskChange {
				outside = append(outside, state.DepotFile)
			}
			continue
		case p4cache.ActionReopen:
			if taskChange != "" && state.OpenChange == taskChange {
				continue
			}
			if state.OpenChange == "default" && !b.inDefaultSnapshot(state.DepotFile) {
				break
			}
			if state.OpenChange == "default" || b.inDefaultSnapshot(state.DepotFile) {
				openedFiles = append(openedFiles, state.DepotFile)
				openedChange = firstNonEmpty(state.OpenChange, "default")
			} else {
				outside = append(outside, state.DepotFile)
			}
		default:
			if state.OpenChange != "" && state.OpenChange != taskChange {
				openedFiles = append(openedFiles, state.DepotFile)
				if openedChange == "" {
					openedChange = state.OpenChange
				}
			}
			if taskChange != "" && state.OpenChange == taskChange {
				continue
			}
		}
		if action == p4cache.ActionRevert {
			continue
		}
		// A +l file anyone else has open is locked to them: the server reports
		// that as a plain otherOpen, and sets otherLock only for `p4 lock`.
		if state.OtherLock || (state.Exclusive() && len(state.OtherOpens) > 0) {
			lockedFiles = append(lockedFiles, state.DepotFile)
			lockedHolders = append(lockedHolders, state.OtherOpens...)
			continue
		}
		ref, _ := b.depotRef(state.DepotFile)
		if state.Exclusive() && !ref.AllowExclusive && action != p4cache.ActionRevert {
			exclusiveFiles = append(exclusiveFiles, state.DepotFile)
		}
	}
	if len(openedFiles) > 0 {
		return rejectP4(p4CodeOpenedByYou, "already open in your changelist "+openedChange, openedFiles, nil)
	}
	if len(outside) > 0 {
		return rejectP4(p4CodeOutsideTaskChange, "file is not open in this task's changelist", outside, nil)
	}
	if len(lockedFiles) > 0 {
		if len(lockedHolders) == 0 {
			lockedHolders = []string{"locked"}
		}
		return rejectP4(p4CodeExclusiveLocked, "someone else holds the exclusive lock", lockedFiles, uniqueStrings(lockedHolders))
	}
	if len(exclusiveFiles) > 0 {
		return rejectP4(p4CodeExclusiveNotAllowed, "this file is exclusive (+l) and the depot does not set allow_exclusive", exclusiveFiles, nil)
	}
	return nil
}

func (b *p4TaskBinding) inDefaultSnapshot(depotFile string) bool {
	for _, file := range b.defaultSnapshot {
		if file == depotFile {
			return true
		}
	}
	return false
}

func workspaceFileWarnings(states []p4cache.FileState, allowExclusive bool) []p4Warning {
	var notHead, elsewhere, exclusive []string
	for _, state := range states {
		if state.DepotFile == "" {
			continue
		}
		if !state.AtHead() {
			notHead = append(notHead, state.DepotFile)
		}
		if len(state.OtherOpens) > 0 && !state.OtherLock {
			elsewhere = append(elsewhere, state.DepotFile)
		}
		if state.Exclusive() && allowExclusive {
			exclusive = append(exclusive, state.DepotFile)
		}
	}
	return packWarnings(
		p4Warning{Kind: "not_head", Files: notHead},
		p4Warning{Kind: "opened_elsewhere", Files: elsewhere},
		p4Warning{Kind: "exclusive_held", Files: exclusive},
	)
}

func packWarnings(warnings ...p4Warning) []p4Warning {
	out := make([]p4Warning, 0, len(warnings))
	for _, warning := range warnings {
		if len(warning.Files) == 0 {
			continue
		}
		out = append(out, warning)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (d *Daemon) workspaceOpened(ctx context.Context, b *p4TaskBinding) (*p4WorkspaceResult, *p4Rejection, error) {
	change := b.changelist()
	var (
		states []p4cache.FileState
		err    error
	)
	if change == "" {
		return workspaceVerbResult(b, p4cache.ActionOpened, "", ""), nil, nil
	} else {
		states, err = d.p4Cache.OpenedInChange(ctx, b.ws, change)
	}
	if err != nil {
		return nil, nil, err
	}
	var lines []string
	files := make([]p4OpenedFile, 0, len(states))
	for _, state := range states {
		lines = append(lines, state.DepotFile+"#"+state.Action)
		fileChange := state.OpenChange
		if fileChange == "" {
			fileChange = change
		}
		files = append(files, p4OpenedFile{
			DepotFile:  state.DepotFile,
			ClientFile: state.LocalPath,
			Action:     state.Action,
			Change:     fileChange,
			Type:       state.HeadType,
		})
	}
	return &p4WorkspaceResult{
		MutateResult: p4cache.MutateResult{
			Path:       b.ws.Dir,
			Client:     b.ws.Client,
			Action:     p4cache.ActionOpened,
			Changelist: change,
			Output:     strings.Join(lines, "\n"),
		},
		Files: files,
	}, nil, nil
}

func p4ReadOnlyAction(action string) bool {
	switch action {
	case p4cache.ActionOpened, p4cache.ActionDescribe, p4cache.ActionFilelog:
		return true
	default:
		return p4QueryAction(action)
	}
}

func (d *Daemon) workspaceDescribe(ctx context.Context, b *p4TaskBinding, requested string) (*p4WorkspaceResult, *p4Rejection, error) {
	requested = strings.TrimSpace(requested)
	taskChange := b.changelist()
	if requested == "" {
		requested = taskChange
	}
	if requested == "" {
		return nil, rejectP4(p4CodeActionNotAllowed, "this task has no changelist yet; pass a number to read another changelist", nil, nil), nil
	}
	if !p4cache.ValidChangeNumber(requested) {
		return nil, rejectP4(p4CodeActionNotAllowed, "describe requires a changelist number", []string{requested}, nil), nil
	}
	text, err := d.p4Cache.DescribeText(ctx, b.ws, requested)
	if err != nil {
		return nil, nil, err
	}
	parsed, ok := p4cache.ParseDescribe(text)
	// The task changelist is already this run's. Another number is a history
	// read: return it only when every listed file is in an authorized depot,
	// and never return an unparsed body that might name another depot.
	if requested != taskChange {
		if !ok || len(parsed.Files) == 0 {
			return nil, rejectP4(p4CodeActionNotAllowed, "could not confirm that changelist's files are in this task's authorized depots", []string{requested}, nil), nil
		}
		for _, file := range parsed.Files {
			if _, allowed := b.depotRef(file.DepotFile); !allowed {
				return nil, rejectP4(p4CodeNotInWorkspace, "changelist includes files outside this task's authorized depots", []string{requested}, nil), nil
			}
		}
	}
	result := &p4WorkspaceResult{
		MutateResult: p4cache.MutateResult{
			Path:       b.ws.Dir,
			Client:     b.ws.Client,
			Action:     p4cache.ActionDescribe,
			Changelist: requested,
			Output:     text,
		},
	}
	if ok {
		result.Description = parsed.Description
		result.Changelist = firstNonEmpty(parsed.Change, requested)
		for _, file := range parsed.Files {
			result.Files = append(result.Files, p4OpenedFile{
				DepotFile: file.DepotFile,
				Action:    file.Action,
				Change:    result.Changelist,
			})
		}
	}
	return result, nil, nil
}

func (d *Daemon) workspaceFilelog(ctx context.Context, b *p4TaskBinding, cwd string, req p4MutateRequest) (*p4WorkspaceResult, *p4Rejection, error) {
	if len(req.Files) == 0 {
		return nil, rejectP4(p4CodeActionNotAllowed, "filelog requires at least one file", nil, nil), nil
	}
	max := req.Max
	if max == 0 {
		max = p4cache.FilelogDefaultMax
	}
	if max < 1 || max > p4cache.FilelogMaxMax {
		return nil, rejectP4(p4CodeActionNotAllowed, fmt.Sprintf("filelog --max must be between 1 and %d", p4cache.FilelogMaxMax), nil, nil), nil
	}
	files, _, rej := b.resolveWorkspacePaths(cwd, req.Files, "")
	if rej != nil {
		return nil, rej, nil
	}
	if len(files) == 0 {
		return nil, rejectP4(p4CodeActionNotAllowed, "filelog requires at least one file", nil, nil), nil
	}
	for i, path := range files {
		resolved, rejection, err := d.workspaceLocalPath(ctx, b, path)
		if rejection != nil || err != nil {
			return nil, rejection, err
		}
		files[i] = resolved
	}
	text, revs, err := d.p4Cache.Filelog(ctx, b.ws, files, max, req.Long)
	if err != nil {
		return nil, nil, err
	}
	return &p4WorkspaceResult{
		MutateResult: p4cache.MutateResult{
			Path:   b.ws.Dir,
			Client: b.ws.Client,
			Action: p4cache.ActionFilelog,
			Output: text,
		},
		Revisions: revs,
	}, nil, nil
}

func (d *Daemon) workspaceRetitle(ctx context.Context, b *p4TaskBinding, description string) (*p4WorkspaceResult, *p4Rejection, error) {
	desc, err := taskChangeDescription(b, description)
	if err != nil {
		return nil, rejectP4(p4CodeTicketRequired, err.Error(), nil, nil), nil
	}
	change, err := d.ensureTaskChange(ctx, b)
	if err != nil {
		return nil, nil, err
	}
	b.touchChange()
	if err := d.p4Cache.SetChangeDescription(ctx, b.ws, change, desc); err != nil {
		return nil, nil, err
	}
	b.mu.Lock()
	b.description = desc
	b.mu.Unlock()
	return &p4WorkspaceResult{
		MutateResult: p4cache.MutateResult{
			Path:       b.ws.Dir,
			Client:     b.ws.Client,
			Action:     p4cache.ActionChange,
			Changelist: change,
			Output:     "Change " + change + " updated.",
		},
	}, nil, nil
}

func taskChangeDescription(b *p4TaskBinding, description string) (string, error) {
	if !p4depot.ValidTicket(b.ticket) {
		return "", fmt.Errorf("this task has no valid bound ticket")
	}
	description = strings.TrimSpace(description)
	if description == "" {
		description = b.title
	}
	// The ticket prefix and the issue trailer are not the agent's to drop.
	ticket := p4depot.TicketFromText(description)
	if ticket != "" && ticket != b.ticket {
		return "", fmt.Errorf("changelist description must reference this task's ticket refs #%s", b.ticket)
	}
	if ticket == "" {
		if strings.Contains(strings.ToLower(description), "refs #") {
			return "", fmt.Errorf("changelist description must contain a valid refs #%s marker", b.ticket)
		}
		return p4depot.ChangeDescription(b.ticket, description, b.issueKey), nil
	}
	if b.issueKey != "" && !p4depot.DescriptionHasTaskTrailer(description, b.issueKey) {
		return strings.TrimRight(description, "\n") + "\n\n" + p4depot.TaskTrailerPrefix + b.issueKey + "\n", nil
	}
	if !strings.HasSuffix(description, "\n") {
		description += "\n"
	}
	return description, nil
}

func (d *Daemon) workspaceShelve(ctx context.Context, b *p4TaskBinding) (*p4WorkspaceResult, *p4Rejection, error) {
	change := b.changelist()
	if change == "" {
		return nil, rejectP4(p4CodeActionNotAllowed, "this task has no changelist yet; open a file first", nil, nil), nil
	}
	opened, err := d.p4Cache.OpenedInChange(ctx, b.ws, change)
	if err != nil {
		return nil, nil, err
	}
	files := make([]string, 0, len(opened))
	for _, file := range opened {
		files = append(files, file.DepotFile)
	}
	if b.initialOpened != nil {
		current, err := d.snapshotP4Change(ctx, b)
		if err != nil {
			return nil, nil, err
		}
		files = changedP4OpenedFiles(b.initialOpened, current)
		if removed := removedP4OpenedFiles(b.initialOpened, current); len(removed) > 0 {
			return nil, rejectP4(p4CodeOpenedByYou, "pre-existing opens were removed; restore them before shelving", removed, nil), nil
		}
	}
	if len(files) == 0 {
		return nil, rejectP4(p4CodeActionNotAllowed, "this run has no changed opens to shelve; its existing shelf is unchanged", nil, nil), nil
	}
	if err := d.validateP4CloseoutFiles(ctx, b, files); err != nil {
		return nil, nil, err
	}
	b.touchChange()
	output, err := d.p4Cache.ShelveFiles(ctx, b.ws, change, files)
	if err != nil {
		return nil, nil, err
	}
	return &p4WorkspaceResult{
		MutateResult: p4cache.MutateResult{
			Path:       b.ws.Dir,
			Client:     b.ws.Client,
			Action:     p4cache.ActionShelve,
			Changelist: change,
			Output:     output,
		},
	}, nil, nil
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
