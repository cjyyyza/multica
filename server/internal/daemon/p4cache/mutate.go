package p4cache

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	ActionEdit      = "edit"
	ActionAdd       = "add"
	ActionDelete    = "delete"
	ActionRevert    = "revert"
	ActionOpened    = "opened"
	ActionReconcile = "reconcile"
	ActionMove      = "move"
	ActionReopen    = "reopen"
	ActionChange    = "change"
	ActionShelve    = "shelve"
	ActionUnshelve  = "unshelve"
	ActionDescribe  = "describe"
	ActionFilelog   = "filelog"
)

const (
	// FilelogDefaultMax is enough precedent to identify a changelist without
	// dumping an entire ancient history. FilelogMaxMax is the hard cap.
	FilelogDefaultMax = 20
	FilelogMaxMax     = 200
)

// MutateResult is the daemon /p4/run response.
type MutateResult struct {
	Path       string `json:"path"`
	Client     string `json:"client"`
	Action     string `json:"action"`
	Changelist string `json:"changelist,omitempty"`
	Output     string `json:"output"`
}

var changeCreated = regexp.MustCompile(`(?i)Change\s+(\d+)\s+created`)

// ErrSubmitDisabled means a human administrator has not granted submission.
var ErrSubmitDisabled = errors.New("submit is disabled for this depot; a human workspace administrator must enable it in Perforce settings")

func buildChangeSpec(client, user, description string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Change: new\n")
	fmt.Fprintf(&b, "Client: %s\n", client)
	fmt.Fprintf(&b, "User: %s\n", user)
	fmt.Fprintf(&b, "Status: new\n")
	fmt.Fprintf(&b, "Description:\n")
	for _, line := range strings.Split(description, "\n") {
		fmt.Fprintf(&b, "\t%s\n", line)
	}
	return b.String()
}
