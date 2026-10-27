package util

import "errors"

// RestrictPrivateFile protects an existing config without changing its contents.
func RestrictPrivateFile(path string) error {
	f, err := openPrivateFile(path)
	if err != nil {
		return err
	}
	return f.Close()
}

// WritePrivateFile restricts the opened file before writing secret data,
// including when an existing file inherited broader permissions.
func WritePrivateFile(path string, data []byte) error {
	f, err := openPrivateFile(path)
	if err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		return err
	}
	_, err = f.Write(data)
	return errors.Join(err, f.Close())
}
