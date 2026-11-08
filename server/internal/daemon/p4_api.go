package daemon

import (
	"errors"
	"net/http"
	"time"
)

// These receipt types drain reports persisted by earlier daemons and carry
// explicit changelist links created through the Multica integration.
type p4Warning struct {
	Kind  string   `json:"kind"`
	Files []string `json:"files"`
}

type p4ChangelistReport struct {
	FencePolls     bool               `json:"fence_polls"`
	SubmittedAt    string             `json:"submitted_at,omitempty"`
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

type p4SwarmReviewBody struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	State       string `json:"state"`
	Author      string `json:"author"`
	Description string `json:"description"`
}

func isNotFound(err error) bool {
	var reqErr *requestError
	return errors.As(err, &reqErr) && reqErr.StatusCode == http.StatusNotFound
}

func p4SubmittedAt(submittedAt time.Time) string {
	return submittedAt.UTC().Format(time.RFC3339)
}
