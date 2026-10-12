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
	ID                string `json:"id"`
	Port              string `json:"port"`
	Number            int    `json:"number"`
	OriginalNumber    int    `json:"original_number"`
	Status            string `json:"status"`
	SwarmReviewNumber int    `json:"swarm_review_number"`
	SwarmURL          string `json:"swarm_url"`
}

type p4OpenChangelistsResponse struct {
	Changelists []p4OpenChangelist `json:"changelists"`
}

type p4StatusUpdate struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	Number           int    `json:"number"`
	SubmittedAt      string `json:"submitted_at,omitempty"`
	SwarmReviewState string `json:"swarm_review_state,omitempty"`
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
			update, ok := d.observeP4Changelist(ctx, cl)
			if ok {
				updates = append(updates, update)
			}
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

func (d *Daemon) observeP4Changelist(ctx context.Context, cl p4OpenChangelist) (p4StatusUpdate, bool) {
	original := cl.OriginalNumber
	if original == 0 {
		original = cl.Number
	}
	ref := p4depot.Ref{Port: cl.Port, Charset: d.p4CharsetForPort(cl.Port)}
	info, err := d.p4Cache.DescribeChange(ctx, ref, strconv.Itoa(original))
	if err != nil {
		if d.logger != nil {
			d.logger.Warn("p4 describe for changelist poll failed", "changelist", original, "error", err)
		}
		return p4StatusUpdate{}, false
	}
	update := p4StatusUpdate{ID: cl.ID, Number: cl.Number, Status: cl.Status}
	switch {
	case info.Missing:
		update.Status = "deleted"
	case strings.EqualFold(info.Status, "submitted"):
		update.Status = "submitted"
		if n, convErr := strconv.Atoi(info.Change); convErr == nil && n > 0 {
			update.Number = n
		}
		update.SubmittedAt = p4SubmittedAt(time.Now())
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
	if state := d.swarmReviewState(ctx, cl); state != "" {
		update.SwarmReviewState = state
	}
	return update, true
}

func (d *Daemon) swarmReviewState(ctx context.Context, cl p4OpenChangelist) string {
	if cl.SwarmReviewNumber <= 0 || strings.TrimSpace(cl.SwarmURL) == "" || strings.TrimSpace(cl.Port) == "" {
		return ""
	}
	result, err := d.p4Cache.Swarm(ctx, p4cache.SwarmParams{
		Ref: p4depot.Ref{
			Port:     cl.Port,
			Depot:    d.p4DepotForPort(cl.Port),
			Charset:  d.p4CharsetForPort(cl.Port),
			SwarmURL: cl.SwarmURL,
		},
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

func (d *Daemon) p4CharsetForPort(port string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, ws := range d.workspaces {
		for _, stored := range ws.allowedP4Identities {
			if strings.EqualFold(stored.Port, port) && stored.Charset != "" {
				return stored.Charset
			}
		}
		for _, taskDepots := range ws.taskP4Refs {
			for _, stored := range taskDepots {
				if strings.EqualFold(stored.Port, port) && stored.Charset != "" {
					return stored.Charset
				}
			}
		}
	}
	return ""
}

func (d *Daemon) p4DepotForPort(port string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, ws := range d.workspaces {
		for _, stored := range ws.allowedP4Identities {
			if strings.EqualFold(stored.Port, port) && stored.Depot != "" {
				return stored.Depot
			}
		}
		for _, taskDepots := range ws.taskP4Refs {
			for _, stored := range taskDepots {
				if strings.EqualFold(stored.Port, port) && stored.Depot != "" {
					return stored.Depot
				}
			}
		}
	}
	// Describe does not need a depot. Swarm's Normalize does, and a poll
	// that only has the port still has to name one. The depot path is not
	// sent to Swarm; it only has to be syntactically valid.
	if strings.TrimSpace(port) != "" {
		return "//depot"
	}
	return ""
}
