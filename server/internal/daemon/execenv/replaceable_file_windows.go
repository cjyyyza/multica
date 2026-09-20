//go:build windows

package execenv

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

// Preparation runs in separate processes. Coordinate both readers and writers
// so a burst cannot keep the destination open throughout the rename retries.
// Keep the lock file in place: deleting it would let peers lock different files.
func lockDiscoveryCache(path string) (func(), error) {
	file, err := openLockFile(filepath.Join(filepath.Dir(path), openclawDiscoveryCacheLockFile))
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		locked, err := lockFileExclusiveNonBlocking(file)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if locked {
			return func() { releaseLockFile(file) }, nil
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, errors.New("timed out waiting for the cache lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Readers must allow deletion for an atomic rename to replace the cache while
// another preparation process reads it. Go's ordinary file open omits this flag.
func readReplaceableFile(path string) ([]byte, error) {
	file, err := openReplaceableFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func openReplaceableFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

// Transient readers outside our control can still deny replacement. Retry for
// a bounded interval without deleting the old file or changing its permissions.
func replaceFile(source, target string) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		err = os.Rename(source, target)
		if err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) &&
			!errors.Is(err, windows.ERROR_LOCK_VIOLATION) &&
			!errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return err
		}
		if attempt < 5 {
			time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
		}
	}
	return err
}
