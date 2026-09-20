package daemon

import (
	"encoding/json"
	"net/http"

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
	Force           bool     `json:"force,omitempty"`
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
	ref.SwarmURL = stored.SwarmURL
	return ref
}

func (d *Daemon) authorizeP4Call(w http.ResponseWriter, r *http.Request, workspaceID, taskID, workDir, op string) (activeRepoCheckoutTask, string, bool) {
	activeTask, authResult := d.activeRepoCheckoutTask(r)
	if authResult != repoCheckoutAuthOK {
		d.writeRepoCheckoutAuthError(w, authResult)
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
	stored := d.taskP4Default(workspaceID, taskID, ref)
	if ref.SwarmURL != "" && ref.SwarmURL != stored.SwarmURL {
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
		activeTask, authorizedWorkDir, ok := d.authorizeP4Call(w, r, req.WorkspaceID, req.TaskID, req.WorkDir, "p4")
		if !ok {
			return
		}
		ref, ok = d.allowP4Ref(w, r, req.WorkspaceID, activeTask.TaskID, ref)
		if !ok {
			return
		}
		cwd := authorizedWorkDir
		if req.Cwd != "" {
			authorizedCwd, cwdErr := authorizeRepoCheckoutWorkDir(activeTask.WorkDir, req.Cwd)
			if cwdErr != nil {
				http.Error(w, "p4 cwd is not owned by the active task", http.StatusForbidden)
				return
			}
			cwd = authorizedCwd
		}
		if d.p4Cache == nil {
			http.Error(w, "perforce is not initialized", http.StatusInternalServerError)
			return
		}
		result, err := d.p4Cache.Mutate(r.Context(), p4cache.MutateParams{
			WorkspaceID:     req.WorkspaceID,
			TaskID:          activeTask.TaskID,
			WorkDir:         activeTask.WorkDir,
			Cwd:             cwd,
			Ref:             ref,
			Action:          req.Action,
			Files:           req.Files,
			Changelist:      req.Changelist,
			Description:     req.Description,
			Dest:            req.Dest,
			RevertUnchanged: req.RevertUnchanged,
			Force:           req.Force,
		})
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			d.logger.Error("p4 run failed", "action", req.Action, "port", ref.Port, "depot", ref.Depot, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
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
		activeTask, _, ok := d.authorizeP4Call(w, r, req.WorkspaceID, req.TaskID, req.WorkDir, "p4 swarm")
		if !ok {
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
			WorkspaceID: req.WorkspaceID,
			TaskID:      activeTask.TaskID,
			WorkDir:     activeTask.WorkDir,
			Ref:         ref,
			Action:      req.Action,
			Changelist:  req.Changelist,
			ReviewID:    req.ReviewID,
			Body:        req.Body,
			Description: req.Description,
			Reviewers:   req.Reviewers,
			Shelve:      req.Shelve,
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
