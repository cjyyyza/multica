package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// Notification is independent of task lifecycle: callers can send questions, results or notices.
type Notification struct {
	Event   string `json:"event,omitempty"`
	Title   string `json:"title,omitempty"`
	Message string `json:"message"`
	URL     string `json:"url,omitempty"`
}

type Receipt struct {
	MessageID string   `json:"messageId"`
	RobotName string   `json:"robotName"`
	Receiver  string   `json:"receiver"`
	AtUIDs    []string `json:"atUids,omitempty"`
}

type SendError struct {
	Err     error
	Unknown bool
}

func (e *SendError) Error() string { return e.Err.Error() }
func (e *SendError) Unwrap() error { return e.Err }

type Popo struct {
	Bin string
	// Run is injectable so default tests never discover or execute a user's CLI.
	Run func(context.Context, string, []string) ([]byte, error)
}

func (p Popo) call(ctx context.Context, command string, args ...string) (json.RawMessage, error) {
	bin := p.Bin
	if bin == "" {
		bin = "popo-cli"
	}
	run := p.Run
	if run == nil {
		run = runPopo
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	raw, err := run(ctx, bin, append([]string{"popo", command}, args...))
	if err != nil {
		return nil, err
	}
	return unwrapPopo(raw)
}

func unwrapPopo(raw []byte) (json.RawMessage, error) {
	for depth := 0; depth < 8; depth++ {
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(raw, &envelope); err != nil || envelope == nil {
			return nil, errors.New("popo-cli returned malformed JSON")
		}
		var ok bool
		if value, exists := envelope["ok"]; exists {
			if bytes.Equal(value, []byte("null")) || json.Unmarshal(value, &ok) != nil {
				return nil, errors.New("malformed popo-cli success flag")
			}
			if !ok {
				return nil, errors.New("popo-cli refused the request without a confirmed POPO outcome")
			}
		}
		var code string
		if value, exists := envelope["code"]; exists {
			if bytes.Equal(value, []byte("null")) || json.Unmarshal(value, &code) != nil {
				return nil, errors.New("malformed popo-cli response code")
			}
			if code != "FABRIC_OK" {
				return nil, fmt.Errorf("popo-cli failed with code %q without a confirmed POPO outcome", code)
			}
		}
		var status int
		if value, exists := envelope["downstream_http_status"]; exists {
			if bytes.Equal(value, []byte("null")) || json.Unmarshal(value, &status) != nil {
				return nil, errors.New("malformed POPO downstream HTTP status")
			}
			if status >= 400 {
				return nil, &SendError{Err: errors.New("POPO downstream request failed"), Unknown: status >= 500}
			}
		}
		if value, exists := envelope["status"]; exists {
			if bytes.Equal(value, []byte("null")) || json.Unmarshal(value, &status) != nil {
				return nil, errors.New("malformed POPO response status")
			}
			if status != 1 {
				var message string
				_ = json.Unmarshal(envelope["message"], &message)
				return nil, &SendError{Err: fmt.Errorf("POPO refused the request: %s", message)}
			}
			if len(envelope["data"]) == 0 || bytes.Equal(envelope["data"], []byte("null")) {
				return nil, errors.New("POPO returned no result")
			}
			return envelope["data"], nil
		}
		next, exists := envelope["data"]
		if !exists {
			return nil, errors.New("popo-cli response has no POPO status")
		}
		raw = next
	}
	return nil, errors.New("popo-cli response nesting exceeds the supported envelope")
}

func runPopo(ctx context.Context, bin string, args []string) ([]byte, error) {
	resolved := bin
	var err error
	if strings.EqualFold(filepath.Ext(bin), ".js") {
		resolved, err = filepath.Abs(bin)
		if err == nil {
			_, err = os.Stat(resolved)
		}
	} else {
		resolved, err = exec.LookPath(bin)
	}
	if err != nil {
		return nil, &SendError{Err: fmt.Errorf("find popo-cli: %w", err)}
	}
	// npm's Windows batch shim cannot preserve arbitrary arguments through cmd.exe.
	// Invoke the installed package's Node entry point with an argument vector instead.
	ext := strings.ToLower(filepath.Ext(resolved))
	if ext == ".cmd" || ext == ".ps1" {
		script := filepath.Join(filepath.Dir(resolved), "node_modules", "@popo", "cli", "bin", "popo.js")
		if _, err := os.Stat(script); err != nil {
			return nil, &SendError{Err: errors.New("unsupported popo-cli wrapper; use --popo-cli with the installed bin/popo.js")}
		}
		resolved = script
	}
	if strings.EqualFold(filepath.Ext(resolved), ".js") {
		node, err := exec.LookPath("node")
		if err != nil {
			return nil, &SendError{Err: fmt.Errorf("find Node.js for popo-cli: %w", err)}
		}
		args = append([]string{resolved}, args...)
		resolved = node
	}
	cmd := exec.CommandContext(ctx, resolved, args...)
	configureProcess(cmd)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// CLI diagnostics can contain provider details; errors do not echo argv or stderr.
	if err := cmd.Start(); err != nil {
		return nil, &SendError{Err: fmt.Errorf("start popo-cli: %w", err)}
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("popo-cli invocation failed: %w", err)
	}
	return stdout.Bytes(), nil
}

func (p Popo) mentionNames(ctx context.Context, target Target) ([]string, error) {
	var names []string
	for _, uid := range target.AtUIDs {
		raw, err := p.call(ctx, "userinfo_search", "query="+uid, "type=2", "page=0", "pageSize=10", "searchRange=1", "includeRobot=true")
		if err != nil {
			return nil, fmt.Errorf("resolve mention %s: %w", uid, err)
		}
		var result struct {
			SearchResults struct {
				Person []struct {
					Content struct {
						UID      string `json:"uid"`
						Name     string `json:"name"`
						ShowName string `json:"showName"`
					} `json:"content"`
				} `json:"person"`
			} `json:"search_results"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, errors.New("malformed POPO user lookup")
		}
		name, count := "", 0
		for _, person := range result.SearchResults.Person {
			if person.Content.UID != uid {
				continue
			}
			count++
			name = person.Content.ShowName
			if name == "" {
				name = person.Content.Name
			}
		}
		if count != 1 || strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\r\n") {
			return nil, fmt.Errorf("mention %s did not resolve to one display name", uid)
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return names, nil
	}
	raw, err := p.call(ctx, "team_info_query", "tid="+target.Receiver, "type=2")
	if err != nil {
		return nil, fmt.Errorf("check POPO group members: %w", err)
	}
	var group struct {
		TID     string `json:"tid"`
		Members []struct {
			Name  string `json:"name"`
			Nick  string `json:"nick"`
			Email string `json:"email"`
		} `json:"members"`
	}
	if json.Unmarshal(raw, &group) != nil || group.TID != target.Receiver || group.Members == nil {
		return nil, errors.New("malformed POPO group member response")
	}
	for i, uid := range target.AtUIDs {
		found := false
		for _, member := range group.Members {
			// Some POPO releases expose names only in the roster.
			if member.Email == uid || member.Email == "" && (member.Name == names[i] || member.Nick == names[i]) {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("mention %s is not in group %s; no invitation was made", uid, target.Receiver)
		}
	}
	return names, nil
}

func (n Notification) Text() (string, error) {
	if !utf8.ValidString(n.Event+n.Title+n.Message+n.URL) || strings.TrimSpace(n.Message) == "" {
		return "", errors.New("notification message must contain valid, non-empty UTF-8 text")
	}
	parts := []string{}
	if n.Event != "" {
		if strings.ContainsAny(n.Event, "\r\n") {
			return "", errors.New("notification event must fit on one line")
		}
		parts = append(parts, "["+n.Event+"]")
	}
	if n.Title != "" {
		parts = append(parts, n.Title)
	}
	parts = append(parts, n.Message)
	if n.URL != "" {
		parsed, err := url.Parse(n.URL)
		if err != nil || parsed.Host == "" || parsed.Scheme != "https" && parsed.Scheme != "http" {
			return "", errors.New("notification URL must be an absolute HTTP(S) URL")
		}
		parts = append(parts, n.URL)
	}
	text := strings.Join(parts, "\n")
	if utf8.RuneCountInString(text) > 3000 {
		return "", errors.New("POPO notification exceeds 3000 characters; shorten the message and provide a URL")
	}
	return text, nil
}

func (p Popo) Send(ctx context.Context, target Target, notification Notification) (Receipt, error) {
	var receipt Receipt
	if err := target.Validate(); err != nil {
		return receipt, err
	}
	text, err := notification.Text()
	if err != nil {
		return receipt, err
	}
	names, err := p.mentionNames(ctx, target)
	if err != nil {
		return receipt, &SendError{Err: err}
	}
	for _, name := range names {
		text += "\n@" + name
	}
	if utf8.RuneCountInString(text) > 3000 {
		return receipt, errors.New("POPO notification exceeds 3000 characters; shorten the message and provide a URL")
	}
	dir, err := os.MkdirTemp("", "multica-popo-notify-*")
	if err != nil {
		return receipt, err
	}
	defer os.RemoveAll(dir)
	messagePath := filepath.Join(dir, "message.txt")
	if err := os.WriteFile(messagePath, []byte(text), 0600); err != nil {
		return receipt, err
	}
	args := []string{"robotName=" + target.RobotName, "receiver=" + target.Receiver, "message=@text:" + messagePath, "msgType=text", "isAtAll=false"}
	if len(target.AtUIDs) > 0 {
		atPath := filepath.Join(dir, "at.json")
		encoded, _ := json.Marshal(target.AtUIDs)
		if err := os.WriteFile(atPath, encoded, 0600); err != nil {
			return receipt, err
		}
		args = append(args, "atUids=@file:"+atPath)
	}
	raw, err := p.call(ctx, "use_robot_send_msg", args...)
	if err != nil {
		var known *SendError
		if errors.As(err, &known) {
			return receipt, err
		}
		return receipt, &SendError{Err: err, Unknown: true}
	}
	var result struct {
		Success *bool  `json:"success"`
		MsgID   string `json:"msgId"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Success == nil {
		return receipt, &SendError{Err: errors.New("POPO returned no confirmed message receipt"), Unknown: true}
	}
	if !*result.Success {
		return receipt, &SendError{Err: errors.New("POPO refused the message")}
	}
	if result.MsgID == "" {
		return receipt, &SendError{Err: errors.New("POPO returned no confirmed message receipt"), Unknown: true}
	}
	return Receipt{MessageID: result.MsgID, RobotName: target.RobotName, Receiver: target.Receiver, AtUIDs: target.AtUIDs}, nil
}
