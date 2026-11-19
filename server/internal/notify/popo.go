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
	"regexp"
	"strings"
	"time"
	"unicode"
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
	// P2P reports submission through the downstream status, even with no data.
	return unwrapPopo(raw, command != "message_send_p2p")
}

func unwrapPopo(raw []byte, requireData bool) (json.RawMessage, error) {
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
			if requireData && (len(envelope["data"]) == 0 || bytes.Equal(envelope["data"], []byte("null"))) {
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

var wrapperEntryPattern = regexp.MustCompile(`(?i)(?:%dp0%[\\/]|%~dp0[\\/]?|\$basedir[\\/])([^"'\r\n]+?\.(?:m|c)?js)\b`)

type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if remaining := b.limit - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			_, _ = b.buf.Write(p[:remaining])
		} else {
			_, _ = b.buf.Write(p)
		}
	}
	return len(p), nil
}

func sanitizeCLIDiagnostic(raw []byte) string {
	text := strings.TrimSpace(string(bytes.ToValidUTF8(raw, []byte("\ufffd"))))
	if text == "" {
		return ""
	}
	var out strings.Builder
	lastSpace := false
	runes := 0
	for _, r := range text {
		if unicode.IsSpace(r) {
			if !lastSpace && out.Len() > 0 {
				out.WriteByte(' ')
				lastSpace = true
				runes++
			}
		} else if !unicode.IsControl(r) {
			out.WriteRune(r)
			lastSpace = false
			runes++
		}
		if runes >= 200 {
			break
		}
	}
	return strings.TrimSpace(out.String())
}

