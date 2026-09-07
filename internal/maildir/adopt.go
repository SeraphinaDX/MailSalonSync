package maildir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnsureStableKey renames an existing Maildir message so its filename contains
// key while preserving its current Maildir subdirectory and :2, flags suffix.
// It does not alter the message contents or seen/unseen state.
func EnsureStableKey(path, key string) (string, error) {
	if path == "" || key == "" {
		return "", errors.New("path and stable key are required")
	}
	name := filepath.Base(path)
	if strings.Contains(name, key) {
		return path, nil
	}

	base, suffix := name, ""
	if idx := strings.LastIndex(name, ":2,"); idx >= 0 {
		base, suffix = name[:idx], name[idx:]
	}
	dst := filepath.Join(filepath.Dir(path), base+"."+key+suffix)
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("stable-key destination already exists: %s", dst)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Rename(path, dst); err != nil {
		return "", err
	}
	return dst, nil
}
