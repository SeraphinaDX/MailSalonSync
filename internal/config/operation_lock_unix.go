//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package config

import (
	"golang.org/x/sys/unix"
	"os"
)

func lockOperationFile(f *os.File) (func(), error) {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, err
	}
	return func() {}, nil // Closing the descriptor releases the advisory lock.
}
