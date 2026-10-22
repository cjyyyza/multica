//go:build windows

package wecom

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func createMediaTempFile(dir string) (*os.File, error) {
	if dir == "" {
		dir = os.TempDir()
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	// Windows mode bits do not restrict access. Create the file with a
	// protected, owner-only DACL before any plaintext can be written.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return nil, err
	}
	attrs := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	path := filepath.Join(dir, "wecom-media-"+rand.Text()+".bin")
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, &attrs,
		windows.CREATE_NEW, windows.FILE_ATTRIBUTE_TEMPORARY, 0)
	if err != nil {
		return nil, err
	}
	// A separate file object keeps the contents alive after the deleting
	// handle closes. Duplicating h would postpone deletion until both closed.
	reader, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_TEMPORARY, 0)
	if err != nil {
		_ = windows.CloseHandle(h)
		_ = os.Remove(path)
		return nil, err
	}
	flags := uint32(windows.FILE_DISPOSITION_DELETE | windows.FILE_DISPOSITION_POSIX_SEMANTICS)
	err = windows.SetFileInformationByHandle(h, windows.FileDispositionInfoEx, (*byte)(unsafe.Pointer(&flags)), uint32(unsafe.Sizeof(flags)))
	_ = windows.CloseHandle(h)
	if err != nil {
		_ = windows.CloseHandle(reader)
		_ = os.Remove(path)
		return nil, err
	}
	return os.NewFile(uintptr(reader), path), nil
}
