package folders

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
)

func fakeFolders(t *testing.T) (*config.Config, *config.Account, *backend, *int) {
	t.Helper()
	a := config.Account{Name: "personal", Protocol: "imap", LocalRoot: t.TempDir(), Mailboxes: []config.Mailbox{{Remote: "INBOX", Local: "."}}}
	cfg := &config.Config{StateDir: t.TempDir(), Accounts: []config.Account{a}}
	aPtr := &cfg.Accounts[0]
	rows := []Folder{{ID: "INBOX", Name: "INBOX", Selectable: true, CanCreateChild: true}, {ID: "Projects", Name: "Projects", Selectable: true, CanCreateChild: true}}
	writes := new(int)
	b := &backend{list: func() ([]Folder, error) { return append([]Folder{}, rows...), nil }, matches: func(remote string, f Folder) bool { return remote == f.ID }}
	b.create = func(name string, parent *Folder) (Folder, error) {
		*writes++
		if parent != nil {
			name = parent.Name + "/" + name
		}
		f := Folder{ID: name, Name: name, Subscribed: true, Selectable: true}
		rows = append(rows, f)
		return f, nil
	}
	b.subscribe = func(f Folder, on bool) error {
		*writes++
		for i := range rows {
			if rows[i].ID == f.ID {
				rows[i].Subscribed = on
			}
		}
		return nil
	}
	return cfg, aPtr, b, writes
}
func TestCreateSubscribePersistAndUnsubscribeKeepsMail(t *testing.T) {
	cfg, a, b, _ := fakeFolders(t)
	result, err := runBackend(cfg, a, Request{Action: "create", Name: "Work", Parent: "Projects"}, b)
	if err != nil {
		t.Fatal(err)
	}
	var created Folder
	for _, f := range result.Folders {
		if f.Name == "Projects/Work" {
			created = f
		}
	}
	if !created.Syncing || !created.Subscribed || created.Local != "Folders/Projects/Work" {
		t.Fatal(created)
	}
	path := filepath.Join(a.LocalRoot, created.Local, "cur", "message:2,S")
	if err := os.WriteFile(path, []byte("kept"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = runBackend(cfg, a, Request{Action: "unsubscribe", Mailbox: created.ID}, b); err != nil {
		t.Fatal(err)
	}
	if len(a.Mailboxes) != 1 {
		t.Fatal(a.Mailboxes)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "kept" {
		t.Fatal("lost cached mail", err)
	}
	// A fresh process reconstructs the same disabled override and resumes the
	// original path when subscribed again.
	fresh := *a
	fresh.Mailboxes = []config.Mailbox{{Remote: "INBOX", Local: "."}}
	if err := cfg.ApplyFolderOverrides(&fresh); err != nil {
		t.Fatal(err)
	}
	if len(fresh.Mailboxes) != 1 {
		t.Fatal(fresh.Mailboxes)
	}
	if _, err = runBackend(cfg, &fresh, Request{Action: "subscribe", Mailbox: created.ID}, b); err != nil {
		t.Fatal(err)
	}
	if len(fresh.Mailboxes) != 2 || fresh.Mailboxes[1].Local != created.Local {
		t.Fatal(fresh.Mailboxes)
	}
	// The configured root-as-INBOX mapping can also be disabled and restored.
	if _, err = runBackend(cfg, &fresh, Request{Action: "unsubscribe", Mailbox: "INBOX"}, b); err != nil {
		t.Fatal(err)
	}
	if _, err = runBackend(cfg, &fresh, Request{Action: "subscribe", Mailbox: "INBOX"}, b); err != nil {
		t.Fatal(err)
	}
}
func TestFolderPreflightPreventsServerMutations(t *testing.T) {
	for _, scenario := range []string{"traversal", "mapped collision", "existing local", "symlink parent", "bad name", "bad parent", "missing mailbox", "changed local", "readonly state"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, a, b, writes := fakeFolders(t)
			req := Request{Action: "create", Name: "New"}
			switch scenario {
			case "traversal":
				req.Local = "../escape"
			case "mapped collision":
				a.Mailboxes = append(a.Mailboxes, config.Mailbox{Remote: "Old", Local: "Folders/New"})
			case "existing local":
				os.MkdirAll(filepath.Join(a.LocalRoot, "Folders/New"), 0700)
			case "symlink parent":
				os.Symlink(t.TempDir(), filepath.Join(a.LocalRoot, "Folders"))
			case "bad name":
				req.Name = "x\r\nSUBSCRIBE y"
			case "bad parent":
				req.Parent = "missing"
			case "missing mailbox":
				req = Request{Action: "subscribe", Mailbox: "missing"}
			case "changed local":
				req = Request{Action: "subscribe", Mailbox: "INBOX", Local: "Other"}
			case "readonly state":
				p := filepath.Join(t.TempDir(), "file")
				os.WriteFile(p, []byte("file"), 0600)
				cfg.StateDir = filepath.Join(p, "state")
			}
			if _, err := runBackend(cfg, a, req, b); err == nil {
				t.Fatal("accepted invalid operation")
			}
			if *writes != 0 {
				t.Fatal("server mutated before preflight failed")
			}
		})
	}
}
func TestFailedRemoteChangeDoesNotSaveMapping(t *testing.T) {
	cfg, a, b, _ := fakeFolders(t)
	b.subscribe = func(Folder, bool) error { return errors.New("server refused") }
	if _, err := runBackend(cfg, a, Request{Action: "subscribe", Mailbox: "Projects"}, b); err == nil {
		t.Fatal("ignored error")
	}
	if _, err := os.Stat(cfg.FolderStatePath(a)); !os.IsNotExist(err) {
		t.Fatal("saved unconfirmed mapping")
	}
}
func TestPartialLocalFailureCanBeRetried(t *testing.T) {
	cfg, a, b, _ := fakeFolders(t)
	create := b.create
	b.create = func(name string, p *Folder) (Folder, error) {
		f, err := create(name, p)
		os.MkdirAll(filepath.Join(a.LocalRoot, "Folders"), 0700)
		os.WriteFile(filepath.Join(a.LocalRoot, "Folders", name), []byte("blocked"), 0600)
		return f, err
	}
	if _, err := runBackend(cfg, a, Request{Action: "create", Name: "New"}, b); err == nil {
		t.Fatal("ignored local failure")
	}
	if err := cfg.ApplyFolderOverrides(a); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(a.LocalRoot, "Folders/New"))
	if _, err := runBackend(cfg, a, Request{Action: "subscribe", Mailbox: "New"}, b); err != nil {
		t.Fatal("retry failed", err)
	}
}

func TestAutomaticLocalNamesRemainPortable(t *testing.T){
	for _,name:=range []string{"Travel/Café", "Trips/CON", "Notes/COM1.txt", "Old/new", "Trailing ", "A:B/../100%", "cur"}{
		local:="Folders/"+safeLocal(name)
		if err:=config.ValidateFolderLocal(local);err!=nil{t.Fatal(name,local,err)}
	}
}
