package syncer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"mime"
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
	Retired int
	Skipped int
}

// AdoptExisting is a one-shot migration for mail that predates MailSalonSync's
// stable filename markers. It never uploads local messages and never changes
// remote mailbox membership. Untagged legacy duplicates of healthy tracked
// copies are moved to state_dir/adopt-backup, while genuinely untracked local
// mail is adopted only when it uniquely matches a message already on the
// server.
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
		total.Retired += summary.Retired
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

		locals, retired, err := reconcileLegacyDuplicates(cfg, a, mapping, "imap", locals, s, opts.DryRun, r)
		out.Retired += retired
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
			if e, exists := s.Entries[key]; exists {
				needs, err := trackedEntryNeedsAdoption(a, e)
				if err != nil {
					return out, err
				}
				if !needs {
					continue
				}
			}
			msg, err := c.UIDFetch(uid)
			if err != nil {
				return out, err
			}
			remote = append(remote, remoteAdoptMessage{ID: id, Raw: msg.Raw})
		}
		r.Set(a.Name, mapping.Remote, fmt.Sprintf("adoption scan: %d untagged local, %d eligible remote", len(locals), len(remote)))
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

		locals, retired, err := reconcileLegacyDuplicates(cfg, a, mapping, "jmap", locals, s, opts.DryRun, r)
		out.Retired += retired
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
			key := state.Key("jmap", mapping.Remote, id)
			e, exists := s.Entries[key]
			if !exists {
				wanted = append(wanted, id)
				continue
			}
			needs, err := trackedEntryNeedsAdoption(a, e)
			if err != nil {
				return out, err
			}
			if needs {
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
		r.Set(a.Name, box.FullName, fmt.Sprintf("adoption scan: %d untagged local, %d eligible remote", len(locals), len(remote)))
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

// trackedEntryNeedsAdoption reports whether an existing state entry still needs
// migration to the current stable mailsalonsync-* filename marker. Legacy state
// entries and state entries whose tagged file has gone missing are eligible.
// Correctly tracked tagged messages are deliberately excluded so adopt-existing
// cannot steal an identity from a healthy local copy in another mapped folder.
func trackedEntryNeedsAdoption(a *config.Account, e state.Entry) (bool, error) {
	if !strings.HasPrefix(e.FileKey, "mailsalonsync-") {
		return true, nil
	}
	for _, mapping := range a.Mailboxes {
		dir := maildir.Open(localMailboxPath(a, mapping.Local))
		_, exists, err := dir.Find(e.FileKey)
		if err != nil {
			return false, err
		}
		if exists {
			return false, nil
		}
	}
	return true, nil
}

func adoptMatches(a *config.Account, mapping config.Mailbox, protocol string, locals []localAdoptMessage, remotes []remoteAdoptMessage, s *state.State, statePath string, dryRun bool, r status.Reporter) (int, int, error) {
	used := map[string]bool{}
	adopted, skipped := 0, 0
	exactMatches, messageIDMatches, headerMatches := 0, 0, 0
	unmatched, ambiguous, noMessageID := 0, 0, 0

	for _, local := range locals {
		matches, method := matchRemoteDetailed(local.Raw, remotes, used)
		if len(matches) != 1 {
			skipped++
			if len(matches) > 1 {
				ambiguous++
			} else if messageID(local.Raw) == "" {
				noMessageID++
			} else {
				unmatched++
			}
			continue
		}

		switch method {
		case "exact":
			exactMatches++
		case "message-id":
			messageIDMatches++
		case "header":
			headerMatches++
		}

		m := matches[0]
		stateKey := state.Key(protocol, mapping.Remote, m.ID)
		fileKey := maildir.StableKey(a.Name + "\n" + stateKey)
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

	r.Set(a.Name, mapping.Remote, fmt.Sprintf("adoption matching: exact=%d message-id=%d header=%d unmatched=%d ambiguous=%d no-message-id=%d", exactMatches, messageIDMatches, headerMatches, unmatched, ambiguous, noMessageID))
	return adopted, skipped, nil
}

func matchRemote(localRaw []byte, remotes []remoteAdoptMessage, used map[string]bool) []remoteAdoptMessage {
	matches, _ := matchRemoteDetailed(localRaw, remotes, used)
	return matches
}

func matchRemoteDetailed(localRaw []byte, remotes []remoteAdoptMessage, used map[string]bool) ([]remoteAdoptMessage, string) {
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
		return exact, "exact"
	}

	mid := messageID(localRaw)
	if mid != "" {
		var byID []remoteAdoptMessage
		for _, r := range remotes {
			if used[r.ID] || !strings.EqualFold(messageID(r.Raw), mid) {
				continue
			}
			byID = append(byID, r)
		}
		if len(byID) > 0 {
			return byID, "message-id"
		}
	}

	fingerprint := headerFingerprint(localRaw)
	if fingerprint == "" {
		return nil, ""
	}
	var byHeader []remoteAdoptMessage
	for _, r := range remotes {
		if used[r.ID] || headerFingerprint(r.Raw) != fingerprint {
			continue
		}
		byHeader = append(byHeader, r)
	}
	if len(byHeader) > 0 {
		return byHeader, "header"
	}
	return nil, ""
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

// headerFingerprint is a conservative fallback for older Maildir copies whose
// raw bytes differ from the server copy and whose Message-ID cannot be matched.
// A match is accepted only when the resulting fingerprint is unique.
func headerFingerprint(raw []byte) string {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}

	date := normalizeDate(m.Header.Get("Date"))
	from := normalizeAddresses(m.Header.Get("From"))
	to := normalizeAddresses(m.Header.Get("To"))
	cc := normalizeAddresses(m.Header.Get("Cc"))
	subject := normalizeSubject(m.Header.Get("Subject"))

	// Require the strongest three envelope fields before using this fallback.
	// This deliberately refuses vague matches such as subject-only newsletters.
	if date == "" || from == "" || subject == "" {
		return ""
	}
	return strings.Join([]string{date, from, to, cc, subject}, "\n")
}

func normalizeDate(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if t, err := mail.ParseDate(value); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func normalizeAddresses(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	addresses, err := mail.ParseAddressList(value)
	if err != nil {
		return strings.ToLower(strings.Join(strings.Fields(value), " "))
	}
	parts := make([]string, 0, len(addresses))
	for _, address := range addresses {
		parts = append(parts, strings.ToLower(strings.TrimSpace(address.Address)))
	}
	return strings.Join(parts, ",")
}

func normalizeSubject(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if decoded, err := new(mime.WordDecoder).DecodeHeader(value); err == nil {
		value = decoded
	}
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func dryRunPrefix(dry bool) string {
	if dry {
		return "would "
	}
	return ""
}
