package p4cache

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/p4depot"
)

const swarmTimeout = 30 * time.Second

// SwarmParams is one Helix Swarm API call made with the host P4 ticket.
type SwarmParams struct {
	WorkspaceID string
	TaskID      string
	WorkDir     string
	Ref         p4depot.Ref
	Action      string
	Changelist  string
	ReviewID    string
	Body        string
	Description string
	Reviewers   []string
	Shelve      bool
}

// SwarmReview is the agent-facing subset of a Swarm review.
type SwarmReview struct {
	ID          int    `json:"id"`
	Author      string `json:"author,omitempty"`
	State       string `json:"state,omitempty"`
	Description string `json:"description,omitempty"`
	Changes     []int  `json:"changes,omitempty"`
	URL         string `json:"url,omitempty"`
}

// SwarmResult is the daemon /p4/swarm response.
type SwarmResult struct {
	Action   string        `json:"action"`
	SwarmURL string        `json:"swarm_url"`
	Reviews  []SwarmReview `json:"reviews,omitempty"`
	Review   *SwarmReview  `json:"review,omitempty"`
	Output   string        `json:"output,omitempty"`
}

const (
	SwarmList    = "reviews"
	SwarmCreate  = "create"
	SwarmShow    = "show"
	SwarmComment = "comment"
)

// Swarm talks to Helix Swarm using the host Helix ticket. Multica does not
// store a Swarm password.
func (c *Cache) Swarm(ctx context.Context, params SwarmParams) (*SwarmResult, error) {
	ref, err := p4depot.Normalize(params.Ref)
	if err != nil {
		return nil, err
	}
	action := strings.ToLower(strings.TrimSpace(params.Action))
	switch action {
	case SwarmList, SwarmCreate, SwarmShow, SwarmComment:
	default:
		return nil, fmt.Errorf("unsupported swarm action %q", params.Action)
	}
	swarmURL := strings.TrimRight(strings.TrimSpace(ref.SwarmURL), "/")
	if swarmURL == "" {
		return nil, fmt.Errorf("swarm_url is not configured for this depot")
	}
	if !p4depot.ValidSwarmURL(swarmURL) {
		return nil, fmt.Errorf("swarm_url must be an http(s) Helix Swarm origin")
	}

	user, err := c.resolveUser(ctx, ref)
	if err != nil {
		return nil, err
	}
	ticket, err := c.helixTicket(ctx, ref, user)
	if err != nil {
		return nil, err
	}

	var shelveErr error
	if action == SwarmCreate && params.Shelve {
		cl := strings.TrimSpace(params.Changelist)
		if cl == "" {
			return nil, fmt.Errorf("create requires --changelist")
		}
		if strings.TrimSpace(params.WorkDir) != "" && strings.TrimSpace(params.TaskID) != "" {
			if _, err := c.Mutate(ctx, MutateParams{
				WorkspaceID: params.WorkspaceID,
				TaskID:      params.TaskID,
				WorkDir:     params.WorkDir,
				Ref:         ref,
				Action:      ActionShelve,
				Changelist:  cl,
				Force:       true,
			}); err != nil {
				// Pending CLs need a shelf; submitted CLs become post-commit
				// reviews without one. Try create either way.
				shelveErr = err
			}
		}
	}

	client := &swarmClient{
		baseURL: swarmURL,
		user:    user,
		ticket:  ticket,
		http:    c.httpClient(),
	}
	switch action {
	case SwarmList:
		reviews, err := client.listReviews(ctx, strings.TrimSpace(params.Changelist))
		if err != nil {
			return nil, err
		}
		return &SwarmResult{Action: action, SwarmURL: swarmURL, Reviews: reviews}, nil
	case SwarmCreate:
		cl := strings.TrimSpace(params.Changelist)
		if cl == "" {
			return nil, fmt.Errorf("create requires --changelist")
		}
		review, err := client.createReview(ctx, cl, params.Description, params.Reviewers)
		if err != nil {
			if shelveErr != nil {
				return nil, fmt.Errorf("%w (also failed to shelve changelist %s: %v)", err, cl, shelveErr)
			}
			return nil, err
		}
		return &SwarmResult{Action: action, SwarmURL: swarmURL, Review: review}, nil
	case SwarmShow:
		id := strings.TrimSpace(params.ReviewID)
		if id == "" {
			return nil, fmt.Errorf("show requires --review")
		}
		review, err := client.getReview(ctx, id)
		if err != nil {
			return nil, err
		}
		return &SwarmResult{Action: action, SwarmURL: swarmURL, Review: review}, nil
	case SwarmComment:
		id := strings.TrimSpace(params.ReviewID)
		body := strings.TrimSpace(params.Body)
		if id == "" || body == "" {
			return nil, fmt.Errorf("comment requires --review and --body")
		}
		if err := client.comment(ctx, id, body); err != nil {
			return nil, err
		}
		return &SwarmResult{Action: action, SwarmURL: swarmURL, Output: "comment posted"}, nil
	default:
		return nil, fmt.Errorf("unsupported swarm action %q", action)
	}
}

