package jmap

import "testing"

func TestResolveMailbox(t *testing.T) {
	role := "inbox"
	boxes := []Mailbox{{ID: "1", Name: "Inbox", FullName: "Inbox", Role: &role}, {ID: "2", Name: "Lists", FullName: "Archive/Lists"}}
	b, err := ResolveMailbox("role:inbox", boxes)
	if err != nil || b.ID != "1" {
		t.Fatalf("resolve role: %v %#v", err, b)
	}
	b, err = ResolveMailbox("Archive/Lists", boxes)
	if err != nil || b.ID != "2" {
		t.Fatalf("resolve path: %v %#v", err, b)
	}
}

func TestExpandTemplate(t *testing.T) {
	got := expandTemplate("https://x/{accountId}/{blobId}/{name}?accept={type}", map[string]string{
		"accountId": "a", "blobId": "b", "name": "m.eml", "type": "message/rfc822",
	})
	want := "https://x/a/b/m.eml?accept=message%2Frfc822"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
