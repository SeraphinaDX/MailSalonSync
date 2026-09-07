package syncer

import "testing"

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
