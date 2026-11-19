package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
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
			if _, _, err := notify.ResolvePopoCommand("popo-cli"); err != nil {
				return err
			}
			token := strings.TrimSpace(os.Getenv(notify.PopoVerificationRelayTokenEnv))
			handler, err := notify.NewPopoVerificationRelayHandler(token, notify.Popo{Bin: "popo-cli"})
			if err != nil {
				return err
			}
			cert, err := tls.LoadX509KeyPair(certFile, keyFile)
			if err != nil {
				return fmt.Errorf("load TLS certificate: %w", err)
			}
			ln, err := net.Listen("tcp", listen)
			if err != nil {
				return fmt.Errorf("listen on %s: %w", listen, err)
			}
			parentCtx := cmd.Context()
			if parentCtx == nil {
				parentCtx = context.Background()
			}
			ctx, cancel := notifyShutdownContext(parentCtx)
			defer cancel()
			server := &http.Server{
				Handler:           handler,
				TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}},
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       10 * time.Second,
				WriteTimeout:      60 * time.Second,
				IdleTimeout:       60 * time.Second,
			}
			tlsListener := tls.NewListener(ln, server.TLSConfig)
			errCh := make(chan error, 1)
			go func() {
				fmt.Fprintf(cmd.ErrOrStderr(), "Starting POPO verification relay on https://%s\n", ln.Addr())
				errCh <- server.Serve(tlsListener)
			}()
			select {
			case err := <-errCh:
				if errors.Is(err, http.ErrServerClosed) {
					return nil
				}
				return err
			case <-ctx.Done():
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer shutdownCancel()
				if err := server.Shutdown(shutdownCtx); err != nil {
					return err
				}
				err := <-errCh
				if errors.Is(err, http.ErrServerClosed) {
					return nil
				}
				return err
			}
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:9443", "Relay listen address")
	cmd.Flags().StringVar(&certFile, "cert", "", "PEM TLS certificate file")
	cmd.Flags().StringVar(&keyFile, "key", "", "PEM TLS private key file")
	_ = cmd.MarkFlagRequired("cert")
	_ = cmd.MarkFlagRequired("key")
	return cmd
}
