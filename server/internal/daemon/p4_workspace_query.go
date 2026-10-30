package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
)

// p4ReadPolicy is one forwarded read. Value flags consume the next argv token
// so a number or filter is not authorized as a path. Unknown flags are boolean
// and are forwarded; p4 rejects the ones it does not have.
type p4ReadPolicy struct {
	valueFlags        map[string]bool
	rejectFlags       map[string]bool
	requireFile       bool
	allowWildcard     bool
	allowRootWildcard bool
	wildcardNeedsMax  bool
	defaultMax        int
	maxMax            int
	// scanListing checks the first // token of each line. scanListingAll checks
	// every depot token for integration pairs. scanDepotFile checks `... depotFile`
	// only, so otherOpen and client paths are not treated as depots.
	scanListing    bool
	scanListingAll bool
	scanDepotFile  bool
	injectScope    bool
}

func p4ReadPolicies() map[string]p4ReadPolicy {
	maxFlag := map[string]bool{"-m": true}
	fstatFlags := map[string]bool{"-A": true, "-F": true, "-T": true, "-m": true, "-c": true, "-e": true}
	changesFlags := map[string]bool{"-m": true, "-e": true, "-c": true, "-s": true, "-u": true}
	printFlags := map[string]bool{"-m": true, "-Q": true, "-B": true, "-L": true, "--offset": true, "--size": true}
	sizeFlags := map[string]bool{"-m": true, "-b": true, "-Q": true, "-B": true, "-L": true}
	return map[string]p4ReadPolicy{
		p4cache.ActionChanges: {
			valueFlags: changesFlags, allowWildcard: true, allowRootWildcard: true,
			defaultMax: p4cache.QueryDefaultMax, maxMax: p4cache.QueryMaxMax, injectScope: true,
		},
		p4cache.ActionFstat: {
			valueFlags: fstatFlags, requireFile: true, allowWildcard: true, wildcardNeedsMax: true,
			maxMax: p4cache.QueryMaxMax, scanDepotFile: true,
		},
		p4cache.ActionFiles: {
			valueFlags: maxFlag, requireFile: true, allowWildcard: true, wildcardNeedsMax: true,
			maxMax: p4cache.QueryMaxMax, scanListing: true,
		},
		p4cache.ActionDirs: {
			valueFlags: map[string]bool{"-S": true}, requireFile: true, allowWildcard: true, scanListing: true,
		},
		p4cache.ActionGrep: {
			valueFlags:  map[string]bool{"-e": true, "-A": true, "-B": true, "-C": true},
			requireFile: true, allowWildcard: true, scanListing: true,
		},
		p4cache.ActionPrint: {
			valueFlags: printFlags, rejectFlags: map[string]bool{"-o": true},
			requireFile: true, maxMax: p4cache.QueryMaxMax,
		},
		p4cache.ActionDiff:     {valueFlags: maxFlag, rejectFlags: map[string]bool{"-As": true}, requireFile: true, maxMax: p4cache.QueryMaxMax},
		p4cache.ActionDiff2:    {rejectFlags: map[string]bool{"-As": true, "-b": true, "-P": true, "-S": true}, requireFile: true},
		p4cache.ActionAnnotate: {valueFlags: map[string]bool{"--tab": true}, requireFile: true},
		p4cache.ActionWhere:    {requireFile: true, scanListing: true},
		p4cache.ActionHave:     {requireFile: true, scanListing: true},
		p4cache.ActionSizes: {
			valueFlags: sizeFlags, requireFile: true, maxMax: p4cache.QueryMaxMax, scanListing: true,
		},
		p4cache.ActionIntegrated: {
			valueFlags: map[string]bool{"-s": true, "-m": true}, rejectFlags: map[string]bool{"-b": true},
			requireFile: true, allowWildcard: true, maxMax: p4cache.QueryMaxMax, scanListingAll: true,
		},
		p4cache.ActionInterchange: {
			valueFlags: map[string]bool{"-u": true}, rejectFlags: map[string]bool{"-b": true, "-P": true, "-S": true},
			requireFile: true, allowWildcard: true, scanListingAll: true,
		},
		p4cache.ActionResolved: {requireFile: true, scanListingAll: true},
	}
}

