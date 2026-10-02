//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !windows

package config

import "os"

// Conservative fallback for platforms without the supported advisory API.
func lockOperationFile(f *os.File) (func(), error) {
	path := f.Name() + ".held"
	held, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	held.Close()
	return func() { _ = os.Remove(path) }, nil
}
