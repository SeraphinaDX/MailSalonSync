package syncer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/imapclient"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/jmap"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/maildir"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/state"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/status"
)

func Sync(ctx context.Context, cfg *config.Config, accountNames []string, r status.Reporter) error {
	selected := map[string]bool{}
	for _, n := range accountNames {
		if n != "" {
			selected[n] = true
		}
	}
	if len(selected) > 0 {
		for n := range selected {
			if _, err := cfg.Account(n); err != nil {
				return err
			}
		}
	}
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		if len(selected) > 0 && !selected[a.Name] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		r.Set(a.Name, "", "connecting")
		var err error
		switch a.Protocol {
		case "imap":
			err = syncIMAP(ctx, cfg, a, r)
		case "jmap":
			err = syncJMAP(ctx, cfg, a, r)
		}
		if err != nil {
			return fmt.Errorf("account %s: %w", a.Name, err)
		}
	}
	return nil
}

func syncIMAP(ctx context.Context, cfg *config.Config, a *config.Account, r status.Reporter) error {
	password, err := a.IMAPPassword()
	if err != nil {
		return err
	}
	c, err := imapclient.Dial(a.IMAP.Address, a.IMAP.Security, 30*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	abortDone := make(chan struct{})
	defer close(abortDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = c.Abort()
		case <-abortDone:
		}
	}()
	if err := c.Login(a.IMAP.Username, password); err != nil {
		return err
	}
	s, path, err := loadState(cfg, a)
	if err != nil {
		return err
	}
	for _, mapping := range a.Mailboxes {
		if err := ctx.Err(); err != nil {
			return err
		}
		r.Set(a.Name, mapping.Remote, "selecting mailbox")
		info, err := c.Select(mapping.Remote)
		if err != nil {
			return err
		}
		if prev, ok := s.IMAP[mapping.Remote]; ok && prev.UIDValidity != 0 && info.UIDValidity != 0 && prev.UIDValidity != info.UIDValidity {
			r.Set(a.Name, mapping.Remote, "UIDVALIDITY changed; rebuilding local mailbox state")
			if err := clearTrackedMailbox(a, s, "imap", mapping); err != nil {
				return err
			}
		}
		s.IMAP[mapping.Remote] = state.IMAPFolder{UIDValidity: info.UIDValidity}
		dir := maildir.Open(localMailboxPath(a, mapping.Local))
		if err := dir.Ensure(); err != nil {
			return err
		}

		// First, detect messages deliberately removed from the Maildir. Before
		// treating a missing file as a deletion, look for the same stable key
		// in another mapped Maildir and propagate that as a server-side move.
		for key, e := range copyEntries(s.Entries) {
			if e.Protocol != "imap" || e.RemoteMailbox != mapping.Remote || e.LocalMailbox != mapping.Local {
				continue
			}
			_, exists, err := dir.Find(e.FileKey)
			if err != nil {
				return err
			}
			if exists || !a.PropagateDeletes {
				continue
			}

			move, err := findTrackedMove(a, mapping, e.FileKey)
			if err != nil {
				return err
			}
			uid64, err := strconv.ParseUint(e.RemoteID, 10, 32)
			if err != nil {
				return fmt.Errorf("bad UID in state: %q", e.RemoteID)
			}
			if move != nil {
				if move.Remote == mapping.Remote {
					e.LocalMailbox = move.Local
					s.Entries[key] = e
					if err := s.Save(path); err != nil {
						return err
					}
					continue
				}
				r.Set(a.Name, mapping.Remote, fmt.Sprintf("moving locally moved message to %s", move.Remote))
				newUID, err := c.MoveUID(uint32(uid64), move.Remote)
				if err != nil {
					return err
				}
				newID := strconv.FormatUint(uint64(newUID), 10)
				newKey := state.Key("imap", move.Remote, newID)
				if existing, ok := s.Entries[newKey]; ok && existing.FileKey != e.FileKey {
					return fmt.Errorf("destination IMAP state collision for mailbox %q UID %s", move.Remote, newID)
				}
				delete(s.Entries, key)
				s.Entries[newKey] = state.Entry{
					Protocol:      "imap",
					RemoteMailbox: move.Remote,
					RemoteID:      newID,
					LocalMailbox:  move.Local,
					FileKey:       e.FileKey,
				}
				if err := s.Save(path); err != nil {
					return err
				}
				continue
			}

			r.Set(a.Name, mapping.Remote, "deleting locally removed message from server")
			if err := c.DeleteUID(uint32(uid64), a.IMAP.AllowExpungeWithoutUIDPlus); err != nil {
				return err
			}
			delete(s.Entries, key)
			r.Deleted(a.Name, mapping.Remote)
		}

		r.Set(a.Name, mapping.Remote, "listing remote messages")
		uids, err := c.UIDSearchAll()
		if err != nil {
			return err
		}
		remote := make(map[string]bool, len(uids))
		for _, uid := range uids {
			remote[strconv.FormatUint(uint64(uid), 10)] = true
		}

		// Messages no longer in the remote mailbox are removed locally.
		for key, e := range copyEntries(s.Entries) {
			if e.Protocol != "imap" || e.RemoteMailbox != mapping.Remote || e.LocalMailbox != mapping.Local {
				continue
			}
			if remote[e.RemoteID] {
				continue
			}
			if err := dir.Remove(e.FileKey); err != nil && !os.IsNotExist(err) {
				return err
			}
			delete(s.Entries, key)
			r.Deleted(a.Name, mapping.Remote)
		}

		for _, uid := range uids {
			if err := ctx.Err(); err != nil {
				return err
			}
			id := strconv.FormatUint(uint64(uid), 10)
			key := state.Key("imap", mapping.Remote, id)
			e, ok := s.Entries[key]
			if ok {
				if _, exists, err := dir.Find(e.FileKey); err != nil {
					return err
				} else if exists {
					continue
				}
			}
			r.Set(a.Name, mapping.Remote, fmt.Sprintf("downloading UID %d", uid))
			msg, err := c.UIDFetch(uid)
			if err != nil {
				return err
			}
			fileKey := maildir.StableKey(a.Name + "\n" + key)
			if _, err := dir.Put(fileKey, msg.Raw, msg.Seen); err != nil {
				return err
			}
			s.Entries[key] = state.Entry{
				Protocol:      "imap",
				RemoteMailbox: mapping.Remote,
				RemoteID:      id,
				LocalMailbox:  mapping.Local,
				FileKey:       fileKey,
			}
			r.Added(a.Name, mapping.Remote)
		}
		if err := s.Save(path); err != nil {
			return err
		}
		r.Set(a.Name, mapping.Remote, fmt.Sprintf("up to date (%d messages)", len(uids)))
	}
	return nil
}

