package p4cache

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/internal/p4depot"
)

// Workspace is a human-owned Helix client resolved from a local directory.
// Every command for it runs with cwd=Dir and spells out -p -u -c -C, because
// the global P4CLIENT on a developer machine routinely points at a client
// bound to a different host.
type Workspace struct {
	Port, User, Client, Charset string
	Root, Stream                string
	Edge                        bool // p4 info serverServices == edge-server

	// Dir is the local directory the task runs in. It resolves P4CONFIG and
	// P4IGNORE and makes relative file arguments mean what the agent meant.
	Dir string
	// Host is the client spec's Host restriction, empty when unrestricted.
	Host string
	// ServerCluster identifies the Helix installation across the commit server
	// and its edges. Empty on servers that do not set one.
	ServerCluster string
	// View is the classic client mappings ("//depot/... //client/..."), empty
	// for a stream client. DetectWorkspace uses it to see which depots this
	// workspace actually covers.
	View []string
}

// FileState is what Perforce knows about one file in a workspace: our own open
// state plus whoever else is holding it.
type FileState struct {
	DepotFile, LocalPath, HeadType string
	HaveRev, HeadRev               int
	OpenChange                     string   // "" not opened here; "default" or a number
	Action                         string   // open action in this client, when OpenChange != ""
	OtherOpens                     []string // user@client
	OtherLock                      bool
}

// Exclusive reports whether the file type carries the +l modifier, which lets
// one open block every other person until it is submitted or reverted.
func (s FileState) Exclusive() bool {
	_, modifiers, ok := strings.Cut(s.HeadType, "+")
	return ok && strings.Contains(modifiers, "l")
}

// AtHead reports whether the local revision is the latest one. A file that is
// not synced to head cannot be submitted without resolving first.
func (s FileState) AtHead() bool {
	return s.HeadRev == 0 || s.HaveRev == s.HeadRev
}

// FinishResult is the outcome of closing out a task changelist.
type FinishResult struct {
	Change                 string
	Files                  []FileState
	Deleted, Shelved       bool
	ExclusiveHeld, NotHead []string
}

// ChangeInfo is the subset of `p4 describe` the status loop needs. A submit
// renumbers a changelist, so Change and OldChange can differ.
type ChangeInfo struct {
	Change, OldChange, Status string
	Missing                   bool // the server has no such changelist (deleted)
}

const defaultChangelist = "default"

// DetectWorkspace resolves the Helix client that owns dir and enforces the
// "own machine, own client" invariant. It deliberately reads the settings
// through dir rather than the daemon's environment: P4CONFIG is how a
// developer selects the right client per directory.
func (c *Cache) DetectWorkspace(ctx context.Context, dir string) (*Workspace, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, fmt.Errorf("a local directory is required to detect a Perforce workspace")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", dir, err)
	}
	settings, err := c.p4Settings(ctx, abs)
	if err != nil {
		return nil, err
	}
	info, err := c.ztagRecord(ctx, abs, nil, "info")
	if err != nil {
		return nil, fmt.Errorf("read Perforce settings in %s (run `p4 login` and check P4CONFIG): %w", abs, err)
	}

	ws := &Workspace{
		Dir:           abs,
		Port:          firstNonBlank(settings["P4PORT"], info["serverAddress"]),
		User:          firstNonBlank(info["userName"], settings["P4USER"]),
		Client:        firstNonBlank(info["clientName"], settings["P4CLIENT"]),
		Charset:       firstNonBlank(settings["P4CHARSET"], info["charset"]),
		ServerCluster: info["serverCluster"],
		Edge:          strings.Contains(strings.ToLower(info["serverServices"]), "edge"),
	}
	if ws.Port == "" || ws.Client == "" {
		return nil, fmt.Errorf("no Perforce client is configured for %s: point P4CONFIG (e.g. p4config.txt) at the workspace client", abs)
	}
	if strings.EqualFold(ws.Client, "unknown") || info["clientRoot"] == "" {
		return nil, fmt.Errorf("Perforce client %q does not exist on %s (p4 info reported no client root)", ws.Client, ws.Port)
	}

	spec, err := c.ztagRecord(ctx, abs, ws.tagArgs(), "client", "-o")
	if err != nil {
		return nil, fmt.Errorf("read the spec of Perforce client %s: %w", ws.Client, err)
	}
	ws.Root = firstNonBlank(spec["Root"], info["clientRoot"])
	ws.Stream = spec["Stream"]
	ws.Host = spec["Host"]
	for i := 0; ; i++ {
		view, ok := spec["View"+strconv.Itoa(i)]
		if !ok {
			break
		}
		if view = strings.TrimSpace(view); view != "" {
			ws.View = append(ws.View, view)
		}
	}
	owner := firstNonBlank(spec["Owner"], spec["owner"])

	if err := ws.checkOwnership(owner); err != nil {
		return nil, err
	}
	return ws, nil
}

