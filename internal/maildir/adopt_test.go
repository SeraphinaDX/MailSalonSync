package maildir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureStableKeyPreservesFlags(t *testing.T) {
	root := t.TempDir()
	if err := Ensure(root); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(root, "cur", "old-message:2,RS")
	if err := os.WriteFile(old, []byte("Subject: test\r\n\r\nbody\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := EnsureStableKey(old, "mailsalonsync-0123456789abcdef01234567")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filepath.Base(got), "mailsalonsync-0123456789abcdef01234567") {
		t.Fatalf("retagged name %q lacks stable key", filepath.Base(got))
	}
	if !strings.HasSuffix(got, ":2,RS") {
		t.Fatalf("retagged path %q did not preserve flags", got)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old path still exists: %v", err)
	}
}
