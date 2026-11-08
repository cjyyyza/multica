package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

// p4TaskBinding supplies the resolved client and working directory. P4 commands
// and their arguments are owned by the installed p4 binary, not by Multica.
type p4TaskBinding struct {
	ws     *p4cache.Workspace
	ref    p4depot.Ref
	ticket string
	taskID string
}

func (b *p4TaskBinding) envSpec() *execenv.P4WorkspaceForEnv {
	return &execenv.P4WorkspaceForEnv{
		Port:   b.ws.Port,
		User:   b.ws.User,
		Client: b.ws.Client,
		Root:   b.ws.Root,
		Stream: b.ws.Stream,
		Ticket: b.ticket,
	}
}

func (d *Daemon) rememberP4Binding(b *p4TaskBinding) {
	d.p4BindingsMu.Lock()
	defer d.p4BindingsMu.Unlock()
	if d.p4Bindings == nil {
		d.p4Bindings = map[string]*p4TaskBinding{}
	}
	d.p4Bindings[b.taskID] = b
}

func (d *Daemon) forgetP4Binding(taskID string) {
	d.p4BindingsMu.Lock()
	defer d.p4BindingsMu.Unlock()
	delete(d.p4Bindings, taskID)
}

func (d *Daemon) p4Binding(taskID string) *p4TaskBinding {
	d.p4BindingsMu.Lock()
	defer d.p4BindingsMu.Unlock()
	return d.p4Bindings[taskID]
}

func (d *Daemon) bindP4Workspace(ctx context.Context, task Task, a *localDirectoryAssignment) (*p4TaskBinding, error) {
	if a == nil || a.UsesWorktree() || len(task.P4Depots) == 0 {
		return nil, nil
	}
	if d.p4Cache == nil {
		return nil, fmt.Errorf("perforce is not initialized")
	}
	stopProbeLease := func() {}
	if d.client != nil {
		stopProbeLease = d.startTaskPrepareLeaseExtender(ctx, task, d.logger)
	}
	defer stopProbeLease()
	ws, err := d.p4Cache.DetectWorkspace(ctx, a.AbsPath)
	if err != nil {
		if !projectExplicitlyTargetsP4(task) && errors.Is(err, p4cache.ErrWorkspaceNotConfigured) {
			return nil, nil
		}
		return nil, fmt.Errorf("this task has Perforce depots, but no usable workspace was detected in %s: %w", a.AbsPath, err)
	}
	ref, _, err := d.matchP4Workspace(ctx, task.P4Depots, ws)
	if err != nil {
		return nil, err
	}
	binding := &p4TaskBinding{ws: ws, ref: ref, ticket: strings.TrimSpace(task.P4Ticket), taskID: task.ID}
	d.rememberP4Binding(binding)
	return binding, nil
}

// matchTaskDepot picks the configured depot this client actually maps.
// The client stream takes priority over imports. Within that class the
// most specific mapped resource wins. An ancestor alone grants no stream access.
func matchTaskDepot(depots []P4DepotData, ws *p4cache.Workspace) (p4depot.Ref, error) {
	var best p4depot.Ref
	bestLen := -1
	bestStream := false
	for _, depot := range depots {
		ref, err := p4depot.Normalize(p4depot.Ref(depot))
		if err != nil || !p4depot.SameEndpoint(ref.Port, ws.Port) {
			continue
		}
		mapped, streamMatch := depotMatchesWorkspace(ref, ws)
		if mapped && ((streamMatch && !bestStream) || (streamMatch == bestStream && len(depotScopePath(ref)) > bestLen)) {
			best, bestLen, bestStream = ref, len(depotScopePath(ref)), streamMatch
		}
	}
	if bestLen < 0 {
		where := ws.Stream
		if where == "" {
			where = "no stream"
		}
		return p4depot.Ref{}, fmt.Errorf("Perforce client %s (%s) is not under a depot configured for this task", ws.Client, where)
	}
	return best, nil
}

func depotMatchesWorkspace(ref p4depot.Ref, ws *p4cache.Workspace) (bool, bool) {
	scope := depotScopePath(ref)
	stream := strings.TrimSuffix(strings.TrimSpace(ws.Stream), "/...")
	if stream != "" && scope == stream {
		return true, true
	}
	if stream != "" && depotCovers(ref, stream) {
		return false, false
	}
	for _, mapping := range ws.View {
		source, excluded, err := p4depot.ViewSource(mapping)
		if err != nil || excluded || !p4depot.ViewOverlaps(scope, source) {
			continue
		}
		prefix := strings.TrimRight(p4depot.ViewLiteralPrefix(source), "/")
		if stream != "" && strings.HasPrefix(prefix, scope+"/") {
			continue
		}
		return true, false
	}
	return false, false
}

func depotScopePath(ref p4depot.Ref) string {
	scope := strings.TrimSpace(ref.Depot)
	if strings.TrimSpace(ref.Stream) != "" {
		scope = strings.TrimSpace(ref.Stream)
	}
	return strings.TrimSuffix(scope, "/...")
}

func depotCovers(ref p4depot.Ref, target string) bool {
	scope := depotScopePath(ref)
	target = strings.TrimSuffix(strings.TrimSpace(target), "/...")
	if scope == "" || target == "" {
		return false
	}
	return target == scope || strings.HasPrefix(target, scope+"/")
}
