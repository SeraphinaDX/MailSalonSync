package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// LockOperations serializes mail synchronization and folder mutations across
// processes. On supported platforms the OS releases it on exit, even when a
// GUI cancellation kills the child process. The file itself must stay in place
// so all processes lock the same inode/handle.
func (c *Config) LockOperations() (func(), error) {
	if err := os.MkdirAll(c.StateDir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(c.StateDir, "mail-operation.lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	release, err := lockOperationFile(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("another mail operation is running (lock %s): %w", path, err)
	}
	unlock := func() { release(); _ = f.Close() }
	if err := f.Truncate(0); err != nil {
		unlock()
		return nil, err
	}
	if _, err = fmt.Fprintf(f, "pid=%d\n", os.Getpid()); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}
