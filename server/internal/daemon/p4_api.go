package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/internal/p4depot"
)

// p4ChangelistReport is the body of POST /api/daemon/tasks/{taskId}/p4/changelists.
type p4ChangelistReport struct {
	Port           string             `json:"port"`
	ServerKey      string             `json:"server_key"`
	Charset        string             `json:"charset"`
	Number         int                `json:"number"`
	OriginalNumber int                `json:"original_number"`
	Status         string             `json:"status"`
	Client         string             `json:"client"`
	P4User         string             `json:"p4_user"`
	Description    string             `json:"description"`
	Ticket         string             `json:"ticket"`
	FilesCount     int                `json:"files_count"`
	ExclusiveFiles []string           `json:"exclusive_files"`
	Warnings       []p4Warning        `json:"warnings,omitempty"`
	SwarmReview    *p4SwarmReviewBody `json:"swarm_review,omitempty"`
}

// p4SwarmReviewBody is the optional review attached to a task changelist report.
type p4SwarmReviewBody struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	State       string `json:"state"`
	Author      string `json:"author"`
	Description string `json:"description"`
}

func (d *Daemon) changelistReport(ctx context.Context, b *p4TaskBinding, result *p4cache.FinishResult) p4ChangelistReport {
	b.mu.Lock()
	desc := b.description
	ticket := b.ticket
	b.mu.Unlock()
	if desc == "" {
		desc = p4depot.ChangeDescription(ticket, b.title, b.issueKey)
	}
	number, _ := strconv.Atoi(result.Change)
	status := "deleted"
	filesCount := 0
	var exclusive []string
	var warnings []p4Warning
	if result.Shelved {
		status = "shelved"
		filesCount = len(result.Files)
		exclusive = result.ExclusiveHeld
		warnings = packWarnings(
			p4Warning{Kind: "not_head", Files: result.NotHead},
			p4Warning{Kind: "exclusive_held", Files: result.ExclusiveHeld},
		)
	}
	if exclusive == nil {
		exclusive = []string{}
	}
	warnings = append(warnings, d.workspaceDriftWarnings(ctx, b)...)
	return p4ChangelistReport{
		Port:           b.ws.Port,
		ServerKey:      p4ServerKey(b.ws),
		Charset:        b.ws.Charset,
		Number:         number,
		OriginalNumber: number,
		Status:         status,
		Client:         b.ws.Client,
		P4User:         b.ws.User,
		Description:    desc,
		Ticket:         ticket,
		FilesCount:     filesCount,
		ExclusiveFiles: exclusive,
		Warnings:       warnings,
	}
}

func p4ServerKey(ws *p4cache.Workspace) string {
	if ws == nil {
		return ""
	}
	if key := strings.TrimSpace(ws.ServerCluster); key != "" {
		return key
	}
	return strings.ToLower(strings.TrimSpace(ws.Port))
}

// workspaceDriftWarnings reports files that showed up outside the task
// changelist while it ran: the editor's default-changelist checkouts, and
// anything opened into some other changelist.
func (d *Daemon) workspaceDriftWarnings(ctx context.Context, b *p4TaskBinding) []p4Warning {
	opened, err := d.p4Cache.Opened(ctx, b.ws)
	if err != nil {
		if d.logger != nil {
			d.logger.Warn("p4 opened for workspace report failed", "client", b.ws.Client, "error", err)
		}
		return nil
	}
	change := b.changelist()
	var freshDefault, unexpected []string
	for _, file := range opened {
		if file.DepotFile == "" || b.inDefaultSnapshot(file.DepotFile) {
			continue
		}
		switch file.OpenChange {
		case "default":
			freshDefault = append(freshDefault, file.DepotFile)
		default:
			if change == "" || file.OpenChange != change {
				unexpected = append(unexpected, file.DepotFile)
			}
		}
	}
	return packWarnings(
		p4Warning{Kind: "default_changelist_files", Files: freshDefault},
		p4Warning{Kind: "unexpected_opens", Files: unexpected},
	)
}

