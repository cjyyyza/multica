package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

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
	Warnings []p4Warning `json:"warnings,omitempty"`
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
	if b.isClosed() {
		return nil, rejectP4(p4CodeActionNotAllowed, "this task's Perforce workspace is already closing", nil, nil), nil
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "submit" {
		return nil, rejectP4(p4CodeActionNotAllowed, p4cache.ErrSubmitNotSupported.Error(), nil, nil), nil
	}
	switch action {
	case "sync", p4cache.ActionUnshelve:
		return nil, rejectP4(p4CodeActionNotAllowed, "workspace mode does not "+action+"; a person syncs this workspace", nil, nil), nil
	case p4cache.ActionEdit, p4cache.ActionAdd, p4cache.ActionDelete, p4cache.ActionMove,
		p4cache.ActionReopen, p4cache.ActionRevert, p4cache.ActionReconcile,
		p4cache.ActionShelve, p4cache.ActionChange, p4cache.ActionOpened, p4cache.ActionDescribe:
	default:
		return nil, rejectP4(p4CodeActionNotAllowed, fmt.Sprintf("perforce action %q is not available in workspace mode", req.Action), nil, nil), nil
	}
	if rej := b.rejectForeignChangelist(req.Changelist); rej != nil {
		return nil, rej, nil
	}
	if actionOpensFiles(action) && strings.TrimSpace(b.ticket) == "" {
		return nil, rejectP4(p4CodeTicketRequired, "this task has no 易协作 ticket; ask a person to add refs #<number> to the task description before opening files", nil, nil), nil
	}

	switch action {
	case p4cache.ActionOpened:
		return d.workspaceOpened(ctx, b)
	case p4cache.ActionDescribe:
		return d.workspaceDescribe(ctx, b)
	case p4cache.ActionChange:
		return d.workspaceRetitle(ctx, b, req.Description)
	case p4cache.ActionShelve:
		return d.workspaceShelve(ctx, b)
	}

	files, dest, rej := b.resolveWorkspacePaths(cwd, req.Files, req.Dest)
	if rej != nil {
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
	if rej := b.rejectFileStates(action, states); rej != nil {
		return nil, rej, nil
	}
	warnings := workspaceFileWarnings(states, b.ref.AllowExclusive)

	change := b.changelist()
	if actionNeedsChange(action) {
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
	case p4cache.ActionMove:
		runFiles = []string{files[0], dest}
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

func (d *Daemon) workspaceLocalPath(ctx context.Context, b *p4TaskBinding, path string) (string, *p4Rejection, error) {
	if strings.HasPrefix(path, "//") {
		local, err := d.p4Cache.LocalPath(ctx, b.ws, path)
		if err != nil {
			return "", nil, err
		}
		paths, rej := b.resolveOneList(b.ws.Dir, []string{local})
		if rej != nil {
			return "", rej, nil
		}
		path = paths[0]
	}
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
	defer b.mu.Unlock()
	if b.change != "" {
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
			b.description = p4depot.ChangeDescription(b.ticket, b.title, b.issueKey)
			return existing, nil
		}
	}
	desc := p4depot.ChangeDescription(b.ticket, b.title, b.issueKey)
	change, err := d.p4Cache.CreateTaskChange(ctx, b.ws, desc)
	if err != nil {
		return "", err
	}
	b.change = change
	b.description = desc
	return change, nil
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
			if !b.depotVisible(file) {
				outside = append(outside, file)
				continue
			}
			out = append(out, file)
			continue
		}
		abs := file
		if !filepath.IsAbs(file) {
			abs = filepath.Join(cwd, file)
		}
		abs, err := filepath.Abs(abs)
		if err != nil || !pathEqualOrInside(b.ws.Root, abs) {
			outside = append(outside, file)
			continue
		}
		out = append(out, abs)
	}
	if len(outside) > 0 {
		return nil, rejectP4(p4CodeNotInWorkspace, "path is outside the Perforce client root "+b.ws.Root, outside, nil)
	}
	return out, nil
}

func (b *p4TaskBinding) depotVisible(path string) bool {
	target := b.ws.Stream
	if target == "" {
		target = depotScopePath(b.ref)
	}
	target = strings.TrimSuffix(target, "/...")
	path = strings.TrimSuffix(strings.TrimSpace(path), "/...")
	return path == target || strings.HasPrefix(path, target+"/")
}

func (b *p4TaskBinding) rejectSidecars(action string, paths []string) *p4Rejection {
	switch action {
	case p4cache.ActionEdit, p4cache.ActionAdd, p4cache.ActionDelete, p4cache.ActionMove,
		p4cache.ActionReconcile, p4cache.ActionReopen:
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
		if state.Exclusive() && !b.ref.AllowExclusive && action != p4cache.ActionRevert {
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
		states, err = d.p4Cache.Opened(ctx, b.ws)
	} else {
		states, err = d.p4Cache.OpenedInChange(ctx, b.ws, change)
	}
	if err != nil {
		return nil, nil, err
	}
	var lines []string
	for _, state := range states {
		lines = append(lines, state.DepotFile+"#"+state.Action)
	}
	return &p4WorkspaceResult{
		MutateResult: p4cache.MutateResult{
			Path:       b.ws.Dir,
			Client:     b.ws.Client,
			Action:     p4cache.ActionOpened,
			Changelist: change,
			Output:     strings.Join(lines, "\n"),
		},
	}, nil, nil
}

func (d *Daemon) workspaceDescribe(ctx context.Context, b *p4TaskBinding) (*p4WorkspaceResult, *p4Rejection, error) {
	change := b.changelist()
	if change == "" {
		return nil, rejectP4(p4CodeActionNotAllowed, "this task has no changelist yet", nil, nil), nil
	}
	info, err := d.p4Cache.DescribeChange(ctx, b.ws.Ref(), change)
	if err != nil {
		return nil, nil, err
	}
	output := "change " + info.Change + " " + info.Status
	return &p4WorkspaceResult{
		MutateResult: p4cache.MutateResult{
			Path:       b.ws.Dir,
			Client:     b.ws.Client,
			Action:     p4cache.ActionDescribe,
			Changelist: change,
			Output:     output,
		},
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
	flags := []string{"-r"}
	if b.ws.Edge {
		flags = append(flags, "-p")
	}
	output, err := d.p4Cache.RunInChange(ctx, b.ws, change, p4cache.ActionShelve, nil, flags...)
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
