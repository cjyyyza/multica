package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Config is deployment-owned JSON or one explicitly marked work partner AGENTS.md fence.
type Config struct {
	Version       int               `json:"version"`
	DefaultTarget string            `json:"defaultTarget"`
	AppURL        string            `json:"appUrl,omitempty"`
	Targets       map[string]Target `json:"targets"`
}

type Target struct {
	RobotName string   `json:"robotName"`
	Receiver  string   `json:"receiver"`
	AtUIDs    []string `json:"atUids,omitempty"`
}

var accountPattern = regexp.MustCompile(`^[A-Za-z0-9._+%-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`)
var groupPattern = regexp.MustCompile(`^[0-9]+$`)

func (t Target) Validate() error {
	if strings.TrimSpace(t.RobotName) == "" || t.RobotName != strings.TrimSpace(t.RobotName) || strings.ContainsAny(t.RobotName, "\r\n") {
		return errors.New("robotName must be the exact, non-empty work partner name")
	}
	if !groupPattern.MatchString(t.Receiver) && !accountPattern.MatchString(t.Receiver) {
		return errors.New("receiver must be a numeric POPO group ID or a POPO account")
	}
	if len(t.AtUIDs) > 0 && !groupPattern.MatchString(t.Receiver) {
		return errors.New("atUids requires a group receiver")
	}
	seen := map[string]bool{}
	for _, uid := range t.AtUIDs {
		if !accountPattern.MatchString(uid) || seen[uid] {
			return fmt.Errorf("atUids contains an invalid or duplicate account %q", uid)
		}
		seen[uid] = true
	}
	return nil
}

func ParseConfig(markdown []byte) (Config, error) {
	var config Config
	var body strings.Builder
	inside, found, closed := false, false, false
	for _, line := range strings.Split(strings.TrimPrefix(string(markdown), "\ufeff"), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "```multica-notify" {
			if found {
				return config, errors.New("AGENTS.md must contain exactly one multica-notify fence")
			}
			inside, found = true, true
			continue
		}
		if inside && strings.TrimSpace(line) == "```" {
			inside, closed = false, true
			continue
		}
		if inside {
			body.WriteString(line)
			body.WriteByte('\n')
		}
	}
	if !found || !closed {
		return config, errors.New("AGENTS.md needs one closed ```multica-notify JSON fence")
	}
	return ParseJSONConfig([]byte(body.String()))
}

func ParseJSONConfig(data []byte) (Config, error) {
	var config Config
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("decode multica-notify configuration: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return config, errors.New("unexpected content after multica-notify configuration")
	}
	if config.Version != 1 || len(config.Targets) == 0 {
		return config, errors.New("multica-notify needs version 1 and at least one target")
	}
	if config.AppURL != "" {
		if err := ValidateAppURL(config.AppURL); err != nil {
			return config, err
		}
	}
	for name, target := range config.Targets {
		if strings.TrimSpace(name) == "" {
			return config, errors.New("notification target name cannot be empty")
		}
		if err := target.Validate(); err != nil {
			return config, fmt.Errorf("target %q: %w", name, err)
		}
	}
	if _, ok := config.Targets[config.DefaultTarget]; !ok {
		return config, errors.New("defaultTarget must name a configured target")
	}
	return config, nil
}

// LoadJSONConfig reads an explicit deployment file; it never inspects repository instructions.
func LoadJSONConfig(path string) (Config, string, error) {
	if strings.TrimSpace(path) == "" {
		return Config{}, "", errors.New("notification JSON configuration path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Config{}, "", err
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return Config{}, absolute, err
	}
	config, err := ParseJSONConfig(data)
	return config, absolute, err
}

// LoadConfig uses the nearest AGENTS.md without merging configuration from different projects.
func LoadConfig(path string) (Config, string, error) {
	if path != "" {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return Config{}, "", err
		}
		data, err := os.ReadFile(absolute)
		if err != nil {
			return Config{}, "", err
		}
		config, err := ParseConfig(data)
		return config, absolute, err
	}
	dir, err := os.Getwd()
	if err != nil {
		return Config{}, "", err
	}
	for {
		candidate := filepath.Join(dir, "AGENTS.md")
		if _, err := os.Stat(candidate); err == nil {
			return LoadConfig(candidate)
		} else if !os.IsNotExist(err) {
			return Config{}, "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Config{}, "", errors.New("no AGENTS.md found; use --agents-file")
		}
		dir = parent
	}
}

func (c Config) Target(name string) (Target, error) {
	if name == "" {
		name = c.DefaultTarget
	}
	target, ok := c.Targets[name]
	if !ok {
		return Target{}, fmt.Errorf("unknown notification target %q", name)
	}
	return target, target.Validate()
}
