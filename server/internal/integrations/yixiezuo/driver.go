package yixiezuo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Driver is the local 易协作 CLI surface. The Multica server never implements this.
type Driver interface {
	ListCards(ctx context.Context) ([]Card, error)
	GetCard(ctx context.Context, externalID string) (Card, error)
	CreateCard(ctx context.Context, fields IssueFields, statusName string) (Card, error)
	UpdateCard(ctx context.Context, externalID string, fields IssueFields, statusName string) (Card, error)
}

// ExecOptions configure the inspected pm-cli issue commands.
type ExecOptions struct {
	Bin         string
	ListQueryID string
	LookPath    func(file string) (string, error)
	Command     func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// ExecDriver shells out to the local 易协作 CLI. Command names come from the
// published pm-cli issue surface (filter/query/get/create/update) — not invented
// popo verbs.
type ExecDriver struct {
	opts ExecOptions
}

func NewExecDriver(opts ExecOptions) *ExecDriver {
	if opts.Bin == "" {
		opts.Bin = "pm-cli"
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.Command == nil {
		opts.Command = exec.CommandContext
	}
	return &ExecDriver{opts: opts}
}

func (d *ExecDriver) resolveBin() (string, error) {
	path, err := d.opts.LookPath(d.opts.Bin)
	if err != nil {
		return "", fmt.Errorf("local 易协作 CLI %q is not on PATH; sync must run on a machine that can open 易协作: %w", d.opts.Bin, err)
	}
	return path, nil
}

func (d *ExecDriver) ListCards(ctx context.Context) ([]Card, error) {
	if d.opts.ListQueryID != "" {
		raw, err := d.run(ctx, "issue", "query", "--query-id", d.opts.ListQueryID, "--pretty")
		if err != nil {
			return nil, err
		}
		return parseCardList(raw)
	}
	var all []Card
	const page = 100
	for offset := 0; ; offset += page {
		raw, err := d.run(ctx, "issue", "filter",
			"--limit", strconv.Itoa(page),
			"--offset", strconv.Itoa(offset),
			"--pretty",
		)
		if err != nil {
			return nil, err
		}
		pageCards, err := parseCardList(raw)
		if err != nil {
			return nil, err
		}
		all = append(all, pageCards...)
		if len(pageCards) < page {
			return all, nil
		}
	}
}

func (d *ExecDriver) GetCard(ctx context.Context, externalID string) (Card, error) {
	raw, err := d.run(ctx, "issue", "get", externalID, "--stdout", "--raw")
	if err != nil {
		return Card{}, err
	}
	return parseOneCard(raw)
}

func (d *ExecDriver) CreateCard(ctx context.Context, fields IssueFields, statusName string) (Card, error) {
	args := []string{"issue", "create", "--subject", fields.Title, "--pretty"}
	if fields.Description != "" {
		args = append(args, "--description", fields.Description)
	}
	if fields.StartDate != "" {
		args = append(args, "--start-date", fields.StartDate)
	}
	if fields.DueDate != "" {
		args = append(args, "--due-date", fields.DueDate)
	}
	raw, err := d.run(ctx, args...)
	if err != nil {
		return Card{}, err
	}
	card, err := parseOneCard(raw)
	if err != nil {
		return Card{}, err
	}
	if statusName != "" && card.ExternalID != "" {
		updated, updateErr := d.UpdateCard(ctx, card.ExternalID, fields, statusName)
		if updateErr == nil {
			return updated, nil
		}
	}
	return card, nil
}

func (d *ExecDriver) UpdateCard(ctx context.Context, externalID string, fields IssueFields, statusName string) (Card, error) {
	args := []string{"issue", "update", externalID, "--pretty"}
	if fields.Title != "" {
		args = append(args, "--subject", fields.Title)
	}
	if fields.Description != "" {
		args = append(args, "--description", fields.Description)
	}
	if statusName != "" {
		args = append(args, "--status", statusName)
	}
	if fields.StartDate != "" {
		args = append(args, "--start-date", fields.StartDate)
	}
	if fields.DueDate != "" {
		args = append(args, "--due-date", fields.DueDate)
	}
	raw, err := d.run(ctx, args...)
	if err != nil {
		return Card{}, err
	}
	return parseOneCard(raw)
}

func (d *ExecDriver) run(ctx context.Context, args ...string) ([]byte, error) {
	bin, err := d.resolveBin()
	if err != nil {
		return nil, err
	}
	cmd := d.opts.Command(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("run %s %s: %w: %s", bin, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

type cliEnvelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   json.RawMessage `json:"error"`
}

type rawCard struct {
	ID          any             `json:"id"`
	Subject     string          `json:"subject"`
	Description string          `json:"description"`
	Status      json.RawMessage `json:"status"`
	Priority    json.RawMessage `json:"priority"`
	StartDate   string          `json:"start_date"`
	DueDate     string          `json:"due_date"`
	UpdatedOn   string          `json:"updated_on"`
}

func parseCardList(raw []byte) ([]Card, error) {
	data, err := unwrapCLI(raw)
	if err != nil {
		return nil, err
	}
	var rows []rawCard
	if err := json.Unmarshal(data, &rows); err != nil {
		var one rawCard
		if oneErr := json.Unmarshal(data, &one); oneErr != nil {
			return nil, fmt.Errorf("decode 易协作 issue list: %w", err)
		}
		card, cardErr := cardFromRaw(one)
		if cardErr != nil {
			return nil, cardErr
		}
		return []Card{card}, nil
	}
	out := make([]Card, 0, len(rows))
	for _, row := range rows {
		card, err := cardFromRaw(row)
		if err != nil {
			return nil, err
		}
		out = append(out, card)
	}
	return out, nil
}

func parseOneCard(raw []byte) (Card, error) {
	data, err := unwrapCLI(raw)
	if err != nil {
		return Card{}, err
	}
	var row rawCard
	if err := json.Unmarshal(data, &row); err != nil {
		return Card{}, fmt.Errorf("decode 易协作 issue: %w", err)
	}
	return cardFromRaw(row)
}

func unwrapCLI(raw []byte) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("易协作 CLI returned empty output")
	}
	var env cliEnvelope
	if err := json.Unmarshal(trimmed, &env); err != nil {
		return nil, fmt.Errorf("decode 易协作 CLI JSON: %w", err)
	}
	if !env.Success {
		return nil, fmt.Errorf("易协作 CLI error: %s", strings.TrimSpace(string(env.Error)))
	}
	if len(env.Data) == 0 {
		return json.RawMessage("[]"), nil
	}
	return env.Data, nil
}

func cardFromRaw(row rawCard) (Card, error) {
	id := stringifyID(row.ID)
	if id == "" {
		return Card{}, fmt.Errorf("易协作 issue is missing id")
	}
	updated, _ := parseCLITime(row.UpdatedOn)
	return Card{
		ExternalID:  id,
		Title:       row.Subject,
		Description: row.Description,
		StatusName:  decodeNamed(row.Status),
		Priority:    decodeNamed(row.Priority),
		StartDate:   row.StartDate,
		DueDate:     row.DueDate,
		UpdatedAt:   updated,
	}, nil
}

func stringifyID(v any) string {
	switch n := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(n)
	case float64:
		return strconv.FormatInt(int64(n), 10)
	case json.Number:
		return n.String()
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func decodeNamed(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		return name
	}
	var obj struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.Name
	}
	return ""
}

func parseCLITime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02 15:04:05", time.DateOnly} {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparsed time %q", s)
}
