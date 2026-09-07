package syncer

import (
	"os"
	"path/filepath"
	"testing"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/maildir"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/state"
)

func TestTrackedLocalAdoptMessagesUsesStateMapKeyOverEntryLabels(t *testing.T) {
	root := t.TempDir()
	a := &config.Account{
		Name:      "acct",
		Protocol:  "jmap",
		LocalRoot: root,
		Mailboxes: []config.Mailbox{{Remote: "role:inbox", Local: "INBOX"}},
	}
	key := "mailsalonsync-0123456789abcdef01234567"
	dir := maildir.Open(filepath.Join(root, "INBOX"))
	if _, err := dir.Put(key, []byte("Message-ID: <x@example>\r\nSubject: test\r\n\r\nbody\r\n"), true); err != nil {
		t.Fatal(err)
	}

	s := state.New()
	stateKey := state.Key("jmap", "role:inbox", "server-id")
	s.Entries[stateKey] = state.Entry{
		Protocol:      "jmap",
		RemoteMailbox: "Inbox",
		RemoteID:      "server-id",
		LocalMailbox:  "Inbox",
		FileKey:       key,
	}

	got, err := trackedLocalAdoptMessages(a, a.Mailboxes[0], "jmap", s)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("tracked candidates = %d, want 1", len(got))
	}
	if got[0].FileKey != key || got[0].RemoteID != "server-id" {
		t.Fatalf("candidate = %#v, want key %q remote server-id", got[0], key)
	}
}

func TestTrackedLocalAdoptMessagesRejectsWrongStateNamespace(t *testing.T) {
	root := t.TempDir()
	a := &config.Account{
		Name:      "acct",
		Protocol:  "jmap",
		LocalRoot: root,
		Mailboxes: []config.Mailbox{{Remote: "role:inbox", Local: "INBOX"}},
	}
	key := "mailsalonsync-0123456789abcdef01234567"
	dir := maildir.Open(filepath.Join(root, "INBOX"))
	if _, err := dir.Put(key, []byte("Subject: test\r\n\r\nbody\r\n"), true); err != nil {
		t.Fatal(err)
	}

	s := state.New()
	s.Entries[state.Key("jmap", "role:sent", "server-id")] = state.Entry{
		Protocol:      "jmap",
		RemoteMailbox: "role:inbox",
		RemoteID:      "server-id",
		LocalMailbox:  "INBOX",
		FileKey:       key,
	}
	got, err := trackedLocalAdoptMessages(a, a.Mailboxes[0], "jmap", s)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("tracked candidates = %d, want 0 for wrong authoritative state namespace", len(got))
	}
}

func TestReconcileLegacyDuplicateWithMismatchedEntryLabels(t *testing.T) {
	root := t.TempDir()
	stateDir := t.TempDir()
	a := &config.Account{
		Name:      "acct",
		Protocol:  "jmap",
		LocalRoot: root,
		Mailboxes: []config.Mailbox{{Remote: "role:inbox", Local: "INBOX"}},
	}
	mailboxPath := filepath.Join(root, "INBOX")
	dir := maildir.Open(mailboxPath)
	if err := dir.Ensure(); err != nil {
		t.Fatal(err)
	}
	raw := []byte("Message-ID: <x@example>\r\nDate: Mon, 7 Sep 2026 12:00:00 -0400\r\nFrom: sender@example.com\r\nTo: user@example.com\r\nSubject: test\r\n\r\nbody\r\n")
	key := "mailsalonsync-0123456789abcdef01234567"
	if _, err := dir.Put(key, raw, true); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(mailboxPath, "cur", "legacy:2,S")
	if err := os.WriteFile(legacy, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	s := state.New()
	s.Entries[state.Key("jmap", "role:inbox", "server-id")] = state.Entry{
		Protocol:      "jmap",
		RemoteMailbox: "Inbox",
		RemoteID:      "server-id",
		LocalMailbox:  "Inbox",
		FileKey:       key,
	}
	cfg := &config.Config{StateDir: stateDir}
	locals := []localAdoptMessage{{Path: legacy, Raw: raw}}
	remaining, retired, err := reconcileLegacyDuplicates(cfg, a, a.Mailboxes[0], "jmap", locals, s, true, discardReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if retired != 1 || len(remaining) != 0 {
		t.Fatalf("retired=%d remaining=%d, want 1/0", retired, len(remaining))
	}
}

type discardReporter struct{}

func (discardReporter) Set(string, string, string) {}
func (discardReporter) Added(string, string)       {}
func (discardReporter) Deleted(string, string)     {}
