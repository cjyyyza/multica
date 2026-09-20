package p4cache

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/internal/p4depot"
)

const (
	ActionEdit      = "edit"
	ActionAdd       = "add"
	ActionDelete    = "delete"
	ActionRevert    = "revert"
	ActionOpened    = "opened"
	ActionReconcile = "reconcile"
	ActionMove      = "move"
	ActionReopen    = "reopen"
	ActionChange    = "change"
	ActionSubmit    = "submit"
	ActionShelve    = "shelve"
	ActionUnshelve  = "unshelve"
	ActionDescribe  = "describe"
)

// MutateParams is one task-scoped Perforce file or changelist operation.
type MutateParams struct {
	WorkspaceID     string
	TaskID          string
	WorkDir         string
	Cwd             string
	Ref             p4depot.Ref
	Action          string
	Files           []string
	Changelist      string
	Description     string
	Dest            string
	RevertUnchanged bool
	Force           bool
}

// MutateResult is the daemon /p4/run response.
type MutateResult struct {
	Path       string `json:"path"`
	Client     string `json:"client"`
	Action     string `json:"action"`
	Changelist string `json:"changelist,omitempty"`
	Output     string `json:"output"`
}

var (
	changeCreated   = regexp.MustCompile(`(?i)Change\s+(\d+)\s+created`)
	changeSubmitted = regexp.MustCompile(`(?i)Change\s+(\d+)\s+submitted`)
)

// Mutate runs an allowlisted p4 verb against the task client created by Sync.
func (c *Cache) Mutate(ctx context.Context, params MutateParams) (*MutateResult, error) {
	ref, err := p4depot.Normalize(params.Ref)
	if err != nil {
		return nil, err
	}
	action := strings.ToLower(strings.TrimSpace(params.Action))
	if !validMutateAction(action) {
		return nil, fmt.Errorf("unsupported perforce action %q", params.Action)
	}
	if strings.TrimSpace(params.WorkDir) == "" {
		return nil, fmt.Errorf("workdir is required")
	}
	if strings.TrimSpace(params.TaskID) == "" {
		return nil, fmt.Errorf("task_id is required")
	}

	user, err := c.resolveUser(ctx, ref)
	if err != nil {
		return nil, err
	}
	client := p4depot.ClientName(params.WorkspaceID, params.TaskID, ref)
	root := ClientRoot(params.WorkDir, params.WorkspaceID, params.TaskID, ref)
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("perforce checkout is missing; run `multica p4 sync --port %s --depot %s` first", ref.Port, ref.Depot)
	}

	cwd := strings.TrimSpace(params.Cwd)
	if cwd == "" {
		cwd = params.WorkDir
	}
	files, err := resolveMutateFiles(root, cwd, params.Files, depotScope(ref))
	if err != nil {
		return nil, err
	}
	dest := strings.TrimSpace(params.Dest)
	if dest != "" {
		resolvedDest, destErr := resolveMutateFiles(root, cwd, []string{dest}, depotScope(ref))
		if destErr != nil {
			return nil, destErr
		}
		if len(resolvedDest) == 1 {
			dest = resolvedDest[0]
			params.Dest = dest
		}
	}
	if err := requireMutateFiles(action, files, dest); err != nil {
		return nil, err
	}

	changelist := strings.TrimSpace(params.Changelist)
	if changelist != "" && !p4depot.ValidChangelist(changelist) {
		return nil, fmt.Errorf("changelist must be a changelist number, label, or #head")
	}

	result := &MutateResult{
		Path:       root,
		Client:     client,
		Action:     action,
		Changelist: changelist,
	}

	switch action {
	case ActionChange:
		desc := strings.TrimSpace(params.Description)
		if desc == "" {
			return nil, fmt.Errorf("description is required to create a changelist")
		}
		out, err := c.createChange(ctx, ref, user, client, root, desc)
		if err != nil {
			return nil, err
		}
		result.Output = strings.TrimSpace(string(out))
		if match := changeCreated.FindStringSubmatch(result.Output); len(match) == 2 {
			result.Changelist = match[1]
		}
		return result, nil
	}

	args, err := mutateArgs(action, files, params)
	if err != nil {
		return nil, err
	}
	out, err := c.outputAt(ctx, root, ref, user, client, args...)
	if err != nil {
		return nil, err
	}
	result.Output = strings.TrimSpace(string(out))
	if action == ActionSubmit {
		if match := changeSubmitted.FindStringSubmatch(result.Output); len(match) == 2 {
			result.Changelist = match[1]
		}
	}
	return result, nil
}

func validMutateAction(action string) bool {
	switch action {
	case ActionEdit, ActionAdd, ActionDelete, ActionRevert, ActionOpened,
		ActionReconcile, ActionMove, ActionReopen, ActionChange, ActionSubmit,
		ActionShelve, ActionUnshelve, ActionDescribe:
		return true
	default:
		return false
	}
}

