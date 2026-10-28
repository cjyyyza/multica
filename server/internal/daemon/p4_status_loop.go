package daemon

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

// p4StatusLoop reconciles changelists this daemon reported with the Helix
// server and Swarm. Submit renumbers a changelist, so each poll describes
// the original number with -O and reports the number it has now.
func (d *Daemon) p4StatusLoop(ctx context.Context) {
	interval := d.cfg.P4PollInterval
	if interval <= 0 {
		if d.logger != nil {
			d.logger.Info("p4 changelist status loop disabled")
		}
		return
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			d.pollP4ChangelistStatuses(ctx)
			timer.Reset(interval)
		}
	}
}

type p4OpenChangelist struct {
	workspaceID       string
	ID                string        `json:"id"`
	Port              string        `json:"port"`
	ServerKey         string        `json:"server_key"`
	Client            string        `json:"client"`
	DaemonID          string        `json:"daemon_id"`
	PollGeneration    int64         `json:"poll_generation"`
	P4Depots          []P4DepotData `json:"p4_depots,omitempty"`
	P4User            string        `json:"p4_user"`
	Charset           string        `json:"charset"`
	Number            int           `json:"number"`
	OriginalNumber    int           `json:"original_number"`
	Status            string        `json:"status"`
	SwarmReviewNumber int           `json:"swarm_review_number"`
	SwarmURL          string        `json:"swarm_url"`
}

type p4OpenChangelistsResponse struct {
	Changelists []p4OpenChangelist `json:"changelists"`
	// Resolved from the linked issues' current project/workspace resources,
	// never from a manual changelist's user-supplied port or review URL.
	P4Depots      []P4DepotData `json:"p4_depots"`
	LeaseRequired bool          `json:"lease_required"`
}

type p4StatusUpdate struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	Number           int    `json:"number"`
	OriginalNumber   int    `json:"original_number,omitempty"`
	ServerKey        string `json:"server_key,omitempty"`
	SubmittedAt      string `json:"submitted_at,omitempty"`
	SwarmReviewState string `json:"swarm_review_state,omitempty"`
	PollGeneration   int64  `json:"poll_generation,omitempty"`
}

type p4StatusUpdateRequest struct {
	WorkspaceID string           `json:"workspace_id"`
	Updates     []p4StatusUpdate `json:"updates"`
}

type p4StatusUpdateResponse struct {
	Updated int `json:"updated"`
}

func (d *Daemon) pollP4ChangelistStatuses(ctx context.Context) {
	if d.client == nil || d.p4Cache == nil {
		return
	}
	for _, workspaceID := range d.p4WorkspaceIDs() {
		if ctx.Err() != nil {
			return
		}
		var listed p4OpenChangelistsResponse
		err := d.client.ListOpenP4Changelists(ctx, workspaceID, &listed)
		if isNotFound(err) {
			if d.logger != nil {
				d.logger.Warn("p4 changelist poll skipped; server has no changelist route")
			}
			return
		}
		if err != nil {
			if d.logger != nil {
				d.logger.Warn("p4 changelist poll failed", "workspace_id", workspaceID, "error", err)
			}
			continue
		}
		updates := make([]p4StatusUpdate, 0, len(listed.Changelists))
		for _, cl := range listed.Changelists {
			cl.workspaceID = workspaceID
			depots := cl.P4Depots
			if depots == nil {
				depots = listed.P4Depots
			}
			cl.P4Depots = depots
			ref, allowed := d.authorizedP4PollRef(ctx, depots, cl)
			if !allowed {
				continue
			}
			if listed.LeaseRequired {
				generation, ok := d.claimP4Poll(ctx, workspaceID, cl.ID, cl.PollGeneration)
				if !ok {
					continue
				}
				cl.PollGeneration = generation
			}
			update, ok := d.observeP4Changelist(ctx, cl, ref)
			if ok {
				update.PollGeneration = cl.PollGeneration
				updates = append(updates, update)
			}
		}
		if listed.LeaseRequired {
			// A large poll batch can outlive the first row's lease. Renew with
			// CAS before reporting; a takeover invalidates its observations.
			current := updates[:0]
			for _, update := range updates {
				generation, ok := d.claimP4Poll(ctx, workspaceID, update.ID, update.PollGeneration)
				if ok {
					update.PollGeneration = generation
					current = append(current, update)
				}
			}
			updates = current
		}
		if len(updates) == 0 {
			continue
		}
		var resp p4StatusUpdateResponse
		err = d.client.ReportP4ChangelistStatus(ctx, p4StatusUpdateRequest{
			WorkspaceID: workspaceID,
			Updates:     updates,
		}, &resp)
		if isNotFound(err) {
			if d.logger != nil {
				d.logger.Warn("p4 changelist status report skipped; server has no changelist route", "workspace_id", workspaceID)
			}
			return
		}
		if err != nil && d.logger != nil {
			d.logger.Warn("p4 changelist status report failed", "workspace_id", workspaceID, "error", err)
		}
	}
}

