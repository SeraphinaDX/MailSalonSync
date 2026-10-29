//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !windows

package config

// Unknown platforms cannot safely determine if another process has exited.
func collectionPIDAlive(pid int) (bool, error) {
	return true, nil
}
