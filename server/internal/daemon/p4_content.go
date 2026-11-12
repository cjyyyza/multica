package daemon

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (d *Daemon) handleP4Content(ctx context.Context, rt Runtime, requestID string) {
	if d.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	endpoint := "/api/daemon/runtimes/" + url.PathEscape(rt.ID) + "/p4-content/" + url.PathEscape(requestID)
	var input struct {
		p4OpenChangelist
		WorkspaceID    string `json:"workspace_id"`
		File           string `json:"file"`
		AllowWorkspace bool   `json:"allow_workspace"`
	}
	var result *protocol.P4ChangelistContent
	err := d.client.getJSON(ctx, endpoint, &input)
	if err == nil {
		d.mu.Lock()
		workspace := d.workspaces[input.WorkspaceID]
		belongs := false
		if workspace != nil {
			for _, id := range workspace.runtimeIDs {
				if id == rt.ID {
					belongs = true
					break
				}
			}
		}
		d.mu.Unlock()
		if !belongs || d.p4Cache == nil {
			err = fmt.Errorf("Perforce is not available in this workspace")
		} else {
			input.workspaceID = input.WorkspaceID
			ref, allowed := d.authorizedP4PollRef(ctx, input.P4Depots, input.p4OpenChangelist)
			if !allowed {
				err = fmt.Errorf("no configured Perforce resource authorizes this read")
			} else {
				result, err = d.p4Cache.ReadChangelistContent(ctx, ref, input.Number, input.File, input.AllowWorkspace && (input.DaemonID == "" || input.DaemonID == d.cfg.DaemonID))
			}
		}
	}
	body := map[string]any{"result": result, "error": ""}
	if err != nil {
		body["error"] = err.Error()
	}
	// The P4 command may have used the entire execution budget. Reporting still
	// gets a short independent budget so the UI receives the timeout reason.
	reportCtx, reportCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer reportCancel()
	if err := d.client.postJSON(reportCtx, endpoint+"/result", body, nil); err != nil {
		d.logger.Warn("report Perforce content failed", "request_id", requestID, "error", err)
	}
}
