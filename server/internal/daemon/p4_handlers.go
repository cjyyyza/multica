package daemon

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

func legacyP4Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "this legacy P4 endpoint is no longer supported; upgrade the multica CLI to the same release as the daemon", http.StatusGone)
	}
}

type p4ChangelistLinkRequest struct {
	WorkspaceID string `json:"workspace_id"`
	TaskID      string `json:"task_id"`
	WorkDir     string `json:"workdir"`
	Changelist  string `json:"changelist"`
}

type p4SwarmRequest struct {
	Port        string   `json:"port"`
	Depot       string   `json:"depot"`
	Stream      string   `json:"stream,omitempty"`
	User        string   `json:"user,omitempty"`
	Charset     string   `json:"charset,omitempty"`
	Changelist  string   `json:"changelist,omitempty"`
	SwarmURL    string   `json:"swarm_url,omitempty"`
	WorkspaceID string   `json:"workspace_id"`
	WorkDir     string   `json:"workdir"`
	TaskID      string   `json:"task_id"`
	Action      string   `json:"action"`
	ReviewID    string   `json:"review_id,omitempty"`
	Body        string   `json:"body,omitempty"`
	Description string   `json:"description,omitempty"`
	Reviewers   []string `json:"reviewers,omitempty"`
}

func (d *Daemon) overlayP4Ref(workspaceID, taskID string, ref p4depot.Ref) p4depot.Ref {
	stored := d.taskP4Default(workspaceID, taskID, ref)
	if ref.User == "" {
		ref.User = stored.User
	}
	if ref.Charset == "" {
		ref.Charset = stored.Charset
	}
	ref.SwarmURL = d.workspaceSwarmURL(workspaceID, ref)
	return ref
}

