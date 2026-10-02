package config

import (
	"golang.org/x/sys/windows"
	"os"
)

func lockOperationFile(f *os.File) (func(), error) {
	var position windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &position); err != nil {
		return nil, err
	}
	return func() {}, nil // Closing the handle releases the kernel lock.
}
