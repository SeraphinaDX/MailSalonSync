package config

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// FolderOverride augments or disables a mapping without rewriting credentials
// or comments in the user's main TOML file. Disabled entries retain their paths.
type FolderOverride struct {
	Remote  string `toml:"remote"`
	Local   string `toml:"local"`
	Enabled bool   `toml:"enabled"`
}
type folderFile struct {
	Version int              `toml:"version"`
	Folders []FolderOverride `toml:"folders"`
}

func (c *Config) FolderStatePath(a *Account) string {
	identity := a.Name + "\n" + a.Protocol + "\n" + a.LocalRoot
	if a.IMAP != nil {
		identity += "\n" + a.IMAP.Address + "\n" + a.IMAP.Username
	}
	if a.JMAP != nil {
		identity += "\n" + a.JMAP.SessionURL + "\n" + a.JMAP.Username + "\n" + a.JMAP.AccountID
	}
	return filepath.Join(c.StateDir, fmt.Sprintf("folders-%x.toml", sha256.Sum256([]byte(identity))))
}
func (c *Config) FolderOverrides(a *Account) ([]FolderOverride, error) {
	path := c.FolderStatePath(a)
	var f folderFile
	md, err := toml.DecodeFile(path, &f)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("managed folders: %w", err)
	}
	if f.Version != 1 || len(md.Undecoded()) != 0 {
		return nil, fmt.Errorf("invalid managed folders file %s", path)
	}
	seen := map[string]bool{}
	for _, m := range f.Folders {
		if m.Remote == "" || seen[m.Remote] {
			return nil, fmt.Errorf("invalid or duplicate managed remote folder %q", m.Remote)
		}
		if m.Local == "" || strings.ContainsRune(m.Local, 0) {
			return nil, fmt.Errorf("invalid managed local folder")
		}
		seen[m.Remote] = true
	}
	return f.Folders, nil
}

// ApplyFolderOverrides is called after validating the original configuration:
// disabling the last mapped mailbox is valid, and must not delete cached mail.
func (c *Config) ApplyFolderOverrides(a *Account) error {
	records, err := c.FolderOverrides(a)
	if err != nil {
		return err
	}
	for _, m := range records {
		kept := a.Mailboxes[:0]
		for _, old := range a.Mailboxes {
			if old.Remote != m.Remote {
				kept = append(kept, old)
			}
		}
		a.Mailboxes = kept
		if m.Enabled {
			a.Mailboxes = append(a.Mailboxes, Mailbox{Remote: m.Remote, Local: m.Local})
		}
	}
	paths := map[string]string{}
	for _, m := range a.Mailboxes {
		path := m.Local
		if !filepath.IsAbs(path) {
			path = filepath.Join(a.LocalRoot, path)
		}
		path = filepath.Clean(path)
		if remote, ok := paths[path]; ok {
			return fmt.Errorf("folders %q and %q share a local Maildir", remote, m.Remote)
		}
		paths[path] = m.Remote
	}
	return nil
}
func (c *Config) SaveFolderOverride(a *Account, record FolderOverride) error {
	records, err := c.FolderOverrides(a)
	if err != nil {
		return err
	}
	if record.Remote == "" || record.Local == "" || strings.ContainsRune(record.Local, 0) {
		return fmt.Errorf("invalid folder mapping")
	}
	found := false
	for i := range records {
		if records[i].Remote == record.Remote {
			records[i] = record
			found = true
		}
	}
	if !found {
		records = append(records, record)
	}
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(folderFile{Version: 1, Folders: records}); err != nil {
		return err
	}
	path := c.FolderStatePath(a)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".folders-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(b.Bytes()); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// GUI-created paths are always relative and portable. Never allow a folder
// name to become an escape from local_root or a Maildir control directory.
func ValidateFolderLocal(local string) error {
	if local == "" || filepath.IsAbs(local) || strings.ContainsAny(local, "\\:\x00\r\n<>\"|?*") {
		return fmt.Errorf("invalid local folder path %q", local)
	}
	for _, p := range strings.Split(local, "/") {
		if p == "" || p == "." || p == ".." || p == "cur" || p == "new" || p == "tmp" || strings.HasSuffix(p, ".") || strings.HasSuffix(p, " ") {
			return fmt.Errorf("invalid local folder component %q", p)
		}
		base := strings.ToUpper(strings.SplitN(p, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
			return fmt.Errorf("reserved local folder component %q", p)
		}
	}
	return nil
}
