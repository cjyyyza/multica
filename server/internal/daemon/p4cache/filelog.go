package p4cache

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// FilelogRevision is one revision from `p4 filelog`, including the changelist
// description an agent needs to judge precedent. JSON names match the daemon
// response.
type FilelogRevision struct {
	DepotFile   string `json:"depot_file"`
	Rev         string `json:"rev"`
	Change      string `json:"change"`
	Action      string `json:"action"`
	Type        string `json:"type,omitempty"`
	User        string `json:"user,omitempty"`
	Client      string `json:"client,omitempty"`
	Time        string `json:"time,omitempty"`
	Description string `json:"description,omitempty"`
}

// DescribedChange is the readable part of `p4 describe -s`. Files are what
// the daemon authorizes before it returns the description.
type DescribedChange struct {
	Change      string
	User        string
	Client      string
	Status      string
	Description string
	Files       []DescribedFile
}

// DescribedFile is one file listed by `p4 describe -s`.
type DescribedFile struct {
	DepotFile string
	Rev       string
	Action    string
}

var (
	filelogRevLine = regexp.MustCompile(`^\.\.\. #(\d+) change (\d+) (\S+) on (\d{4}/\d{2}/\d{2}(?: \d{2}:\d{2}:\d{2})?) by (\S+) \(([^)]*)\)(.*)$`)
	describeHeader = regexp.MustCompile(`^Change (\d+) by (\S+) on .*$`)
	describeFile   = regexp.MustCompile(`^\.\.\. (//\S+?)#(\d+) (\S+)`)
)

// Filelog runs `p4 filelog` on already-authorized local paths. max and long
// select -m and -l/-L; nothing else from the caller becomes a p4 flag.
func (c *Cache) Filelog(ctx context.Context, ws *Workspace, files []string, max int, long bool) (string, []FilelogRevision, error) {
	if ws == nil {
		return "", nil, fmt.Errorf("perforce workspace is required")
	}
	files = compactStrings(files)
	if len(files) == 0 {
		return "", nil, fmt.Errorf("filelog requires at least one file")
	}
	for _, file := range files {
		if strings.HasPrefix(file, "-") {
			return "", nil, fmt.Errorf("filelog file %q is not a path", file)
		}
	}
	if max < 1 || max > FilelogMaxMax {
		return "", nil, fmt.Errorf("filelog max must be between 1 and %d", FilelogMaxMax)
	}
	length := "-L"
	if long {
		length = "-l"
	}
	args := append(ws.baseArgs(), "filelog", "-t", length, "-m", strconv.Itoa(max))
	args = append(args, files...)
	stdout, stderr, err := c.runAt(ctx, ws.Dir, args)
	if err != nil {
		return strings.TrimSpace(stdout), nil, fmt.Errorf("p4 filelog: %w: %s", err, strings.TrimSpace(stderr))
	}
	text := strings.TrimSpace(stdout)
	return text, ParseFilelog(text), nil
}

// DescribeText runs `p4 describe -s` and, when the number is the original
// number of a submitted changelist, retries with -O. The number is validated
// so it cannot become another flag.
func (c *Cache) DescribeText(ctx context.Context, ws *Workspace, number string) (string, error) {
	if ws == nil {
		return "", fmt.Errorf("perforce workspace is required")
	}
	number = strings.TrimSpace(number)
	if !ValidChangeNumber(number) {
		return "", fmt.Errorf("a changelist number is required")
	}
	stdout, stderr, err := c.runAt(ctx, ws.Dir, append(ws.baseArgs(), "describe", "-s", number))
	if changeIsUnknown(stderr) || changeIsUnknown(stdout) {
		stdout, stderr, err = c.runAt(ctx, ws.Dir, append(ws.baseArgs(), "describe", "-s", "-O", number))
	}
	if err != nil {
		if changeIsUnknown(stderr) || changeIsUnknown(stdout) {
			return "", fmt.Errorf("changelist %s was not found", number)
		}
		return "", fmt.Errorf("p4 describe %s: %w: %s", number, err, strings.TrimSpace(stderr))
	}
	return strings.TrimSpace(stdout), nil
}

