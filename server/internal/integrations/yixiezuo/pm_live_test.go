//go:build yixiezuointegration

package yixiezuo

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// This opt-in test queries the configured DJ01 source and never connects to
// Multica, writes a source issue, or executes an agent.
func TestPMLiveSourceReadOnly(t *testing.T) {
	if os.Getenv("MULTICA_RUN_YIXIEZUO_READ_SMOKE") != "1" {
		t.Skip("set MULTICA_RUN_YIXIEZUO_READ_SMOKE=1 to authorize source account access")
	}
	scope := PMScope{SourceHost: "dj01.pm.netease.com", SourceProject: "dj01", AssigneeEmails: []string{"zengrui01@corp.netease.com", "zhououxiang@corp.netease.com"},
		TriageMention: "[@任务单分诊员](mention://agent/ee6f11fb-44c2-4226-a01d-1b0ba9921651)"}
	if err := scope.Normalize(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	d := NewPMReader(ExecOptions{}, scope)
	if err := d.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	sources, err := d.Discover(ctx)
	if err != nil {
		t.Fatalf("discovery: %v (read %d sources before failure)", err, len(sources))
	}
	t.Logf("instance=%s source_project=%s discovered=%d", scope.SourceHost, scope.SourceProject, len(sources))
	if len(sources) == 0 {
		return
	}
	// Exercise the independent detail reader used for closure reconciliation.
	d.issues = map[string]PMIssue{}
	item, err := d.ReadIssue(ctx, sources[0])
	if err != nil {
		t.Fatalf("detail source %s: %v", sources[0].ID, err)
	}
	receipt, err := json.Marshal(map[string]any{"source": item.Source, "status": item.Status, "priority": PMDisplay(item.Priority),
		"project": item.Project, "assignee": item.Assignee, "observed_at": item.ObservedAt})
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(receipt))
}
