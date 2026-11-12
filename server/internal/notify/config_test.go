package notify

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const configJSON = `{"version":1,"defaultTarget":"team","targets":{"team":{"robotName":"DeepSeek Harness","receiver":"8493291","atUids":["yujian01@corp.netease.com"]},"self":{"robotName":"DeepSeek Harness","receiver":"yujian01@corp.netease.com"}}}`

func configMarkdown(body string) []byte {
	return []byte("# Agent instructions\n\n```multica-notify\n" + body + "\n```\n")
}

func TestConfigRoutingAndValidation(t *testing.T) {
	configuration, err := ParseConfig(configMarkdown(configJSON))
	if err != nil {
		t.Fatal(err)
	}
	group, err := configuration.Target("")
	if err != nil || group.RobotName != "DeepSeek Harness" || group.Receiver != "8493291" || len(group.AtUIDs) != 1 {
		t.Fatalf("default routing: %+v %v", group, err)
	}
	self, err := configuration.Target("self")
	if err != nil || self.Receiver != "yujian01@corp.netease.com" || len(self.AtUIDs) != 0 {
		t.Fatalf("private routing: %+v %v", self, err)
	}
	if _, err := configuration.Target("missing"); err == nil {
		t.Fatal("unknown target accepted")
	}
	for name, input := range map[string][]byte{
		"missing fence":         []byte(configJSON),
		"unclosed fence":        []byte("```multica-notify\n" + configJSON),
		"multiple fences":       append(configMarkdown(configJSON), configMarkdown(configJSON)...),
		"unknown field":         configMarkdown(strings.Replace(configJSON, `"version":1`, `"version":1,"token":"must-not-be-configured"`, 1)),
		"trailing JSON":         configMarkdown(configJSON + ` {}`),
		"unknown version":       configMarkdown(strings.Replace(configJSON, `"version":1`, `"version":2`, 1)),
		"missing default":       configMarkdown(strings.Replace(configJSON, `"defaultTarget":"team"`, `"defaultTarget":"missing"`, 1)),
		"unresolved group name": configMarkdown(strings.Replace(configJSON, `"8493291"`, `"测试"`, 1)),
		"private mentions":      configMarkdown(strings.Replace(configJSON, `"8493291"`, `"yujian01@corp.netease.com"`, 1)),
		"duplicate mentions":    configMarkdown(strings.Replace(configJSON, `["yujian01@corp.netease.com"]`, `["yujian01@corp.netease.com","yujian01@corp.netease.com"]`, 1)),
		"invalid mention":       configMarkdown(strings.Replace(configJSON, `["yujian01@corp.netease.com"]`, `["俞坚"]`, 1)),
		"credential in app URL": configMarkdown(strings.Replace(configJSON, `"version":1`, `"version":1,"appUrl":"https://member:token@multica.example"`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseConfig(input); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestConfigUsesNearestFileWithoutMergingOrFallingBack(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	parentFile := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(parentFile, configMarkdown(configJSON), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(child)
	_, path, err := LoadConfig("")
	if err != nil || path != parentFile {
		t.Fatalf("nearest: %s %v", path, err)
	}
	if err := os.WriteFile(filepath.Join(child, "AGENTS.md"), []byte("# No notification target"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadConfig(""); err == nil {
		t.Fatal("missing local routing silently inherited another file")
	}
	if _, path, err := LoadConfig(parentFile); err != nil || path != parentFile {
		t.Fatalf("explicit: %s %v", path, err)
	}
}

func TestDeploymentJSONAndWorkPartnerMarkdownShareConfig(t *testing.T) {
	agents, err := ParseConfig(configMarkdown(configJSON))
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := ParseJSONConfig(append([]byte("\xef\xbb\xbf"), []byte(configJSON)...))
	if err != nil || !reflect.DeepEqual(agents, deployment) {
		t.Fatalf("configuration contracts differ: %+v %v", deployment, err)
	}
	for _, invalid := range []string{configJSON + " {}", strings.Replace(configJSON, `"version":1`, `"version":1,"token":"not-configuration"`, 1), "# Instructions\n" + configJSON} {
		if _, err := ParseJSONConfig([]byte(invalid)); err == nil {
			t.Fatal("invalid deployment JSON accepted")
		}
	}
	root := t.TempDir()
	path := filepath.Join(root, "notify.json")
	if err := os.WriteFile(path, []byte(configJSON), 0600); err != nil {
		t.Fatal(err)
	}
	// Deployment reads only the selected file even if the working directory has no AGENTS.md.
	t.Chdir(t.TempDir())
	loaded, actual, err := LoadJSONConfig(path)
	if err != nil || actual != path || !reflect.DeepEqual(deployment, loaded) {
		t.Fatalf("explicit deployment file: %+v %s %v", loaded, actual, err)
	}
}
