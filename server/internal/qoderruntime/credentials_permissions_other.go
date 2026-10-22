//go:build !windows

package qoderruntime

import "os"

func privateCredentialPermissions(_ *os.File, info os.FileInfo) bool {
	return info.Mode().Perm()&0o077 == 0
}
