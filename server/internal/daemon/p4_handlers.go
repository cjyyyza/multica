package daemon

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

type p4MutateRequest struct {
	Port            string   `json:"port"`
	Depot           string   `json:"depot"`
	Stream          string   `json:"stream,omitempty"`
	User            string   `json:"user,omitempty"`
	Charset         string   `json:"charset,omitempty"`
	Changelist      string   `json:"changelist,omitempty"`
	SwarmURL        string   `json:"swarm_url,omitempty"`
	WorkspaceID     string   `json:"workspace_id"`
	WorkDir         string   `json:"workdir"`
	Cwd             string   `json:"cwd,omitempty"`
	TaskID          string   `json:"task_id"`
	Action          string   `json:"action"`
	Files           []string `json:"files,omitempty"`
	Description     string   `json:"description,omitempty"`
	Dest            string   `json:"dest,omitempty"`
	RevertUnchanged bool     `json:"revert_unchanged,omitempty"`
	Preview         bool     `json:"preview,omitempty"`
	Force           bool     `json:"force,omitempty"`
	Fresh           bool     `json:"fresh,omitempty"`
	// Max and Long are filelog bounds. They are not p4 flags from the caller.
	Max  int  `json:"max,omitempty"`
	Long bool `json:"long,omitempty"`
	// Args is the p4 argv after the verb for forwarded reads and resolve.
	// Other write actions ignore it.
	Args []string `json:"args,omitempty"`
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
	Shelve      bool     `json:"shelve,omitempty"`
}

func (d *Daemon) overlayP4Ref(workspaceID, taskID string, ref p4depot.Ref, includeChangelist bool) p4depot.Ref {
	stored := d.taskP4Default(workspaceID, taskID, ref)
	if ref.User == "" {
		ref.User = stored.User
	}
	if ref.Charset == "" {
		ref.Charset = stored.Charset
	}
	if includeChangelist && ref.Changelist == "" {
		ref.Changelist = stored.Changelist
	}
	// A task may not choose where the host's Helix ticket is sent.
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
	return d.overlayP4Ref(workspaceID, taskID, ref, false), true
}