// checkOwnership is invariant I8: the daemon only ever drives a client that is
// bound to this machine, owned by the logged-in user, and rooted above the
// directory the task runs in.
func (ws *Workspace) checkOwnership(owner string) error {
	if host := strings.TrimSpace(ws.Host); host != "" {
		hostname, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("Perforce client %s is restricted to host %q and this machine's hostname is unavailable: %w", ws.Client, host, err)
		}
		if !sameHost(host, hostname) {
			return fmt.Errorf("Perforce client %s is restricted to host %q, but this machine is %q: the workspace belongs to another computer", ws.Client, host, hostname)
		}
	}
	if owner != "" && !strings.EqualFold(owner, ws.User) {
		return fmt.Errorf("Perforce client %s is owned by %q, not by the logged-in user %q", ws.Client, owner, ws.User)
	}
	if ws.Root == "" {
		return fmt.Errorf("Perforce client %s has no root", ws.Client)
	}
	if !pathWithin(ws.Root, ws.Dir) {
		return fmt.Errorf("%s is outside the root %s of Perforce client %s: point the local directory resource at the workspace root", ws.Dir, ws.Root, ws.Client)
	}
	return nil
}

// FileStates runs fstat on exactly the files it was given. The whole point is
// never to walk a client that holds two million files.
func (c *Cache) FileStates(ctx context.Context, ws *Workspace, files []string) ([]FileState, error) {
	if ws == nil {
		return nil, fmt.Errorf("perforce workspace is required")
	}
	cleaned := compactStrings(files)
	if len(cleaned) == 0 {
		return nil, nil
	}
	args := append(ws.tagArgs(), "fstat")
	stdout, stderr, err := c.runAt(ctx, ws.Dir, append(args, cleaned...))
	records := parseZtag(stdout)
	if err != nil && len(records) == 0 {
		// A file the agent is about to `add` has no depot record yet, and that
		// is the normal case rather than a failure.
		if onlyMissingFiles(stderr) {
			return nil, nil
		}
		return nil, fmt.Errorf("p4 fstat: %w: %s", err, strings.TrimSpace(stderr))
	}
	states := make([]FileState, 0, len(records))
	for _, record := range records {
		if record["depotFile"] == "" {
			continue
		}
		states = append(states, fileStateFromZtag(record))
	}
	return states, nil
}

// LocalPath resolves a depot filespec even before add creates a depot record.
// Refuse ambiguous/negative mappings instead of bypassing the local guards.
func (c *Cache) LocalPath(ctx context.Context, ws *Workspace, file string) (string, error) {
	stdout, stderr, err := c.runAt(ctx, ws.Dir, append(ws.tagArgs(), "where", file))
	if err != nil {
		return "", fmt.Errorf("p4 where %s: %w: %s", file, err, strings.TrimSpace(stderr))
	}
	records := parseZtag(stdout)
	if len(records) != 1 || records[0]["path"] == "" {
		return "", fmt.Errorf("p4 where %s did not return one local file", file)
	}
	if _, excluded := records[0]["unmap"]; excluded || !filepath.IsAbs(records[0]["path"]) {
		return "", fmt.Errorf("%s is not mapped into this Perforce client", file)
	}
	return records[0]["path"], nil
}

// Opened lists every file this client has opened, in every changelist.
func (c *Cache) Opened(ctx context.Context, ws *Workspace) ([]FileState, error) {
	return c.opened(ctx, ws, nil)
}

// OpenedInChange lists the files opened in one changelist of this client.
// change may be "default".
func (c *Cache) OpenedInChange(ctx context.Context, ws *Workspace, change string) ([]FileState, error) {
	change = strings.TrimSpace(change)
	if change == "" {
		return nil, fmt.Errorf("changelist is required")
	}
	return c.opened(ctx, ws, []string{"-c", change})
}

