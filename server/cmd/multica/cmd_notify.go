package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/notify"
)

var notifyCmd = newNotifyCommand(nil)

type notifyOptions struct {
	configFile string
	agentsFile string
	targetName string
	stateDir   string
	popoCLI    string
}

func newNotifyCommand(sender notify.Sender) *cobra.Command {
	options := &notifyOptions{}
	root := &cobra.Command{Use: "notify", Short: "Send work partner notifications and watch Multica tasks", PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		for _, flag := range []string{"config", "agents-file"} {
			if cmd.Flags().Changed(flag) {
				value, _ := cmd.Flags().GetString(flag)
				if strings.TrimSpace(value) == "" {
					return fmt.Errorf("--%s requires a non-empty file path", flag)
				}
			}
		}
		return nil
	}}
	root.PersistentFlags().StringVar(&options.configFile, "config", "", "Deployment JSON configuration file (env: MULTICA_NOTIFY_CONFIG); mutually exclusive with --agents-file")
	root.PersistentFlags().StringVar(&options.agentsFile, "agents-file", "", "AGENTS.md containing a multica-notify JSON fence (default: nearest AGENTS.md)")
	root.PersistentFlags().StringVar(&options.targetName, "target", "", "Named notification target (default: defaultTarget in AGENTS.md)")
	root.PersistentFlags().StringVar(&options.stateDir, "state-dir", "", "Durable delivery/checkpoint directory (default: user config directory per configuration file)")
	root.PersistentFlags().StringVar(&options.popoCLI, "popo-cli", "popo-cli", "POPO CLI executable or installed bin/popo.js")
	selectedSender := func() notify.Sender {
		if sender != nil {
			return sender
		}
		return notify.Popo{Bin: options.popoCLI}
	}
	config := &cobra.Command{Use: "config", Short: "Validate and show the selected notification target without sending", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			configuration, path, target, err := options.load()
			if err != nil {
				return err
			}
			format := "agents"
			if options.configFile != "" {
				format = "json"
			}
			return cli.PrintJSON(cmd.OutOrStdout(), map[string]any{"configFile": path, "format": format, "targetName": options.targetName, "target": target, "appUrl": configuration.AppURL})
		}}
	send := &cobra.Command{Use: "send", Short: "Send an arbitrary notice, question or result as the configured work partner", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, _, target, err := options.load()
			if err != nil {
				return err
			}
			message, provided, err := resolveTextFlag(cmd, "message")
			if err != nil {
				return err
			}
			if !provided {
				return errors.New("one of --message, --message-file or --message-stdin is required")
			}
			message = strings.TrimPrefix(message, "\ufeff")
			event, _ := cmd.Flags().GetString("event")
			title, _ := cmd.Flags().GetString("title")
			link, _ := cmd.Flags().GetString("url")
			notification := notify.Notification{Event: event, Title: title, Message: message, URL: link}
			if _, err := notification.Text(); err != nil {
				return err
			}
			key, _ := cmd.Flags().GetString("idempotency-key")
			if cmd.Flags().Changed("idempotency-key") && strings.TrimSpace(key) == "" {
				return errors.New("idempotency key cannot be blank")
			}
			if dry, _ := cmd.Flags().GetBool("dry-run"); dry {
				return cli.PrintJSON(cmd.OutOrStdout(), map[string]any{"dryRun": true, "target": target, "notification": notification})
			}
			receipt, err := (notify.Store{Dir: options.stateDir}).Send(cmd.Context(), selectedSender(), target, notification, key)
			if err != nil {
				return err
			}
			return cli.PrintJSON(cmd.OutOrStdout(), receipt)
		}}
	send.Flags().String("event", "notice", "Caller-defined event name, e.g. question, result or build.failed")
	send.Flags().String("title", "", "Optional notification title")
	send.Flags().String("message", "", "Notification body")
	send.Flags().String("message-file", "", "Read the body from an existing UTF-8 file in the current workdir")
	send.Flags().Bool("message-stdin", false, "Read the body from stdin")
	send.Flags().Bool("allow-external-file", false, "Allow --message-file outside the current workdir")
	send.Flags().String("url", "", "Optional absolute HTTP(S) link")
	send.Flags().String("idempotency-key", "", "Stable logical notification ID; omitted keys are derived from target and content")
	send.Flags().Bool("dry-run", false, "Validate and print the request without calling POPO or writing delivery state")
	confirm := &cobra.Command{Use: "confirm-delivery <key>", Short: "Record an independently verified POPO receipt after an unknown send outcome; never sends", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, _, _, err := options.load(); err != nil {
				return err
			}
			messageID, _ := cmd.Flags().GetString("message-id")
			receipt, err := (notify.Store{Dir: options.stateDir}).Confirm(args[0], messageID)
			if err != nil {
				return err
			}
			return cli.PrintJSON(cmd.OutOrStdout(), receipt)
		}}
	confirm.Flags().String("message-id", "", "POPO message ID independently verified in the configured destination")
	watch := &cobra.Command{Use: "watch", Short: "Watch one task for questions, approval, review and completion without keeping an agent conversation alive", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			configuration, path, target, err := options.load()
			if err != nil {
				return err
			}
			client, err := notifyAPIClient(cmd)
			if err != nil {
				return err
			}
			requirementID, _ := cmd.Flags().GetString("requirement")
			issueID, _ := cmd.Flags().GetString("issue")
			if (requirementID == "") == (issueID == "") {
				return errors.New("provide exactly one of --requirement or --issue")
			}
			source := notify.Source{Kind: "requirement", ID: requirementID}
			if issueID != "" {
				source = notify.Source{Kind: "issue", ID: issueID}
			}
			if err := source.Validate(); err != nil {
				return err
			}
			appURL := cli.FlagOrEnv(cmd, "app-url", "MULTICA_APP_URL", configuration.AppURL)
			if err := notify.ValidateAppURL(appURL); err != nil {
				return fmt.Errorf("configure --app-url, MULTICA_APP_URL or appUrl in the notification configuration: %w", err)
			}
			reader := &notify.Reader{Client: client, Source: source, AppURL: appURL}
			interval, _ := cmd.Flags().GetDuration("interval")
			once, _ := cmd.Flags().GetBool("once")
			background, _ := cmd.Flags().GetBool("background")
			if interval < time.Second {
				return errors.New("--interval must be at least one second")
			}
			if once && background {
				return errors.New("--once and --background cannot be combined")
			}
			initial, err := reader.Read(cmd.Context())
			if err != nil {
				return err
			}
			// UUID and human-readable issue aliases must share a single watcher.
			source.ID, reader.Source.ID = initial.ID, initial.ID
			id := notify.WatchID(client.BaseURL, client.WorkspaceID, source, path, options.targetName, target)
			firstRead := true
			read := func(ctx context.Context) (notify.Snapshot, error) {
				if firstRead {
					firstRead = false
					return initial, nil
				}
				return reader.Read(ctx)
			}
			watcher := &notify.Watcher{Store: notify.Store{Dir: options.stateDir}, Sender: selectedSender(), Target: target, Source: source, Identity: id, Read: read, Interval: interval, Once: once, Out: cmd.OutOrStdout()}
			if background {
				return startNotifyBackground(cmd, options, client, source, appURL, watcher)
			}
			readyPath, _ := cmd.Flags().GetString("ready-file")
			if readyPath != "" {
				watcher.Ready = func() error {
					return writeNotifyReady(readyPath, notifyReady{PID: os.Getpid(), WatchID: id, Checkpoint: watcher.Path()})
				}
			}
			ctx, cancel := notifyShutdownContext(cmd.Context())
			defer cancel()
			err = watcher.Run(ctx)
			if err != nil && readyPath != "" {
				if _, statErr := os.Stat(readyPath); os.IsNotExist(statErr) {
					_ = writeNotifyReady(readyPath, notifyReady{Error: cli.UserMessage(err)})
				}
			}
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}}
	watch.Flags().String("requirement", "", "Requirement UUID (use for requirement-owned issues)")
	watch.Flags().String("issue", "", "Standalone issue UUID or identifier")
	watch.Flags().String("app-url", "", "Multica web app base URL (env: MULTICA_APP_URL, then configuration appUrl)")
	watch.Flags().Duration("interval", 10*time.Second, "Read-only polling interval")
	watch.Flags().Bool("once", false, "Poll once and exit, preserving deduplication state")
	watch.Flags().Bool("background", false, "Detach a hidden local process; requires the same machine's logged-in POPO CLI")
	watch.Flags().String("ready-file", "", "Internal background startup acknowledgement")
	_ = watch.Flags().MarkHidden("ready-file")
	root.AddCommand(config, send, confirm, watch)
	return root
}

