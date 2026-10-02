// Package folders manages remote mailboxes and their local sync mappings.
package folders

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/imapclient"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/jmap"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/maildir"
)

type Request struct {
	Action            string `json:"action"`
	Mailbox           string `json:"mailbox,omitempty"`
	Name              string `json:"name,omitempty"`
	Parent            string `json:"parent,omitempty"`
	Local             string `json:"local,omitempty"`
	ExpectedLocalRoot string `json:"expected_local_root,omitempty"`
}
type Folder struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Subscribed     bool   `json:"subscribed"`
	Syncing        bool   `json:"syncing"`
	Local          string `json:"local,omitempty"`
	Selectable     bool   `json:"selectable"`
	CanCreateChild bool   `json:"can_create_child"`
	delimiter      string
}
type Response struct {
	Version   int      `json:"version"`
	Account   string   `json:"account"`
	LocalRoot string   `json:"local_root"`
	Folders   []Folder `json:"folders"`
}
type backend struct {
	list      func() ([]Folder, error)
	create    func(string, *Folder) (Folder, error)
	subscribe func(Folder, bool) error
	matches   func(string, Folder) bool
	close     func()
}

func open(ctx context.Context, a *config.Account) (*backend, error) {
	if a.Protocol == "imap" {
		secret, err := a.IMAPPassword()
		if err != nil {
			return nil, err
		}
		c, err := imapclient.Dial(a.IMAP.Address, a.IMAP.Security, 30*time.Second)
		if err != nil {
			return nil, err
		}
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				c.Abort()
			case <-done:
			}
		}()
		closeClient := func() { close(done); c.Close() }
		if err = c.Login(a.IMAP.Username, secret); err != nil {
			closeClient()
			return nil, err
		}
		b := &backend{close: closeClient}
		b.list = func() ([]Folder, error) {
			rows, err := c.ListMailboxes()
			if err != nil {
				return nil, err
			}
			out := []Folder{}
			for _, m := range rows {
				name := m.Name
				if m.Delimiter != "" {
					name = strings.ReplaceAll(name, m.Delimiter, "/")
				}
				out = append(out, Folder{ID: m.Name, Name: name, Subscribed: m.Subscribed, Selectable: m.Selectable, CanCreateChild: m.CanCreateChild, delimiter: m.Delimiter})
			}
			return out, nil
		}
		b.matches = func(remote string, f Folder) bool {
			return remote == f.ID || (strings.EqualFold(remote, "INBOX") && strings.EqualFold(f.ID, "INBOX"))
		}
		b.subscribe = func(f Folder, on bool) error { return c.SetSubscribed(f.ID, on) }
		b.create = func(name string, parent *Folder) (Folder, error) {
			delimiter := ""
			full := name
			if parent != nil {
				delimiter = parent.delimiter
				full = parent.ID + delimiter + name
			} else {
				rows, err := c.ListMailboxes()
				if err != nil {
					return Folder{}, err
				}
				for _, f := range rows {
					if f.Delimiter != "" {
						delimiter = f.Delimiter
						break
					}
				}
			}
			if delimiter != "" && strings.Contains(name, delimiter) {
				return Folder{}, fmt.Errorf("folder name contains the server's hierarchy separator %q", delimiter)
			}
			if err := c.CreateMailbox(full); err != nil {
				return Folder{}, err
			}
			f := Folder{ID: full, Name: full, Selectable: true, CanCreateChild: delimiter != "", delimiter: delimiter}
			if delimiter != "" {
				f.Name = strings.ReplaceAll(full, delimiter, "/")
			}
			if err := c.SetSubscribed(full, true); err != nil {
				return f, fmt.Errorf("folder %q was created, but subscription failed: %w; use subscribe to finish", full, err)
			}
			f.Subscribed = true
			return f, nil
		}
		return b, nil
	}
	secret, err := a.JMAPSecret()
	if err != nil {
		return nil, err
	}
	c, err := jmap.New(ctx, a.JMAP.SessionURL, jmap.Auth{Mode: a.JMAP.Auth, Username: a.JMAP.Username, Secret: secret}, a.JMAP.AccountID)
	if err != nil {
		return nil, err
	}
	var boxes []jmap.Mailbox
	b := &backend{close: func() {}}
	b.list = func() ([]Folder, error) {
		var err error
		boxes, err = c.ListMailboxes(ctx)
		if err != nil {
			return nil, err
		}
		out := []Folder{}
		for _, m := range boxes {
			out = append(out, Folder{ID: "id:" + m.ID, Name: m.FullName, Subscribed: m.IsSubscribed, Selectable: true, CanCreateChild: m.MyRights.MayCreateChild})
		}
		return out, nil
	}
	b.matches = func(remote string, f Folder) bool {
		m, err := jmap.ResolveMailbox(remote, boxes)
		return err == nil && "id:"+m.ID == f.ID
	}
	b.subscribe = func(f Folder, on bool) error { return c.SetSubscribed(ctx, strings.TrimPrefix(f.ID, "id:"), on) }
	b.create = func(name string, parent *Folder) (Folder, error) {
		var id *string
		full := name
		if parent != nil {
			s := strings.TrimPrefix(parent.ID, "id:")
			id = &s
			full = parent.Name + "/" + name
		}
		created, err := c.CreateMailbox(ctx, name, id)
		return Folder{ID: "id:" + created, Name: full, Subscribed: true, Selectable: true}, err
	}
	return b, nil
}