func (c *Cache) opened(ctx context.Context, ws *Workspace, extra []string) ([]FileState, error) {
	if ws == nil {
		return nil, fmt.Errorf("perforce workspace is required")
	}
	args := append(ws.tagArgs(), "opened")
	args = append(args, extra...)
	stdout, stderr, err := c.runAt(ctx, ws.Dir, args)
	records := parseZtag(stdout)
	if err != nil && len(records) == 0 {
		if onlyMissingFiles(stderr) {
			return nil, nil
		}
		return nil, fmt.Errorf("p4 opened: %w: %s", err, strings.TrimSpace(stderr))
	}
	states := make([]FileState, 0, len(records))
	for _, record := range records {
		if record["depotFile"] == "" {
			continue
		}
		states = append(states, fileStateFromZtag(record))
	}
	return states, nil
}

// CreateTaskChange opens a pending numbered changelist owned by this client.
func (c *Cache) CreateTaskChange(ctx context.Context, ws *Workspace, desc string) (string, error) {
	if ws == nil {
		return "", fmt.Errorf("perforce workspace is required")
	}
	if strings.TrimSpace(desc) == "" {
		return "", fmt.Errorf("a changelist description is required")
	}
	out, err := c.inputAt(ctx, ws.Dir, append(ws.baseArgs(), "change", "-i"), buildChangeSpec(ws.Client, ws.User, desc))
	if err != nil {
		return "", err
	}
	match := changeCreated.FindStringSubmatch(out)
	if len(match) != 2 {
		return "", fmt.Errorf("p4 change -i did not report a changelist number: %s", strings.TrimSpace(out))
	}
	return match[1], nil
}

// FindTaskChange returns the pending changelist on this client whose
// description carries the Multica trailer for issueKey, so a second run on the
// same issue continues in the changelist the person already sees in P4V.
func (c *Cache) FindTaskChange(ctx context.Context, ws *Workspace, issueKey string) (string, error) {
	if ws == nil {
		return "", fmt.Errorf("perforce workspace is required")
	}
	if strings.TrimSpace(issueKey) == "" {
		return "", nil
	}
	args := append(ws.baseArgs(), "changes", "-s", "pending", "-c", ws.Client, "-l")
	stdout, stderr, err := c.runAt(ctx, ws.Dir, args)
	if err != nil {
		return "", fmt.Errorf("p4 changes -s pending: %w: %s", err, strings.TrimSpace(stderr))
	}
	for _, candidate := range parsePendingChanges(stdout) {
		if p4depot.DescriptionHasTaskTrailer(candidate.description, issueKey) {
			return candidate.number, nil
		}
	}
	return "", nil
}

// SetChangeDescription rewrites the description of a pending changelist and
// leaves its file list untouched.
func (c *Cache) SetChangeDescription(ctx context.Context, ws *Workspace, change, desc string) error {
	if ws == nil {
		return fmt.Errorf("perforce workspace is required")
	}
	change = strings.TrimSpace(change)
	if change == "" || strings.TrimSpace(desc) == "" {
		return fmt.Errorf("a changelist number and description are required")
	}
	stdout, stderr, err := c.runAt(ctx, ws.Dir, append(ws.baseArgs(), "change", "-o", change))
	if err != nil {
		return fmt.Errorf("p4 change -o %s: %w: %s", change, err, strings.TrimSpace(stderr))
	}
	if _, err := c.inputAt(ctx, ws.Dir, append(ws.baseArgs(), "change", "-i"), replaceSpecDescription(stdout, desc)); err != nil {
		return err
	}
	return nil
}

// RunInChange executes one allowlisted verb against the task changelist. The
// -c is not optional: without it p4 would fall back to the default changelist,
// which on a human's client holds that person's own work.
func (c *Cache) RunInChange(ctx context.Context, ws *Workspace, change, verb string, files []string, flags ...string) (string, error) {
	if ws == nil {
		return "", fmt.Errorf("perforce workspace is required")
	}
	change = strings.TrimSpace(change)
	if change == "" {
		return "", fmt.Errorf("a changelist number is required to run p4 %s in workspace mode", verb)
	}
	args := append(ws.baseArgs(), verb)
	args = append(args, flags...)
	args = append(args, "-c", change)
	args = append(args, compactStrings(files)...)
	stdout, stderr, err := c.runAt(ctx, ws.Dir, args)
	if err != nil {
		return strings.TrimSpace(stdout), fmt.Errorf("p4 %s: %w: %s", verb, err, strings.TrimSpace(stderr))
	}
	return strings.TrimSpace(stdout), nil
}

