package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedFoldersLoadWithoutRewritingMainConfig(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	path := filepath.Join(t.TempDir(), "config.toml")
	text := fmt.Sprintf("# retain this comment\nstate_dir = %q\n[[accounts]]\nname = 'mail'\nprotocol = 'imap'\nlocal_root = %q\n[[accounts.mailboxes]]\nremote = 'INBOX'\nlocal = '.'\n[accounts.imap]\naddress = 'mail.example:993'\nusername = 'me'\npassword_env = 'SECRET'\n", state, root)
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	a := &cfg.Accounts[0]
	for _, r := range []FolderOverride{{Remote: "Work", Local: "Folders/Work", Enabled: true}, {Remote: "INBOX", Local: ".", Enabled: false}} {
		if err := cfg.SaveFolderOverride(a, r); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Accounts[0].Mailboxes) != 1 || cfg.Accounts[0].Mailboxes[0].Remote != "Work" {
		t.Fatal(cfg.Accounts[0].Mailboxes)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != text {
		t.Fatal("rewrote main config")
	}
	changed := cfg.Accounts[0]
	changed.LocalRoot = t.TempDir()
	if cfg.FolderStatePath(&changed) == cfg.FolderStatePath(&cfg.Accounts[0]) {
		t.Fatal("different roots share folder metadata")
	}
	if err := cfg.SaveFolderOverride(&cfg.Accounts[0], FolderOverride{Remote: "Work", Local: "Folders/Work", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || len(cfg.Accounts[0].Mailboxes) != 0 {
		t.Fatal("disabling last mapping failed", err)
	}
}
func TestOperationLockAndCorruptFolderFile(t *testing.T) {
	cfg := &Config{StateDir: t.TempDir()}
	unlock, err := cfg.LockOperations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.LockOperations(); err == nil {
		t.Fatal("accepted concurrent operation")
	}
	unlock()
	unlock, err = cfg.LockOperations()
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	a := &Account{Name: "mail", LocalRoot: t.TempDir()}
	os.WriteFile(cfg.FolderStatePath(a), []byte("version = 999\n"), 0600)
	if err := cfg.ApplyFolderOverrides(a); err == nil {
		t.Fatal("accepted unknown sidecar schema")
	}
}
