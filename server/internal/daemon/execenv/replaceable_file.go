//go:build !windows

package execenv

import "os"

func lockDiscoveryCache(string) (func(), error) { return func() {}, nil }

func readReplaceableFile(path string) ([]byte, error)   { return os.ReadFile(path) }
func openReplaceableFile(path string) (*os.File, error) { return os.Open(path) }
func replaceFile(source, target string) error           { return os.Rename(source, target) }
