package pim

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
)

type fakeBackend struct {
	items           map[string]Object
	writes, deletes int
	fail            error
	snapshotHook    func()
}

func (b *fakeBackend) Snapshot(context.Context) ([]Object, error) {
	if b.snapshotHook != nil {
		b.snapshotHook()
	}
	if b.fail != nil {
		return nil, b.fail
	}
	out := []Object{}
	for _, o := range b.items {
		out = append(out, o)
	}
	return out, nil
}
func (b *fakeBackend) Put(_ context.Context, id, rev string, data []byte) (Object, error) {
	if id == "" {
		id = "created"
	} else if b.items[id].Revision != rev {
		return Object{}, errors.New("stale revision")
	}
	b.writes++
	o := Object{ID: id, Revision: "new", Data: data}
	b.items[id] = o
	return o, nil
}
func (b *fakeBackend) Delete(_ context.Context, o Object) error {
	b.deletes++
	delete(b.items, o.ID)
	return nil
}
func card(uid, name string) []byte {
	return []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nUID:" + uid + "\r\nFN:" + name + "\r\nX-EXTRA:preserve me\r\nEND:VCARD\r\n")
}
func setup(t *testing.T) (config.Collection, *fakeBackend, string) {
	t.Helper()
	c := config.Collection{Name: "contacts", Protocol: "carddav", Remote: "https://example.test/book/", LocalDir: t.TempDir()}
	b := &fakeBackend{items: map[string]Object{"a": {ID: "a", Revision: "1", Data: card("u1", "Alice")}}}
	if err := SyncCollection(context.Background(), c, b); err != nil {
		t.Fatal(err)
	}
	return c, b, filepath.Join(c.LocalDir, digest([]byte("a"))+".vcf")
}
func TestSyncDownloadUpdateCreateAndDelete(t *testing.T) {
	c, b, path := setup(t)
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "X-EXTRA") {
		t.Fatal("lost unknown field")
	}
	if err := os.WriteFile(path, card("u1", "Local edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SyncCollection(context.Background(), c, b); err != nil {
		t.Fatal(err)
	}
	if b.writes != 1 || !strings.Contains(string(b.items["a"].Data), "Local edit") {
		t.Fatal("did not upload edit")
	}
	b.items["a"] = Object{ID: "a", Revision: "2", Data: card("u1", "Remote edit")}
	if err := SyncCollection(context.Background(), c, b); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "Remote edit") {
		t.Fatal("did not download edit")
	}
	os.WriteFile(filepath.Join(c.LocalDir, "new.vcf"), card("u2", "Bob"), 0600)
	if err := SyncCollection(context.Background(), c, b); err != nil {
		t.Fatal(err)
	}
	if b.writes != 2 {
		t.Fatal("did not create local-only item")
	}
	c.PropagateDeletes = true
	os.Remove(path)
	if err := SyncCollection(context.Background(), c, b); err != nil {
		t.Fatal(err)
	}
	if b.deletes != 1 {
		t.Fatal("did not delete remote item")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("redownloaded deleted item")
	}
}
func TestSyncDeleteDisabledAndRemoteBackup(t *testing.T) {
	c, b, path := setup(t)
	os.Remove(path)
	if err := SyncCollection(context.Background(), c, b); err != nil {
		t.Fatal(err)
	}
	if b.deletes != 0 {
		t.Fatal("deleted without opt-in")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("did not restore missing local item")
	}
	delete(b.items, "a")
	if err := SyncCollection(context.Background(), c, b); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("remote deletion not reflected locally")
	}
	if _, err := os.Stat(filepath.Join(c.LocalDir, ".mss-backup", filepath.Base(path))); err != nil {
		t.Fatal("missing deletion backup")
	}
}
func TestSyncConflictsPreserveBothCopies(t *testing.T) {
	for _, test := range []string{"edit-edit", "local-delete", "remote-delete"} {
		t.Run(test, func(t *testing.T) {
			c, b, path := setup(t)
			c.PropagateDeletes = true
			if test == "local-delete" {
				os.Remove(path)
			} else {
				os.WriteFile(path, card("u1", "Local"), 0600)
			}
			if test == "remote-delete" {
				delete(b.items, "a")
			} else {
				b.items["a"] = Object{ID: "a", Revision: "2", Data: card("u1", "Remote")}
			}
			if err := SyncCollection(context.Background(), c, b); err == nil || !strings.Contains(err.Error(), "conflict") {
				t.Fatalf("expected conflict: %v", err)
			}
			if b.writes != 0 || b.deletes != 0 {
				t.Fatal("mutated conflicted collection")
			}
			if test != "local-delete" {
				data, _ := os.ReadFile(path)
				if !strings.Contains(string(data), "Local") {
					t.Fatal("lost local edit")
				}
			}
			if test != "remote-delete" {
				data, err := os.ReadFile(filepath.Join(c.LocalDir, ".mss-conflicts", digest([]byte("a"))+".vcf"))
				if err != nil || !strings.Contains(string(data), "Remote") {
					t.Fatal("remote conflict copy missing")
				}
			}
		})
	}
}
func TestSyncRefusesFailedSnapshotAndConcurrentLocalEdit(t *testing.T) {
	c, b, path := setup(t)
	b.fail = errors.New("partial listing")
	if err := SyncCollection(context.Background(), c, b); err == nil {
		t.Fatal("ignored incomplete listing")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("lost local item")
	}
	b.fail = nil
	b.items["a"] = Object{ID: "a", Revision: "2", Data: card("u1", "Remote")}
	b.snapshotHook = func() { os.WriteFile(path, card("u1", "During sync"), 0600) }
	if err := SyncCollection(context.Background(), c, b); err == nil {
		t.Fatal("overwrote concurrent local edit")
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "During sync") {
		t.Fatal("lost concurrent local edit")
	}
}
func TestSyncAdoptsMatchingUIDAndRefusesDuplicate(t *testing.T) {
	c := config.Collection{Protocol: "carddav", Remote: "https://example.test/", LocalDir: t.TempDir()}
	b := &fakeBackend{items: map[string]Object{"a": {ID: "a", Revision: "1", Data: card("u1", "Alice")}}}
	os.WriteFile(filepath.Join(c.LocalDir, "existing.vcf"), card("u1", "Alice"), 0600)
	if err := SyncCollection(context.Background(), c, b); err != nil {
		t.Fatal(err)
	}
	if b.writes != 0 {
		t.Fatal("uploaded duplicate instead of adopting")
	}
	os.WriteFile(filepath.Join(c.LocalDir, "duplicate.vcf"), card("u1", "Alice"), 0600)
	if err := SyncCollection(context.Background(), c, b); err == nil {
		t.Fatal("accepted duplicate UID")
	}
}
func TestUIDRejectsMalformedContainers(t *testing.T) {
	for _, data := range []string{"BEGIN:VCARD\nUID:a\nEND:VEVENT", "BEGIN:VCARD\nFN:no uid\nEND:VCARD", "BEGIN:VCARD\nUID:a\nEND:VCARD\nBEGIN:VCARD\nUID:b\nEND:VCARD"} {
		if _, err := UID([]byte(data), ".vcf"); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
	if uid, err := UID([]byte("BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:a\nEND:VEVENT\nBEGIN:VEVENT\nUID:a\nRECURRENCE-ID:20260930T010000Z\nEND:VEVENT\nEND:VCALENDAR"), ".ics"); err != nil || uid != "a" {
		t.Fatalf("rejected recurrence exceptions: %s %v", uid, err)
	}
}
func TestSyncLockAndConfigurationBinding(t *testing.T) {
	c, b, _ := setup(t)
	os.WriteFile(filepath.Join(c.LocalDir, ".mss-lock"), nil, 0600)
	if err := SyncCollection(context.Background(), c, b); err == nil {
		t.Fatal("ignored lock")
	}
	os.Remove(filepath.Join(c.LocalDir, ".mss-lock"))
	c.Remote = "https://other.test/"
	if err := SyncCollection(context.Background(), c, b); err == nil {
		t.Fatal("reused state with different remote")
	}
}
