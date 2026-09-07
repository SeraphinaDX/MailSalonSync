package syncer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/mail"
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

type AdoptOptions struct {
	DryRun bool
}

type remoteAdoptMessage struct {
	ID  string
	Raw []byte
}

type localAdoptMessage struct {
	Path string
	Raw  []byte
}

type AdoptSummary struct {
	Adopted int
	Skipped int
}

// AdoptExisting is a one-shot migration for mail that predates MailSalonSync's
// stable filename markers. It never uploads local messages and never changes
// remote mailbox membership. Existing state is preserved; untracked local mail
// is adopted only when it uniquely matches a message already present in the
// corresponding remote mailbox.
func AdoptExisting(ctx context.Context, cfg *config.Config, accountNames []string, opts AdoptOptions, r status.Reporter) (AdoptSummary, error) {
	selected := map[string]bool{}
	for _, n := range accountNames {
		if n != "" {
			selected[n] = true
		}
	}
	if len(selected) > 0 {
		for n := range selected {
			if _, err := cfg.Account(n); err != nil {
				return AdoptSummary{}, err
			}
		}
	}

	var total AdoptSummary
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		if len(selected) > 0 && !selected[a.Name] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
		r.Set(a.Name, "", "adopting existing local mail")
		var summary AdoptSummary
		var err error
		switch a.Protocol {
		case "imap":
			summary, err = adoptIMAP(ctx, cfg, a, opts, r)
		case "jmap":
			summary, err = adoptJMAP(ctx, cfg, a, opts, r)
		}
		total.Adopted += summary.Adopted
		total.Skipped += summary.Skipped
		if err != nil {
			return total, fmt.Errorf("account %s: %w", a.Name, err)
		}
	}
	return total, nil
}

func adoptIMAP(ctx context.Context, cfg *config.Config, a *config.Account, opts AdoptOptions, r status.Reporter) (AdoptSummary, error) {
	password, err := a.IMAPPassword()
	if err != nil {
		return AdoptSummary{}, err
	}
	c, err := imapclient.Dial(a.IMAP.Address, a.IMAP.Security, 30*time.Second)
	if err != nil {
		return AdoptSummary{}, err
	}
	defer c.Close()
	if err := c.Login(a.IMAP.Username, password); err != nil {
		return AdoptSummary{}, err
	}

	s, statePath, err := loadState(cfg, a)
	if err != nil {
		return AdoptSummary{}, err
	}
	var out AdoptSummary
	for _, mapping := range a.Mailboxes {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		r.Set(a.Name, mapping.Remote, "scanning existing local mail")
		info, err := c.Select(mapping.Remote)
		if err != nil {
			return out, err
		}
		if !opts.DryRun {
			s.IMAP[mapping.Remote] = state.IMAPFolder{UIDValidity: info.UIDValidity}
		}
		locals, err := untaggedLocalMessages(localMailboxPath(a, mapping.Local))
		if err != nil {
			return out, err
		}
		if len(locals) == 0 {
			continue
		}
		uids, err := c.UIDSearchAll()
		if err != nil {
			return out, err
		}
		remote := make([]remoteAdoptMessage, 0, len(uids))
		for _, uid := range uids {
			id := strconv.FormatUint(uint64(uid), 10)
			key := state.Key("imap", mapping.Remote, id)
			if _, exists := s.Entries[key]; exists {
				continue
			}
			msg, err := c.UIDFetch(uid)
			if err != nil {
				return out, err
			}
			remote = append(remote, remoteAdoptMessage{ID: id, Raw: msg.Raw})
		}
		adopted, skipped, err := adoptMatches(a, mapping, "imap", locals, remote, s, statePath, opts.DryRun, r)
		out.Adopted += adopted
		out.Skipped += skipped
		if err != nil {
			return out, err
		}
	}
	if !opts.DryRun {
		if err := s.Save(statePath); err != nil {
			return out, err
		}
	}
	return out, nil
}