func (c *Cache) helixTicket(ctx context.Context, ref p4depot.Ref, user string) (string, error) {
	out, err := c.output(ctx, ref, user, "", "tickets")
	if err != nil {
		return "", fmt.Errorf("read Helix ticket (run `p4 login` on this machine): %w", err)
	}
	if ticket := ticketForPort(string(out), ref.Port, user); ticket != "" {
		return ticket, nil
	}
	return "", fmt.Errorf("no Helix ticket for %s (run `p4 login` on this machine)", ref.Port)
}

func ticketForPort(out, port, user string) string {
	host := ticketHost(port)
	user = strings.TrimSpace(user)
	for _, line := range strings.Split(out, "\n") {
		// `p4 tickets` prints "host:port (user) ticket", not the ticket file format.
		fields := strings.Fields(line)
		if len(fields) != 3 || !strings.HasPrefix(fields[1], "(") || !strings.HasSuffix(fields[1], ")") {
			continue
		}
		hostPart, gotUser, ticket := fields[0], fields[1][1:len(fields[1])-1], fields[2]
		if user != "" && gotUser != user {
			continue
		}
		if ticketHost(hostPart) == host || hostPart == port {
			return ticket
		}
	}
	return ""
}

func ticketHost(port string) string {
	port = strings.TrimSpace(port)
	if loc := strings.Index(port, ":"); loc >= 0 {
		prefix := strings.ToLower(port[:loc])
		switch prefix {
		case "ssl", "ssl4", "ssl6", "tcp", "tcp4", "tcp6":
			port = port[loc+1:]
		}
	}
	return strings.ToLower(port)
}

type swarmClient struct {
	baseURL string
	user    string
	ticket  string
	http    *http.Client
}

