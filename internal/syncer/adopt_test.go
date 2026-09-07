package syncer

import (
	"os"
	"path/filepath"
	"testing"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/maildir"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/state"
)

func TestMatchRemotePrefersExactContent(t *testing.T) {
	local := []byte("Message-ID: <same@example>\r\nSubject: Local\r\n\r\nbody\r\n")
	remotes := []remoteAdoptMessage{
		{ID: "1", Raw: []byte("Message-ID: <same@example>\r\nSubject: Different\r\n\r\nbody\r\n")},
		{ID: "2", Raw: local},
	}
	got := matchRemote(local, remotes, map[string]bool{})
	if len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("matches = %#v, want exact ID 2", got)
	}
}

func TestMatchRemoteFallsBackToUniqueMessageID(t *testing.T) {
	local := []byte("Message-ID: <unique@example>\nSubject: Local copy\n\nbody\n")
	remotes := []remoteAdoptMessage{
		{ID: "9", Raw: []byte("Message-ID: <unique@example>\r\nSubject: Server copy\r\n\r\nbody changed\r\n")},
	}
	got := matchRemote(local, remotes, map[string]bool{})
	if len(got) != 1 || got[0].ID != "9" {
		t.Fatalf("matches = %#v, want ID 9", got)
	}
}

func TestMatchRemoteRejectsAmbiguousMessageID(t *testing.T) {
	local := []byte("Message-ID: <dupe@example>\r\n\r\nlocal\r\n")
	remotes := []remoteAdoptMessage{
		{ID: "1", Raw: []byte("Message-ID: <dupe@example>\r\n\r\na\r\n")},
		{ID: "2", Raw: []byte("Message-ID: <dupe@example>\r\n\r\nb\r\n")},
	}
	if got := matchRemote(local, remotes, map[string]bool{}); len(got) != 2 {
		t.Fatalf("matches = %#v, want ambiguous pair", got)
	}
}

func TestMatchRemoteFallsBackToUniqueHeaderFingerprint(t *testing.T) {
	local := []byte("Message-ID: <local-changed@example>\r\nDate: Tue, 1 Sep 2026 10:30:00 -0400\r\nFrom: Sender <sender@example.com>\r\nTo: User <user@example.com>\r\nSubject: =?UTF-8?Q?Hello_World?=\r\nX-OfflineIMAP: local-copy\r\n\r\nlocal body\r\n")
	remotes := []remoteAdoptMessage{
		{ID: "11", Raw: []byte("Message-ID: <server@example>\r\nDate: Tue, 1 Sep 2026 14:30:00 +0000\r\nFrom: sender@example.com\r\nTo: user@example.com\r\nSubject: Hello World\r\n\r\nserver body\r\n")},
	}
	got, method := matchRemoteDetailed(local, remotes, map[string]bool{})
	if len(got) != 1 || got[0].ID != "11" {
		t.Fatalf("matches = %#v, want ID 11", got)
	}
	if method != "header" {
		t.Fatalf("method = %q, want header", method)
	}
}

func TestTrackedEntryNeedsAdoptionForLegacyFileKey(t *testing.T) {
	a := &config.Account{LocalRoot: t.TempDir()}
	e := state.Entry{FileKey: "legacy-file-name"}
	needs, err := trackedEntryNeedsAdoption(a, e)
	if err != nil {
		t.Fatal(err)
	}
	if !needs {
		t.Fatal("legacy file key should need adoption")
	}
}

func TestTrackedEntryNeedsAdoptionWhenTaggedFileMissing(t *testing.T) {
	a := &config.Account{
		LocalRoot: t.TempDir(),
		Mailboxes: []config.Mailbox{{Remote: "INBOX", Local: "INBOX"}},
	}
	e := state.Entry{FileKey: "mailsalonsync-deadbeefdeadbeefdeadbeef"}
	needs, err := trackedEntryNeedsAdoption(a, e)
	if err != nil {
		t.Fatal(err)
	}
	if !needs {
		t.Fatal("missing tagged file should need adoption")
	}
}

func TestTrackedEntryDoesNotNeedAdoptionWhenTaggedFileExists(t *testing.T) {
	root := t.TempDir()
	a := &config.Account{
		LocalRoot: root,
		Mailboxes: []config.Mailbox{{Remote: "INBOX", Local: "INBOX"}},
	}
	key := "mailsalonsync-0123456789abcdef01234567"
	dir := maildir.Open(filepath.Join(root, "INBOX"))
	if _, err := dir.Put(key, []byte("Subject: test\r\n\r\nbody\r\n"), true); err != nil {
		t.Fatal(err)
	}
	e := state.Entry{FileKey: key}
	needs, err := trackedEntryNeedsAdoption(a, e)
	if err != nil {
		t.Fatal(err)
	}
	if needs {
		t.Fatal("existing tagged file should not need adoption")
	}
}

func TestUntaggedLocalMessagesFindsLegacyMail(t *testing.T) {
	root := t.TempDir()
	for _, sub := range []string{"new", "cur", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "cur", "legacy:2,S"), []byte("Subject: old\r\n\r\nbody\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	msgs, err := untaggedLocalMessages(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("found %d untagged messages, want 1", len(msgs))
	}
}