func (d *Daemon) p4RunHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req p4MutateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if activeTask, authResult := d.activeRepoCheckoutTask(r); authResult == repoCheckoutAuthOK {
			if binding := d.p4Binding(activeTask.TaskID); binding != nil {
				d.serveWorkspaceP4Run(w, r, activeTask, binding, req)
				return
			}
		}
		if _, _, ok := d.authorizeP4Call(w, r, req.WorkspaceID, req.TaskID, req.WorkDir, "p4 run"); !ok {
			return
		}
		writeP4Rejection(w, p4Rejection{Code: p4CodeActionNotAllowed, Message: p4cache.ErrTaskClientNotCreated.Error()})
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
		if binding := d.p4Binding(activeTask.TaskID); binding != nil {
			action := strings.ToLower(strings.TrimSpace(req.Action))
			if binding.readOnly && action != p4cache.SwarmList && action != p4cache.SwarmShow {
				writeP4Rejection(w, p4Rejection{Code: p4CodeActionNotAllowed, Message: "chat and tasks without an issue may only read the Perforce workspace"})
				return
			}
			selected, matches := binding.requestRef(strings.TrimSpace(req.Port), strings.TrimSpace(req.Depot), strings.TrimSpace(req.Stream))
			if !matches {
				writeP4Rejection(w, p4Rejection{Code: p4CodeNotInWorkspace, Message: "resource does not match this task's verified workspace"})
				return
			}
			req.Port, req.Depot, req.Stream = selected.Port, selected.Depot, selected.Stream
			req.User, req.Charset = selected.User, selected.Charset
			if req.User == "" {
				req.User = binding.ws.User
			}
			if req.Charset == "" {
				req.Charset = binding.ws.Charset
			}
			if strings.TrimSpace(req.Changelist) == "" && action == p4cache.SwarmCreate {
				req.Changelist = binding.changelist()
			}
		}
		ref, err := p4depot.Normalize(p4depot.Ref{
			Port:       req.Port,
			Depot:      req.Depot,
			Stream:     req.Stream,
			User:       req.User,
			Charset:    req.Charset,
			Changelist: req.Changelist,
			SwarmURL:   req.SwarmURL,
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
		if req.Shelve && strings.EqualFold(strings.TrimSpace(req.Action), p4cache.SwarmCreate) {
			binding := d.p4Binding(activeTask.TaskID)
			if binding == nil {
				writeP4Rejection(w, p4Rejection{Code: p4CodeActionNotAllowed, Message: p4cache.ErrTaskClientNotCreated.Error()})
				return
			}
			if _, matches := binding.requestRef(ref.Port, ref.Depot, ref.Stream); !matches {
				writeP4Rejection(w, p4Rejection{Code: p4CodeNotInWorkspace, Message: "depot does not match the workspace this task is bound to"})
				return
			}
			if rej := binding.rejectForeignChangelist(req.Changelist); rej != nil {
				writeP4Rejection(w, *rej)
				return
			}
			if !binding.isSubmitted() {
				_, rej, err := d.runWorkspaceP4(r.Context(), binding, p4MutateRequest{Action: p4cache.ActionShelve, Changelist: req.Changelist}, binding.ws.Dir)
				if rej != nil {
					writeP4Rejection(w, *rej)
					return
				}
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
			}
		}
		result, err := d.p4Cache.Swarm(r.Context(), p4cache.SwarmParams{
			Ref:         ref,
			Action:      req.Action,
			Changelist:  req.Changelist,
			ReviewID:    req.ReviewID,
			Body:        req.Body,
			Description: req.Description,
			Reviewers:   req.Reviewers,
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
		json.NewEncoder(w).Encode(result)
	}
}

func writeP4Rejection(w http.ResponseWriter, rej p4Rejection) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	json.NewEncoder(w).Encode(rej)
}

func (d *Daemon) serveWorkspaceP4Run(w http.ResponseWriter, r *http.Request, activeTask activeRepoCheckoutTask, binding *p4TaskBinding, req p4MutateRequest) {
	if !allowActiveTaskExternalTools(w, activeTask) {
		return
	}
	if req.WorkspaceID != "" && req.WorkspaceID != activeTask.WorkspaceID {
		http.Error(w, "p4 task context does not match the active task", http.StatusForbidden)
		return
	}
	if req.TaskID != "" && req.TaskID != activeTask.TaskID {
		http.Error(w, "p4 task context does not match the active task", http.StatusForbidden)
		return
	}
	if _, matches := binding.requestRef(strings.TrimSpace(req.Port), strings.TrimSpace(req.Depot), strings.TrimSpace(req.Stream)); !matches {
		http.Error(w, "resource does not match the workspace this task is bound to", http.StatusBadRequest)
		return
	}
	cwd := binding.ws.Dir
	if strings.TrimSpace(req.WorkDir) != "" {
		authorized, err := authorizeRepoCheckoutWorkDir(activeTask.WorkDir, req.WorkDir)
		if err != nil {
			http.Error(w, "p4 workdir is not owned by the active task", http.StatusForbidden)
			return
		}
		cwd = authorized
	}
	if strings.TrimSpace(req.Cwd) != "" {
		authorized, err := authorizeRepoCheckoutWorkDir(activeTask.WorkDir, req.Cwd)
		if err != nil {
			http.Error(w, "p4 cwd is not owned by the active task", http.StatusForbidden)
			return
		}
		cwd = authorized
	}
	if d.p4Cache == nil {
		http.Error(w, "perforce is not initialized", http.StatusInternalServerError)
		return
	}
	result, rej, err := d.runWorkspaceP4(r.Context(), binding, req, cwd)
	if rej != nil {
		writeP4Rejection(w, *rej)
		return
	}
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		d.logger.Error("p4 workspace run failed", "action", req.Action, "client", binding.ws.Client, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (d *Daemon) p4StatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		activeTask, authResult := d.activeRepoCheckoutTask(r)
		if authResult != repoCheckoutAuthOK {
			d.writeRepoCheckoutAuthError(w, authResult)
			return
		}
		binding := d.p4Binding(activeTask.TaskID)
		if binding == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(p4Rejection{
				Code:    "no_workspace",
				Message: "this task is not bound to a Perforce workspace",
			})
			return
		}
		if d.p4Cache == nil {
			http.Error(w, "perforce is not initialized", http.StatusInternalServerError)
			return
		}
		status, err := d.workspaceStatus(r.Context(), binding)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			d.logger.Error("p4 status failed", "client", binding.ws.Client, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	}
}
