// Package p4depot is the shared Perforce depot identity used by the API,
// daemon allowlist, and CLI. It validates P4PORT / depot / stream refs and
// does not talk to a Perforce server.
package p4depot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Ref is the stored and claim-wire shape for one Perforce depot.
// Credentials stay on the daemon host (P4USER, P4TICKETS, P4CONFIG);
// User is only a P4USER hint when the host default should not be used.
type Ref struct {
	Port        string `json:"port"`
	Depot       string `json:"depot"`
	Stream      string `json:"stream,omitempty"`
	User        string `json:"user,omitempty"`
	Charset     string `json:"charset,omitempty"`
	Changelist  string `json:"changelist,omitempty"`
	Description string `json:"description,omitempty"`
}

var (
	portPrefix = regexp.MustCompile(`(?i)^(ssl|ssl4|ssl6|tcp|tcp4|tcp6):`)
	hostPort   = regexp.MustCompile(`^(\[[0-9a-fA-F:.]+\]|[A-Za-z0-9._-]+)(:[0-9]{1,5})?$`)
	depotPath  = regexp.MustCompile(`^//[A-Za-z0-9][A-Za-z0-9_.-]*(/[^ \t\n]*)?$`)
	tokenName  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	changelist = regexp.MustCompile(`^(#head|[0-9]+|@?[A-Za-z0-9._-]+)$`)
)

// Identity is the allowlist key: port + depot + stream. User and changelist
// are overlays, not part of which depot is configured.
func Identity(r Ref) string {
	return strings.ToLower(strings.TrimSpace(r.Port)) + "\n" +
		strings.TrimSpace(r.Depot) + "\n" +
		strings.TrimSpace(r.Stream)
}

// ClientName is a Helix-safe P4CLIENT for one task depot, under 64 characters.
func ClientName(workspaceID, taskID string, r Ref) string {
	sum := sha256.Sum256([]byte(workspaceID + "\n" + taskID + "\n" + Identity(r)))
	return "mc" + hex.EncodeToString(sum[:8])
}

// Normalize trims fields and rejects values the daemon cannot sync.
func Normalize(r Ref) (Ref, error) {
	r.Port = strings.TrimSpace(r.Port)
	r.Depot = strings.TrimSpace(r.Depot)
	r.Stream = strings.TrimSpace(r.Stream)
	r.User = strings.TrimSpace(r.User)
	r.Charset = strings.TrimSpace(r.Charset)
	r.Changelist = strings.TrimSpace(r.Changelist)
	r.Description = strings.TrimSpace(r.Description)

	if r.Port == "" {
		return Ref{}, fmt.Errorf("port is required")
	}
	if !ValidPort(r.Port) {
		return Ref{}, fmt.Errorf("port must be a P4PORT (host, host:port, or ssl:host:port)")
	}
	if r.Depot == "" {
		return Ref{}, fmt.Errorf("depot is required")
	}
	if !ValidDepot(r.Depot) {
		return Ref{}, fmt.Errorf("depot must be a Perforce depot path starting with //")
	}
	if r.Stream != "" && !ValidDepot(r.Stream) {
		return Ref{}, fmt.Errorf("stream must be a Perforce stream path starting with //")
	}
	if r.User != "" && !tokenName.MatchString(r.User) {
		return Ref{}, fmt.Errorf("user must be a P4USER name")
	}
	if r.Charset != "" && !tokenName.MatchString(r.Charset) {
		return Ref{}, fmt.Errorf("charset must be a P4CHARSET value")
	}
	if r.Changelist != "" && !ValidChangelist(r.Changelist) {
		return Ref{}, fmt.Errorf("changelist must be a changelist number, label, or #head")
	}
	return r, nil
}

// ValidPort accepts common P4PORT forms. HTTP URLs are rejected so a Git
// remote cannot be stored as a depot by mistake.
func ValidPort(s string) bool {
	if s == "" {
		return false
	}
	if strings.Contains(s, "://") {
		return false
	}
	if strings.HasPrefix(strings.ToLower(s), "rsh:") {
		return len(strings.TrimSpace(s[4:])) > 0
	}
	if strings.ContainsAny(s, " \t\n") {
		return false
	}
	rest := s
	if loc := portPrefix.FindStringIndex(s); loc != nil {
		rest = s[loc[1]:]
	}
	return hostPort.MatchString(rest)
}

// ValidDepot accepts //depot or //depot/path, including a trailing /...
func ValidDepot(s string) bool {
	return depotPath.MatchString(s)
}

// ValidChangelist accepts a number, #head, or a label (with or without @).
func ValidChangelist(s string) bool {
	return changelist.MatchString(s)
}

// ParseJSON decodes and normalizes a depot ref.
func ParseJSON(raw []byte) (Ref, error) {
	var r Ref
	if err := json.Unmarshal(raw, &r); err != nil {
		return Ref{}, fmt.Errorf("invalid perforce_depot payload: %w", err)
	}
	return Normalize(r)
}

// MarshalJSON encodes a normalized ref.
func MarshalJSON(r Ref) ([]byte, error) {
	return json.Marshal(r)
}

// SyncPath is the depot path passed to `p4 sync`. A directory depot gets /...
// when the caller did not already specify a wildcard or a file.
func SyncPath(depot, changelistNum string) string {
	depot = strings.TrimSpace(depot)
	if depot == "" {
		return depot
	}
	last := depot
	if i := strings.LastIndex(depot, "/"); i >= 0 {
		last = depot[i+1:]
	}
	if !strings.HasSuffix(depot, "/...") && !strings.Contains(last, ".") {
		depot += "/..."
	}
	cl := strings.TrimSpace(changelistNum)
	if cl == "" {
		return depot
	}
	if strings.HasPrefix(cl, "@") || strings.HasPrefix(cl, "#") {
		return depot + cl
	}
	return depot + "@" + cl
}

// ViewMaps returns the left-hand depot mapping and the client-relative path
// used in a classic (non-stream) client spec View line.
func ViewMaps(depot string) (depotSide, clientRel string) {
	depotSide = SyncPath(depot, "")
	if strings.HasPrefix(depotSide, "//") {
		clientRel = depotSide[2:]
	} else {
		clientRel = depotSide
	}
	return depotSide, clientRel
}
