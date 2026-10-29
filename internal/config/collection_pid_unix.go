//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package config

import (
	"errors"

	"golang.org/x/sys/unix"
)

func collectionPIDAlive(pid int) (bool, error) {
	err := unix.Kill(pid, 0)
	switch {
	case err == nil, errors.Is(err, unix.EPERM):
		return true, nil
	case errors.Is(err, unix.ESRCH):
		return false, nil
	default:
		return true, err
	}
}
