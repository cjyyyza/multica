//go:build windows

package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestEnsureCodexMcpConfigRestrictsExistingWindowsACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("sandbox_mode = \"workspace-write\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureCodexMcpConfig(path, json.RawMessage(`{"mcpServers":{"fixture":{"command":"fake","env":{"TOKEN":"test-secret"}}}}`), nil); err != nil {
		t.Fatal(err)
	}
	assertPrivateCodexConfig(t, path)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	public, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;GR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := public.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if err := ensureCodexMcpConfig(path, json.RawMessage(`{"mcpServers":{"fixture":{"command":"fake","env":{"TOKEN":"test-secret"}}}}`), nil); err != nil {
		t.Fatal(err)
	}
	assertPrivateCodexConfig(t, path)
}

func assertPrivateCodexConfig(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount != 1 || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("secret config must have one protected access entry: %s (%v)", sd.String(), err)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	const fileAllAccess = 0x1f01ff
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask != fileAllAccess || !(*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(user.User.Sid) {
		t.Fatalf("secret config must grant full access only to its owner: %s", sd.String())
	}
}
