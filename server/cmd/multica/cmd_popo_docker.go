package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/notify"
)

const dockerPopoStdioCommand = "verification-docker-stdio"

var dockerPopoContainerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

type dockerPopoTarget struct {
	id   string
	user string
}

func newPopoDockerWorkerCmd() *cobra.Command {
	var apiURL string
	cmd := &cobra.Command{
		Use:    "verification-docker-worker",
		Short:  "Connect the Windows POPO sender to the local Docker API container",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if runtime.GOOS != "windows" {
				return errors.New("the local Docker POPO sender runs on Windows")
			}
			if err := requireHumanLocalCommand("popo verification-docker-worker"); err != nil {
				return err
			}
			ctx, cancel := notifyShutdownContext(cmd.Context())
			defer cancel()
			port := popoDockerLocalAPIPort(ctx, apiURL, net.DefaultResolver.LookupNetIP, net.InterfaceAddrs)
			if port == "" {
				return nil
			}
			// EOF also stops the worker when its Desktop parent exits.
			go func() {
				_, _ = io.Copy(io.Discard, cmd.InOrStdin())
				cancel()
			}()
			waitForDockerPopo(ctx, port, cmd.ErrOrStderr())
			return nil
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "Desktop API URL")
	return cmd
}

func newPopoDockerStdioCmd() *cobra.Command {
	return &cobra.Command{
		Use:    dockerPopoStdioCommand,
		Short:  "Connect Docker exec stdio to the API's private POPO socket",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if runtime.GOOS != "linux" || os.Getenv(notify.DockerPopoVerificationEnv) != "1" {
				return errors.New("the POPO socket connector runs in a Multica Docker image")
			}
			dialer := net.Dialer{Timeout: 3 * time.Second}
			conn, err := dialer.DialContext(cmd.Context(), "unix", notify.DockerPopoVerificationSocket)
			if err != nil {
				return fmt.Errorf("connect to the API's POPO verification socket: %w", err)
			}
			defer conn.Close()
			go func() {
				_, _ = io.Copy(conn, cmd.InOrStdin())
				_ = conn.Close()
			}()
			_, err = io.Copy(cmd.OutOrStdout(), conn)
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		},
	}
}

// The configured API must be on this Windows machine before we look up Docker.
func popoDockerLocalAPIPort(ctx context.Context, rawURL string,
	lookup func(context.Context, string, string) ([]netip.Addr, error),
	localAddrs func() ([]net.Addr, error),
) string {
	api, err := url.Parse(rawURL)
	if err != nil || (api.Scheme != "http" && api.Scheme != "https") || api.Hostname() == "" || api.User != nil {
		return ""
	}
	port := api.Port()
	if port == "" {
		port = "80"
		if api.Scheme == "https" {
			port = "443"
		}
	}
	if parsed, err := strconv.ParseUint(port, 10, 16); err != nil || parsed == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	remoteIPs, err := lookup(ctx, "ip", api.Hostname())
	if err != nil || len(remoteIPs) == 0 {
		return ""
	}
	interfaces, err := localAddrs()
	if err != nil {
		return ""
	}
	localIPs := make(map[netip.Addr]bool, len(interfaces))
	for _, address := range interfaces {
		host, _, _ := strings.Cut(address.String(), "/")
		if ip, err := netip.ParseAddr(host); err == nil {
			localIPs[ip.WithZone("").Unmap()] = true
		}
	}
	for _, ip := range remoteIPs {
		ip = ip.WithZone("").Unmap()
		if !ip.IsLoopback() && !localIPs[ip] {
			return ""
		}
	}
	return port
}

func discoverDockerPopoTarget(port string, docker func(...string) ([]byte, error)) (dockerPopoTarget, error) {
	label := notify.DockerPopoVerificationLabel
	list, err := docker("ps", "--no-trunc", "--filter", "label="+label,
		"--format", `{{.ID}} {{.Label "`+label+`"}}`)
	if err != nil {
		return dockerPopoTarget{}, err
	}
	var match dockerPopoTarget
	for line := range strings.Lines(string(list)) {
		fields := strings.Fields(line)
		if len(fields) != 2 || !dockerPopoContainerID.MatchString(fields[0]) || (fields[1] != "0:0" && fields[1] != "1001:1001") {
			continue
		}
		id := fields[0]
		data, err := docker("inspect", "--type", "container", "--format", "{{json .NetworkSettings.Ports}}", id)
		if err != nil {
			return dockerPopoTarget{}, err
		}
		var bindings map[string][]struct{ HostPort string }
		if err := json.Unmarshal(data, &bindings); err != nil {
			return dockerPopoTarget{}, err
		}
		for _, binding := range bindings["8080/tcp"] {
			if binding.HostPort != port {
				continue
			}
			if match.id != "" {
				return dockerPopoTarget{}, errors.New("several local Multica containers publish the configured API port")
			}
			match = dockerPopoTarget{id: id, user: fields[1]}
			break
		}
	}
	if match.id == "" {
		return dockerPopoTarget{}, errors.New("waiting for the local Multica container to publish the configured API port")
	}
	return match, nil
}

func localDockerCommand(ctx context.Context, dockerExe string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, dockerExe, append([]string{"--host", "npipe:////./pipe/docker_engine"}, args...)...)
	cmd.SysProcAttr = daemonSysProcAttr(false)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS", "DOCKER_TLS_VERIFY":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	return cmd
}

func waitForDockerPopo(ctx context.Context, port string, diagnostics io.Writer) {
	lastError := ""
	for ctx.Err() == nil {
		err := connectDockerPopo(ctx, port)
		if ctx.Err() != nil {
			return
		}
		if err != nil && err.Error() != lastError {
			fmt.Fprintf(diagnostics, "Local Docker POPO sender: %v\n", err)
			lastError = err.Error()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func connectDockerPopo(ctx context.Context, port string) error {
	if _, err := exec.LookPath("popo-cli"); err != nil {
		return fmt.Errorf("find the Windows popo-cli: %w", err)
	}
	dockerExe, err := exec.LookPath("docker.exe")
	if err != nil {
		return fmt.Errorf("find the Windows Docker CLI: %w", err)
	}
	target, err := discoverDockerPopoTarget(port, func(args ...string) ([]byte, error) {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return localDockerCommand(probeCtx, dockerExe, args...).Output()
	})
	if err != nil {
		return err
	}

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	docker := localDockerCommand(workerCtx, dockerExe, "exec", "-i", "--user", target.user,
		target.id, "/app/multica", "popo", dockerPopoStdioCommand)
	var diagnostic bytes.Buffer
	docker.Stderr = &diagnostic
	requests, err := docker.StdoutPipe()
	if err != nil {
		return err
	}
	receipts, err := docker.StdinPipe()
	if err != nil {
		return err
	}
	if err := docker.Start(); err != nil {
		return err
	}
	serveErr := notify.RunDockerPopoVerificationWorker(workerCtx, requests, receipts, notify.Popo{Bin: "popo-cli"})
	_ = receipts.Close()
	if serveErr != nil {
		cancel()
		_ = docker.Wait()
		return serveErr
	}
	if err := docker.Wait(); err != nil {
		return fmt.Errorf("local Docker exec: %w (%s)", err, strings.TrimSpace(diagnostic.String()))
	}
	return nil
}