func p4QueryAction(action string) bool {
	_, ok := p4ReadPolicies()[action]
	return ok
}

type p4ReadFlag struct {
	name     string
	value    string
	hasValue bool
}

// workspaceQuery runs a read in the bound client and returns p4's own text.
// The write shield points P4CLIENT at a name that does not exist, so raw
// `p4 fstat` / `p4 changes` cannot see this workspace. Parsing a subset of
// flags would drop the fields an agent actually needs (otherOpen, -Ol, -s).
func (d *Daemon) workspaceQuery(ctx context.Context, b *p4TaskBinding, cwd string, req p4MutateRequest) (*p4WorkspaceResult, *p4Rejection, error) {
	policy, ok := p4ReadPolicies()[req.Action]
	if !ok {
		return nil, rejectP4(p4CodeActionNotAllowed, fmt.Sprintf("perforce action %q is not available in workspace mode", req.Action), nil, nil), nil
	}
	if strings.TrimSpace(req.Changelist) != "" {
		return nil, rejectP4(p4CodeActionNotAllowed, "pass p4 flags after the verb; -c/--changelist is not forwarded as a client or a write target", nil, nil), nil
	}
	ztag, flags, files, rej := classifyReadArgs(policy, req.Args)
	if rej != nil {
		return nil, rej, nil
	}
	flags, rej = applyReadMax(policy, flags)
	if rej != nil {
		return nil, rej, nil
	}
	if rej := rejectUnreadStream(b, flags); rej != nil {
		return nil, rej, nil
	}
	files, rej, err := d.authorizeReadFilespecs(ctx, b, cwd, policy, files, hasReadMax(flags))
	if rej != nil || err != nil {
		return nil, rej, err
	}
	if policy.injectScope && len(files) == 0 {
		files = b.changesScopeFilespecs()
		if len(files) == 0 {
			return nil, rejectP4(p4CodeActionNotAllowed, "this task has no authorized depot to list changelists for", nil, nil), nil
		}
	}
	if policy.requireFile && len(files) == 0 {
		return nil, rejectP4(p4CodeActionNotAllowed, req.Action+" requires at least one file", nil, nil), nil
	}
	args := rebuildReadArgs(flags, files)
	stdout, stderr, runErr := d.p4Cache.RunRead(ctx, b.ws, req.Action, args, ztag)
	if ctx.Err() != nil {
		return nil, nil, runErr
	}
	if errors.Is(runErr, p4cache.ErrQueryOutputTooLarge) {
		return nil, rejectP4(p4CodeActionNotAllowed, "p4 output is too large; narrow the filespec or lower -m", nil, nil), nil
	}
	code, exited := p4cache.ExitCode(runErr)
	if runErr != nil && !exited {
		return nil, nil, fmt.Errorf("p4 %s: %w: %s", req.Action, runErr, strings.TrimSpace(stderr))
	}
	if len(stdout) > p4cache.QueryOutputMax || len(stderr) > p4cache.QueryOutputMax {
		return nil, rejectP4(p4CodeActionNotAllowed, "p4 output is too large; narrow the filespec or lower -m", nil, nil), nil
	}
	if rej := rejectForeignReadOutput(b, policy, stdout); rej != nil {
		return nil, rej, nil
	}
	if rej := rejectForeignReadOutput(b, p4ReadPolicy{scanListingAll: true}, stderr); rej != nil {
		return nil, rej, nil
	}
	return &p4WorkspaceResult{
		MutateResult: p4cache.MutateResult{
			Path:   b.ws.Dir,
			Client: b.ws.Client,
			Action: req.Action,
			Output: strings.TrimRight(stdout, "\r\n"),
		},
		Stderr:   strings.TrimSpace(stderr),
		ExitCode: code,
	}, nil, nil
}