func (d *Daemon) observeP4Changelist(ctx context.Context, cl p4OpenChangelist, ref p4depot.Ref) (p4StatusUpdate, bool) {
	key, err := d.p4Cache.ServerKey(ctx, ref)
	if err != nil {
		return p4StatusUpdate{}, false
	}
	if p4depot.ClusterKey(cl.ServerKey, cl.Port) && key != cl.ServerKey {
		if !d.registeredP4PollIdentity(ctx, ref, cl) {
			return p4StatusUpdate{}, false
		}
		key = cl.ServerKey
	}
	original := cl.OriginalNumber
	if original == 0 {
		original = cl.Number
	}
	info, err := d.p4Cache.DescribeChange(ctx, ref, strconv.Itoa(original))
	if err != nil {
		if d.logger != nil {
			d.logger.Warn("p4 describe for changelist poll failed", "changelist", original, "error", err)
		}
		return p4StatusUpdate{}, false
	}
	// Pending/shelved CLs can be edge-local. Absence or a pending result on
	// another configured endpoint is not proof of the original CL's state.
	if !p4depot.SameEndpoint(ref.Port, cl.Port) && !strings.EqualFold(info.Status, "submitted") {
		return p4StatusUpdate{}, false
	}
	update := p4StatusUpdate{ID: cl.ID, Number: cl.Number, Status: cl.Status}
	if !info.Missing {
		update.ServerKey = key
		if n, err := strconv.Atoi(info.OldChange); err == nil && n > 0 {
			update.OriginalNumber = n
		}
	}
	switch {
	case info.Missing:
		update.Status = "deleted"
	case strings.EqualFold(info.Status, "submitted"):
		update.Status = "submitted"
		if n, convErr := strconv.Atoi(info.Change); convErr == nil && n > 0 {
			update.Number = n
		}
		update.SubmittedAt = p4SubmittedAt(info.SubmittedAt)
	default:
		if cl.Status == "shelved" {
			update.Status = "shelved"
		} else {
			update.Status = "pending"
		}
		if n, convErr := strconv.Atoi(info.Change); convErr == nil && n > 0 {
			update.Number = n
		}
	}
	if state := d.swarmReviewState(ctx, cl, ref); state != "" {
		update.SwarmReviewState = state
	}
	return update, true
}

// A registered edge can have its own fallback identity while task reports use
// the verified commit identity. Revalidate that relationship before trusting
// observations from the original edge, including edge-local missing changes.
func (d *Daemon) registeredP4PollIdentity(ctx context.Context, ref p4depot.Ref, cl p4OpenChangelist) bool {
	if cl.Client == "" || cl.DaemonID == "" || !p4depot.SameEndpoint(ref.Port, cl.Port) {
		return false
	}
	info, err := d.p4Cache.ServerInfo(ctx, ref)
	if err != nil {
		return false
	}
	edge := &p4cache.Workspace{
		Port: ref.Port, ServerID: firstNonEmpty(info["ServerID"], info["serverID"]),
		Replica: info["replica"], Edge: strings.Contains(strings.ToLower(info["serverServices"]), "edge"),
	}
	for _, data := range cl.P4Depots {
		upstream, err := p4depot.Normalize(p4depot.Ref(data))
		if err != nil || !p4depot.SameTransport(edge.Port, upstream.Port) || !p4depot.SameEndpoint(edge.Replica, upstream.Port) {
			continue
		}
		key, err := d.p4Cache.ServerKey(ctx, upstream)
		if err == nil && key == cl.ServerKey && d.registeredP4Edge(ctx, upstream, edge, key) {
			return true
		}
	}
	return false
}

