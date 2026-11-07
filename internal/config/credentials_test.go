package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestKeyringCredentials(t *testing.T) {
	keyring.MockInit()
	a := Account{Name: "mail", Protocol: "imap", IMAP: &IMAP{Address: "imap.example:993", Username: "user"}}
	if _, err := a.IMAPPassword(); err == nil {
		t.Fatal("missing credential accepted")
	}
	if err := a.Credential().Store(" secret \t"); err != nil {
		t.Fatal(err)
	}
	if got, err := a.IMAPPassword(); err != nil || got != " secret \t" {
		t.Fatalf("got %q, %v", got, err)
	}
	a.IMAP.Address = "other.example:993"
	if _, err := a.IMAPPassword(); err == nil {
		t.Fatal("credential reused for another endpoint")
	}
	a.IMAP.Address = "imap.example:993"
	t.Setenv("TEST_MAIL_SECRET", "override")
	a.IMAP.PasswordEnv = "TEST_MAIL_SECRET"
	if got, err := a.IMAPPassword(); err != nil || got != "override" {
		t.Fatalf("got %q, %v", got, err)
	}
	a.IMAP.PasswordEnv = ""
	if err := a.Credential().Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.IMAPPassword(); err == nil {
		t.Fatal("deleted credential still available")
	}
	if err := a.Credential().Store(""); err == nil {
		t.Fatal("empty secret accepted")
	}
}

func TestCollectionAndJMAPCredentials(t *testing.T) {
	keyring.MockInit()
	for _, auth := range []string{"basic", "bearer"} {
		c := Collection{Name: "contacts", JMAP: &JMAP{SessionURL: "https://example/jmap", Username: "user", Auth: auth}}
		if err := c.Credential().Store(auth); err != nil {
			t.Fatal(err)
		}
		if got, err := c.Secret(); err != nil || got != auth {
			t.Fatalf("got %q, %v", got, err)
		}
	}
	c := Collection{Name: "calendar", Protocol: "caldav", Remote: "https://example/cal", DAV: &DAV{Username: "user"}}
	if err := c.Credential().Store("dav"); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Secret(); err != nil || got != "dav" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestLoadRejectsPlaintextSecrets(t *testing.T) {
	for _, field := range []string{"password", "bearer_token"} {
		path := filepath.Join(t.TempDir(), "config.toml")
		content := "[[accounts]]\nname='mail'\n[accounts.jmap]\n" + field + "='do-not-print-me'\n"
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "plaintext secret") || strings.Contains(err.Error(), "do-not-print-me") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestDAVCollectionsShareServerLogin(t *testing.T) {
	keyring.MockInit()
	contacts := Collection{Name: "contacts", Protocol: "carddav", Remote: "https://example/addressbooks/personal", DAV: &DAV{Username: "user"}}
	calendar := Collection{Name: "calendar", Protocol: "caldav", Remote: "https://example/calendars/work", DAV: &DAV{Username: "user"}}
	if err := contacts.Credential().Store("shared"); err != nil {
		t.Fatal(err)
	}
	if got, err := calendar.Secret(); err != nil || got != "shared" {
		t.Fatalf("got %q, %v", got, err)
	}
	calendar.DAV.Username = "other"
	if _, err := calendar.Secret(); err == nil {
		t.Fatal("reused another user's password")
	}
	calendar.DAV.Username = "user"
	calendar.Remote = "https://other/calendar"
	if _, err := calendar.Secret(); err == nil {
		t.Fatal("reused another server's password")
	}
}