func classifyReadArgs(policy p4ReadPolicy, args []string) (bool, []p4ReadFlag, []string, *p4Rejection) {
	if len(args) > 64 {
		return false, nil, nil, rejectP4(p4CodeActionNotAllowed, "too many p4 arguments", nil, nil)
	}
	var flags []p4ReadFlag
	var files []string
	ztag := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "" || strings.ContainsAny(arg, "\r\n") {
			return false, nil, nil, rejectP4(p4CodeActionNotAllowed, "empty or multi-line p4 arguments are not accepted", []string{arg}, nil)
		}
		if arg == "--" {
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			files = append(files, arg)
			continue
		}
		if arg == "-ztag" {
			ztag = true
			continue
		}
		name, attached, hasAttached := splitReadFlag(policy, arg)
		if rejectedReadFlag(policy, name) {
			return false, nil, nil, rejectP4(p4CodeActionNotAllowed, "that flag is not forwarded; it would change the connection, write a file, or expand the authorized read", []string{arg}, nil)
		}
		if hasAttached && !policy.valueFlags[name] {
			flags = append(flags, p4ReadFlag{name: arg})
			continue
		}
		flag := p4ReadFlag{name: name}
		if policy.valueFlags[name] {
			if hasAttached {
				flag.value = attached
				flag.hasValue = true
			} else {
				if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
					return false, nil, nil, rejectP4(p4CodeActionNotAllowed, name+" requires a value", []string{name}, nil)
				}
				i++
				flag.value = args[i]
				flag.hasValue = true
			}
		}
		flags = append(flags, flag)
	}
	return ztag, flags, files, nil
}

func rejectedReadFlag(policy p4ReadPolicy, name string) bool {
	if policy.rejectFlags[name] {
		return true
	}
	switch name {
	case "-p", "-x", "-Mj":
		return true
	}
	return false
}

func splitReadFlag(policy p4ReadPolicy, arg string) (string, string, bool) {
	if strings.HasPrefix(arg, "--") {
		name, value, attached := strings.Cut(arg, "=")
		return name, value, attached
	}
	best := ""
	for _, flags := range []map[string]bool{
		policy.valueFlags,
		policy.rejectFlags,
		{"-p": true, "-x": true, "-Mj": true},
	} {
		for flag := range flags {
			if len(arg) > len(flag) && len(flag) > len(best) && strings.HasPrefix(arg, flag) {
				best = flag
			}
		}
	}
	if best != "" {
		return best, arg[len(best):], true
	}
	return arg, "", false
}

func applyReadMax(policy p4ReadPolicy, flags []p4ReadFlag) ([]p4ReadFlag, *p4Rejection) {
	found := false
	for i, flag := range flags {
		if flag.name != "-m" {
			continue
		}
		found = true
		n, err := strconv.Atoi(strings.TrimSpace(flag.value))
		limit := policy.maxMax
		if limit == 0 {
			limit = p4cache.QueryMaxMax
		}
		if err != nil || n < 1 || n > limit {
			return nil, rejectP4(p4CodeActionNotAllowed, fmt.Sprintf("-m must be between 1 and %d", limit), []string{flag.value}, nil)
		}
		flags[i].value = strconv.Itoa(n)
		flags[i].hasValue = true
	}
	if !found && policy.defaultMax > 0 {
		flags = append([]p4ReadFlag{{name: "-m", value: strconv.Itoa(policy.defaultMax), hasValue: true}}, flags...)
	}
	return flags, nil
}

func hasReadMax(flags []p4ReadFlag) bool {
	for _, flag := range flags {
		if flag.name == "-m" && flag.hasValue {
			return true
		}
	}
	return false
}

func rejectUnreadStream(b *p4TaskBinding, flags []p4ReadFlag) *p4Rejection {
	for _, flag := range flags {
		if flag.name != "-S" || flag.value == "" {
			continue
		}
		if _, ok := b.depotRef(flag.value); !ok {
			return rejectP4(p4CodeNotInWorkspace, "stream is outside this task's authorized depots", nil, nil)
		}
	}
	return nil
}