// FinishChange closes out a task changelist: revert what was opened but never
// changed, delete the changelist if nothing is left, otherwise shelve it for
// review. Changed files stay open — the person submits them from UE.
func (c *Cache) FinishChange(ctx context.Context, ws *Workspace, change string) (*FinishResult, error) {
	if ws == nil {
		return nil, fmt.Errorf("perforce workspace is required")
	}
	change = strings.TrimSpace(change)
	if change == "" {
		return nil, fmt.Errorf("a changelist number is required")
	}
	result := &FinishResult{Change: change}
	// -a is the whole point: an unchanged open still holds a +l lock and still
	// shows up in the person's P4V, so it must not survive the task. The path
	// is the client's own //<client>/... view: the `-c change` already limits
	// it to the task changelist, and a bare //... is not reliably passed
	// through the Windows shells this daemon runs under.
	if _, err := c.RunInChange(ctx, ws, change, "revert", []string{"//" + ws.Client + "/..."}, "-a"); err != nil {
		// A changelist the person submitted or deleted mid-task is reported,
		// not silently recreated.
		return nil, err
	}
	opened, err := c.OpenedInChange(ctx, ws, change)
	if err != nil {
		return nil, err
	}
	if len(opened) == 0 {
		// A previous run may have shelved files the person has since reverted.
		// A changelist cannot be deleted until that shelf is removed as well.
		if _, stderr, err := c.runAt(ctx, ws.Dir, append(ws.baseArgs(), "shelve", "-d", "-c", change)); err != nil && !noShelvedFiles.MatchString(strings.TrimSpace(stderr)) {
			return result, fmt.Errorf("p4 shelve -d %s: %w: %s", change, err, strings.TrimSpace(stderr))
		}
		if _, _, err := c.runAt(ctx, ws.Dir, append(ws.baseArgs(), "change", "-d", change)); err != nil {
			return nil, fmt.Errorf("p4 change -d %s: %w", change, err)
		}
		result.Deleted = true
		return result, nil
	}

	paths := make([]string, 0, len(opened))
	for _, file := range opened {
		paths = append(paths, file.DepotFile)
	}
	states, err := c.FileStates(ctx, ws, paths)
	if err != nil {
		// Diagnostics must not cost the shelf; report what opened already told us.
		c.logger().Warn("p4 fstat for changelist report failed", "change", change, "error", err)
		states = opened
	}
	result.Files = states
	for _, file := range states {
		if file.Exclusive() {
			result.ExclusiveHeld = append(result.ExclusiveHeld, file.DepotFile)
		}
		if !file.AtHead() {
			result.NotHead = append(result.NotHead, file.DepotFile)
		}
	}

	flags := []string{"-r"}
	if ws.Edge {
		// Swarm only reads shelves that reached the commit server.
		flags = append(flags, "-p")
	}
	if _, err := c.RunInChange(ctx, ws, change, "shelve", nil, flags...); err != nil {
		return result, err
	}
	result.Shelved = true
	return result, nil
}

var noShelvedFiles = regexp.MustCompile(`(?i)^(no shelved files in changelist to delete\.?|change \d+ has no shelved files\.?)$`)

// DescribeChange accepts either number a person can copy from P4V. A current
// number resolves directly; -O resolves the original number after submission.
func (c *Cache) DescribeChange(ctx context.Context, ref p4depot.Ref, number string) (*ChangeInfo, error) {
	number = strings.TrimSpace(number)
	if number == "" {
		return nil, fmt.Errorf("a changelist number is required")
	}
	user, err := c.resolveUser(ctx, ref)
	if err != nil {
		return nil, err
	}
	args := append([]string{"-ztag"}, c.baseArgs(ref, user, "")...)
	args = append(args, "describe", "-s", number)
	stdout, stderr, err := c.runAt(ctx, "", args)
	if changeIsUnknown(stderr) || changeIsUnknown(stdout) {
		args = append(args[:len(args)-1], "-O", number)
		stdout, stderr, err = c.runAt(ctx, "", args)
	}
	if err != nil {
		if changeIsUnknown(stderr) || changeIsUnknown(stdout) {
			return &ChangeInfo{Change: number, OldChange: number, Missing: true}, nil
		}
		return nil, fmt.Errorf("p4 describe -O %s: %w: %s", number, err, strings.TrimSpace(stderr))
	}
	record := firstZtagRecord(stdout)
	if changeIsUnknown(stdout) || record["change"] == "" {
		return &ChangeInfo{Change: number, OldChange: number, Missing: true}, nil
	}
	info := &ChangeInfo{
		Change:    record["change"],
		OldChange: firstNonBlank(record["oldChange"], number),
		Status:    record["status"],
	}
	return info, nil
}

