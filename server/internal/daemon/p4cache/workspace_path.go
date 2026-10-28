package p4cache

import (
	"fmt"
	"path/filepath"

	"github.com/multica-ai/multica/server/internal/util"
)

// PathWithin compares filesystem locations, including Windows junctions and
// missing descendants of existing directories. Sidecar roots may themselves
// be missing. Callers must reject paths when their location is unresolvable.
func PathWithin(root, path string) (bool, error) {
	if root == "" || path == "" {
		return false, fmt.Errorf("both containment paths are required")
	}
	realRoot, err := util.ResolveSymlinksBestEffort(root)
	if err != nil {
		return false, err
	}
	realPath, err := util.ResolveSymlinksBestEffort(path)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil {
		return false, nil // Different volumes cannot contain one another.
	}
	return rel == "." || filepath.IsLocal(rel), nil
}

// ResolveLocalPath gives P4 a spelling under its client root for the location
// we checked. Passing the original alias or link/.. to P4 could let its own
// lexical normalization select a different file from the filesystem check.
func (ws *Workspace) ResolveLocalPath(path string) (string, error) {
	realRoot, err := util.ResolveSymlinks(ws.Root)
	if err != nil {
		return "", err
	}
	realPath, err := util.ResolveSymlinksBestEffort(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil {
		return "", err
	}
	if rel != "." && !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%s is outside the Perforce client root %s", path, ws.Root)
	}
	return filepath.Join(ws.Root, rel), nil
}
