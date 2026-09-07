package syncer

import (
	"os"
	"path/filepath"
	"testing"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/maildir"
)

func TestFindTrackedMoveToArchive(t *testing.T) {
	root := t.TempDir()
	a := &config.Account{
		Name:      "test",
		LocalRoot: root,
		Mailboxes: []config.Mailbox{
			{Remote: "INBOX", Local: "INBOX"},
			{Remote: "Archive", Local: "Archive"},
		},
	}

	archive := maildir.Open(filepath.Join(root, "Archive"))
	if _, err := archive.Put("stable-key", []byte("Subject: test\r\n\r\nbody\r\n"), true); err != nil {
		t.Fatal(err)
	}

	move, err := findTrackedMove(a, a.Mailboxes[0], "stable-key")
	if err != nil {
		t.Fatal(err)
	}
	if move == nil {
		t.Fatal("expected a move destination")
	}
	if move.Local != "Archive" || move.Remote != "Archive" {
		t.Fatalf("move = %#v, want Archive", *move)
	}
}

func TestFindTrackedMoveRejectsAmbiguousDestination(t *testing.T) {
	root := t.TempDir()
	a := &config.Account{
		Name:      "test",
		LocalRoot: root,
		Mailboxes: []config.Mailbox{
			{Remote: "INBOX", Local: "INBOX"},
			{Remote: "Archive", Local: "Archive"},
			{Remote: "Projects", Local: "Projects"},
		},
	}

	for _, name := range []string{"Archive", "Projects"} {
		d := maildir.Open(filepath.Join(root, name))
		if _, err := d.Put("stable-key", []byte("Subject: test\r\n\r\nbody\r\n"), false); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := findTrackedMove(a, a.Mailboxes[0], "stable-key"); err == nil {
		t.Fatal("expected ambiguous move error")
	}
}

func TestFindTrackedMoveIgnoresUnmappedFile(t *testing.T) {
	root := t.TempDir()
	a := &config.Account{
		Name:      "test",
		LocalRoot: root,
		Mailboxes: []config.Mailbox{
			{Remote: "INBOX", Local: "INBOX"},
			{Remote: "Archive", Local: "Archive"},
		},
	}

	unmapped := filepath.Join(root, "Loose")
	for _, sub := range []string{"cur", "new", "tmp"} {
		if err := os.MkdirAll(filepath.Join(unmapped, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(unmapped, "cur", "123.stable-key:2,S"), []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}

	move, err := findTrackedMove(a, a.Mailboxes[0], "stable-key")
	if err != nil {
		t.Fatal(err)
	}
	if move != nil {
		t.Fatalf("unexpected move to %#v", *move)
	}
}
