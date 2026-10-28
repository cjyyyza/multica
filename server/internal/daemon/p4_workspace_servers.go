package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

// The local endpoint comes from the human-attached directory's own client.
// Cluster equality only resolves its spelling; depot scope and action grants
// still come from the task's current configured resources.
func (d *Daemon) matchP4Workspace(ctx context.Context, depots []P4DepotData, ws *p4cache.Workspace) (p4depot.Ref, []p4depot.Ref, error) {
	var accepted []p4depot.Ref
	var best p4depot.Ref
	bestLen := -1
	bestStream := false
	clusters := map[string]string{}
	for _, data := range depots {
		ref, err := p4depot.Normalize(p4depot.Ref(data))
		if err != nil {
			continue
		}
		matches := p4depot.SameEndpoint(ref.Port, ws.Port)
		if !matches && ws.ServerCluster != "" && p4depot.SameTransport(ref.Port, ws.Port) {
			key, seen := clusters[ref.Port]
			if !seen {
				key, err = d.p4Cache.ServerKey(ctx, ref)
				if err != nil {
					key = ""
				}
				clusters[ref.Port] = key
			}
			matches = key != "" && key == ws.ServerCluster && !strings.EqualFold(key, ref.Port)
			if !matches && d.registeredP4Edge(ctx, ref, ws, key) {
				matches = true
				ws.ServerCluster = key
			}
		}
		if !matches {
			continue
		}
		mapped, streamMatch := depotMatchesWorkspace(ref, ws)
		if !mapped {
			continue
		}
		accepted = append(accepted, ref)
		if (streamMatch && !bestStream) || (streamMatch == bestStream && len(depotScopePath(ref)) > bestLen) {
			best, bestLen = ref, len(depotScopePath(ref))
			bestStream = streamMatch
		}
	}
	if bestLen < 0 {
		return p4depot.Ref{}, nil, fmt.Errorf("Perforce client %s does not map an authorized task depot on a verified server", ws.Client)
	}
	return best, accepted, nil
}

// Binding and polling must prove the same registered edge relationship. The
// upstream must already be configured; the edge's replica field grants nothing.
func (d *Daemon) registeredP4Edge(ctx context.Context, upstream p4depot.Ref, edge *p4cache.Workspace, key string) bool {
	if !edge.Edge || edge.ServerID == "" || !p4depot.ClusterKey(key, upstream.Port) ||
		!p4depot.SameTransport(edge.Port, upstream.Port) || !p4depot.SameEndpoint(edge.Replica, upstream.Port) {
		return false
	}
	registered, err := d.p4Cache.ServerSpec(ctx, upstream, edge.ServerID)
	return err == nil && registered["ServerID"] == edge.ServerID && registered["Services"] == "edge-server" &&
		p4depot.SameEndpoint(registered["Address"], edge.Port)
}

func (b *p4TaskBinding) depotRef(path string) (p4depot.Ref, bool) {
	refs := b.depots
	if len(refs) == 0 {
		refs = []p4depot.Ref{b.ref}
	}
	var best p4depot.Ref
	bestLen := -1
	for _, ref := range refs {
		if depotCovers(ref, path) && len(depotScopePath(ref)) > bestLen {
			best, bestLen = ref, len(depotScopePath(ref))
		}
	}
	return best, bestLen >= 0
}

func (b *p4TaskBinding) requestRef(port, depot, stream string) (p4depot.Ref, bool) {
	refs := append([]p4depot.Ref{b.ref}, b.depots...)
	for _, ref := range refs {
		if depot != "" && depot != ref.Depot && depot != ref.Stream && !(ref == b.ref && depot == b.ws.Stream) {
			continue
		}
		if port != "" && !p4depot.SameEndpoint(port, ref.Port) && !p4depot.SameEndpoint(port, b.ws.Port) {
			continue
		}
		if stream != "" && stream != ref.Stream && stream != b.ws.Stream {
			continue
		}
		return ref, true
	}
	return p4depot.Ref{}, false
}

func (b *p4TaskBinding) rejectDepotFiles(action string, files []p4cache.FileState) *p4Rejection {
	for _, file := range files {
		ref, ok := b.depotRef(file.DepotFile)
		if !ok {
			return rejectP4(p4CodeNotInWorkspace, "file is outside this task's authorized depots", []string{file.DepotFile}, nil)
		}
		if (action == "submit" && !ref.AllowSubmit) || (action == "unshelve" && !ref.AllowUnshelve) {
			return rejectP4(p4CodeActionNotAllowed, "this file's configured depot does not allow "+action, []string{file.DepotFile}, nil)
		}
	}
	return nil
}

func (d *Daemon) validateP4CloseoutFiles(ctx context.Context, b *p4TaskBinding, files []string) error {
	for _, file := range files {
		local, rejection, err := d.workspaceLocalPath(ctx, b, file)
		if err != nil {
			return err
		}
		if rejection != nil {
			return fmt.Errorf("%s: %s", rejection.Code, rejection.Message)
		}
		if b.isSidecar(local) {
			return fmt.Errorf("refuse to shelve a Multica private path: %s", file)
		}
	}
	return nil
}
