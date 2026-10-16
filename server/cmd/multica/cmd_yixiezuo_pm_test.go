package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/integrations/yixiezuo"
	"github.com/spf13/cobra"
)

type fakePMReader struct{ reads map[string]int }

func (d *fakePMReader) Prepare(context.Context) error { return nil }
func (d *fakePMReader) Discover(context.Context) ([]yixiezuo.Source, error) {
	return []yixiezuo.Source{fakePMSource("1"), fakePMSource("2")}, nil
}

func TestPMInvalidOutputReturnsReportBeforeAnyWrite(t *testing.T) {
	scope := yixiezuo.PMScope{SourceHost: "dj01.pm.netease.com", SourceProject: "dj01", AssigneeEmails: []string{"owner@corp.netease.com"},
		ProjectID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", TriageMention: "[@任务单分诊员](mention://agent/ee6f11fb-44c2-4226-a01d-1b0ba9921651)"}
	raw, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pm.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.Flags().String("config-file", path, "")
	cmd.Flags().String("output", "yaml", "")
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runYixiezuoPMSync(cmd, nil); err != errSilent || !strings.Contains(out.String(), "output must be markdown or json") || !strings.Contains(out.String(), "异常与待人工确认") {
		t.Fatalf("error=%v report=%s", err, out.String())
	}
}

func TestPMCycleRejectsMalformedReceiptsAndCandidateIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/reconcile"):
			json.NewEncoder(w).Encode(map[string]any{"sources": []any{}, "errors": []any{}})
		case strings.HasSuffix(r.URL.Path, "/issues"):
			var req yixiezuo.PMSourceRequest
			json.NewDecoder(r.Body).Decode(&req)
			json.NewEncoder(w).Encode(yixiezuo.PMResult{ExternalID: req.Issue.Source.ID, State: "updated"})
		case strings.HasSuffix(r.URL.Path, "/triage/candidates"):
			json.NewEncoder(w).Encode(map[string]any{"issues": []map[string]string{{"id": "../publish", "identifier": "MUL-1"}}})
		default:
			t.Errorf("malformed candidate dispatched: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	r := runPMCycle(context.Background(), cli.NewAPIClient(server.URL, "workspace", "fake-token"), &fakePMReader{reads: map[string]int{}}, yixiezuo.PMReport{})
	if !r.HasFailures() || len(r.Errors) != 3 || len(r.Results) != 0 || len(r.Triage) != 0 {
		t.Fatalf("report=%+v", r)
	}
}
func fakePMSource(id string) yixiezuo.Source {
	return yixiezuo.Source{Host: "dj01.pm.netease.com", ID: id, URL: "https://dj01.pm.netease.com/issues/" + id}
}
func (d *fakePMReader) ReadIssue(_ context.Context, s yixiezuo.Source) (yixiezuo.PMIssue, error) {
	d.reads[s.ID]++
	if s.ID == "3" {
		return yixiezuo.PMIssue{}, fmt.Errorf("source unavailable")
	}
	return yixiezuo.PMIssue{Source: s, Title: "source", Status: "新建", ObservedAt: time.Now().UTC()}, nil
}

func TestPMCycleDeduplicatesSourcesAndReportsPartialFailures(t *testing.T) {
	const issueID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	writes := map[string]int{}
	triageScanned := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/reconcile"):
			json.NewEncoder(w).Encode(map[string]any{"sources": []yixiezuo.Source{fakePMSource("2"), fakePMSource("3")}, "errors": []any{}, "has_more": false})
		case strings.HasSuffix(path, "/issues"):
			var req yixiezuo.PMSourceRequest
			json.NewDecoder(r.Body).Decode(&req)
			writes[req.Issue.Source.ID]++
			if req.Issue.Source.ID == "2" {
				http.Error(w, "database write failed", 500)
				return
			}
			json.NewEncoder(w).Encode(yixiezuo.PMResult{ExternalID: req.Issue.Source.ID, IssueID: issueID, Identifier: "MUL-1", State: "created", Assignee: "未分配"})
		case strings.HasSuffix(path, "/triage/candidates"):
			triageScanned = true
			json.NewEncoder(w).Encode(map[string]any{"issues": []map[string]string{{"id": issueID, "identifier": "MUL-1"}}, "has_more": false})
		case strings.Contains(path, "/triage/"):
			if !triageScanned {
				t.Error("dispatch before candidate scan")
			}
			json.NewEncoder(w).Encode(yixiezuo.PMTriageResult{IssueID: issueID, Identifier: "MUL-1", State: "failed", Error: "invocation_not_allowed", ReasonCode: "invocation_not_allowed"})
		default:
			t.Errorf("unexpected endpoint: %s", path)
			http.Error(w, "unexpected", 500)
		}
	}))
	defer server.Close()
	d := &fakePMReader{reads: map[string]int{}}
	report := runPMCycle(context.Background(), cli.NewAPIClient(server.URL, "workspace", "fake-token"), d, yixiezuo.PMReport{ScannedAt: time.Now().UTC()})
	if writes["1"] != 1 || writes["2"] != 1 || writes["3"] != 0 || d.reads["2"] != 1 {
		t.Fatalf("writes=%v reads=%v", writes, d.reads)
	}
	if !report.HasFailures() || len(report.Errors) != 2 || len(report.Triage) != 1 {
		t.Fatalf("report=%+v", report)
	}
	for _, want := range []string{"source unavailable", "invocation_not_allowed", "结果可能不确定"} {
		if !strings.Contains(report.Markdown(), want) {
			t.Errorf("report missing %s", want)
		}
	}
}

func TestPMCycleCollectsAllReconcilePagesBeforeClosing(t *testing.T) {
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/reconcile"):
			reads++
			json.NewEncoder(w).Encode(map[string]any{"sources": []yixiezuo.Source{fakePMSource(fmt.Sprint(reads))}, "errors": []any{}, "has_more": reads == 1, "next_offset": reads})
		case strings.HasSuffix(r.URL.Path, "/issues"):
			if reads != 2 {
				t.Errorf("writes began after only %d pages", reads)
			}
			var req yixiezuo.PMSourceRequest
			json.NewDecoder(r.Body).Decode(&req)
			json.NewEncoder(w).Encode(yixiezuo.PMResult{ExternalID: req.Issue.Source.ID, IssueID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", State: "unchanged"})
		case strings.HasSuffix(r.URL.Path, "/triage/candidates"):
			json.NewEncoder(w).Encode(map[string]any{"issues": []any{}, "has_more": false})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	defer server.Close()
	r := runPMCycle(context.Background(), cli.NewAPIClient(server.URL, "workspace", "fake-token"), &fakePMReader{reads: map[string]int{}}, yixiezuo.PMReport{})
	if r.HasFailures() || r.Reconciled != 2 {
		t.Fatalf("report=%+v", r)
	}
}