func requireMutateFiles(action string, files []string, dest string) error {
	switch action {
	case ActionEdit, ActionAdd, ActionDelete, ActionReopen:
		if len(files) == 0 {
			return fmt.Errorf("%s requires at least one file", action)
		}
	case ActionMove:
		if len(files) != 1 || strings.TrimSpace(dest) == "" {
			return fmt.Errorf("move requires a source file and --dest")
		}
	}
	return nil
}

func mutateArgs(action string, files []string, params MutateParams) ([]string, error) {
	cl := strings.TrimSpace(params.Changelist)
	switch action {
	case ActionEdit, ActionAdd, ActionDelete:
		args := []string{action}
		if cl != "" {
			args = append(args, "-c", cl)
		}
		return append(args, files...), nil
	case ActionRevert:
		args := []string{"revert"}
		if params.RevertUnchanged {
			args = append(args, "-a")
		}
		if cl != "" {
			args = append(args, "-c", cl)
		}
		if len(files) == 0 {
			files = []string{"..."}
		}
		return append(args, files...), nil
	case ActionOpened:
		args := []string{"opened"}
		if cl != "" {
			args = append(args, "-c", cl)
		}
		return args, nil
	case ActionReconcile:
		args := []string{"reconcile", "-aed"}
		if cl != "" {
			args = append(args, "-c", cl)
		}
		if len(files) == 0 {
			files = []string{"..."}
		}
		return append(args, files...), nil
	case ActionMove:
		args := []string{"move"}
		if cl != "" {
			args = append(args, "-c", cl)
		}
		return append(args, files[0], strings.TrimSpace(params.Dest)), nil
	case ActionReopen:
		if cl == "" {
			return nil, fmt.Errorf("reopen requires --changelist")
		}
		return append([]string{"reopen", "-c", cl}, files...), nil
	case ActionSubmit:
		args := []string{"submit"}
		if cl != "" {
			args = append(args, "-c", cl)
		} else if desc := strings.TrimSpace(params.Description); desc != "" {
			args = append(args, "-d", desc)
		}
		return args, nil
	case ActionShelve:
		if cl == "" {
			return nil, fmt.Errorf("shelve requires --changelist")
		}
		args := []string{"shelve", "-c", cl}
		if params.Force {
			args = append(args, "-f")
		}
		return append(args, files...), nil
	case ActionUnshelve:
		if cl == "" {
			return nil, fmt.Errorf("unshelve requires --changelist")
		}
		return []string{"unshelve", "-s", cl}, nil
	case ActionDescribe:
		if cl == "" {
			return nil, fmt.Errorf("describe requires --changelist")
		}
		return []string{"describe", "-s", cl}, nil
	default:
		return nil, fmt.Errorf("unsupported perforce action %q", action)
	}
}

func (c *Cache) createChange(ctx context.Context, ref p4depot.Ref, user, client, root, description string) ([]byte, error) {
	spec := buildChangeSpec(client, user, description)
	args := append(c.baseArgs(ref, user, client), "change", "-i")
	cmd, cancel, err := c.commandAt(ctx, root, args...)
	if err != nil {
		return nil, err
	}
	defer cancel()
	cmd.Stdin = strings.NewReader(spec)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("p4 change -i: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func buildChangeSpec(client, user, description string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Change: new\n")
	fmt.Fprintf(&b, "Client: %s\n", client)
	fmt.Fprintf(&b, "User: %s\n", user)
	fmt.Fprintf(&b, "Status: new\n")
	fmt.Fprintf(&b, "Description:\n")
	for _, line := range strings.Split(description, "\n") {
		fmt.Fprintf(&b, "\t%s\n", line)
	}
	return b.String()
}

func depotScope(ref p4depot.Ref) string {
	scope := strings.TrimSpace(ref.Depot)
	if ref.Stream != "" {
		scope = strings.TrimSpace(ref.Stream)
	}
	return strings.TrimSuffix(scope, "/...")
}

func resolveMutateFiles(clientRoot, cwd string, files []string, depotPrefix string) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(files))
	for _, file := range files {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		if file == "..." || strings.HasPrefix(file, "//") {
			if file != "..." && !depotPathAllowed(file, depotPrefix) {
				return nil, fmt.Errorf("file %s is outside the configured depot", file)
			}
			out = append(out, file)
			continue
		}
		abs := file
		if !filepath.IsAbs(file) {
			abs = filepath.Join(cwd, file)
		}
		resolved, err := filepath.Abs(abs)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", file, err)
		}
		rel, err := filepath.Rel(clientRoot, resolved)
		if err != nil || !filepath.IsLocal(rel) {
			return nil, fmt.Errorf("file %s is outside the Perforce checkout", file)
		}
		out = append(out, resolved)
	}
	return out, nil
}

func depotPathAllowed(path, depotPrefix string) bool {
	depotPrefix = strings.TrimSpace(depotPrefix)
	path = strings.TrimSpace(path)
	if depotPrefix == "" {
		return false
	}
	if path == depotPrefix {
		return true
	}
	return strings.HasPrefix(path, depotPrefix+"/")
}
