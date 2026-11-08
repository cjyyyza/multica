package p4cache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/p4depot"
	"github.com/multica-ai/multica/server/internal/util"
)

var ErrWorkspaceNotConfigured = errors.New("no Perforce workspace is configured for this directory")

// Workspace is the Helix client resolved from a local directory.
type Workspace struct {
	Port, User, Client, Charset string
	Root, Stream                string
	Edge                        bool
	Dir                         string
	Host                        string
	ServerCluster               string
	ServerID                    string
	Replica                     string
	View                        []string
}

type ChangeInfo struct {
	Change, OldChange, Status string
	Client, Description       string
	FilesCount                int
	Missing                   bool
	Shelved                   bool
	SubmittedAt               time.Time
}

// DetectWorkspace resolves the client through the directory's own P4CONFIG and
// verifies that it belongs to this host, this user, and this directory.
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
		ServerCluster: serverIdentityKey(info, firstNonBlank(settings["P4PORT"], info["serverAddress"])),
		ServerID:      firstNonBlank(info["ServerID"], info["serverID"]),
		Replica:       info["replica"],
		Edge:          strings.Contains(strings.ToLower(info["serverServices"]), "edge"),
	}
	if ws.Port == "" || ws.Client == "" {
		return nil, fmt.Errorf("no Perforce client is configured for %s: point P4CONFIG (e.g. p4config.txt) at the workspace client", abs)
	}
	if strings.EqualFold(ws.Client, "unknown") {
		return nil, fmt.Errorf("%w: Perforce client %q does not exist on %s", ErrWorkspaceNotConfigured, ws.Client, ws.Port)
	}
	if info["clientRoot"] == "" {
		return nil, fmt.Errorf("Perforce client %q could not be verified on %s: p4 info reported no client root", ws.Client, ws.Port)
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
	if err := ws.checkOwnership(firstNonBlank(spec["Owner"], spec["owner"])); err != nil {
		return nil, err
	}
	return ws, nil
}

func (ws *Workspace) checkOwnership(owner string) error {
	if host := strings.TrimSpace(ws.Host); host != "" {
		hostname, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("Perforce client %s is restricted to host %q and this machine's hostname is unavailable: %w", ws.Client, host, err)
		}
		if !sameHost(host, hostname) {
			return fmt.Errorf("Perforce client %s is restricted to host %q, but this machine is %q", ws.Client, host, hostname)
		}
	}
	if owner != "" && !strings.EqualFold(owner, ws.User) {
		return fmt.Errorf("Perforce client %s is owned by %q, not by the logged-in user %q", ws.Client, owner, ws.User)
	}
	if ws.Root == "" {
		return fmt.Errorf("Perforce client %s has no root", ws.Client)
	}
	if _, err := util.ResolveSymlinks(ws.Root); err != nil {
		return fmt.Errorf("resolve Perforce client root %s: %w", ws.Root, err)
	}
	if _, err := util.ResolveSymlinks(ws.Dir); err != nil {
		return fmt.Errorf("resolve Perforce task directory %s: %w", ws.Dir, err)
	}
	inside, err := PathWithin(ws.Root, ws.Dir)
	if err != nil {
		return fmt.Errorf("verify Perforce client root: %w", err)
	}
	if !inside {
		return fmt.Errorf("%w: %s is outside the root %s of Perforce client %s", ErrWorkspaceNotConfigured, ws.Dir, ws.Root, ws.Client)
	}
	return nil
}

// DescribeChange accepts either a current number or the original number after
// submit renumbering.
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
		Change: record["change"], OldChange: firstNonBlank(record["oldChange"], number),
		Status: record["status"], Client: record["client"],
		Description: firstNonBlank(record["description"], record["desc"]),
	}
	for key := range record {
		if strings.HasPrefix(key, "depotFile") {
			info.FilesCount++
		}
	}
	if strings.EqualFold(info.Status, "submitted") {
		info.SubmittedAt, err = submissionTime(number, record["time"])
		if err != nil {
			return nil, err
		}
	}
	return info, nil
}