// ServerKey resolves the shared cluster identity for a manually linked CL.
// An unsuccessful info is not evidence that the server has no cluster.
func (c *Cache) ServerKey(ctx context.Context, ref p4depot.Ref) (string, error) {
	user, err := c.resolveUser(ctx, ref)
	if err != nil {
		return "", err
	}
	out, err := c.output(ctx, ref, user, "", "-ztag", "info")
	if err != nil {
		return "", err
	}
	if cluster := strings.TrimSpace(firstZtagRecord(string(out))["serverCluster"]); cluster != "" {
		return cluster, nil
	}
	return strings.ToLower(strings.TrimSpace(ref.Port)), nil
}

// baseArgs spells out the connection for every command. Relying on the
// environment would reach for the global P4CLIENT, which on a developer
// machine is routinely bound to a different host.
func (ws *Workspace) baseArgs() []string {
	args := []string{"-p", ws.Port}
	if ws.User != "" {
		args = append(args, "-u", ws.User)
	}
	if ws.Client != "" {
		args = append(args, "-c", ws.Client)
	}
	if ws.Charset != "" {
		args = append(args, "-C", ws.Charset)
	}
	return args
}

// tagArgs is baseArgs for the commands whose output we parse.
func (ws *Workspace) tagArgs() []string {
	return append([]string{"-ztag"}, ws.baseArgs()...)
}

// Ref is the depot identity of this workspace's server, for the Swarm and
// ticket helpers that take one.
func (ws *Workspace) Ref() p4depot.Ref {
	return p4depot.Ref{Port: ws.Port, User: ws.User, Charset: ws.Charset}
}

// p4Settings reads `p4 set -q` in dir, which is the only place a per-directory
// P4CONFIG shows up.
func (c *Cache) p4Settings(ctx context.Context, dir string) (map[string]string, error) {
	stdout, stderr, err := c.runAt(ctx, dir, []string{"set", "-q"})
	if err != nil {
		return nil, fmt.Errorf("p4 set -q in %s: %w: %s", dir, err, strings.TrimSpace(stderr))
	}
	settings := map[string]string{}
	for _, line := range strings.Split(stdout, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		// `p4 set` annotates where a value came from: "P4CLIENT=x (config)".
		if idx := strings.LastIndex(value, " ("); idx >= 0 && strings.HasSuffix(value, ")") {
			value = value[:idx]
		}
		settings[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return settings, nil
}

func (c *Cache) ztagRecord(ctx context.Context, dir string, base []string, args ...string) (map[string]string, error) {
	full := base
	if len(full) == 0 {
		full = []string{"-ztag"}
	}
	stdout, stderr, err := c.runAt(ctx, dir, append(append([]string{}, full...), args...))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr))
	}
	return firstZtagRecord(stdout), nil
}

func (c *Cache) runAt(ctx context.Context, dir string, args []string) (string, string, error) {
	cmd, cancel, err := c.commandAt(ctx, dir, args...)
	if err != nil {
		return "", "", err
	}
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stdout.String(), stderr.String(), err
}