func (d *Daemon) workspaceSwarmURL(workspaceID string, ref p4depot.Ref) string {
	ref, err := p4depot.Normalize(ref)
	if err != nil {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if ws := d.workspaces[workspaceID]; ws != nil {
		return ws.allowedP4Identities[p4depot.Identity(ref)].SwarmURL
	}
	return ""
}

func (d *Daemon) authorizeP4Call(w http.ResponseWriter, r *http.Request, workspaceID, taskID, workDir, op string) (activeRepoCheckoutTask, string, bool) {
	activeTask, authResult := d.activeRepoCheckoutTask(r)
	if authResult != repoCheckoutAuthOK {
		d.writeRepoCheckoutAuthError(w, authResult)
		return activeRepoCheckoutTask{}, "", false
	}
	if !allowActiveTaskExternalTools(w, activeTask) {
		return activeRepoCheckoutTask{}, "", false
	}
	if workspaceID == "" {
		http.Error(w, "workspace_id is required", http.StatusBadRequest)
		return activeRepoCheckoutTask{}, "", false
	}
	if workDir == "" {
		http.Error(w, "workdir is required", http.StatusBadRequest)
		return activeRepoCheckoutTask{}, "", false
	}
	if workspaceID != activeTask.WorkspaceID || taskID != activeTask.TaskID {
		http.Error(w, op+" task context does not match the active task", http.StatusForbidden)
		return activeRepoCheckoutTask{}, "", false
	}
	root, err := authorizeRepoCheckoutWorkDir(activeTask.WorkDir, activeTask.WorkDir)
	if err != nil {
		http.Error(w, op+" task workdir is unavailable", http.StatusForbidden)
		return activeRepoCheckoutTask{}, "", false
	}
	activeTask.WorkDir = root
	authorizedWorkDir, err := authorizeRepoCheckoutWorkDir(activeTask.WorkDir, workDir)
	if err != nil {
		http.Error(w, op+" workdir is not owned by the active task", http.StatusForbidden)
		return activeRepoCheckoutTask{}, "", false
	}
	return activeTask, authorizedWorkDir, true
}

func (d *Daemon) allowP4Ref(w http.ResponseWriter, r *http.Request, workspaceID, taskID string, ref p4depot.Ref) (p4depot.Ref, bool) {
	if !d.workspaceP4Allowed(workspaceID, taskID, ref) {
		if refreshErr := d.refreshWorkspaceP4Allowlist(r.Context(), workspaceID); refreshErr != nil {
			d.logger.Debug("p4 allowlist refresh failed", "error", refreshErr)
		}
	}
	if !d.workspaceP4Allowed(workspaceID, taskID, ref) {
		http.Error(w, "perforce depot is not configured for this workspace or task", http.StatusBadRequest)
		return p4depot.Ref{}, false
	}
	if ref.SwarmURL != "" && ref.SwarmURL != d.workspaceSwarmURL(workspaceID, ref) {
		http.Error(w, "swarm_url must match the configured depot URL", http.StatusForbidden)
		return p4depot.Ref{}, false
	}
	return d.overlayP4Ref(workspaceID, taskID, ref), true
}

func (d *Daemon) p4ChangelistLinkHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req p4ChangelistLinkRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		number, err := strconv.Atoi(strings.TrimSpace(req.Changelist))
		if err != nil || number <= 0 {
			http.Error(w, "changelist must be a positive number", http.StatusBadRequest)
			return
		}
		activeTask, _, ok := d.authorizeP4Call(w, r, req.WorkspaceID, req.TaskID, req.WorkDir, "p4 changelist link")
		if !ok {
			return
		}
		binding := d.p4Binding(activeTask.TaskID)
		if binding == nil {
			http.Error(w, "this task is not bound to a Perforce workspace", http.StatusConflict)
			return
		}
		if d.p4Cache == nil || d.client == nil {
			http.Error(w, "perforce is not initialized", http.StatusInternalServerError)
			return
		}
		ref := binding.ref
		if ref.User == "" {
			ref.User = binding.ws.User
		}
		if ref.Charset == "" {
			ref.Charset = binding.ws.Charset
		}
		info, err := d.p4Cache.DescribeTaskChange(r.Context(), ref, strconv.Itoa(number))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if info.Missing {
			http.Error(w, "changelist was not found", http.StatusNotFound)
			return
		}
		if info.Client == "" || !strings.EqualFold(info.Client, binding.ws.Client) {
			http.Error(w, "changelist does not belong to the bound Perforce client "+binding.ws.Client, http.StatusConflict)
			return
		}
		current, err := strconv.Atoi(info.Change)
		if err != nil || current <= 0 {
			http.Error(w, "p4 describe returned an invalid changelist number", http.StatusInternalServerError)
			return
		}
		original, err := strconv.Atoi(info.OldChange)
		if err != nil || original <= 0 {
			original = current
		}
		status := "pending"
		if info.Shelved {
			status = "shelved"
		}
		if strings.EqualFold(info.Status, "submitted") {
			status = "submitted"
		}
		serverKey := strings.TrimSpace(binding.ws.ServerCluster)
		if serverKey == "" {
			serverKey = strings.ToLower(strings.TrimSpace(binding.ws.Port))
		}
		report := p4ChangelistReport{
			FencePolls: true, Port: binding.ws.Port, ServerKey: serverKey, Charset: binding.ws.Charset,
			Number: current, OriginalNumber: original, Status: status, Client: binding.ws.Client,
			P4User: binding.ws.User, Description: info.Description, Ticket: binding.ticket,
			FilesCount: info.FilesCount, ExclusiveFiles: []string{},
		}
		if status == "submitted" && !info.SubmittedAt.IsZero() {
			report.SubmittedAt = p4SubmittedAt(info.SubmittedAt)
		}
		var response map[string]any
		if err := d.client.ReportTaskP4Changelist(r.Context(), activeTask.TaskID, report, &response); err != nil {
			http.Error(w, "link changelist to the issue: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}
}

func (d *Daemon) p4SwarmHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req p4SwarmRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		activeTask, _, ok := d.authorizeP4Call(w, r, req.WorkspaceID, req.TaskID, req.WorkDir, "p4 swarm")
		if !ok {
			return
		}
		if binding := d.p4Binding(activeTask.TaskID); binding != nil &&
			strings.TrimSpace(req.Port) == "" && strings.TrimSpace(req.Depot) == "" && strings.TrimSpace(req.Stream) == "" {
			req.Port, req.Depot, req.Stream = binding.ref.Port, binding.ref.Depot, binding.ref.Stream
			if req.User == "" {
				req.User = binding.ref.User
				if req.User == "" {
					req.User = binding.ws.User
				}
			}
			if req.Charset == "" {
				req.Charset = binding.ref.Charset
				if req.Charset == "" {
					req.Charset = binding.ws.Charset
				}
			}
		}
		ref, err := p4depot.Normalize(p4depot.Ref{
			Port: req.Port, Depot: req.Depot, Stream: req.Stream, User: req.User,
			Charset: req.Charset, Changelist: req.Changelist, SwarmURL: req.SwarmURL,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ref, ok = d.allowP4Ref(w, r, req.WorkspaceID, activeTask.TaskID, ref)
		if !ok {
			return
		}
		if d.p4Cache == nil {
			http.Error(w, "perforce is not initialized", http.StatusInternalServerError)
			return
		}
		result, err := d.p4Cache.Swarm(r.Context(), p4cache.SwarmParams{
			Ref: ref, Action: req.Action, Changelist: req.Changelist, ReviewID: req.ReviewID,
			Body: req.Body, Description: req.Description, Reviewers: req.Reviewers,
		})
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			d.logger.Error("p4 swarm failed", "action", req.Action, "port", ref.Port, "depot", ref.Depot, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}
}