func syncJMAP(ctx context.Context, cfg *config.Config, a *config.Account, r status.Reporter) error {
	secret, err := a.JMAPSecret()
	if err != nil {
		return err
	}
	c, err := jmap.New(ctx, a.JMAP.SessionURL, jmap.Auth{Mode: a.JMAP.Auth, Username: a.JMAP.Username, Secret: secret}, a.JMAP.AccountID)
	if err != nil {
		return err
	}
	boxes, err := c.ListMailboxes(ctx)
	if err != nil {
		return err
	}
	s, path, err := loadState(cfg, a)
	if err != nil {
		return err
	}
	for _, mapping := range a.Mailboxes {
		if err := ctx.Err(); err != nil {
			return err
		}
		box, err := jmap.ResolveMailbox(mapping.Remote, boxes)
		if err != nil {
			return err
		}
		r.Set(a.Name, box.FullName, "checking local deletions and moves")
		dir := maildir.Open(localMailboxPath(a, mapping.Local))
		if err := dir.Ensure(); err != nil {
			return err
		}
		for key, e := range copyEntries(s.Entries) {
			if e.Protocol != "jmap" || e.RemoteMailbox != mapping.Remote || e.LocalMailbox != mapping.Local {
				continue
			}
			_, exists, err := dir.Find(e.FileKey)
			if err != nil {
				return err
			}
			if exists || !a.PropagateDeletes {
				continue
			}

			move, err := findTrackedMove(a, mapping, e.FileKey)
			if err != nil {
				return err
			}
			if move != nil {
				if move.Remote == mapping.Remote {
					e.LocalMailbox = move.Local
					s.Entries[key] = e
					if err := s.Save(path); err != nil {
						return err
					}
					continue
				}
				destBox, err := jmap.ResolveMailbox(move.Remote, boxes)
				if err != nil {
					return err
				}
				r.Set(a.Name, box.FullName, fmt.Sprintf("moving locally moved message to %s", destBox.FullName))
				if err := c.MoveBetweenMailboxes(ctx, e.RemoteID, box.ID, destBox.ID); err != nil {
					return err
				}
				newKey := state.Key("jmap", move.Remote, e.RemoteID)
				if existing, ok := s.Entries[newKey]; ok && newKey != key && existing.FileKey != e.FileKey {
					return fmt.Errorf("destination JMAP state collision for mailbox %q email %s", move.Remote, e.RemoteID)
				}
				if newKey != key {
					delete(s.Entries, key)
				}
				s.Entries[newKey] = state.Entry{
					Protocol:      "jmap",
					RemoteMailbox: move.Remote,
					RemoteID:      e.RemoteID,
					LocalMailbox:  move.Local,
					FileKey:       e.FileKey,
				}
				if err := s.Save(path); err != nil {
					return err
				}
				continue
			}

			r.Set(a.Name, box.FullName, "removing locally deleted message from remote mailbox")
			if err := c.RemoveFromMailbox(ctx, e.RemoteID, box.ID); err != nil {
				return err
			}
			delete(s.Entries, key)
			r.Deleted(a.Name, box.FullName)
		}

		r.Set(a.Name, box.FullName, "listing remote messages")
		ids, err := c.QueryEmailIDs(ctx, box.ID)
		if err != nil {
			return err
		}
		remote := make(map[string]bool, len(ids))
		for _, id := range ids {
			remote[id] = true
		}
		for key, e := range copyEntries(s.Entries) {
			if e.Protocol != "jmap" || e.RemoteMailbox != mapping.Remote || e.LocalMailbox != mapping.Local {
				continue
			}
			if remote[e.RemoteID] {
				continue
			}
			if err := dir.Remove(e.FileKey); err != nil && !os.IsNotExist(err) {
				return err
			}
			delete(s.Entries, key)
			r.Deleted(a.Name, box.FullName)
		}

		var missing []string
		for _, id := range ids {
			key := state.Key("jmap", mapping.Remote, id)
			e, ok := s.Entries[key]
			if !ok {
				missing = append(missing, id)
				continue
			}
			if _, exists, err := dir.Find(e.FileKey); err != nil {
				return err
			} else if !exists {
				missing = append(missing, id)
			}
		}
		metadata, err := c.GetEmails(ctx, missing)
		if err != nil {
			return err
		}
		for _, id := range missing {
			if err := ctx.Err(); err != nil {
				return err
			}
			e, ok := metadata[id]
			if !ok {
				continue // It disappeared between query and get; next sync will settle state.
			}
			r.Set(a.Name, box.FullName, "downloading message "+id)
			raw, err := c.DownloadEmail(ctx, e.BlobID)
			if err != nil {
				return err
			}
			key := state.Key("jmap", mapping.Remote, id)
			fileKey := maildir.StableKey(a.Name + "\n" + key)
			if _, err := dir.Put(fileKey, raw, e.Keywords["$seen"]); err != nil {
				return err
			}
			s.Entries[key] = state.Entry{
				Protocol:      "jmap",
				RemoteMailbox: mapping.Remote,
				RemoteID:      id,
				LocalMailbox:  mapping.Local,
				FileKey:       fileKey,
			}
			r.Added(a.Name, box.FullName)
		}
		if err := s.Save(path); err != nil {
			return err
		}
		r.Set(a.Name, box.FullName, fmt.Sprintf("up to date (%d messages)", len(ids)))
	}
	return nil
}

