package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Older MailSalon versions create empty .mss-lock files. They cannot be
// safely reclaimed automatically because the editor may still be running.
type collectionLockOwner struct {
	Version int    `json:"version"`
	PID     int    `json:"pid"`
	Host    string `json:"host"`
}

// LockCollection serializes collection syncs while remaining compatible with
// MailSalon's O_EXCL .mss-lock. A separate persistent OS-managed guard ensures
// only one modern sync can reclaim a dead owner's lock at a time.
func LockCollection(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, ".mss-lock")
	guard, err := os.OpenFile(path+".guard", os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	releaseGuard, err := lockOperationFile(guard)
	if err != nil {
		_ = guard.Close()
		return nil, fmt.Errorf("collection is locked (%s): another sync is running: %w", path, err)
	}
	closeGuard := func() {
		releaseGuard()
		_ = guard.Close()
	}
	host, err := os.Hostname()
	if err != nil {
		closeGuard()
		return nil, fmt.Errorf("cannot determine hostname for collection lock: %w", err)
	}
	if host == "" {
		closeGuard()
		return nil, errors.New("cannot determine hostname for collection lock")
	}
	if err := reclaimDeadCollectionLock(path, host); err != nil {
		closeGuard()
		return nil, err
	}
	mine, err := createCollectionLock(path, collectionLockOwner{Version: 1, PID: os.Getpid(), Host: host})
	if err != nil {
		closeGuard()
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("collection is locked (%s): another editor is active: %w", path, err)
		}
		return nil, fmt.Errorf("create collection lock (%s): %w", path, err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			// Do not remove a lock replaced by somebody else.
			if current, err := os.Lstat(path); err == nil && os.SameFile(mine, current) {
				_ = os.Remove(path)
			}
			closeGuard()
		})
	}, nil
}

// Write owner metadata first, then publish the file with an atomic no-replace
// hard link. Crashes during creation cannot leave an empty .mss-lock.
func createCollectionLock(path string, owner collectionLockOwner) (os.FileInfo, error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".mss-lock-tmp-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	data, err := json.Marshal(owner)
	if err != nil {
		return nil, err
	}
	if _, err = f.Write(append(data, '\n')); err != nil {
		return nil, err
	}
	if err = f.Sync(); err != nil {
		return nil, err
	}
	mine, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	// An older MailSalon may have acquired the path in the meantime.
	if err = os.Link(f.Name(), path); err != nil {
		return nil, err
	}
	return mine, nil
}

// Called only while holding .mss-lock.guard. Empty/legacy locks are left
// untouched: their owner could be an actively editing older MailSalon.
func reclaimDeadCollectionLock(path, host string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	unknown := fmt.Errorf("collection is locked (%s): legacy or unrecognized owner; stop MailSalon and MailSalonSync before manually removing .mss-lock", path)
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return unknown
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var owner collectionLockOwner
	if json.Unmarshal(data, &owner) != nil || owner.Version != 1 || owner.PID <= 0 || owner.Host == "" {
		return unknown
	}
	if owner.Host != host {
		return fmt.Errorf("collection is locked (%s): owner PID %d is on another host (%s); cannot verify", path, owner.PID, owner.Host)
	}
	alive, err := collectionPIDAlive(owner.PID)
	if err != nil {
		return fmt.Errorf("collection is locked (%s): cannot verify PID %d: %w", path, owner.PID, err)
	}
	if alive {
		return fmt.Errorf("collection is locked (%s): owner PID %d is still running", path, owner.PID)
	}
	// A modern contender cannot change the file while we hold the guard.
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(info, current) {
		return fmt.Errorf("collection lock (%s) changed during recovery; retry", path)
	}
	return os.Remove(path)
}
