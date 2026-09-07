package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateJMAPRequiresHTTPS(t *testing.T) {
	cfg := &Config{Accounts: []Account{{
		Name: "a", Protocol: "jmap", LocalRoot: "/tmp/mail",
		Mailboxes: []Mailbox{{Remote: "role:inbox", Local: "INBOX"}},
		JMAP:      &JMAP{SessionURL: "http://mail.example/.well-known/jmap", Auth: "bearer", BearerToken: "x"},
	}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected non-HTTPS JMAP URL to be rejected")
	}
}

func TestValidateJMAPBasicRequiresUsername(t *testing.T) {
	cfg := &Config{Accounts: []Account{{
		Name: "a", Protocol: "jmap", LocalRoot: "/tmp/mail",
		Mailboxes: []Mailbox{{Remote: "role:inbox", Local: "INBOX"}},
		JMAP:      &JMAP{SessionURL: "https://mail.example/.well-known/jmap", Auth: "basic", Password: "x"},
	}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing basic-auth username to be rejected")
	}
}

func TestLoadTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	data := `state_dir = "~/state"

[[accounts]]
name = "personal"
protocol = "jmap"
local_root = "~/Maildir"
propagate_deletes = true

[[accounts.mailboxes]]
remote = "role:inbox"
local = "INBOX"

[accounts.jmap]
session_url = "https://mail.example/.well-known/jmap"
auth = "basic"
username = "user"
password_env = "MAIL_PASSWORD"
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(cfg.Accounts))
	}
	a := cfg.Accounts[0]
	if a.Name != "personal" || a.JMAP == nil || a.JMAP.PasswordEnv != "MAIL_PASSWORD" {
		t.Fatalf("unexpected account: %#v", a)
	}
}

func TestLoadTOMLRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	data := `state_dir = "/tmp/state"
unknown_option = true

[[accounts]]
name = "personal"
protocol = "jmap"
local_root = "/tmp/mail"

[[accounts.mailboxes]]
remote = "role:inbox"
local = "INBOX"

[accounts.jmap]
session_url = "https://mail.example/.well-known/jmap"
username = "user"
password_env = "MAIL_PASSWORD"
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unknown fields") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}