func loadState(cfg *config.Config, a *config.Account) (*state.State, string, error) {
	name := sanitizeFileName(a.Name) + ".json"
	path := filepath.Join(cfg.StateDir, name)
	s, err := state.Load(path)
	return s, path, err
}

func localMailboxPath(a *config.Account, local string) string {
	if filepath.IsAbs(local) {
		return local
	}
	if local == "." {
		return a.LocalRoot
	}
	return filepath.Join(a.LocalRoot, local)
}

func clearTrackedMailbox(a *config.Account, s *state.State, protocol string, mapping config.Mailbox) error {
	dir := maildir.Open(localMailboxPath(a, mapping.Local))
	for key, e := range copyEntries(s.Entries) {
		if e.Protocol == protocol && e.RemoteMailbox == mapping.Remote && e.LocalMailbox == mapping.Local {
			if err := dir.Remove(e.FileKey); err != nil && !os.IsNotExist(err) {
				return err
			}
			delete(s.Entries, key)
		}
	}
	return nil
}

func copyEntries(in map[string]state.Entry) map[string]state.Entry {
	out := make(map[string]state.Entry, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sanitizeFileName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "account"
	}
	return b.String()
}

func SendJMAP(ctx context.Context, a *config.Account, raw []byte, r status.Reporter) (string, error) {
	if a.Protocol != "jmap" || a.JMAP == nil {
		return "", fmt.Errorf("account %q is not a JMAP account", a.Name)
	}
	secret, err := a.JMAPSecret()
	if err != nil {
		return "", err
	}
	r.Set(a.Name, "", "connecting to JMAP")
	c, err := jmap.New(ctx, a.JMAP.SessionURL, jmap.Auth{Mode: a.JMAP.Auth, Username: a.JMAP.Username, Secret: secret}, a.JMAP.AccountID)
	if err != nil {
		return "", err
	}
	r.Set(a.Name, "", "uploading and submitting message")
	id, err := c.SendRaw(ctx, raw, a.JMAP.IdentityID, a.JMAP.DraftsMailbox, a.JMAP.SentMailbox)
	if err != nil {
		return id, err
	}
	r.Set(a.Name, "", "message submitted")
	return id, nil
}
