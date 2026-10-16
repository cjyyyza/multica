package yixiezuo

import (
	"fmt"
	"strings"
	"time"
)

type PMFailure struct {
	Stage      string `json:"stage"`
	ExternalID string `json:"external_id"`
	Error      string `json:"error"`
}

type PMReport struct {
	ScannedAt     time.Time        `json:"scanned_at"`
	Scope         PMScope          `json:"scope"`
	Discovered    int              `json:"discovered"`
	Reconciled    int              `json:"reconciled"`
	TriageScanned int              `json:"triage_scanned"`
	Results       []PMResult       `json:"results"`
	Triage        []PMTriageResult `json:"triage"`
	Errors        []PMFailure      `json:"errors"`
}

func (r PMReport) HasFailures() bool {
	if len(r.Errors) > 0 {
		return true
	}
	for _, result := range r.Results {
		if result.State == "failed" {
			return true
		}
	}
	for _, result := range r.Triage {
		if result.State == "failed" {
			return true
		}
	}
	return false
}

func (r PMReport) Markdown() string {
	var b strings.Builder
	clock := "缺失"
	if !r.ScannedAt.IsZero() {
		clock = r.ScannedAt.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02 15:04:05 -07:00")
	}
	fmt.Fprintf(&b, "# PM 同步报告\n\n扫描时间：%s\n\n查询范围：%s / %s；类型：%s；指派给：%s；Multica 项目：%s\n\n本轮单数：来源发现 %d，关闭对账 %d，未分类扫描 %d\n", clock, pmReportText(r.Scope.SourceHost), pmReportText(r.Scope.SourceProject), pmReportText(r.Scope.IssueType), pmReportText(strings.Join(r.Scope.AssigneeEmails, ", ")), pmReportText(r.Scope.ProjectID), r.Discovered, r.Reconciled, r.TriageScanned)
	changed := false
	for _, result := range r.Results {
		if result.State == "created" || result.State == "updated" {
			changed = true
		}
	}
	for _, result := range r.Triage {
		if result.State == "notified" {
			changed = true
		}
	}
	if !changed && !r.HasFailures() {
		b.WriteString("\n本轮无变化。\n")
	}
	b.WriteString("\n## 新增同步\n")
	count := 0
	for _, result := range r.Results {
		if result.State == "created" {
			fmt.Fprintf(&b, "\n- 外部单号：%s；Multica：%s；负责人：%s\n", pmReportText(result.ExternalID), pmReportLink(result), pmReportText(result.Assignee))
			count++
		}
	}
	if count == 0 {
		b.WriteString("\n无。\n")
	}
	b.WriteString("\n## 更新\n")
	count = 0
	for _, result := range r.Results {
		if result.State == "updated" {
			changes := []string{}
			for _, change := range result.Changes {
				changes = append(changes, pmReportText(change.Field)+"："+pmReportText(change.Old)+" → "+pmReportText(change.New))
			}
			fmt.Fprintf(&b, "\n- 外部单号：%s；Multica：%s；%s\n", pmReportText(result.ExternalID), pmReportLink(result), strings.Join(changes, "；"))
			count++
		}
	}
	if count == 0 {
		b.WriteString("\n无。\n")
	}
	b.WriteString("\n## 未分类转交\n")
	count = 0
	for _, result := range r.Triage {
		if result.State == "notified" {
			fmt.Fprintf(&b, "\n- %s：已通知；评论 ID：%s\n", pmReportText(pmTriageIdentifier(result)), pmReportText(result.CommentID))
			count++
		}
		if result.State == "failed" {
			fmt.Fprintf(&b, "\n- %s：失败；%s\n", pmReportText(pmTriageIdentifier(result)), pmReportText(firstNonEmpty(result.Error, result.ReasonCode)))
			count++
		}
	}
	if count == 0 {
		b.WriteString("\n无。\n")
	}
	b.WriteString("\n## 异常与待人工确认\n")
	count = 0
	for _, failure := range r.Errors {
		fmt.Fprintf(&b, "\n- %s；单号：%s；%s\n", pmReportText(failure.Stage), pmReportText(failure.ExternalID), pmReportText(failure.Error))
		count++
	}
	for _, result := range r.Results {
		if result.State == "failed" {
			fmt.Fprintf(&b, "\n- 外部单号：%s；Multica 单号：%s；%s\n", pmReportText(result.ExternalID), pmReportText(firstNonEmpty(result.Identifier, result.IssueID)), pmReportText(result.Error))
			count++
		}
		for _, warning := range result.Warnings {
			fmt.Fprintf(&b, "\n- 外部单号：%s；%s\n", pmReportText(result.ExternalID), pmReportText(warning))
			count++
		}
	}
	if count == 0 {
		b.WriteString("\n无。\n")
	}
	return b.String()
}

// Remote fields are summary data, not executable mentions or report sections.
// Structured JSON receipts keep the original values unchanged.
func pmReportText(value string) string {
	return strings.NewReplacer("[", "［", "]", "］", "\r", `\r`, "\n", `\n`).Replace(PMDisplay(value))
}

func pmReportLink(result PMResult) string {
	if result.URL == "" {
		return "缺失"
	}
	label := result.Identifier
	if label == "" {
		label = result.IssueID
	}
	return "[" + pmReportText(label) + "](" + result.URL + ")"
}

func pmTriageIdentifier(result PMTriageResult) string {
	if result.Identifier != "" {
		return result.Identifier
	}
	return PMDisplay(result.IssueID)
}
