package maildir

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Dir struct {
	Path string
}

func Open(path string) *Dir { return &Dir{Path: path} }

func (d *Dir) Ensure() error {
	for _, sub := range []string{"tmp", "new", "cur"} {
		if err := os.MkdirAll(filepath.Join(d.Path, sub), 0o700); err != nil {
			return err
		}
	}
	return nil
}

func StableKey(remoteIdentity string) string {
	sum := sha256.Sum256([]byte(remoteIdentity))
	return "mailsalonsync-" + hex.EncodeToString(sum[:12])
}

func (d *Dir) Find(key string) (string, bool, error) {
	for _, sub := range []string{"new", "cur"} {
		entries, err := os.ReadDir(filepath.Join(d.Path, sub))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", false, err
		}
		for _, e := range entries {
			if !e.IsDir() && strings.Contains(e.Name(), key) {
				return filepath.Join(d.Path, sub, e.Name()), true, nil
			}
		}
	}
	return "", false, nil
}

func (d *Dir) Put(key string, raw []byte, seen bool) (string, error) {
	if err := d.Ensure(); err != nil {
		return "", err
	}
	if p, ok, err := d.Find(key); err != nil {
		return "", err
	} else if ok {
		return p, nil
	}
	host, _ := os.Hostname()
	host = strings.NewReplacer("/", "_", ":", "_").Replace(host)
	base := fmt.Sprintf("%d.%d_%s.%s", time.Now().UnixNano(), os.Getpid(), host, key)
	tmp := filepath.Join(d.Path, "tmp", base)
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return "", err
	}
	var final string
	if seen {
		final = filepath.Join(d.Path, "cur", base+":2,S")
	} else {
		final = filepath.Join(d.Path, "new", base)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return final, nil
}

func (d *Dir) Remove(key string) error {
	p, ok, err := d.Find(key)
	if err != nil || !ok {
		return err
	}
	return os.Remove(p)
}
