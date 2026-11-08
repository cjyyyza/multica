package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

// The local endpoint comes from the human-attached directory's own client.
// Cluster equality resolves its spelling against the configured resources.
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
			best, bestLen, bestStream = ref, len(depotScopePath(ref)), streamMatch
			bestStream = streamMatch
		}
	}
	if bestLen < 0 {
		return p4depot.Ref{}, nil, fmt.Errorf("Perforce client %s does not map a configured task depot on a verified server", ws.Client)
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
