package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/notify"
)

type notifyReady struct {
	PID        int    `json:"pid,omitempty"`
	WatchID    string `json:"watchId,omitempty"`
	Checkpoint string `json:"checkpoint,omitempty"`
	LogFile    string `json:"logFile,omitempty"`
	Error      string `json:"error,omitempty"`
}

func writeNotifyReady(path string, ready notifyReady) error {
	data, err := json.Marshal(ready)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func notifyBackgroundArgs(options *notifyOptions, source notify.Source, appURL, interval, readyFile string) []string {
	args := []string{"notify", "watch", "--target", options.targetName, "--state-dir", options.stateDir, "--popo-cli", options.popoCLI, "--" + source.Kind, source.ID, "--app-url", appURL, "--interval", interval, "--ready-file", readyFile}
	if options.configFile != "" {
		return append(args, "--config", options.configFile)
	}
	return append(args, "--agents-file", options.agentsFile)
}

func startNotifyBackground(cmd *cobra.Command, options *notifyOptions, client *cli.APIClient, source notify.Source, appURL string, watcher *notify.Watcher) error {
	if err := os.MkdirAll(options.stateDir, 0700); err != nil {
		return err
	}
	readyFile, err := os.CreateTemp(options.stateDir, ".notify-ready-*")
	if err != nil {
		return err
	}
	readyPath := readyFile.Name()
	readyFile.Close()
	if err := os.Remove(readyPath); err != nil {
		return err
	}
	defer os.Remove(readyPath)
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(options.stateDir, "watch-"+watcher.Identity+".log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	interval, _ := cmd.Flags().GetDuration("interval")
	child := exec.Command(executable, notifyBackgroundArgs(options, source, appURL, interval.String(), readyPath)...)
	child.SysProcAttr = daemonSysProcAttr(true)
	child.Stdout, child.Stderr = log, log
	// Credentials remain in the child environment, never argv, AGENTS.md or checkpoints.
	child.Env = notifyBackgroundEnv(os.Environ(), client)
	if err := child.Start(); err != nil {
		return fmt.Errorf("detach notification watch: %w", err)
	}
	finished := make(chan error, 1)
	go func() { finished <- child.Wait() }()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	readReady := func() (notifyReady, bool) {
		var ready notifyReady
		data, err := os.ReadFile(readyPath)
		if err != nil || json.Unmarshal(data, &ready) != nil {
			return ready, false
		}
		return ready, ready.Error != "" || ready.PID == child.Process.Pid && ready.WatchID == watcher.Identity
	}
	result := func(ready notifyReady) error {
		if ready.Error != "" {
			return errors.New(ready.Error)
		}
		ready.LogFile = logPath
		return cli.PrintJSON(cmd.OutOrStdout(), ready)
	}
	for {
		if ready, ok := readReady(); ok {
			return result(ready)
		}
		select {
		case <-finished:
			// A terminal task may send and exit before the next acknowledgement poll.
			if ready, ok := readReady(); ok {
				return result(ready)
			}
			return fmt.Errorf("notification watch exited during startup; inspect %s", logPath)
		case <-cmd.Context().Done():
			_ = child.Process.Kill()
			<-finished
			return cmd.Context().Err()
		case <-timer.C:
			_ = child.Process.Kill()
			<-finished
			return fmt.Errorf("notification watch did not acknowledge startup; inspect %s", logPath)
		case <-tick.C:
		}
	}
}

func notifyBackgroundEnv(environ []string, client *cli.APIClient) []string {
	result := make([]string, 0, len(environ)+3)
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "MULTICA_SERVER_URL", "MULTICA_WORKSPACE_ID", "MULTICA_TOKEN", "MULTICA_AGENT_ID", "MULTICA_TASK_ID", "MULTICA_TASK_MODE", "MULTICA_MANAGED_BY":
			continue
		}
		result = append(result, entry)
	}
	return append(result, "MULTICA_SERVER_URL="+client.BaseURL, "MULTICA_WORKSPACE_ID="+client.WorkspaceID, "MULTICA_TOKEN="+client.Token)
}
