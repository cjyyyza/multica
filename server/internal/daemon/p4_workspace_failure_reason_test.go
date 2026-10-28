package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/p4cache"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

func TestRunTaskP4WorkspaceFailuresHaveActionableDirectoryReason(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p4bin := writeDaemonFakeP4(t)
	t.Setenv("P4_FAKE_INFO", "... userName alice\n... clientName unknown\n")
	for _, kind := range []string{"missing_binding", "wrong_mode", "p4_unavailable", "invalid_client"} {
		t.Run(kind, func(t *testing.T) {
			d := &Daemon{cfg: Config{DaemonID: "daemon"}, logger: log}
			task := Task{ID: "task", AgentID: "agent", Agent: &AgentData{ID: "agent"}, WorkspaceID: "workspace", ProjectID: "project", IssueID: "issue",
				P4Depots:         []P4DepotData{{Port: "p4:1666", Depot: "//depot/game"}},
				ProjectResources: []ProjectResourceData{{ResourceType: "perforce_depot"}},
			}
			if kind != "missing_binding" {
				mode := localDirectoryModeInPlace
				if kind == "wrong_mode" {
					mode = localDirectoryModeWorktree
				}
				ref, err := json.Marshal(localDirectoryRef{LocalPath: t.TempDir(), DaemonID: "daemon", ExecutionMode: mode})
				if err != nil {
					t.Fatal(err)
				}
				task.ProjectResources = append(task.ProjectResources, ProjectResourceData{ResourceType: "local_directory", ResourceRef: ref})
			}
			if kind == "invalid_client" {
				d.p4Cache = &p4cache.Cache{P4Path: p4bin}
			}
			_, err := d.runTask(t.Context(), task, "claude", 0, log)
			if err == nil {
				t.Fatal("unusable P4 workspace must fail before starting an agent")
			}
			if got := taskRunFailureReason(err); got != "local_directory_error" {
				t.Errorf("reason=%q, want local_directory_error; raw error: %v", got, err)
			}
			if got := taskfailure.NormalizeDaemonReason(taskRunFailureReason(err), err.Error()).String(); got != "local_directory_error" {
				t.Errorf("persisted failure reason=%q, want local_directory_error", got)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "perforce") {
				t.Errorf("workspace diagnostic lost: %v", err)
			}
		})
	}
}

func TestRequirementPlanRejectsLocalDirectoryBeforeP4Preparation(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := &Daemon{cfg: Config{DaemonID: "daemon"}, logger: log}
	ref, err := json.Marshal(localDirectoryRef{LocalPath: t.TempDir(), DaemonID: "daemon", ExecutionMode: localDirectoryModeInPlace})
	if err != nil {
		t.Fatal(err)
	}
	task := Task{ID: "plan", WorkspaceID: "workspace", AgentID: "agent", Agent: &AgentData{ID: "agent"},
		Mode: taskpolicy.ModePlan, PlanPermission: taskpolicy.PlanPermissionClaude, RequirementID: "requirement", ChatSessionID: "chat",
		P4Depots:         []P4DepotData{{Port: "p4:1666", Depot: "//depot/main"}},
		ProjectResources: []ProjectResourceData{{ResourceType: "local_directory", ResourceRef: ref}},
	}
	_, err = d.runTask(t.Context(), task, "claude", 0, log)
	if err == nil {
		t.Fatal("Plan must refuse a local directory before preparing P4")
	}
	if taskRunFailureReason(err) != "plan_mode_unavailable" {
		t.Fatalf("reason=%v error=%v", taskRunFailureReason(err), err)
	}
	if !strings.Contains(err.Error(), "will not enter") {
		t.Fatalf("Plan must explain that it will not enter the local directory: %v", err)
	}
	if got := taskfailure.NormalizeDaemonReason(taskRunFailureReason(err), err.Error()).String(); got != "plan_mode_unavailable" {
		t.Fatalf("Plan failure reason was not preserved: %s", got)
	}
}

func TestP4DirectorySetupPreservesPreparationTimeoutReason(t *testing.T) {
	err := asLocalDirectorySetupFailure(fmt.Errorf("P4 workspace probe: %w", errTaskPrepareTimeout))
	if got := taskRunFailureReason(err); got != "timeout" {
		t.Errorf("reason=%q, want timeout", got)
	}
}