func (c *swarmClient) listReviews(ctx context.Context, changelist string) ([]SwarmReview, error) {
	q := url.Values{}
	q.Set("max", "50")
	if changelist != "" {
		q.Set("change", changelist)
	}
	raw, err := c.do(ctx, http.MethodGet, "/reviews?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	return parseReviewList(c.baseURL, raw)
}

func (c *swarmClient) createReview(ctx context.Context, changelist, description string, reviewers []string) (*SwarmReview, error) {
	body := map[string]any{"change": changelist}
	if strings.TrimSpace(description) != "" {
		body["description"] = description
	}
	if cleaned := compactStrings(reviewers); len(cleaned) > 0 {
		body["reviewers"] = cleaned
	}
	raw, err := c.do(ctx, http.MethodPost, "/reviews", body)
	if err != nil {
		return nil, err
	}
	return parseReview(c.baseURL, raw)
}

func (c *swarmClient) getReview(ctx context.Context, id string) (*SwarmReview, error) {
	raw, err := c.do(ctx, http.MethodGet, "/reviews/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	return parseReview(c.baseURL, raw)
}

func (c *swarmClient) comment(ctx context.Context, id, body string) error {
	payload := map[string]any{"body": body}
	if _, err := c.do(ctx, http.MethodPost, "/comments/reviews/"+url.PathEscape(id), payload); err != nil {
		return err
	}
	return nil
}

func (c *swarmClient) do(ctx context.Context, method, apiPath string, body any) (json.RawMessage, error) {
	runCtx, cancel := context.WithTimeout(ctx, swarmTimeout)
	defer cancel()
	var lastErr error
	for _, version := range []string{"v11", "v9"} {
		raw, err := c.doVersion(runCtx, method, version, apiPath, body)
		if err == nil {
			return raw, nil
		}
		lastErr = err
		if !isSwarmMissing(err) {
			return nil, err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("swarm request failed")
	}
	return nil, lastErr
}

type swarmHTTPError struct {
	status int
	body   string
}

func (e swarmHTTPError) Error() string {
	if strings.TrimSpace(e.body) == "" {
		return fmt.Sprintf("swarm HTTP %d", e.status)
	}
	return fmt.Sprintf("swarm HTTP %d: %s", e.status, e.body)
}

func isSwarmMissing(err error) bool {
	httpErr, ok := err.(swarmHTTPError)
	return ok && (httpErr.status == http.StatusNotFound || httpErr.status == http.StatusMethodNotAllowed)
}

func (c *swarmClient) doVersion(ctx context.Context, method, version, apiPath string, body any) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	endpoint := strings.TrimRight(c.baseURL, "/") + "/api/" + version + apiPath
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.ticket)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, swarmHTTPError{status: resp.StatusCode, body: strings.TrimSpace(string(payload))}
	}
	return json.RawMessage(payload), nil
}

func parseReviewList(baseURL string, raw json.RawMessage) ([]SwarmReview, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("parse swarm reviews: %w", err)
	}
	if data, ok := envelope["data"]; ok {
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(data, &inner); err == nil {
			if list, ok := inner["reviews"]; ok {
				return decodeReviews(baseURL, list)
			}
		}
	}
	if list, ok := envelope["reviews"]; ok {
		return decodeReviews(baseURL, list)
	}
	return nil, nil
}

func parseReview(baseURL string, raw json.RawMessage) (*SwarmReview, error) {
	if reviews, err := parseReviewList(baseURL, raw); err == nil && len(reviews) == 1 {
		review := reviews[0]
		return &review, nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("parse swarm review: %w", err)
	}
	for _, key := range []string{"review", "data"} {
		if item, ok := envelope[key]; ok {
			if review, ok := decodeOneReview(baseURL, item); ok {
				return review, nil
			}
			var inner map[string]json.RawMessage
			if err := json.Unmarshal(item, &inner); err == nil {
				if nested, ok := inner["review"]; ok {
					if review, ok := decodeOneReview(baseURL, nested); ok {
						return review, nil
					}
				}
			}
		}
	}
	if review, ok := decodeOneReview(baseURL, raw); ok && review.ID != 0 {
		return review, nil
	}
	return nil, fmt.Errorf("swarm response did not include a review")
}

func decodeReviews(baseURL string, raw json.RawMessage) ([]SwarmReview, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("parse swarm review list: %w", err)
	}
	out := make([]SwarmReview, 0, len(items))
	for _, item := range items {
		if review, ok := decodeOneReview(baseURL, item); ok {
			out = append(out, *review)
		}
	}
	return out, nil
}

func decodeOneReview(baseURL string, raw json.RawMessage) (*SwarmReview, bool) {
	var parsed struct {
		ID          any    `json:"id"`
		Author      string `json:"author"`
		State       string `json:"state"`
		Description string `json:"description"`
		Changes     []int  `json:"changes"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, false
	}
	id := anyToInt(parsed.ID)
	if id == 0 {
		return nil, false
	}
	review := &SwarmReview{
		ID:          id,
		Author:      parsed.Author,
		State:       parsed.State,
		Description: parsed.Description,
		Changes:     parsed.Changes,
		URL:         strings.TrimRight(baseURL, "/") + "/reviews/" + strconv.Itoa(id),
	}
	return review, true
}

func anyToInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(n))
		return i
	default:
		return 0
	}
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