func (c *Cache) inputAt(ctx context.Context, dir string, args []string, spec string) (string, error) {
	cmd, cancel, err := c.commandAt(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	defer cancel()
	cmd.Stdin = strings.NewReader(spec)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("p4 %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// parseZtag splits `p4 -ztag` output into one map per record. A key with no
// value (otherLock) is kept with an empty string so callers can test presence.
//
// A blank line is not a record boundary: `p4 -ztag client -o` prints one right
// after the multi-line Description field, and treating it as the end of the
// record dropped Root, Stream and View. A record starts again only when a key
// repeats, which is how fstat and opened lay out consecutive files (each begins
// with depotFile).
func parseZtag(out string) []map[string]string {
	var records []map[string]string
	current := map[string]string{}
	flush := func() {
		if len(current) > 0 {
			records = append(records, current)
			current = map[string]string{}
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		rest, ok := strings.CutPrefix(line, "... ")
		if !ok {
			continue
		}
		// Per-client details (otherOpenN, otherLockN, ...) arrive nested as
		// "... ... key value". They belong to the record they follow, so the
		// extra marker is dropped rather than read as a key.
		for {
			nested, isNested := strings.CutPrefix(rest, "... ")
			if !isNested {
				break
			}
			rest = nested
		}
		key, value, _ := strings.Cut(rest, " ")
		if _, exists := current[key]; exists {
			// A second record started without a blank line between them.
			flush()
		}
		current[key] = strings.TrimSpace(value)
	}
	flush()
	return records
}

func firstZtagRecord(out string) map[string]string {
	records := parseZtag(out)
	if len(records) == 0 {
		return map[string]string{}
	}
	return records[0]
}

func fileStateFromZtag(record map[string]string) FileState {
	state := FileState{
		DepotFile: record["depotFile"],
		LocalPath: record["clientFile"],
		HeadType:  firstNonBlank(record["headType"], record["type"]),
		HaveRev:   atoiOrZero(record["haveRev"]),
		HeadRev:   atoiOrZero(record["headRev"]),
		Action:    record["action"],
	}
	if change := record["change"]; change != "" {
		state.OpenChange = change
	} else if state.Action != "" {
		// `p4 opened` omits change for the default changelist.
		state.OpenChange = defaultChangelist
	}
	if _, locked := record["otherLock"]; locked {
		state.OtherLock = true
	}
	for i := 0; ; i++ {
		holder, ok := record["otherOpen"+strconv.Itoa(i)]
		if !ok {
			break
		}
		state.OtherOpens = append(state.OtherOpens, holder)
	}
	return state
}

type pendingChange struct {
	number      string
	description string
}

// parsePendingChanges reads `p4 changes -l` output, where each header line is
// followed by the indented description of that changelist.
func parsePendingChanges(out string) []pendingChange {
	var changes []pendingChange
	var current *pendingChange
	var description strings.Builder
	flush := func() {
		if current != nil {
			current.description = description.String()
			changes = append(changes, *current)
		}
		current = nil
		description.Reset()
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if rest, ok := strings.CutPrefix(line, "Change "); ok {
			flush()
			number, _, _ := strings.Cut(rest, " ")
			current = &pendingChange{number: strings.TrimSpace(number)}
			continue
		}
		if current == nil {
			continue
		}
		description.WriteString(strings.TrimSpace(line))
		description.WriteString("\n")
	}
	flush()
	return changes
}

// replaceSpecDescription swaps the Description block of a change spec and
// keeps every other field, including the file list, as the server wrote it.
func replaceSpecDescription(spec, desc string) string {
	var out strings.Builder
	skipping := false
	for _, line := range strings.Split(spec, "\n") {
		line = strings.TrimRight(line, "\r")
		if skipping {
			if line == "" || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, " ") {
				continue
			}
			skipping = false
		}
		if strings.HasPrefix(line, "Description:") {
			out.WriteString("Description:\n")
			for _, descLine := range strings.Split(strings.TrimRight(desc, "\n"), "\n") {
				out.WriteString("\t")
				out.WriteString(descLine)
				out.WriteString("\n")
			}
			out.WriteString("\n")
			skipping = true
			continue
		}
		out.WriteString(line)
		out.WriteString("\n")
	}
	return out.String()
}

// onlyMissingFiles reports whether every complaint on stderr is "this path has
// no depot record", which is the expected answer for a file about to be added.
func onlyMissingFiles(stderr string) bool {
	saw := false
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "" {
			continue
		}
		saw = true
		if !strings.Contains(line, "no such file") && !strings.Contains(line, "not opened on this client") &&
			!strings.Contains(line, "file(s) not opened") {
			return false
		}
	}
	return saw
}

// changeUnknownLine matches the two answers a server gives for a changelist
// that does not exist: "Change 12345 unknown." and "12345 - no such
// changelist.". Matching a bare "unknown" would also catch "Client 'x'
// unknown" or a connection error and mark a live changelist deleted.
var changeUnknownLine = regexp.MustCompile(`(?im)^\s*(change\s+\d+\s+unknown\.?|\d+\s+-\s+no such changelist\.?)\s*$`)

func changeIsUnknown(out string) bool {
	return changeUnknownLine.MatchString(out)
}

func sameHost(a, b string) bool {
	shorten := func(s string) string {
		s = strings.TrimSpace(s)
		if host, _, ok := strings.Cut(s, "."); ok {
			return host
		}
		return s
	}
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)) || strings.EqualFold(shorten(a), shorten(b))
}

// pathWithin reports whether path sits inside root, comparing the paths the
// filesystem actually resolves so a junctioned workspace still matches.
func pathWithin(root, path string) bool {
	resolve := func(p string) string {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return resolved
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return p
		}
		return abs
	}
	rel, err := filepath.Rel(resolve(root), resolve(path))
	if err != nil {
		return false
	}
	return rel == "." || filepath.IsLocal(rel)
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func atoiOrZero(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
