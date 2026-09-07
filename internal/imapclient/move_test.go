package imapclient

import "testing"

func TestParseCopyUID(t *testing.T) {
	uid, err := parseCopyUID("A0007 OK [COPYUID 42 17 991] MOVE completed")
	if err != nil {
		t.Fatalf("parseCopyUID: %v", err)
	}
	if uid != 991 {
		t.Fatalf("uid = %d, want 991", uid)
	}
}

func TestParseCopyUIDRejectsSet(t *testing.T) {
	if _, err := parseCopyUID("A0007 OK [COPYUID 42 17 991:992] MOVE completed"); err == nil {
		t.Fatal("expected error for multi-UID COPYUID response")
	}
}
