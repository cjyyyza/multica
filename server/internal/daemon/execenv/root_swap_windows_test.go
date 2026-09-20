//go:build windows

package execenv

import (
	"errors"
	"golang.org/x/sys/windows"
)

func rootSwapBlockedByOS(err error) bool { return errors.Is(err, windows.ERROR_SHARING_VIOLATION) }
