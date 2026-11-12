package p4cache

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/p4depot"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const p4ContentLimit = 512 * 1024

type contentBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *contentBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.buffer.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

func (b *contentBuffer) String() string { return b.buffer.String() }

// contentOutput bounds output and always uses P4's built-in diff. A host's
// configured interactive diff program must not be launched by the viewer.
func (c *Cache) contentOutput(ctx context.Context, dir string, ref p4depot.Ref, user, client string, limit int, args ...string) (string, bool, error) {
	cmd, cancel, err := c.commandAt(ctx, dir, append(c.baseArgs(ref, user, client), args...)...)
	if err != nil {
		return "", false, err
	}
	defer cancel()
	env := cmd.Env[:0]
	for _, entry := range cmd.Env {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, "P4DIFF") && !strings.EqualFold(key, "P4DIFFUNICODE") {
			env = append(env, entry)
		}
	}
	cmd.Env = append(env, "P4DIFF=", "P4DIFFUNICODE=")
	stdout := &contentBuffer{limit: limit}
	stderr := &contentBuffer{limit: 8192}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return stdout.String(), stdout.truncated, fmt.Errorf("%s", message)
		}
		return stdout.String(), stdout.truncated, fmt.Errorf("p4 %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), stdout.truncated, nil
}

// ReadChangelistContent lists the exact snapshot being reviewed. A shelf takes
// precedence over open files, which may have changed since the last shelve.
// Only a path in that snapshot can be requested as a diff.
func (c *Cache) ReadChangelistContent(ctx context.Context, ref p4depot.Ref, number int, file string, allowWorkspace bool) (*protocol.P4ChangelistContent, error) {
	if number <= 0 {
		return nil, fmt.Errorf("a positive changelist number is required")
	}
	user, err := c.resolveUser(ctx, ref)
	if err != nil {
		return nil, err
	}
	num := strconv.Itoa(number)
	metadata, truncated, err := c.contentOutput(ctx, "", ref, user, "", 2*p4ContentLimit, "-ztag", "describe", "-s", num)
	if err != nil && (changeIsUnknown(metadata) || changeIsUnknown(err.Error())) {
		metadata, truncated, err = c.contentOutput(ctx, "", ref, user, "", 2*p4ContentLimit, "-ztag", "describe", "-s", "-O", num)
	}
	if err != nil {
		return nil, err
	}
	record := firstZtagRecord(metadata)
	if record["change"] == "" || truncated {
		return nil, fmt.Errorf("changelist details are missing or too large to read")
	}
	num = record["change"]
	client := record["client"]
	result := &protocol.P4ChangelistContent{Source: "submitted", Files: []protocol.P4ChangelistFile{}}
	if !strings.EqualFold(record["status"], "submitted") {
		result.Source = "workspace"
		shelf, cut, shelfErr := c.contentOutput(ctx, "", ref, user, "", 2*p4ContentLimit, "-ztag", "describe", "-s", "-S", num)
		if shelfErr != nil && !noShelvedFiles.MatchString(shelfErr.Error()) {
			return nil, shelfErr
		}
		if cut {
			return nil, fmt.Errorf("shelved file list is too large to read")
		}
		if shelfRecord := firstZtagRecord(shelf); shelfErr == nil && shelfRecord["depotFile0"] != "" {
			record, result.Source = shelfRecord, "shelf"
		}
	}
	for i := 0; ; i++ {
		suffix := strconv.Itoa(i)
		path := record["depotFile"+suffix]
		if path == "" {
			break
		}
		rev, _ := strconv.Atoi(record["rev"+suffix])
		result.Files = append(result.Files, protocol.P4ChangelistFile{
			DepotPath: path, Action: record["action"+suffix], Type: record["type"+suffix], Revision: rev,
		})
	}
	if file == "" {
		return result, nil
	}
	var selected *protocol.P4ChangelistFile
	for i := range result.Files {
		if result.Files[i].DepotPath == file {
			selected = &result.Files[i]
			break
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("file is not part of this changelist snapshot")
	}
	result.File = file
	baseType, _, _ := strings.Cut(selected.Type, "+")
	switch baseType {
	case "text", "unicode", "utf8", "utf16":
	default:
		result.Binary = true
		return result, nil
	}
	baseRev := selected.Revision
	if result.Source == "submitted" {
		baseRev--
	}
	added := selected.Action == "add" || selected.Action == "move/add" || selected.Action == "branch"
	deleted := selected.Action == "delete" || selected.Action == "move/delete"
	var body string
	var cut bool
	if result.Source == "workspace" && !deleted {
		if !allowWorkspace {
			result.Unavailable = "workspace_unavailable"
			return result, nil
		}
		root, localPath, pathErr := c.contentWorkspacePath(ctx, ref, user, client, file)
		if pathErr != nil {
			result.Unavailable = "workspace_unavailable"
			return result, nil
		}
		if added {
			f, openErr := os.Open(localPath)
			if openErr != nil {
				return nil, openErr
			}
			defer f.Close()
			data, readErr := io.ReadAll(io.LimitReader(f, p4ContentLimit+1))
			if readErr != nil {
				return nil, readErr
			}
			cut = len(data) > p4ContentLimit
			if cut {
				data = data[:p4ContentLimit]
			}
			body = string(data)
		} else {
			body, cut, err = c.contentOutput(ctx, root, ref, user, client, p4ContentLimit, "diff", "-du", "-f", file+"#"+strconv.Itoa(baseRev))
		}
	} else if added || deleted {
		spec := file + "@=" + num
		if deleted {
			spec = file + "#" + strconv.Itoa(baseRev)
		}
		body, cut, err = c.contentOutput(ctx, "", ref, user, "", p4ContentLimit, "print", "-q", spec)
	} else {
		body, cut, err = c.contentOutput(ctx, "", ref, user, "", p4ContentLimit, "diff2", "-du", file+"#"+strconv.Itoa(baseRev), file+"@="+num)
	}
	if err != nil {
		return nil, err
	}
	result.Truncated = cut
	if lines := strings.SplitAfter(body, "\n"); len(lines) > 6000 && lines[6000] != "" {
		body, cut, result.Truncated = strings.Join(lines[:6000], ""), true, true
	}
	if cut {
		// A clipped final line is not a complete line of the diff.
		if end := strings.LastIndex(body, "\n"); end >= 0 {
			body = body[:end+1]
		}
	}
	if !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
		result.Unavailable = "encoding"
		return result, nil
	}
	if added || deleted {
		body = wholeFileDiff(body, deleted)
	}
	result.Diff = body
	return result, nil
}