func adoptJMAP(ctx context.Context, cfg *config.Config, a *config.Account, opts AdoptOptions, r status.Reporter) (AdoptSummary, error) {
	secret, err := a.JMAPSecret()
	if err != nil {
		return AdoptSummary{}, err
	}
	c, err := jmap.New(ctx, a.JMAP.SessionURL, jmap.Auth{Mode: a.JMAP.Auth, Username: a.JMAP.Username, Secret: secret}, a.JMAP.AccountID)
	if err != nil {
		return AdoptSummary{}, err
	}
	boxes, err := c.ListMailboxes(ctx)
	if err != nil {
		return AdoptSummary{}, err
	}
	s, statePath, err := loadState(cfg, a)
	if err != nil {
		return AdoptSummary{}, err
	}
	var out AdoptSummary
	for _, mapping := range a.Mailboxes {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		box, err := jmap.ResolveMailbox(mapping.Remote, boxes)
		if err != nil {
			return out, err
		}
		r.Set(a.Name, box.FullName, "scanning existing local mail")
		locals, err := untaggedLocalMessages(localMailboxPath(a, mapping.Local))
		if err != nil {
			return out, err
		}
		if len(locals) == 0 {
			continue
		}
		ids, err := c.QueryEmailIDs(ctx, box.ID)
		if err != nil {
			return out, err
		}
		var wanted []string
		for _, id := range ids {
			if _, exists := s.Entries[state.Key("jmap", mapping.Remote, id)]; !exists {
				wanted = append(wanted, id)
			}
		}
		metadata, err := c.GetEmails(ctx, wanted)
		if err != nil {
			return out, err
		}
		remote := make([]remoteAdoptMessage, 0, len(wanted))
		for _, id := range wanted {
			e, ok := metadata[id]
			if !ok {
				continue
			}
			raw, err := c.DownloadEmail(ctx, e.BlobID)
			if err != nil {
				return out, err
			}
			remote = append(remote, remoteAdoptMessage{ID: id, Raw: raw})
		}
		adopted, skipped, err := adoptMatches(a, mapping, "jmap", locals, remote, s, statePath, opts.DryRun, r)
		out.Adopted += adopted
		out.Skipped += skipped
		if err != nil {
			return out, err
		}
	}
	if !opts.DryRun {
		if err := s.Save(statePath); err != nil {
			return out, err
		}
	}
	return out, nil
}

func adoptMatches(a *config.Account, mapping config.Mailbox, protocol string, locals []localAdoptMessage, remotes []remoteAdoptMessage, s *state.State, statePath string, dryRun bool, r status.Reporter) (int, int, error) {
	used := map[string]bool{}
	adopted, skipped := 0, 0
	for _, local := range locals {
		matches := matchRemote(local.Raw, remotes, used)
		if len(matches) != 1 {
			skipped++
			if len(matches) == 0 {
				r.Set(a.Name, mapping.Remote, "skipping unmatched existing local message "+filepath.Base(local.Path))
			} else {
				r.Set(a.Name, mapping.Remote, "skipping ambiguous existing local message "+filepath.Base(local.Path))
			}
			continue
		}
		m := matches[0]
		stateKey := state.Key(protocol, mapping.Remote, m.ID)
		fileKey := maildir.StableKey(a.Name + "\n" + stateKey)
		r.Set(a.Name, mapping.Remote, fmt.Sprintf("%sadopting existing message %s", dryRunPrefix(dryRun), filepath.Base(local.Path)))
		if !dryRun {
			if _, err := maildir.EnsureStableKey(local.Path, fileKey); err != nil {
				return adopted, skipped, err
			}
			s.Entries[stateKey] = state.Entry{
				Protocol:      protocol,
				RemoteMailbox: mapping.Remote,
				RemoteID:      m.ID,
				LocalMailbox:  mapping.Local,
				FileKey:       fileKey,
			}
			if err := s.Save(statePath); err != nil {
				return adopted, skipped, err
			}
		}
		used[m.ID] = true
		adopted++
	}
	return adopted, skipped, nil
}

func matchRemote(localRaw []byte, remotes []remoteAdoptMessage, used map[string]bool) []remoteAdoptMessage {
	localHash := sha256.Sum256(normalizeRaw(localRaw))
	var exact []remoteAdoptMessage
	for _, r := range remotes {
		if used[r.ID] {
			continue
		}
		if sha256.Sum256(normalizeRaw(r.Raw)) == localHash {
			exact = append(exact, r)
		}
	}
	if len(exact) > 0 {
		return exact
	}

	mid := messageID(localRaw)
	if mid == "" {
		return nil
	}
	var byID []remoteAdoptMessage
	for _, r := range remotes {
		if used[r.ID] || !strings.EqualFold(messageID(r.Raw), mid) {
			continue
		}
		byID = append(byID, r)
	}
	return byID
}

func untaggedLocalMessages(maildirPath string) ([]localAdoptMessage, error) {
	var out []localAdoptMessage
	for _, sub := range []string{"new", "cur"} {
		entries, err := os.ReadDir(filepath.Join(maildirPath, sub))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || strings.Contains(e.Name(), "mailsalonsync-") {
				continue
			}
			path := filepath.Join(maildirPath, sub, e.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			out = append(out, localAdoptMessage{Path: path, Raw: raw})
		}
	}
	return out, nil
}

func normalizeRaw(raw []byte) []byte {
	return bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
}

func messageID(raw []byte) string {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(m.Header.Get("Message-ID"))
}

func dryRunPrefix(dry bool) string {
	if dry {
		return "would "
	}
	return ""
}
