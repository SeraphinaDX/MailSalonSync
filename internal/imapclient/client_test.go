package imapclient

import "testing"

func TestQuote(t *testing.T) {
	got := quote(`a"b\c`)
	want := `"a\"b\\c"`
	if got != want {
		t.Fatalf("quote = %q, want %q", got, want)
	}
}

func TestTaggedOK(t *testing.T) {
	if !taggedOK("A0001 OK done") {
		t.Fatal("expected OK")
	}
	if taggedOK("A0001 NO nope") {
		t.Fatal("unexpected OK")
	}
}