func (d *Daemon) authorizeReadFilespecs(ctx context.Context, b *p4TaskBinding, cwd string, policy p4ReadPolicy, files []string, hasMax bool) ([]string, *p4Rejection, error) {
	if cwd == "" {
		cwd = b.ws.Dir
	}
	out := make([]string, 0, len(files))
	for _, raw := range files {
		base, rev := splitFilespecRev(raw)
		if base == "" || base == "/" || base == "." {
			return nil, rejectP4(p4CodeActionNotAllowed, "a filespec is required", []string{raw}, nil), nil
		}
		dir, suffix, wild := splitWildcard(base)
		if wild && !policy.allowWildcard {
			return nil, rejectP4(p4CodeActionNotAllowed, "this read requires individual files, not a wildcard", []string{raw}, nil), nil
		}
		if wild && policy.wildcardNeedsMax && !hasMax {
			return nil, rejectP4(p4CodeActionNotAllowed, "a wildcard read requires -m (at most 200)", []string{raw}, nil), nil
		}
		if strings.HasPrefix(base, "//") {
			check := base
			if wild {
				check = dir
			}
			if check == "" || check == "//" {
				return nil, rejectP4(p4CodeActionNotAllowed, "wildcard is too broad; name a subdirectory of an authorized depot", []string{raw}, nil), nil
			}
			if _, ok := b.depotRef(check); !ok {
				return nil, rejectP4(p4CodeNotInWorkspace, "filespec is outside this task's authorized depots", nil, nil), nil
			}
			if wild && b.filespecTooBroad(check) && !policy.allowRootWildcard {
				return nil, rejectP4(p4CodeActionNotAllowed, "wildcard covers a whole authorized depot; name a subdirectory", []string{raw}, nil), nil
			}
			out = append(out, raw)
			continue
		}
		if wild {
			anchor := dir
			if anchor == "" || anchor == "." {
				anchor = cwd
			}
			resolved, rej := b.resolveOneList(cwd, []string{anchor})
			if rej != nil {
				return nil, rej, nil
			}
			depot, _, err := d.p4Cache.MappedFile(ctx, b.ws, resolved[0])
			if err != nil {
				if strings.Contains(err.Error(), "not mapped") {
					return nil, rejectP4(p4CodeNotInWorkspace, "filespec is not mapped into this task's authorized depots", nil, nil), nil
				}
				return nil, nil, err
			}
			if _, ok := b.depotRef(depot); !ok {
				return nil, rejectP4(p4CodeNotInWorkspace, "filespec is outside this task's authorized depots", nil, nil), nil
			}
			if b.filespecTooBroad(depot) && !policy.allowRootWildcard {
				return nil, rejectP4(p4CodeActionNotAllowed, "wildcard covers a whole authorized depot; name a subdirectory", []string{raw}, nil), nil
			}
			depot = strings.TrimSuffix(depot, "/")
			if !strings.HasPrefix(suffix, "/") {
				suffix = "/" + suffix
			}
			out = append(out, depot+suffix+rev)
			continue
		}
		resolved, rej := b.resolveOneList(cwd, []string{base})
		if rej != nil {
			return nil, rej, nil
		}
		depot, local, err := d.p4Cache.MappedFile(ctx, b.ws, resolved[0])
		if err != nil {
			if strings.Contains(err.Error(), "not mapped") {
				return nil, rejectP4(p4CodeNotInWorkspace, "filespec is not mapped into this task's authorized depots", nil, nil), nil
			}
			return nil, nil, err
		}
		if _, ok := b.depotRef(depot); !ok {
			return nil, rejectP4(p4CodeNotInWorkspace, "filespec is outside this task's authorized depots", nil, nil), nil
		}
		paths, rej := b.resolveOneList(b.ws.Dir, []string{local})
		if rej != nil {
			return nil, rej, nil
		}
		if info, statErr := os.Stat(paths[0]); statErr == nil && info.IsDir() && !policy.allowWildcard {
			return nil, rejectP4(p4CodeActionNotAllowed, "this read requires individual files, not a directory", []string{raw}, nil), nil
		}
		out = append(out, paths[0]+rev)
	}
	return out, nil, nil
}

