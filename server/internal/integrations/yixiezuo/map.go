package yixiezuo

import (
	"strings"
	"time"
)

// Card is the Multica-side snapshot of one 易协作 issue.
type Card struct {
	ExternalID  string
	Title       string
	Description string
	StatusName  string
	Priority    string
	StartDate   string
	DueDate     string
	UpdatedAt   time.Time
}

// IssueFields is the Multica issue slice the sync engine reads and writes.
type IssueFields struct {
	Title       string
	Description string
	Status      string
	Priority    string
	StartDate   string
	DueDate     string
	UpdatedAt   time.Time
	Revision    int64
}

// DefaultStatusMap is used when a connection has no explicit mapping.
// Keys are 易协作 status names observed on NetEase PM boards.
var DefaultStatusMap = map[string]string{
	"新建":   "todo",
	"待处理":  "todo",
	"待开始":  "todo",
	"打开":   "todo",
	"重新打开": "todo",
	"开发中":  "in_progress",
	"进行中":  "in_progress",
	"待验证":  "in_review",
	"阻塞":   "blocked",
	"已解决":  "done",
	"已关闭":  "done",
	"完成":   "done",
	"关闭":   "done",
	"已拒绝":  "cancelled",
	"已取消":  "cancelled",
}

var defaultPriorityMap = map[string]string{
	"紧急":     "urgent",
	"urgent": "urgent",
	"高":      "high",
	"high":   "high",
	"中":      "medium",
	"普通":     "medium",
	"normal": "medium",
	"medium": "medium",
	"低":      "low",
	"low":    "low",
	"none":   "none",
	"无":      "none",
}

// MergeStatusMap overlays configured pairs on DefaultStatusMap.
func MergeStatusMap(configured map[string]string) map[string]string {
	out := make(map[string]string, len(DefaultStatusMap)+len(configured))
	for k, v := range DefaultStatusMap {
		out[normalize(k)] = strings.TrimSpace(v)
	}
	for k, v := range configured {
		key, val := normalize(k), strings.TrimSpace(v)
		if key == "" || val == "" {
			continue
		}
		out[key] = val
	}
	return out
}

// MapIncomingStatus converts an 易协作 status name to a Multica status key.
// catalogNames maps Multica status key → display name (and the reverse is
// tried against the incoming name). Unknown names return fallback.
func MapIncomingStatus(name string, configured map[string]string, catalogKeys, catalogNames []string, fallback string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fallback
	}
	norm := normalize(trimmed)
	merged := MergeStatusMap(configured)
	if key, ok := merged[norm]; ok {
		return key
	}
	for _, key := range catalogKeys {
		if normalize(key) == norm {
			return key
		}
	}
	for i, display := range catalogNames {
		if normalize(display) == norm && i < len(catalogKeys) {
			return catalogKeys[i]
		}
	}
	return fallback
}

var preferredOutgoing = []struct {
	name string
	key  string
}{
	{"新建", "todo"},
	{"开发中", "in_progress"},
	{"待验证", "in_review"},
	{"阻塞", "blocked"},
	{"已解决", "done"},
	{"已拒绝", "cancelled"},
}

// MapOutgoingStatus converts a Multica status key to an 易协作 status name.
func MapOutgoingStatus(statusKey string, configured map[string]string) string {
	key := strings.TrimSpace(statusKey)
	if key == "" {
		return key
	}
	for name, mapped := range configured {
		if strings.TrimSpace(mapped) == key {
			return strings.TrimSpace(name)
		}
	}
	for _, pair := range preferredOutgoing {
		if pair.key == key {
			return pair.name
		}
	}
	return key
}

// ResolveOutgoingStatusName picks a 易协作 status name that exists in catalog.
func ResolveOutgoingStatusName(statusKey string, configured map[string]string, catalogNames []string) string {
	name := MapOutgoingStatus(statusKey, configured)
	if len(catalogNames) == 0 || catalogHasName(catalogNames, name) {
		return name
	}
	merged := MergeStatusMap(configured)
	for alias, mapped := range merged {
		if mapped == strings.TrimSpace(statusKey) && catalogHasName(catalogNames, alias) {
			return alias
		}
	}
	return name
}

func catalogHasName(names []string, want string) bool {
	norm := normalize(want)
	for _, name := range names {
		if normalize(name) == norm {
			return true
		}
	}
	return false
}

// MapIncomingPriority maps an 易协作 priority name onto Multica's closed set.
func MapIncomingPriority(name, fallback string) string {
	if key, ok := defaultPriorityMap[normalize(name)]; ok {
		return key
	}
	return fallback
}

// MapOutgoingPriority maps a Multica priority onto an 易协作 priority name.
func MapOutgoingPriority(priority string) string {
	switch strings.TrimSpace(priority) {
	case "urgent":
		return "紧急"
	case "high":
		return "高"
	case "medium":
		return "中"
	case "low":
		return "低"
	default:
		return strings.TrimSpace(priority)
	}
}

// PreferRemote reports whether an inbound card should overwrite the local issue.
// Dirty local edits win when they are strictly newer than the remote card.
func PreferRemote(remote, local time.Time, dirty bool) bool {
	if !dirty {
		return true
	}
	if remote.IsZero() {
		return false
	}
	if local.IsZero() {
		return true
	}
	return !remote.Before(local)
}

func normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
