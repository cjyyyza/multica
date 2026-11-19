package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/notify"
)

func newPopoVerificationRelayCmd() *cobra.Command {
	var listen, certFile, keyFile string
	cmd := &cobra.Command{
		Use:   "verification-relay",
		Short: "Serve the POPO verification sender over HTTPS",
		Long:  "Run under the OS account with an authenticated popo-cli. Configure the relay token through " + notify.PopoVerificationRelayTokenEnv + ".",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := exec.LookPath("popo-cli"); err != nil {
				return fmt.Errorf("find popo-cli: %w", err)
			}
			token := strings.TrimSpace(os.Getenv(notify.PopoVerificationRelayTokenEnv))
			handler, err := notify.NewPopoVerificationRelayHandler(token, notify.Popo{Bin: "popo-cli"})
			if err != nil {
				return err
			}
			server := &http.Server{
				Addr:              listen,
				Handler:           handler,
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       10 * time.Second,
				WriteTimeout:      60 * time.Second,
				IdleTimeout:       60 * time.Second,
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Starting POPO verification relay on https://%s\n", listen)
			return server.ListenAndServeTLS(certFile, keyFile)
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:9443", "Relay listen address")
	cmd.Flags().StringVar(&certFile, "cert", "", "PEM TLS certificate file")
	cmd.Flags().StringVar(&keyFile, "key", "", "PEM TLS private key file")
	_ = cmd.MarkFlagRequired("cert")
	_ = cmd.MarkFlagRequired("key")
	return cmd
}
