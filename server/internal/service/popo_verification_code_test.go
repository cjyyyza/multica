package service

import (
	"os"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/notify"
)

func TestPopoVerificationCodeServiceFromEnv(t *testing.T) {
	t.Setenv(notify.PopoVerificationRelayURLEnv, "")
	t.Setenv(notify.PopoVerificationRelayTokenEnv, "")
	tests := []struct {
		name    string
		value   string
		unset   bool
		wantBin string
	}{
		{name: "unset defaults to enabled", unset: true, wantBin: "popo-cli"},
		{name: "empty defaults to enabled", value: "", wantBin: "popo-cli"},
		{name: "true enables POPO", value: "true", wantBin: "popo-cli"},
		{name: "false disables POPO", value: "false"},
		{name: "false is case insensitive", value: " FALSE "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MULTICA_POPO_CLI", tc.value)
			if tc.unset {
				if err := os.Unsetenv("MULTICA_POPO_CLI"); err != nil {
					t.Fatal(err)
				}
			}

			sender := NewPopoVerificationCodeServiceFromEnv()
			if tc.wantBin == "" {
				if sender != nil {
					t.Fatal("false must disable POPO and preserve email delivery")
				}
				return
			}
			cli, ok := sender.(notify.Popo)
			if !ok {
				t.Fatalf("expected POPO CLI sender, got %T", sender)
			}
			if cli.Bin != tc.wantBin {
				t.Fatalf("CLI path = %q, want %q", cli.Bin, tc.wantBin)
			}
		})
	}
}

func TestPopoVerificationRelayConfigurationDoesNotFallBackToLocalCLI(t *testing.T) {
	t.Setenv("MULTICA_POPO_CLI", "true")
	t.Setenv(notify.PopoVerificationRelayURLEnv, "http://invalid.test")
	t.Setenv(notify.PopoVerificationRelayTokenEnv, strings.Repeat("a", 64))
	sender := NewPopoVerificationCodeServiceFromEnv()
	if _, local := sender.(notify.Popo); sender == nil || local {
		t.Fatalf("expected a relay sender, got %T", sender)
	}
	err := sender.SendVerificationCode(t.Context(), "user@example.test", "123456")
	if err == nil || !strings.Contains(err.Error(), "must be an HTTPS origin") {
		t.Fatalf("expected a relay configuration error, got %v", err)
	}
	t.Setenv("MULTICA_POPO_CLI", "false")
	if sender := NewPopoVerificationCodeServiceFromEnv(); sender != nil {
		t.Fatalf("false must disable relay delivery, got %T", sender)
	}
}
