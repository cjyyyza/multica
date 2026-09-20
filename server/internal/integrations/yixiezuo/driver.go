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

const (
	defaultCLIBin    = "popo-cli"
	defaultMCPTarget = "gcp"
)

// ExecOptions configure the Windows-local popo-cli pmmcp issue tools.
type ExecOptions struct {
	Bin       string
	MCPTarget string
	GCPHost   string
	LookPath  func(file string) (string, error)
	Run       func(ctx context.Context, bin string, args []string) ([]byte, error)
}

// ExecDriver shells out to the Windows-local popo-cli. Command names come from
// the inspected `popo-cli pmmcp` surface on this machine.
type ExecDriver struct {
	opts ExecOptions
}

func NewExecDriver(opts ExecOptions) *ExecDriver {
	if opts.Bin == "" {
		opts.Bin = defaultCLIBin
	}
	if opts.MCPTarget == "" {
		opts.MCPTarget = defaultMCPTarget
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.Run == nil {
		opts.Run = defaultRun
	}
	return &ExecDriver{opts: opts}
}

func defaultRun(ctx context.Context, bin string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("run %s %s: %w: %s", bin, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (d *ExecDriver) resolveBin() (string, error) {
	path, err := d.opts.LookPath(d.opts.Bin)
	if err != nil {
		return "", fmt.Errorf("local popo-cli %q is not on PATH; sync must run on the Windows machine that can open 易协作: %w", d.opts.Bin, err)
	}
	return path, nil
}

func (d *ExecDriver) GetCard(ctx context.Context, externalID string) (Card, error) {
	id, err := parseIntish(externalID)
	if err != nil {
		return Card{}, fmt.Errorf("易协作 issue id %q: %w", externalID, err)
	}
	raw, err := d.toolCall(ctx, "get_issue_base", map[string]any{"id": id})
	if err != nil {
		return Card{}, err
	}
	return parseIssueBase(raw)
}

func (d *ExecDriver) toolCall(ctx context.Context, toolName string, arguments any) ([]byte, error) {
	host, err := d.resolveHost(ctx)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(arguments)
	if err != nil {
		return nil, err
	}
	raw, err := d.run(ctx, "pmmcp", "tool_call",
		"mcpTarget="+d.opts.MCPTarget,
		"gcpHost="+host,
		"toolName="+toolName,
		"arguments="+string(payload),
	)
	if err != nil {
		return nil, err
	}
	return unwrapPMMCP(raw)
}

func (d *ExecDriver) resolveHost(ctx context.Context) (string, error) {
	if host := strings.TrimSpace(d.opts.GCPHost); host != "" {
		return host, nil
	}
	raw, err := d.run(ctx, "pmmcp", "list_instances")
	if err != nil {
		return "", err
	}
	inner, err := unwrapPMMCP(raw)
	if err != nil {
		return "", err
	}
	var instances []struct {
		Domain string `json:"domain"`
		Name   string `json:"name"`
	}
	if err := json.Unmarshal(inner, &instances); err != nil {
		return "", fmt.Errorf("decode 易协作 list_instances: %w", err)
	}
	if len(instances) == 1 && strings.TrimSpace(instances[0].Domain) != "" {
		d.opts.GCPHost = instances[0].Domain
		return d.opts.GCPHost, nil
	}
	if len(instances) == 0 {
		return "", fmt.Errorf("popo-cli pmmcp list_instances returned no 易协作 hosts")
	}
	return "", fmt.Errorf("multiple 易协作 instances; set gcp_host (this machine has more than one domain)")
}

func (d *ExecDriver) run(ctx context.Context, args ...string) ([]byte, error) {
	bin, err := d.resolveBin()
	if err != nil {
		return nil, err
	}
	return d.opts.Run(ctx, bin, args)
}

type rawCard struct {
	ID          any             `json:"id"`
	ProjectID   any             `json:"project_id"`
	Subject     json.RawMessage `json:"subject"`
	Description json.RawMessage `json:"description"`
	Status      json.RawMessage `json:"status"`
	Priority    json.RawMessage `json:"priority"`
	StartDate   json.RawMessage `json:"start_date"`
	DueDate     json.RawMessage `json:"due_date"`
	UpdatedOn   json.RawMessage `json:"updated_on"`
}

func unwrapPMMCP(raw []byte) (json.RawMessage, error) {
	inner, err := unwrapFabric(raw)
	if err != nil {
		return nil, err
	}
	inner, err = unwrapContentText(inner)
	if err != nil {
		return nil, err
	}
	return unwrapResCode(inner)
}

func unwrapFabric(raw []byte) (json.RawMessage, error) {
	current := bytes.TrimSpace(raw)
	if len(current) == 0 {
		return nil, fmt.Errorf("易协作 CLI returned empty output")
	}
	for i := 0; i < 8; i++ {
		var env struct {
			OK      *bool           `json:"ok"`
			Success *bool           `json:"success"`
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Error   json.RawMessage `json:"error"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(current, &env); err != nil {
			return nil, fmt.Errorf("decode 易协作 CLI JSON: %w", err)
		}
		if env.OK != nil && !*env.OK {
			return nil, fmt.Errorf("易协作 CLI error: %s", firstNonEmpty(env.Message, strings.TrimSpace(string(env.Error))))
		}
		if env.Success != nil && !*env.Success {
			return nil, fmt.Errorf("易协作 CLI error: %s", firstNonEmpty(env.Message, strings.TrimSpace(string(env.Error))))
		}
		data := bytes.TrimSpace(env.Data)
		if len(data) == 0 {
			return current, nil
		}
		if data[0] == '[' {
			return json.RawMessage(data), nil
		}
		var peek map[string]json.RawMessage
		if json.Unmarshal(data, &peek) == nil && isFabricEnvelope(peek) {
			current = json.RawMessage(data)
			continue
		}
		return json.RawMessage(data), nil
	}
	return current, nil
}

func isFabricEnvelope(peek map[string]json.RawMessage) bool {
	if _, hasData := peek["data"]; !hasData {
		return false
	}
	for _, key := range []string{"list", "base", "res_code", "issues", "issue"} {
		if _, ok := peek[key]; ok {
			return false
		}
	}
	_, hasID := peek["id"]
	_, hasSubject := peek["subject"]
	return !(hasID && hasSubject)
}

func unwrapContentText(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return raw, nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return raw, nil
	}
	if len(blocks) == 1 && blocks[0].Type == "text" && strings.TrimSpace(blocks[0].Text) != "" {
		return json.RawMessage(blocks[0].Text), nil
	}
	return raw, nil
}

func unwrapResCode(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return json.RawMessage("null"), nil
	}
	if trimmed[0] != '{' {
		return raw, nil
	}
	var env struct {
		ResCode any             `json:"res_code"`
		ResMsg  string          `json:"res_msg"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(trimmed, &env); err != nil {
		return raw, nil
	}
	if env.ResCode == nil {
		return raw, nil
	}
	code := stringifyID(env.ResCode)
	if code != "1" {
		return nil, fmt.Errorf("易协作 CLI error: %s", firstNonEmpty(env.ResMsg, code))
	}
	if len(bytes.TrimSpace(env.Data)) == 0 {
		return json.RawMessage("{}"), nil
	}
	return env.Data, nil
}

func cardFromListRow(row rawCard) (Card, error) {
	id := stringifyID(row.ID)
	if id == "" {
		return Card{}, fmt.Errorf("易协作 issue is missing id")
	}
	updated, _ := parseCLITime(decodeStringish(row.UpdatedOn))
	return Card{
		ExternalID:  id,
		Title:       decodeStringish(row.Subject),
		Description: decodeStringish(row.Description),
		StatusName:  decodeNamed(row.Status),
		Priority:    decodeNamed(row.Priority),
		StartDate:   decodeStringish(row.StartDate),
		DueDate:     decodeStringish(row.DueDate),
		UpdatedAt:   updated,
	}, nil
}

func parseIssueBase(raw []byte) (Card, error) {
	var box struct {
		Base       rawCard           `json:"base"`
		CoreFields []json.RawMessage `json:"core_fields"`
		ID         any               `json:"id"`
		Issue      rawCard           `json:"issue"`
	}
	if err := json.Unmarshal(raw, &box); err != nil {
		return Card{}, fmt.Errorf("decode 易协作 get_issue_base: %w", err)
	}
	row := box.Base
	if stringifyID(row.ID) == "" {
		row = box.Issue
	}
	if stringifyID(row.ID) == "" && stringifyID(box.ID) != "" {
		row.ID = box.ID
	}
	card, err := cardFromListRow(row)
	if err != nil {
		return Card{}, err
	}
	applyCoreFields(&card, box.CoreFields)
	return card, nil
}

func applyCoreFields(card *Card, groups []json.RawMessage) {
	for _, group := range groups {
		var fields []struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
			Name  string          `json:"name"`
		}
		if json.Unmarshal(group, &fields) != nil {
			continue
		}
		for _, field := range fields {
			value := decodeStringish(field.Value)
			switch field.Key {
			case "subject":
				if card.Title == "" {
					card.Title = value
				}
			case "description":
				if card.Description == "" {
					card.Description = value
				}
			case "status_id":
				if card.StatusName == "" {
					card.StatusName = value
				}
			case "start_date":
				if card.StartDate == "" {
					card.StartDate = value
				}
			case "due_date":
				if card.DueDate == "" {
					card.DueDate = value
				}
			}
			if card.Priority == "" && normalize(field.Name) == "priority" {
				card.Priority = value
			}
		}
	}
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
	case int:
		return strconv.Itoa(n)
	case int64:
		return strconv.FormatInt(n, 10)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func parseIntish(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty id")
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func decodeNamed(raw json.RawMessage) string {
	if text := decodeStringish(raw); text != "" {
		return text
	}
	return ""
}

func decodeStringish(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 || string(raw) == "null" {
		return ""
	}
	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		return strings.TrimSpace(name)
	}
	var obj struct {
		Name  string `json:"name"`
		Value any    `json:"value"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		if obj.Name != "" {
			return strings.TrimSpace(obj.Name)
		}
		if text := stringifyID(obj.Value); text != "" && text != "<nil>" {
			return text
		}
	}
	return ""
}

func parseCLITime(s string) (time.Time, error) { return ParseLocalTime(strings.TrimSpace(s)) }

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "unknown error"
}