func wholeFileDiff(body string, deleted bool) string {
	if body == "" {
		return ""
	}
	lines := strings.Split(strings.TrimSuffix(strings.ReplaceAll(body, "\r\n", "\n"), "\n"), "\n")
	prefix := "+"
	header := fmt.Sprintf("@@ -0,0 +1,%d @@\n", len(lines))
	if deleted {
		prefix = "-"
		header = fmt.Sprintf("@@ -1,%d +0,0 @@\n", len(lines))
	}
	diff := header + prefix + strings.Join(lines, "\n"+prefix) + "\n"
	if !strings.HasSuffix(body, "\n") {
		diff += "\\ No newline at end of file\n"
	}
	return diff
}

func (c *Cache) contentWorkspacePath(ctx context.Context, ref p4depot.Ref, user, client, file string) (string, string, error) {
	if client == "" {
		return "", "", fmt.Errorf("changelist has no client")
	}
	base := append([]string{"-ztag"}, c.baseArgs(ref, user, client)...)
	spec, err := c.ztagRecord(ctx, "", base, "client", "-o", client)
	if err != nil {
		return "", "", err
	}
	ws := &Workspace{User: user, Client: client, Root: spec["Root"], Dir: spec["Root"], Host: spec["Host"]}
	if err := ws.checkOwnership(spec["Owner"]); err != nil {
		return "", "", err
	}
	where, err := c.ztagRecord(ctx, ws.Root, base, "where", file)
	if err != nil || where["depotFile"] != file {
		return "", "", fmt.Errorf("file is not mapped in this client")
	}
	path, err := util.ResolveSymlinks(where["path"])
	if err != nil {
		return "", "", err
	}
	inside, err := PathWithin(ws.Root, path)
	if err != nil || !inside {
		return "", "", fmt.Errorf("file is outside the client root")
	}
	return ws.Root, path, nil
}