// DescribeTaskChange adds the client ownership and shelf details needed when
// explicitly associating a native changelist with a Multica task.
func (c *Cache) DescribeTaskChange(ctx context.Context, ref p4depot.Ref, number string) (*ChangeInfo, error) {
	info, err := c.DescribeChange(ctx, ref, number)
	if err != nil || info.Missing || strings.EqualFold(info.Status, "submitted") {
		return info, err
	}
	user, err := c.resolveUser(ctx, ref)
	if err != nil {
		return nil, err
	}
	args := append([]string{"-ztag"}, c.baseArgs(ref, user, "")...)
	args = append(args, "describe", "-s", "-S", info.Change)
	stdout, stderr, err := c.runAt(ctx, "", args)
	if err != nil {
		if noShelvedFiles.MatchString(strings.TrimSpace(stderr)) && strings.TrimSpace(stdout) == "" {
			return info, nil
		}
		return nil, fmt.Errorf("read shelf %s: %w: %s", info.Change, err, strings.TrimSpace(stderr))
	}
	shelfFiles := 0
	for key := range firstZtagRecord(stdout) {
		if strings.HasPrefix(key, "depotFile") {
			shelfFiles++
		}
	}
	info.Shelved = shelfFiles > 0
	if shelfFiles > info.FilesCount {
		info.FilesCount = shelfFiles
	}
	return info, nil
}

func submissionTime(number, raw string) (time.Time, error) {
	seconds, err := strconv.ParseInt(raw, 10, 64)
	stamp := time.Unix(seconds, 0).UTC()
	if err != nil || seconds <= 0 || stamp.Year() > 9999 {
		return time.Time{}, fmt.Errorf("p4 describe %s returned an invalid submission time %q", number, raw)
	}
	return stamp, nil
}

func (c *Cache) ServerKey(ctx context.Context, ref p4depot.Ref) (string, error) {
	record, err := c.ServerInfo(ctx, ref)
	if err != nil {
		return "", err
	}
	return serverIdentityKey(record, ref.Port), nil
}

func serverIdentityKey(record map[string]string, port string) string {
	if cluster := strings.TrimSpace(record["serverCluster"]); cluster != "" {
		return cluster
	}
	if id := firstNonBlank(record["ServerID"], record["serverID"]); id != "" && strings.TrimSpace(record["serverAddress"]) != "" {
		return "server:" + id + "@" + p4depot.EndpointKey(record["serverAddress"])
	}
	return strings.ToLower(strings.TrimSpace(port))
}

func (c *Cache) ServerInfo(ctx context.Context, ref p4depot.Ref) (map[string]string, error) {
	user, err := c.resolveUser(ctx, ref)
	if err != nil {
		return nil, err
	}
	out, err := c.output(ctx, ref, user, "", "-ztag", "info")
	if err != nil {
		return nil, err
	}
	return firstZtagRecord(string(out)), nil
}

func (c *Cache) ServerSpec(ctx context.Context, ref p4depot.Ref, id string) (map[string]string, error) {
	if id == "" || strings.HasPrefix(id, "-") || strings.ContainsAny(id, " \t\r\n") {
		return nil, fmt.Errorf("invalid Perforce server ID")
	}
	user, err := c.resolveUser(ctx, ref)
	if err != nil {
		return nil, err
	}
	out, err := c.output(ctx, ref, user, "", "-ztag", "server", "-o", id)
	if err != nil {
		return nil, err
	}
	return firstZtagRecord(string(out)), nil
}

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

func (ws *Workspace) tagArgs() []string {
	return append([]string{"-ztag"}, ws.baseArgs()...)
}

func (ws *Workspace) Ref() p4depot.Ref {
	return p4depot.Ref{Port: ws.Port, User: ws.User, Charset: ws.Charset}
}

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
		for {
			nested, isNested := strings.CutPrefix(rest, "... ")
			if !isNested {
				break
			}
			rest = nested
		}
		key, value, _ := strings.Cut(rest, " ")
		if _, exists := current[key]; exists {
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

var (
	changeUnknownLine = regexp.MustCompile(`(?im)^\s*(change\s+\d+\s+unknown\.?|\d+\s+-\s+no such changelist\.?)\s*$`)
	noShelvedFiles    = regexp.MustCompile(`(?i)^(no shelved files in changelist to delete\.?|change \d+ has no shelved files\.?)$`)
)

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

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
