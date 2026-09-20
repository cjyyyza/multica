//go:build !windows

package execenv

func rootSwapBlockedByOS(error) bool { return false }