// Run is called while the CLI holds the mail-operation lock. Server operations
// happen only after checking account identity, local path collisions and that
// the mapping file can be written. Partial success is reported explicitly.
func Run(ctx context.Context, cfg *config.Config, account string, req Request) (Response, error) {
	a, err := cfg.Account(account)
	if err != nil {
		return Response{}, err
	}
	if req.Action != "list" && req.Action != "create" && req.Action != "subscribe" && req.Action != "unsubscribe" {
		return Response{}, fmt.Errorf("unknown folder action %q", req.Action)
	}
	if req.ExpectedLocalRoot != "" && !sameRoot(a.LocalRoot, req.ExpectedLocalRoot) {
		return Response{}, fmt.Errorf("sync account %q uses %s, not the GUI Maildir %s", a.Name, a.LocalRoot, req.ExpectedLocalRoot)
	}
	b, err := open(ctx, a)
	if err != nil {
		return Response{}, err
	}
	defer b.close()
	return runBackend(cfg, a, req, b)
}
func sameRoot(a, b string) bool {
	normalize := func(s string) string {
		p, err := filepath.Abs(config.ExpandPath(s))
		if err != nil {
			return ""
		}
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		return filepath.Clean(p)
	}
	return normalize(a) == normalize(b)
}
func runBackend(cfg *config.Config, a *config.Account, req Request, b *backend) (Response, error) {
	rows, err := b.list()
	if err != nil {
		return Response{}, err
	}
	records, err := cfg.FolderOverrides(a)
	if err != nil {
		return Response{}, err
	}
	mapping := func(f Folder) (config.Mailbox, bool) {
		for _, m := range a.Mailboxes {
			if b.matches(m.Remote, f) {
				return m, true
			}
		}
		for _, m := range records {
			if b.matches(m.Remote, f) {
				return config.Mailbox{Remote: m.Remote, Local: m.Local}, true
			}
		}
		return config.Mailbox{}, false
	}
	response := func(rows []Folder) Response {
		for i := range rows {
			if m, ok := mapping(rows[i]); ok {
				rows[i].Local = m.Local
				for _, active := range a.Mailboxes {
					if active.Remote == m.Remote {
						rows[i].Syncing = true
					}
				}
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		return Response{Version: 1, Account: a.Name, LocalRoot: a.LocalRoot, Folders: rows}
	}
	if req.Action == "list" {
		return response(rows), nil
	}
	var target Folder
	var parent *Folder
	if req.Action == "create" {
		if strings.TrimSpace(req.Name) == "" || req.Name != strings.TrimSpace(req.Name) || len(req.Name) > 255 || strings.ContainsAny(req.Name, "/\\\x00\r\n") {
			return Response{}, fmt.Errorf("enter a single folder name; choose the parent separately")
		}
		for _, r := range req.Name {
			if unicode.IsControl(r) {
				return Response{}, fmt.Errorf("folder name contains a control character")
			}
		}
		if req.Parent != "" {
			for i := range rows {
				if rows[i].ID == req.Parent {
					parent = &rows[i]
				}
			}
			if parent == nil || !parent.CanCreateChild {
				return Response{}, fmt.Errorf("parent folder cannot contain new folders")
			}
		}
		full := req.Name
		if parent != nil {
			full = parent.Name + "/" + req.Name
		}
		for _, f := range rows {
			if f.Name == full {
				return Response{}, fmt.Errorf("folder %q already exists; subscribe to it instead", full)
			}
		}
		target = Folder{Name: full}
	} else {
		found := false
		for _, f := range rows {
			if f.ID == req.Mailbox {
				target = f
				found = true
				break
			}
		}
		if !found {
			return Response{}, fmt.Errorf("remote folder no longer exists; refresh the folder list")
		}
		if !target.Selectable && req.Action == "subscribe" {
			return Response{}, fmt.Errorf("folder cannot contain mail")
		}
	}
	m, existing := mapping(target)
	if req.Action == "create" {
		existing = false
	}
	if !existing {
		m = config.Mailbox{Remote: target.ID, Local: req.Local}
		if m.Local == "" {
			m.Local = "Folders/" + safeLocal(target.Name)
		}
		if err := config.ValidateFolderLocal(m.Local); err != nil {
			return Response{}, err
		}
	}
	if existing && req.Local != "" && req.Local != m.Local {
		return Response{}, fmt.Errorf("folder already has local mapping %s; changing it would orphan tracked mail", m.Local)
	}
	if req.Action == "unsubscribe" && !existing { // Record a disabled mapping for later resubscription.
		m.Local = "Folders/" + safeLocal(target.Name)
	}
	path := localPath(a, m.Local)
	// Include disabled mappings in collision checks; their retained Maildirs must
	// not silently become storage for a different server mailbox.
	all := append([]config.Mailbox{}, a.Mailboxes...)
	for _, r := range records {
		all = append(all, config.Mailbox{Remote: r.Remote, Local: r.Local})
	}
	for _, other := range all {
		if other.Remote != m.Remote && filepath.Clean(localPath(a, other.Local)) == filepath.Clean(path) {
			return Response{}, fmt.Errorf("local folder %s is already mapped to %s", m.Local, other.Remote)
		}
	}
	if !existing && req.Action != "unsubscribe" {
		if _, err := os.Lstat(path); err == nil {
			return Response{}, fmt.Errorf("local path %s already exists; choose a different local folder", path)
		} else if !os.IsNotExist(err) {
			return Response{}, err
		}
		if err := safeParents(a.LocalRoot, path); err != nil {
			return Response{}, err
		}
	}
	if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
		return Response{}, err
	}
	probe, err := os.CreateTemp(cfg.StateDir, ".folder-check-*")
	if err != nil {
		return Response{}, err
	}
	probe.Close()
	os.Remove(probe.Name())
	if req.Action == "create" {
		target, err = b.create(req.Name, parent)
		m.Remote = target.ID
	} else {
		err = b.subscribe(target, req.Action == "subscribe")
	}
	if err != nil {
		return Response{}, err
	}
	if err = cfg.SaveFolderOverride(a, config.FolderOverride{Remote: m.Remote, Local: m.Local, Enabled: req.Action != "unsubscribe"}); err != nil {
		return Response{}, fmt.Errorf("remote folder was updated, but saving its sync mapping failed: %w", err)
	}
	if req.Action != "unsubscribe" {
		if err = maildir.Open(path).Ensure(); err != nil {
			return Response{}, fmt.Errorf("remote folder and mapping were updated, but local Maildir failed: %w; subscribe again to finish", err)
		}
	}
	if err = cfg.ApplyFolderOverrides(a); err != nil {
		return Response{}, err
	}
	records, err = cfg.FolderOverrides(a)
	if err != nil {
		return Response{}, err
	}
	rows, err = b.list()
	if err != nil {
		return Response{}, fmt.Errorf("folder was updated; refresh failed: %w", err)
	}
	return response(rows), nil
}
func localPath(a *config.Account, local string) string {
	if filepath.IsAbs(local) {
		return local
	}
	return filepath.Join(a.LocalRoot, local)
}
func safeParents(root, path string) error { // Reject symlink parents for new paths.
	for p := filepath.Dir(path); p != filepath.Clean(root); p = filepath.Dir(p) {
		if p == filepath.Dir(p) {
			return fmt.Errorf("local folder escapes Maildir root")
		}
		if st, err := os.Lstat(p); err == nil {
			if st.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("local folder parent is a symlink: %s", p)
			}
			if !st.IsDir() {
				return fmt.Errorf("local folder parent is not a directory")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
func safeLocal(name string) string {
	parts := strings.Split(name, "/")
	for i, s := range parts {
		var b strings.Builder
		for _, r := range s {
			if unicode.IsControl(r) || strings.ContainsRune(".\\:<>\"|?*%", r) {
				for _, v := range []byte(string(r)) {
					fmt.Fprintf(&b, "%%%02X", v)
				}
			} else {
				b.WriteRune(r)
			}
		}
		parts[i] = b.String()
		trimmed := strings.TrimRight(parts[i], " ")
		parts[i] = trimmed + strings.Repeat("%20", len(parts[i])-len(trimmed))
		if parts[i] == "" {
			parts[i] = "%00"
		}
		if s == "cur" || s == "new" || s == "tmp" {
			parts[i] = "%" + fmt.Sprintf("%02X", s[0]) + s[1:]
		}
		if config.ValidateFolderLocal(parts[i]) != nil && len(parts[i]) > 0 {
			parts[i] = fmt.Sprintf("%%%02X", parts[i][0]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "/")
}