func (d *Daemon) swarmReviewState(ctx context.Context, cl p4OpenChangelist, ref p4depot.Ref) string {
	if cl.SwarmReviewNumber <= 0 || ref.SwarmURL == "" || d.workspaceSwarmURL(cl.workspaceID, ref) != ref.SwarmURL {
		return ""
	}
	result, err := d.p4Cache.Swarm(ctx, p4cache.SwarmParams{
		Ref:      ref,
		Action:   p4cache.SwarmShow,
		ReviewID: strconv.Itoa(cl.SwarmReviewNumber),
	})
	if err != nil || result == nil || result.Review == nil {
		if err != nil && d.logger != nil {
			d.logger.Warn("p4 swarm review poll failed", "review", cl.SwarmReviewNumber, "error", err)
		}
		return ""
	}
	return result.Review.State
}

func (d *Daemon) p4WorkspaceIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids := make([]string, 0, len(d.workspaces))
	for id := range d.workspaces {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// p4PollRef selects only a resource configured for this poll's workspace.
// The changelist row can name a server/review, but cannot authorize either.
func p4PollRef(depots []P4DepotData, cl p4OpenChangelist) (p4depot.Ref, bool) {
	var selected p4depot.Ref
	found := false
	for _, depot := range depots {
		ref, err := p4depot.Normalize(p4depot.Ref(depot))
		if err != nil || !p4depot.SameEndpoint(ref.Port, cl.Port) {
			continue
		}
		// P4PORT rsh executes a local program. Polling is a background read,
		// not authorization to start a process on behalf of a workspace member.
		if strings.HasPrefix(strings.ToLower(ref.Port), "rsh:") {
			continue
		}
		if !found {
			selected, found = ref, true
			selected.SwarmURL = ""
		}
		if ref.SwarmURL != "" && ref.SwarmURL == strings.TrimRight(strings.TrimSpace(cl.SwarmURL), "/") {
			selected = ref
			break
		}
	}
	if !found {
		return p4depot.Ref{}, false
	}
	// Saved context can fill omissions, never authorize a server or replace
	// the current configured account, charset, or review origin.
	if selected.User == "" {
		selected.User = strings.TrimSpace(cl.P4User)
	}
	if selected.Charset == "" {
		selected.Charset = strings.TrimSpace(cl.Charset)
	}
	selected, err := p4depot.Normalize(selected)
	return selected, err == nil
}

func (d *Daemon) claimP4Poll(ctx context.Context, workspaceID, id string, generation int64) (int64, bool) {
	var out struct {
		Generation int64 `json:"poll_generation"`
	}
	err := d.client.ClaimP4ChangelistPoll(ctx, workspaceID, id, generation, &out)
	return out.Generation, err == nil && out.Generation > generation
}

func (d *Daemon) authorizedP4PollRef(ctx context.Context, depots []P4DepotData, cl p4OpenChangelist) (p4depot.Ref, bool) {
	if ref, ok := p4PollRef(depots, cl); ok {
		return ref, true
	}
	if cl.Client == "" || cl.DaemonID == "" || !p4depot.ClusterKey(cl.ServerKey, cl.Port) {
		return p4depot.Ref{}, false
	}
	for _, data := range depots {
		if !p4depot.SameTransport(data.Port, cl.Port) {
			continue
		}
		probe := cl
		probe.Port = data.Port
		ref, ok := p4PollRef([]P4DepotData{data}, probe)
		if !ok {
			continue
		}
		key, err := d.p4Cache.ServerKey(ctx, ref)
		if err == nil && key == cl.ServerKey {
			return ref, true
		}
	}
	return p4depot.Ref{}, false
}
