// Package pim synchronizes independent address books and calendars. Remote
// objects retain their native representation, including properties unknown to
// MailSalon. A three-way comparison prevents silent last-writer-wins data loss.
package pim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
)

type Object struct {
	ID, Revision string
	Data         []byte
}

// Snapshot must be complete. Backends must fail on partial listings, missing
// objects, malformed data, or a changing JMAP state rather than infer deletions.
type Backend interface {
	Snapshot(context.Context) ([]Object, error)
	Put(context.Context, string, string, []byte) (Object, error)
	Delete(context.Context, Object) error
}

type entry struct{ File, Hash, UID string }
type diskState struct {
	Version  int
	Identity string
	Entries  map[string]entry
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func hash(b []byte, ext string) string {
	if ext == ".json" {
		var v any
		if json.Unmarshal(b, &v) == nil {
			b, _ = json.Marshal(v)
		}
	}
	return digest(b)
}
func Extension(protocol string) string {
	switch protocol {
	case "carddav":
		return ".vcf"
	case "caldav":
		return ".ics"
	default:
		return ".json"
	}
}

// UID validates the outer container and identity without rewriting its content.
// Recurrence exceptions may repeat the calendar UID in one resource.
func UID(data []byte, ext string) (string, error) {
	if ext == ".json" {
		var v map[string]any
		if err := json.Unmarshal(data, &v); err != nil {
			return "", err
		}
		uid, _ := v["uid"].(string)
		if uid == "" {
			return "", errors.New("JSON object needs a uid")
		}
		return uid, nil
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\n ", ""), "\n\t", "")
	container := "VCARD"
	if ext == ".ics" {
		container = "VCALENDAR"
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) < 3 || strings.ToUpper(lines[0]) != "BEGIN:"+container || strings.ToUpper(lines[len(lines)-1]) != "END:"+container {
		return "", fmt.Errorf("expected one %s container", container)
	}
	stack := []string{}
	uid := ""
	outerCount := 0
	for _, line := range lines {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return "", errors.New("malformed content line")
		}
		key = strings.ToUpper(strings.Split(key, ";")[0])
		if p := strings.LastIndex(key, "."); p >= 0 {
			key = key[p+1:]
		}
		switch key {
		case "BEGIN":
			if len(stack) == 0 {
				outerCount++
				if outerCount != 1 || strings.ToUpper(val) != container {
					return "", errors.New("expected a single outer container")
				}
			}
			stack = append(stack, strings.ToUpper(val))
		case "END":
			if len(stack) == 0 || stack[len(stack)-1] != strings.ToUpper(val) {
				return "", errors.New("unbalanced content container")
			}
			stack = stack[:len(stack)-1]
		case "UID":
			if len(stack) == 0 {
				return "", errors.New("UID outside container")
			}
			top := stack[len(stack)-1]
			if top != "VCARD" && top != "VEVENT" && top != "VTODO" {
				continue
			}
			if uid != "" && uid != val {
				return "", errors.New("resource contains multiple UIDs; split it into individual files")
			}
			uid = val
		}
	}
	if len(stack) != 0 || uid == "" {
		return "", errors.New("resource needs a UID and balanced containers")
	}
	return uid, nil
}

// atomicWrite uses a unique temporary file in the same directory and restrictive
// permissions. Files are never exposed in a half-written state.
func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".mss-tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func SyncCollection(ctx context.Context, c config.Collection, b Backend) error {
	if err := os.MkdirAll(c.LocalDir, 0700); err != nil {
		return err
	}
	lock := filepath.Join(c.LocalDir, ".mss-lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("collection is locked (%s): %w", lock, err)
	}
	f.Close()
	defer os.Remove(lock)
	ext := Extension(c.Protocol)
	identity := c.Protocol + "\n" + c.Remote
	if bound, ok := b.(interface{ Identity() string }); ok {
		identity += "\n" + bound.Identity()
	}
	if c.JMAP != nil {
		identity += "\n" + c.JMAP.SessionURL + "\n" + c.JMAP.AccountID + "\n" + c.JMAP.Username
	}
	if c.DAV != nil {
		identity += "\n" + c.DAV.Username
	}
	statePath := filepath.Join(c.LocalDir, ".mss-state.json")
	s := diskState{Version: 1, Identity: identity, Entries: map[string]entry{}}
	if data, err := os.ReadFile(statePath); err == nil {
		if err = json.Unmarshal(data, &s); err != nil {
			return err
		}
		if s.Version != 1 || s.Identity != identity || s.Entries == nil {
			return errors.New("collection state does not match configuration; refusing to reuse it")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	save := func() error {
		data, err := json.MarshalIndent(s, "", "  ")
		if err != nil {
			return err
		}
		return atomicWrite(statePath, data)
	}

	local := map[string][]byte{}
	localUID := map[string]string{}
	files, err := os.ReadDir(c.LocalDir)
	if err != nil {
		return err
	}
	for _, f := range files {
		if strings.HasPrefix(f.Name(), ".") || filepath.Ext(f.Name()) != ext {
			continue
		}
		if !f.Type().IsRegular() {
			return fmt.Errorf("local item %s is not a regular file", f.Name())
		}
		data, err := os.ReadFile(filepath.Join(c.LocalDir, f.Name()))
		if err != nil {
			return err
		}
		uid, err := UID(data, ext)
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name(), err)
		}
		if other := localUID[uid]; other != "" {
			return fmt.Errorf("duplicate local UID in %s and %s", other, f.Name())
		}
		local[f.Name()] = data
		localUID[uid] = f.Name()
	}
	objects, err := b.Snapshot(ctx)
	if err != nil {
		return err
	}
	remote := map[string]Object{}
	remoteUID := map[string]string{}
	for _, o := range objects {
		if o.ID == "" || o.Revision == "" {
			return errors.New("remote snapshot contains an object without ID or revision")
		}
		uid, err := UID(o.Data, ext)
		if err != nil {
			return fmt.Errorf("remote %s: %w", o.ID, err)
		}
		if _, ok := remote[o.ID]; ok {
			return errors.New("duplicate ID in remote listing")
		}
		if _, ok := remoteUID[uid]; ok {
			return errors.New("duplicate UID in remote listing")
		}
		remote[o.ID] = o
		remoteUID[uid] = o.ID
	}
	// Preflight the entire collection before performing any mutation. The remote
	// copy is saved separately so the user can resolve divergent edits explicitly.
	conflict := func(file string, o Object) error {
		if len(o.Data) > 0 {
			dir := filepath.Join(c.LocalDir, ".mss-conflicts")
			if err := os.MkdirAll(dir, 0700); err != nil {
				return err
			}
			if err := atomicWrite(filepath.Join(dir, digest([]byte(o.ID))+ext), o.Data); err != nil {
				return err
			}
		}
		return fmt.Errorf("conflict in %s: both sides changed (or edit versus deletion); local item preserved; remote copy in .mss-conflicts", file)
	}
	used := map[string]bool{}
	for id, e := range s.Entries {
		if filepath.Base(e.File) != e.File || filepath.Ext(e.File) != ext || strings.HasPrefix(e.File, ".") || used[e.File] {
			return errors.New("invalid filename in collection state")
		}
		used[e.File] = true
		data, exists := local[e.File]
		o, present := remote[id]
		if exists {
			uid, _ := UID(data, ext)
			if uid != e.UID {
				return fmt.Errorf("%s: changing a tracked UID is not supported", e.File)
			}
		}
		if present {
			uid, _ := UID(o.Data, ext)
			if uid != e.UID {
				return conflict(e.File, o)
			}
		}
		lc := !exists || hash(data, ext) != e.Hash
		rc := !present || hash(o.Data, ext) != e.Hash
		if lc && rc && (exists != present || (exists && hash(data, ext) != hash(o.Data, ext))) {
			return conflict(e.File, o)
		}
	}
	for _, o := range objects {
		if _, ok := s.Entries[o.ID]; ok {
			continue
		}
		uid, _ := UID(o.Data, ext)
		if file := localUID[uid]; file != "" && (used[file] || hash(local[file], ext) != hash(o.Data, ext)) {
			return conflict(file, o)
		}
		if localUID[uid] == "" {
			file := digest([]byte(o.ID)) + ext
			if _, exists := local[file]; exists || used[file] {
				return conflict(file, o)
			}
		}
	}
	// Verify a file still has the value read during preflight before replacing or
	// deleting it. The shared lock also protects MailSalon's own edits.
	verify := func(file string) error {
		data, err := os.ReadFile(filepath.Join(c.LocalDir, file))
		want, exists := local[file]
		if !exists && os.IsNotExist(err) {
			return nil
		}
		if err != nil || !exists || hash(data, ext) != hash(want, ext) {
			return fmt.Errorf("%s changed during sync; rerun", file)
		}
		return nil
	}
	store := func(id, file string, o Object) error {
		uid, err := UID(o.Data, ext)
		if err != nil || o.ID != id || o.Revision == "" {
			return fmt.Errorf("invalid remote object after write: %s", id)
		}
		if current, exists := local[file]; exists {
			previousUID, _ := UID(current, ext)
			if previousUID != uid {
				return errors.New("remote changed the item's UID; local copy preserved")
			}
		}
		if err := verify(file); err != nil {
			return err
		}
		if err := atomicWrite(filepath.Join(c.LocalDir, file), o.Data); err != nil {
			return err
		}
		s.Entries[id] = entry{File: file, Hash: hash(o.Data, ext), UID: uid}
		local[file] = o.Data
		return save()
	}
	ids := make([]string, 0, len(s.Entries))
	for id := range s.Entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		e := s.Entries[id]
		data, exists := local[e.File]
		o, present := remote[id]
		switch {
		case !present:
			if exists {
				if err := verify(e.File); err != nil {
					return err
				}
				dir := filepath.Join(c.LocalDir, ".mss-backup")
				if err := os.MkdirAll(dir, 0700); err != nil {
					return err
				}
				if err := atomicWrite(filepath.Join(dir, e.File), data); err != nil {
					return err
				}
				if err := os.Remove(filepath.Join(c.LocalDir, e.File)); err != nil {
					return err
				}
				delete(local, e.File)
			}
			delete(s.Entries, id)
			if err := save(); err != nil {
				return err
			}
		case !exists && c.PropagateDeletes:
			if err := verify(e.File); err != nil {
				return err
			}
			if err := b.Delete(ctx, o); err != nil {
				return err
			}
			delete(s.Entries, id)
			if err := save(); err != nil {
				return err
			}
		case !exists || hash(data, ext) == e.Hash || hash(data, ext) == hash(o.Data, ext):
			if !exists || hash(data, ext) != hash(o.Data, ext) {
				if err := store(id, e.File, o); err != nil {
					return err
				}
			} else {
				e.Hash = hash(o.Data, ext)
				s.Entries[id] = e
				if err := save(); err != nil {
					return err
				}
			}
		default:
			if err := verify(e.File); err != nil {
				return err
			}
			updated, err := b.Put(ctx, id, o.Revision, data)
			if err != nil {
				return err
			}
			if err := store(id, e.File, updated); err != nil {
				return err
			}
		}
	}
	for _, o := range objects {
		if _, ok := s.Entries[o.ID]; ok {
			continue
		}
		// Do not redownload an object just deleted above.
		if wasTrackedID(ids, o.ID) {
			continue
		}
		uid, _ := UID(o.Data, ext)
		file := localUID[uid]
		if file == "" {
			file = digest([]byte(o.ID)) + ext
		}
		if err := store(o.ID, file, o); err != nil {
			return err
		}
		used[file] = true
	}
	// Upload only files that are not tracked/adopted. UID-derived DAV names and
	// UID adoption on the next run recover a create if checkpointing was interrupted.
	names := make([]string, 0, len(local))
	for file := range local {
		names = append(names, file)
	}
	sort.Strings(names)
	for _, file := range names {
		tracked := false
		for _, e := range s.Entries {
			if e.File == file {
				tracked = true
				break
			}
		}
		if tracked {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := verify(file); err != nil {
			return err
		}
		o, err := b.Put(ctx, "", "", local[file])
		if err != nil {
			return err
		}
		if err := store(o.ID, file, o); err != nil {
			return err
		}
	}
	return save()
}

func wasTrackedID(ids []string, id string) bool {
	i := sort.SearchStrings(ids, id)
	return i < len(ids) && ids[i] == id
}
