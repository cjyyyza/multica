//go:build windows

package wecom

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func assertMediaTempFilePrivate(t *testing.T, f *os.File) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	want, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	if got := sd.String(); got != want.String() {
		t.Fatalf("plaintext DACL = %s, want owner-only %s", got, want.String())
	}
}
