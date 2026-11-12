package protocol

const DaemonCapabilityP4ContentV1 = "p4-content-v1"

type P4ChangelistFile struct {
	DepotPath string `json:"depot_path"`
	Action    string `json:"action"`
	Type      string `json:"type"`
	Revision  int    `json:"revision"`
}

// P4ChangelistContent is a live read, never a persisted copy of a workspace.
type P4ChangelistContent struct {
	Source      string             `json:"source"`
	Files       []P4ChangelistFile `json:"files"`
	File        string             `json:"file"`
	Diff        string             `json:"diff"`
	Binary      bool               `json:"binary"`
	Truncated   bool               `json:"truncated"`
	Unavailable string             `json:"unavailable"`
}

type DaemonHeartbeatPendingP4Content struct {
	ID string `json:"id"`
}
