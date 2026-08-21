package maildir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindSurvivesNewToCurRename(t *testing.T) {
	d := Open(t.TempDir())
	key := StableKey("account\nremote-id")
	p, err := d.Put(key, []byte("Subject: test\r\n\r\nbody\r\n"), false)
	if err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(d.Path, "cur", filepath.Base(p)+":2,S")
	if err := os.Rename(p, newPath); err != nil {
		t.Fatal(err)
	}
	got, ok, err := d.Find(key)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != newPath {
		t.Fatalf("Find = %q, %v; want %q, true", got, ok, newPath)
	}
}