func (d *Daemon) attachSwarmReview(ctx context.Context, task Task, b *p4TaskBinding, report *p4ChangelistReport) {
	if d.p4Cache == nil || strings.TrimSpace(b.ref.SwarmURL) == "" || report.Number <= 0 {
		return
	}
	change := strconv.Itoa(report.Number)
	params := p4cache.SwarmParams{
		WorkspaceID: task.WorkspaceID,
		TaskID:      task.ID,
		WorkDir:     b.ws.Dir,
		Ref:         b.ref,
		Changelist:  change,
		Description: report.Description,
		Shelve:      false,
	}
	existing, err := d.p4Cache.Swarm(ctx, swarmAction(params, p4cache.SwarmList))
	if err != nil {
		if d.logger != nil {
			d.logger.Warn("p4 swarm review lookup failed", "change", change, "error", err)
		}
	}
	var review *p4cache.SwarmReview
	swarmURL := b.ref.SwarmURL
	if existing != nil && len(existing.Reviews) > 0 {
		review = &existing.Reviews[0]
		if existing.SwarmURL != "" {
			swarmURL = existing.SwarmURL
		}
	} else {
		created, createErr := d.p4Cache.Swarm(ctx, swarmAction(params, p4cache.SwarmCreate))
		if createErr != nil {
			if d.logger != nil {
				d.logger.Warn("p4 swarm review create failed", "change", change, "error", createErr)
			}
			return
		}
		review = created.Review
		if created.SwarmURL != "" {
			swarmURL = created.SwarmURL
		}
	}
	if review == nil || review.ID <= 0 {
		return
	}
	// The review rides on the changelist report only. Posting it to the issue's
	// swarm-reviews route as well would link it a second time, and the issue
	// would show it twice: on the changelist card and in the pull-request list.
	report.SwarmReview = &p4SwarmReviewBody{
		Number:      review.ID,
		URL:         review.URL,
		State:       review.State,
		Author:      review.Author,
		Description: review.Description,
	}
	if report.SwarmReview.URL == "" {
		report.SwarmReview.URL = strings.TrimRight(swarmURL, "/") + "/reviews/" + strconv.Itoa(review.ID)
	}
}

func swarmAction(params p4cache.SwarmParams, action string) p4cache.SwarmParams {
	params.Action = action
	return params
}

// reportTaskP4Changelist tells the server about the task changelist. A 404
// means this server build has no changelist route yet; that is a warning,
// not a failed task.
func (d *Daemon) reportTaskP4Changelist(ctx context.Context, task Task, report p4ChangelistReport) error {
	if d.client == nil {
		return fmt.Errorf("daemon client is not initialized")
	}
	token := strings.TrimSpace(task.AuthToken)
	if token == "" {
		return fmt.Errorf("task %s has no auth token for the changelist report", task.ID)
	}
	err := d.client.ReportTaskP4Changelist(ctx, task.ID, token, report, &map[string]any{})
	if isNotFound(err) {
		if d.logger != nil {
			d.logger.Warn("p4 changelist report skipped; server has no changelist route", "task_id", task.ID)
		}
		return nil
	}
	return err
}

func isNotFound(err error) bool {
	var reqErr *requestError
	return errors.As(err, &reqErr) && reqErr.StatusCode == http.StatusNotFound
}

// p4StatusFile is one opened file in the GET /p4/status response.
type p4StatusFile struct {
	DepotFile string `json:"depot_file"`
	Action    string `json:"action"`
	Type      string `json:"type"`
}

// p4StatusResponse is the bound-workspace body of GET /p4/status.
type p4StatusResponse struct {
	Port     string         `json:"port"`
	User     string         `json:"user"`
	Client   string         `json:"client"`
	Root     string         `json:"root"`
	Stream   string         `json:"stream"`
	Edge     bool           `json:"edge"`
	Ticket   string         `json:"ticket"`
	Change   string         `json:"change"`
	Files    []p4StatusFile `json:"files"`
	Warnings []p4Warning    `json:"warnings,omitempty"`
}

func (d *Daemon) workspaceStatus(ctx context.Context, b *p4TaskBinding) (*p4StatusResponse, error) {
	change := b.changelist()
	resp := &p4StatusResponse{
		Port:   b.ws.Port,
		User:   b.ws.User,
		Client: b.ws.Client,
		Root:   b.ws.Root,
		Stream: b.ws.Stream,
		Edge:   b.ws.Edge,
		Ticket: b.ticket,
		Change: change,
		Files:  []p4StatusFile{},
	}
	if change != "" {
		opened, err := d.p4Cache.OpenedInChange(ctx, b.ws, change)
		if err != nil {
			return nil, err
		}
		paths := make([]string, 0, len(opened))
		for _, file := range opened {
			resp.Files = append(resp.Files, p4StatusFile{
				DepotFile: file.DepotFile,
				Action:    file.Action,
				Type:      file.HeadType,
			})
			paths = append(paths, file.DepotFile)
		}
		if states, err := d.p4Cache.FileStates(ctx, b.ws, paths); err == nil {
			resp.Warnings = append(resp.Warnings, workspaceFileWarnings(states, b.ref.AllowExclusive)...)
		}
	}
	resp.Warnings = append(resp.Warnings, d.workspaceDriftWarnings(ctx, b)...)
	return resp, nil
}

// Keep time imported for the status loop's submitted_at formatting helper
// that lives next to the report types.
func p4SubmittedAt(now time.Time) string {
	return now.UTC().Format(time.RFC3339)
}