func resolvePopoWrapperScript(wrapperPath string) (string, bool) {
	dir := filepath.Dir(wrapperPath)
	if data, err := os.ReadFile(wrapperPath); err == nil {
		if len(data) > 16384 {
			data = data[:16384]
		}
		for _, match := range wrapperEntryPattern.FindAllSubmatch(data, -1) {
			rel := strings.TrimSpace(string(match[1]))
			if rel == "" {
				continue
			}
			rel = strings.ReplaceAll(rel, `\`, "/")
			candidate := filepath.Join(dir, filepath.FromSlash(rel))
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, true
			}
		}
	}
	defaultScript := filepath.Join(dir, "node_modules", "@popo", "cli", "bin", "popo.js")
	if info, err := os.Stat(defaultScript); err == nil && !info.IsDir() {
		return defaultScript, true
	}
	return "", false
}

func isNodeScript(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".js", ".mjs", ".cjs":
		return true
	default:
		return false
	}
}

// ResolvePopoCommand resolves the POPO CLI binary (unwrapping Windows npm/pnpm
// shims to Node.js) and returns the executable and any leading script arguments.
func ResolvePopoCommand(bin string) (string, []string, error) {
	if strings.TrimSpace(bin) == "" {
		bin = "popo-cli"
	}
	resolved := bin
	var err error
	if isNodeScript(bin) {
		resolved, err = filepath.Abs(bin)
		if err == nil {
			_, err = os.Stat(resolved)
		}
	} else {
		resolved, err = exec.LookPath(bin)
	}
	if err != nil {
		return "", nil, &SendError{Err: fmt.Errorf("find popo-cli: %w", err)}
	}
	// npm/pnpm Windows batch shims cannot preserve arbitrary arguments through cmd.exe.
	// Invoke the installed package's Node entry point with an argument vector instead.
	ext := strings.ToLower(filepath.Ext(resolved))
	if ext == ".cmd" || ext == ".ps1" {
		script, ok := resolvePopoWrapperScript(resolved)
		if !ok {
			return "", nil, &SendError{Err: errors.New("unsupported popo-cli wrapper; install @popo/cli with a Node entry point or use --popo-cli with bin/popo.js")}
		}
		resolved = script
	}
	if isNodeScript(resolved) {
		node, err := exec.LookPath("node")
		if err != nil {
			return "", nil, &SendError{Err: fmt.Errorf("find Node.js for popo-cli: %w", err)}
		}
		return node, []string{resolved}, nil
	}
	return resolved, nil, nil
}

func runPopo(ctx context.Context, bin string, args []string) ([]byte, error) {
	resolved, prefixArgs, err := ResolvePopoCommand(bin)
	if err != nil {
		return nil, err
	}
	if len(prefixArgs) > 0 {
		args = append(append([]string(nil), prefixArgs...), args...)
	}
	cmd := exec.CommandContext(ctx, resolved, args...)
	configureProcess(cmd)
	var stdout bytes.Buffer
	stderr := limitedBuffer{limit: 512}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, &SendError{Err: fmt.Errorf("start popo-cli: %w", err)}
	}
	if err := cmd.Wait(); err != nil {
		// When the process exited cleanly on its own (not timed out / canceled),
		// inspect stdout first so an explicit downstream refusal is preserved
		// instead of being misclassified as an unknown delivery outcome.
		var unwrapErr error
		stdoutValidJSON := false
		if ctx.Err() == nil && stdout.Len() > 0 {
			stdoutBytes := stdout.Bytes()
			stdoutValidJSON = json.Valid(stdoutBytes)
			if _, unwrapErr = unwrapPopo(stdoutBytes, true); unwrapErr != nil {
				var known *SendError
				if errors.As(unwrapErr, &known) && !known.Unknown {
					return nil, known
				}
			}
		}
		diag := sanitizeCLIDiagnostic(stderr.buf.Bytes())
		if diag == "" && !stdoutValidJSON && stdout.Len() > 0 {
			diag = sanitizeCLIDiagnostic(stdout.Bytes())
		}
		switch {
		case stdoutValidJSON && unwrapErr != nil && diag != "":
			return nil, fmt.Errorf("popo-cli invocation failed: %w (%v): %s", err, unwrapErr, diag)
		case stdoutValidJSON && unwrapErr != nil:
			return nil, fmt.Errorf("popo-cli invocation failed: %w (%v)", err, unwrapErr)
		case diag != "":
			return nil, fmt.Errorf("popo-cli invocation failed: %w: %s", err, diag)
		default:
			return nil, fmt.Errorf("popo-cli invocation failed: %w", err)
		}
	}
	return stdout.Bytes(), nil
}

func (p Popo) mentionNames(ctx context.Context, target Target) ([]string, []string, error) {
	var names []string
	var canonicalUIDs []string
	for _, uid := range target.AtUIDs {
		raw, err := p.call(ctx, "userinfo_search", "query="+uid, "type=2", "page=0", "pageSize=10", "searchRange=1", "includeRobot=true")
		if err != nil {
			return nil, nil, fmt.Errorf("resolve mention %s: %w", uid, err)
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
			return nil, nil, errors.New("malformed POPO user lookup")
		}
		name, canonicalUID, count := "", "", 0
		for _, person := range result.SearchResults.Person {
			if !strings.EqualFold(person.Content.UID, uid) {
				continue
			}
			count++
			canonicalUID = person.Content.UID
			name = person.Content.ShowName
			if name == "" {
				name = person.Content.Name
			}
		}
		if count != 1 || strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\r\n") {
			return nil, nil, fmt.Errorf("mention %s did not resolve to one display name", uid)
		}
		names = append(names, name)
		canonicalUIDs = append(canonicalUIDs, canonicalUID)
	}
	if len(names) == 0 {
		return names, canonicalUIDs, nil
	}
	raw, err := p.call(ctx, "team_info_query", "tid="+target.Receiver, "type=2")
	if err != nil {
		return nil, nil, fmt.Errorf("check POPO group members: %w", err)
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
		return nil, nil, errors.New("malformed POPO group member response")
	}
	for i, uid := range canonicalUIDs {
		found := false
		for _, member := range group.Members {
			// Some POPO releases expose names only in the roster.
			if strings.EqualFold(member.Email, uid) || member.Email == "" && (member.Name == names[i] || member.Nick == names[i]) {
				found = true
			}
		}
		if !found {
			return nil, nil, fmt.Errorf("mention %s is not in group %s; no invitation was made", target.AtUIDs[i], target.Receiver)
		}
	}
	return names, canonicalUIDs, nil
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

func (p Popo) sendMessage(ctx context.Context, tempPrefix, command, text string, extraArgs func(dir string) ([]string, error)) (string, error) {
	dir, err := os.MkdirTemp("", tempPrefix)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	messagePath := filepath.Join(dir, "message.txt")
	if err := os.WriteFile(messagePath, []byte(text), 0600); err != nil {
		return "", err
	}
	args, err := extraArgs(dir)
	if err != nil {
		return "", err
	}
	args = append(args, "message=@text:"+messagePath)
	raw, err := p.call(ctx, command, args...)
	if err != nil {
		var known *SendError
		if errors.As(err, &known) {
			return "", err
		}
		return "", &SendError{Err: err, Unknown: true}
	}
	var result struct {
		Success *bool  `json:"success"`
		MsgID   string `json:"msgId"`
	}
	if len(raw) != 0 && json.Unmarshal(raw, &result) != nil {
		return "", &SendError{Err: errors.New("POPO returned no confirmed message receipt"), Unknown: true}
	}
	if result.Success != nil && !*result.Success {
		return "", &SendError{Err: errors.New("POPO refused the message")}
	}
	if command != "message_send_p2p" && (result.Success == nil || result.MsgID == "") {
		return "", &SendError{Err: errors.New("POPO returned no confirmed message receipt"), Unknown: true}
	}
	return result.MsgID, nil
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
	names, canonicalUIDs, err := p.mentionNames(ctx, target)
	if err != nil {
		return receipt, &SendError{Err: err}
	}
	for _, name := range names {
		text += "\n@" + name
	}
	if utf8.RuneCountInString(text) > 3000 {
		return receipt, errors.New("POPO notification exceeds 3000 characters; shorten the message and provide a URL")
	}
	msgID, err := p.sendMessage(ctx, "multica-popo-notify-*", "use_robot_send_msg", text, func(dir string) ([]string, error) {
		args := []string{"robotName=" + target.RobotName, "receiver=" + target.Receiver, "msgType=text", "isAtAll=false"}
		if len(canonicalUIDs) > 0 {
			atPath := filepath.Join(dir, "at.json")
			encoded, _ := json.Marshal(canonicalUIDs)
			if err := os.WriteFile(atPath, encoded, 0600); err != nil {
				return nil, err
			}
			args = append(args, "atUids=@file:"+atPath)
		}
		return args, nil
	})
	if err != nil {
		return receipt, err
	}
	return Receipt{MessageID: msgID, RobotName: target.RobotName, Receiver: target.Receiver, AtUIDs: canonicalUIDs}, nil
}

func (p Popo) SendP2P(ctx context.Context, receiver, message string) (Receipt, error) {
	var receipt Receipt
	if !accountPattern.MatchString(receiver) {
		return receipt, errors.New("receiver must be a POPO account")
	}
	text, err := (Notification{Message: message}).Text()
	if err != nil {
		return receipt, err
	}
	msgID, err := p.sendMessage(ctx, "multica-popo-p2p-*", "message_send_p2p", text, func(string) ([]string, error) {
		return []string{"receiver=" + receiver, "source=AGENT"}, nil
	})
	if err != nil {
		return receipt, err
	}
	return Receipt{MessageID: msgID, Receiver: receiver}, nil
}

func (p Popo) SendVerificationCode(ctx context.Context, email, code string) error {
	message := fmt.Sprintf("Multica 验证码：%s\n用于注册或登录 Multica，10 分钟内有效。如果不是你本人发起，请忽略此消息。", code)
	if _, err := p.SendP2P(ctx, email, message); err != nil {
		return fmt.Errorf("send verification code with popo-cli: %w", err)
	}
	return nil
}