// ValidChangeNumber reports whether s is a positive changelist number and
// nothing else. A path, flag, or revision specifier must not reach p4.
func ValidChangeNumber(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 12 || strings.HasPrefix(s, "+") {
		return false
	}
	n, err := strconv.Atoi(s)
	return err == nil && n > 0
}

// ParseFilelog splits `p4 filelog -t -l/-L` text into revisions. The raw text
// remains the agent-facing output; this is the structured view.
func ParseFilelog(text string) []FilelogRevision {
	var (
		out     []FilelogRevision
		current *FilelogRevision
		long    []string
	)
	flush := func() {
		if current == nil || current.Rev == "" {
			current = nil
			long = nil
			return
		}
		if desc := strings.TrimSpace(strings.Join(long, "\n")); desc != "" {
			current.Description = desc
		}
		out = append(out, *current)
		current = nil
		long = nil
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if depot, ok := filelogDepotHeader(line); ok {
			flush()
			current = &FilelogRevision{DepotFile: depot}
			continue
		}
		match := filelogRevLine.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			if current != nil && (strings.HasPrefix(line, "\t") || strings.HasPrefix(line, " ")) {
				long = append(long, strings.TrimSpace(line))
			}
			continue
		}
		// A revision line starts a new record. The depot header may have
		// opened an empty placeholder; replace it instead of emitting a blank.
		depot := ""
		if current != nil {
			depot = current.DepotFile
			if current.Rev != "" {
				flush()
			} else {
				current = nil
				long = nil
			}
		}
		user, client, _ := strings.Cut(match[5], "@")
		rev := &FilelogRevision{
			DepotFile:   depot,
			Rev:         match[1],
			Change:      match[2],
			Action:      match[3],
			Time:        match[4],
			User:        user,
			Client:      client,
			Type:        match[6],
			Description: filelogQuoted(match[7]),
		}
		current = rev
		long = nil
	}
	flush()
	return out
}

func filelogDepotHeader(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "//") || strings.HasPrefix(line, "...") {
		return "", false
	}
	if i := strings.IndexAny(line, "#@"); i > 0 {
		line = line[:i]
	}
	return line, line != ""
}

func filelogQuoted(rest string) string {
	rest = strings.TrimSpace(rest)
	if len(rest) < 2 || !strings.HasPrefix(rest, "'") || !strings.HasSuffix(rest, "'") {
		return ""
	}
	return strings.ReplaceAll(rest[1:len(rest)-1], "''", "'")
}

// ParseDescribe reads `p4 describe -s` text. ok is false when the header is
// not a describe record, so the caller can fail closed instead of returning
// an unparsed body.
func ParseDescribe(text string) (DescribedChange, bool) {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return DescribedChange{}, false
	}
	header := strings.TrimRight(lines[0], "\r")
	match := describeHeader.FindStringSubmatch(strings.TrimSpace(header))
	if match == nil {
		return DescribedChange{}, false
	}
	user, client, _ := strings.Cut(match[2], "@")
	parsed := DescribedChange{Change: match[1], User: user, Client: client}
	if strings.Contains(header, "*pending*") {
		parsed.Status = "pending"
	}
	var desc []string
	inFiles := false
	for _, line := range lines[1:] {
		line = strings.TrimRight(line, "\r")
		if !inFiles && describeSection(line) {
			inFiles = true
		}
		if file := describeFile.FindStringSubmatch(strings.TrimSpace(line)); file != nil {
			inFiles = true
			parsed.Files = append(parsed.Files, DescribedFile{
				DepotFile: file[1],
				Rev:       file[2],
				Action:    file[3],
			})
			continue
		}
		if inFiles || strings.TrimSpace(line) == "" {
			continue
		}
		desc = append(desc, strings.TrimSpace(line))
	}
	parsed.Description = strings.TrimSpace(strings.Join(desc, "\n"))
	return parsed, true
}

func describeSection(line string) bool {
	switch strings.TrimSpace(line) {
	case "Affected files ...", "Shelved files ...", "Jobs fixed ...":
		return true
	default:
		return strings.HasPrefix(strings.TrimSpace(line), "Affected files") ||
			strings.HasPrefix(strings.TrimSpace(line), "Shelved files")
	}
}