func splitFilespecRev(spec string) (string, string) {
	if i := strings.IndexAny(spec, "#@"); i > 1 {
		return spec[:i], spec[i:]
	}
	return spec, ""
}

func splitWildcard(base string) (string, string, bool) {
	i := strings.Index(base, "...")
	if j := strings.IndexAny(base, "*?"); j >= 0 && (i < 0 || j < i) {
		i = j
	}
	if i < 0 {
		return base, "", false
	}
	slash := strings.LastIndex(base[:i], "/")
	if slash < 0 {
		return "", base[i:], true
	}
	return base[:slash], base[slash:], true
}

func (b *p4TaskBinding) filespecTooBroad(depotPath string) bool {
	depotPath = strings.TrimSuffix(strings.TrimSpace(depotPath), "/")
	for _, scope := range b.authorizedScopes() {
		if depotPath == scope {
			return true
		}
	}
	return false
}

func (b *p4TaskBinding) authorizedScopes() []string {
	refs := b.depots
	if len(refs) == 0 {
		refs = append(refs, b.ref)
	}
	var out []string
	seen := map[string]bool{}
	for _, ref := range refs {
		scope := strings.TrimSuffix(depotScopePath(ref), "/")
		if scope == "" || seen[scope] {
			continue
		}
		seen[scope] = true
		out = append(out, scope)
	}
	return out
}

func (b *p4TaskBinding) changesScopeFilespecs() []string {
	scopes := b.authorizedScopes()
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, scope+"/...")
	}
	return out
}

func rebuildReadArgs(flags []p4ReadFlag, files []string) []string {
	args := make([]string, 0, len(flags)*2+len(files))
	for _, flag := range flags {
		args = append(args, flag.name)
		if flag.hasValue {
			args = append(args, flag.value)
		}
	}
	return append(args, files...)
}

func rejectForeignReadOutput(b *p4TaskBinding, policy p4ReadPolicy, stdout string) *p4Rejection {
	for _, path := range readOutputDepotPaths(policy, stdout) {
		if _, ok := b.depotRef(path); !ok {
			return rejectP4(p4CodeNotInWorkspace, "result includes files outside this task's authorized depots", nil, nil)
		}
	}
	return nil
}

func readOutputDepotPaths(policy p4ReadPolicy, stdout string) []string {
	if !policy.scanDepotFile && !policy.scanListing && !policy.scanListingAll {
		return nil
	}
	var out []string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		// ztag and plain fstat both use these fields. clientFile and otherOpen
		// stay in the forwarded text; they are not depot authorization.
		fields := []string{"depotFile"}
		if policy.scanDepotFile {
			fields = append(fields, "movedFile", "resolveFromFile", "resolveBaseFile")
		}
		matched := false
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "... "); ok {
			name, value, hasValue := strings.Cut(rest, " ")
			for _, field := range fields {
				if hasValue && matchFstatDepotField(name, field) {
					if path := depotPathOnly(strings.TrimSpace(value)); path != "" {
						out = append(out, path)
						matched = true
					}
				}
			}
		}
		if matched {
			continue
		}
		if policy.scanListingAll {
			for _, field := range strings.Fields(line) {
				if strings.HasPrefix(field, "//") {
					out = append(out, depotPathOnly(field))
				}
			}
			continue
		}
		if policy.scanListing {
			if path := listingDepotPath(line); path != "" {
				out = append(out, path)
			}
		}
	}
	return out
}

func matchFstatDepotField(name, field string) bool {
	if name == field {
		return true
	}
	if field != "resolveFromFile" && field != "resolveBaseFile" {
		return false
	}
	suffix, ok := strings.CutPrefix(name, field)
	if !ok || suffix == "" {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func listingDepotPath(line string) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "//") {
		return ""
	}
	field, _, _ := strings.Cut(line, " ")
	return depotPathOnly(field)
}

func depotPathOnly(path string) string {
	if i := strings.IndexAny(path, "#@"); i > 1 {
		path = path[:i]
	}
	return strings.TrimSpace(path)
}
