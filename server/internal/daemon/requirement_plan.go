package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/taskpolicy"
)

type requirementPlanContext struct {
	Snapshot json.RawMessage
	Revision int64
}

func requirementPlanPath(task Task) string {
	return "/api/workspaces/" + url.PathEscape(task.WorkspaceID) + "/requirements/" + url.PathEscape(task.RequirementID)
}

// Capture the revision before planning. A concurrent human edit must invalidate
// the candidate rather than being silently overwritten after the model returns.
func (c *Client) loadRequirementPlanContext(ctx context.Context, task Task) (*requirementPlanContext, error) {
	if !taskpolicy.IsPlan(task.Mode) {
		return nil, nil
	}
	token, err := taskScopedAuthToken(task)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := c.getJSONWithToken(ctx, requirementPlanPath(task), token, &raw); err != nil {
		return nil, fmt.Errorf("load planning requirement: %w", err)
	}
	var detail struct {
		ID, WorkspaceID, AgentID, ChatSessionID string
		IssueID                                 *string
		Revision                                int64
	}
	if err := json.Unmarshal(raw, &detail); err != nil {
		return nil, fmt.Errorf("decode planning requirement: %w", err)
	}
	if detail.ID != task.RequirementID || detail.WorkspaceID != task.WorkspaceID || detail.AgentID != task.AgentID || detail.ChatSessionID != task.ChatSessionID || detail.IssueID != nil || detail.Revision <= 0 {
		return nil, errors.New("planning requirement does not match the claimed task")
	}
	return &requirementPlanContext{Snapshot: raw, Revision: detail.Revision}, nil
}

func decodeRequirementPlanOutput(raw json.RawMessage) (string, json.RawMessage, error) {
	var output struct {
		Reply     string          `json:"reply"`
		Candidate json.RawMessage `json:"candidate"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		return "", nil, fmt.Errorf("invalid structured Plan output: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return "", nil, errors.New("structured Plan output must contain one JSON object")
	}
	reply := strings.TrimSpace(output.Reply)
	candidate := bytes.TrimSpace(output.Candidate)
	if reply == "" || len(candidate) == 0 {
		return "", nil, errors.New("structured Plan output requires reply and candidate")
	}
	if bytes.Equal(candidate, []byte("null")) {
		return reply, nil, nil
	}
	if candidate[0] != '{' {
		return "", nil, errors.New("Plan candidate must be an object or null")
	}
	return reply, candidate, nil
}

// The model supplies data only. The daemon submits with the same task token,
// revision and idempotency key on every retry, including a lost success response.
func (c *Client) submitRequirementPlanOutput(ctx context.Context, task Task, plan *requirementPlanContext, raw json.RawMessage) (string, error) {
	reply, candidate, err := decodeRequirementPlanOutput(raw)
	if err != nil {
		return "", err
	}
	if candidate == nil {
		return reply, nil
	}
	if plan == nil || !taskpolicy.IsPlan(task.Mode) || plan.Revision <= 0 {
		return "", errors.New("candidate requires a captured Plan requirement")
	}
	token, err := taskScopedAuthToken(task)
	if err != nil {
		return "", err
	}
	body := struct {
		ExpectedRevision int64           `json:"expectedRevision"`
		Snapshot         json.RawMessage `json:"snapshot"`
	}{plan.Revision, candidate}
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	key := fmt.Sprintf("plan:%s:%x", task.ID, sha256.Sum256(encoded))
	for attempt := 0; ; attempt++ {
		err := c.postJSONWithTokenAndKey(ctx, requirementPlanPath(task)+"/candidates", token, key, body, nil)
		if err == nil {
			return reply, nil
		}
		if attempt == 2 || !isTransientError(err) {
			return "", fmt.Errorf("save Plan candidate: %w", err)
		}
		if waitErr := retrySleep(ctx, time.Duration(attempt+1)*time.Second); waitErr != nil {
			return "", waitErr
		}
	}
}
