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

// MapIncomingPriority maps an 易协作 priority name onto Multica's closed set.
func MapIncomingPriority(name, fallback string) string {
	if key, ok := defaultPriorityMap[normalize(name)]; ok {
		return key
	}
	return fallback
}

func normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