func (o *notifyOptions) load() (notify.Config, string, notify.Target, error) {
	if o.configFile != "" && o.agentsFile != "" {
		return notify.Config{}, "", notify.Target{}, errors.New("--config and --agents-file are mutually exclusive")
	}
	if o.configFile == "" && o.agentsFile == "" {
		o.configFile = strings.TrimSpace(os.Getenv("MULTICA_NOTIFY_CONFIG"))
	}
	var configuration notify.Config
	var path string
	var err error
	if o.configFile != "" {
		configuration, path, err = notify.LoadJSONConfig(o.configFile)
	} else {
		configuration, path, err = notify.LoadConfig(o.agentsFile)
	}
	if err != nil {
		return configuration, path, notify.Target{}, err
	}
	if o.configFile != "" {
		o.configFile = path
	} else {
		o.agentsFile = path
	}
	if o.targetName == "" {
		o.targetName = configuration.DefaultTarget
	}
	target, err := configuration.Target(o.targetName)
	if err != nil {
		return configuration, path, target, err
	}
	if o.stateDir == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return configuration, path, target, err
		}
		sum := sha256.Sum256([]byte(path))
		o.stateDir = filepath.Join(root, "multica", "notifications", hex.EncodeToString(sum[:])[:16])
	}
	o.stateDir, err = filepath.Abs(o.stateDir)
	return configuration, path, target, err
}

func notifyAPIClient(cmd *cobra.Command) (*cli.APIClient, error) {
	server := strings.TrimSpace(cli.FlagOrEnv(cmd, "server-url", "MULTICA_SERVER_URL", ""))
	workspace := strings.TrimSpace(cli.FlagOrEnv(cmd, "workspace-id", "MULTICA_WORKSPACE_ID", ""))
	token := strings.TrimSpace(os.Getenv("MULTICA_TOKEN"))
	if server == "" || workspace == "" || token == "" {
		return nil, errors.New("notify watch requires explicit MULTICA_SERVER_URL, MULTICA_WORKSPACE_ID and MULTICA_TOKEN (server/workspace flags are also supported); profile credentials are disabled")
	}
	if strings.HasPrefix(token, cli.TaskTokenPrefix) || inDaemonManagedExecutionContext() {
		return nil, errors.New("notify watch requires member credentials on the work partner host; task-scoped credentials expire with the run")
	}
	if err := notify.ValidateAppURL(server); err != nil {
		return nil, fmt.Errorf("invalid Multica server URL: %w", err)
	}
	return cli.NewAPIClient(server, workspace, token), nil
}
