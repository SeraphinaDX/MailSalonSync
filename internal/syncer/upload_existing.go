package syncer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/jmap"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/maildir"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/state"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/status"
)

var ErrUploadQuotaExceeded = errors.New("JMAP upload quota exceeded")

type UploadExistingOptions struct {
	DryRun bool
}

type UploadExistingSummary struct {
	Uploaded           int
	Adopted            int
	Elsewhere          int
	Ambiguous          int
	Duplicates         int
	DuplicateExact     int
	DuplicateMessageID int
	DuplicateHeader    int
	Skipped            int
}

type existingRemoteIndex struct {
	messages  []remoteAdoptMessage
	locations map[string]map[string]bool
	// migrated marks messages selected/imported by this upload-existing run.
	// A later matching local file is therefore a legacy local duplicate and can
	// be retired safely without confusing it with pre-existing server mail.
	migrated map[string]bool
}

// UploadExisting is a one-shot migration for local Maildir messages that are
// not present on the server. It currently supports JMAP accounts. Before
// importing a local message, it compares it against messages in all configured
// server mailboxes for the account using the conservative exact / Message-ID /
// header matching used by adopt-existing. A unique match in the same target
// mailbox is adopted instead of uploaded, making reruns safe after partial or
// interrupted migrations. A match in another mailbox is skipped rather than
// duplicated. Additional local copies of a message migrated by this run are
// moved to state_dir/adopt-backup so only the tracked canonical copy remains in
// active Maildir folders.
func UploadExisting(ctx context.Context, cfg *config.Config, accountNames []string, opts UploadExistingOptions, r status.Reporter) (UploadExistingSummary, error) {
	selected := map[string]bool{}
	for _, n := range accountNames {
		if n != "" {
			selected[n] = true
		}
	}
	if len(selected) > 0 {
		for n := range selected {
			if _, err := cfg.Account(n); err != nil {
				return UploadExistingSummary{}, err
			}
		}
	}

	var total UploadExistingSummary
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		if len(selected) > 0 && !selected[a.Name] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if a.Protocol != "jmap" {
			if len(selected) > 0 && selected[a.Name] {
				return total, fmt.Errorf("account %s: upload-existing currently supports JMAP accounts only", a.Name)
			}
			r.Set(a.Name, "", "skipping upload-existing: JMAP only")
			continue
		}
		summary, err := uploadExistingJMAP(ctx, cfg, a, opts, r)
		addUploadSummary(&total, summary)
		if err != nil {
			return total, fmt.Errorf("account %s: %w", a.Name, err)
		}
	}
	return total, nil
}

func uploadExistingJMAP(ctx context.Context, cfg *config.Config, a *config.Account, opts UploadExistingOptions, r status.Reporter) (UploadExistingSummary, error) {
	secret, err := a.JMAPSecret()
	if err != nil {
		return UploadExistingSummary{}, err
	}
	c, err := jmap.New(ctx, a.JMAP.SessionURL, jmap.Auth{Mode: a.JMAP.Auth, Username: a.JMAP.Username, Secret: secret}, a.JMAP.AccountID)
	if err != nil {
		return UploadExistingSummary{}, err
	}
	boxes, err := c.ListMailboxes(ctx)
	if err != nil {
		return UploadExistingSummary{}, err
	}
	s, statePath, err := loadState(cfg, a)
	if err != nil {
		return UploadExistingSummary{}, err
	}
	index, resolved, err := buildExistingRemoteIndex(ctx, c, a, boxes, r)
	if err != nil {
		return UploadExistingSummary{}, err
	}

	var out UploadExistingSummary
	planned := 0
	for _, mapping := range a.Mailboxes {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		box, ok := resolved[mapping.Remote]
		if !ok {
			return out, fmt.Errorf("no resolved mailbox for %q", mapping.Remote)
		}
		r.Set(a.Name, box.FullName, "scanning local-only mail for upload")
		locals, err := untaggedLocalMessages(localMailboxPath(a, mapping.Local))
		if err != nil {
			return out, err
		}
		if len(locals) == 0 {
			continue
		}

		uploaded, adopted := 0, 0
		elsewhere, ambiguous, duplicates, skipped := 0, 0, 0, 0
		duplicateExact, duplicateMessageID, duplicateHeader := 0, 0, 0

		flushMailboxSummary := func() {
			out.Uploaded += uploaded
			out.Adopted += adopted
			out.Elsewhere += elsewhere
			out.Ambiguous += ambiguous
			out.Duplicates += duplicates
			out.DuplicateExact += duplicateExact
			out.DuplicateMessageID += duplicateMessageID
			out.DuplicateHeader += duplicateHeader
			out.Skipped += skipped
		}

		for _, local := range locals {
			if err := ctx.Err(); err != nil {
				flushMailboxSummary()
				return out, err
			}
			matches, method := matchRemoteDetailed(local.Raw, index.messages, map[string]bool{})
			if len(matches) > 1 {
				ambiguous++
				skipped++
				continue
			}

			if len(matches) == 1 {
				remoteID := matches[0].ID

				if index.migrated[remoteID] {
					if !opts.DryRun {
						if err := backupLegacyDuplicate(cfg.StateDir, a.Name, mapping.Local, local.Path); err != nil {
							flushMailboxSummary()
							return out, fmt.Errorf("retire duplicate %s: %w", filepath.Base(local.Path), err)
						}
					}
					duplicates++
					countDuplicateMethod(method, &duplicateExact, &duplicateMessageID, &duplicateHeader)
					skipped++
					continue
				}

				if !index.locations[remoteID][box.ID] {
					elsewhere++
					skipped++
					continue
				}
				stateKey := state.Key("jmap", mapping.Remote, remoteID)
				fileKey := maildir.StableKey(a.Name + "\n" + stateKey)
				if existing, ok := s.Entries[stateKey]; ok && existing.FileKey != "" {
					fileKey = existing.FileKey
					dir := maildir.Open(localMailboxPath(a, mapping.Local))
					if _, exists, err := dir.Find(fileKey); err != nil {
						flushMailboxSummary()
						return out, err
					} else if exists {
						if !opts.DryRun {
							if err := backupLegacyDuplicate(cfg.StateDir, a.Name, mapping.Local, local.Path); err != nil {
								flushMailboxSummary()
								return out, fmt.Errorf("retire duplicate %s: %w", filepath.Base(local.Path), err)
							}
						}
						duplicates++
						countDuplicateMethod(method, &duplicateExact, &duplicateMessageID, &duplicateHeader)
						skipped++
						continue
					}
				}
				if !opts.DryRun {
					if _, err := maildir.EnsureStableKey(local.Path, fileKey); err != nil {
						flushMailboxSummary()
						return out, err
					}
					s.Entries[stateKey] = state.Entry{
						Protocol:      "jmap",
						RemoteMailbox: mapping.Remote,
						RemoteID:      remoteID,
						LocalMailbox:  mapping.Local,
						FileKey:       fileKey,
					}
					if err := s.Save(statePath); err != nil {
						flushMailboxSummary()
						return out, err
					}
				}
				adopted++
				continue
			}

			if opts.DryRun {
				uploaded++
				planned++
				id := fmt.Sprintf("planned-upload-%d", planned)
				index.messages = append(index.messages, remoteAdoptMessage{ID: id, Raw: local.Raw})
				index.locations[id] = map[string]bool{box.ID: true}
				index.migrated[id] = true
				continue
			}

			seen := localMaildirSeen(local.Path)
			remoteID, err := c.ImportRaw(ctx, local.Raw, box.ID, seen)
			if err != nil {
				if jmap.IsUploadQuotaExceeded(err) {
					flushMailboxSummary()
					r.Set(a.Name, box.FullName, fmt.Sprintf("upload quota reached after %d completed upload(s) in this run; progress saved", out.Uploaded))
					return out, fmt.Errorf("%w: progress saved; raise/reset the server JMAP upload quota and rerun upload-existing", ErrUploadQuotaExceeded)
				}
				flushMailboxSummary()
				return out, fmt.Errorf("import %s: %w", filepath.Base(local.Path), err)
			}
			stateKey := state.Key("jmap", mapping.Remote, remoteID)
			fileKey := maildir.StableKey(a.Name + "\n" + stateKey)
			if _, err := maildir.EnsureStableKey(local.Path, fileKey); err != nil {
				flushMailboxSummary()
				return out, fmt.Errorf("imported remote message %s but failed to tag local file %s: %w", remoteID, filepath.Base(local.Path), err)
			}
			s.Entries[stateKey] = state.Entry{
				Protocol:      "jmap",
				RemoteMailbox: mapping.Remote,
				RemoteID:      remoteID,
				LocalMailbox:  mapping.Local,
				FileKey:       fileKey,
			}
			if err := s.Save(statePath); err != nil {
				flushMailboxSummary()
				return out, err
			}
			index.messages = append(index.messages, remoteAdoptMessage{ID: remoteID, Raw: local.Raw})
			index.locations[remoteID] = map[string]bool{box.ID: true}
			index.migrated[remoteID] = true
			uploaded++
		}

		action := "uploaded"
		adoptAction := "adopted"
		duplicateAction := "retired to adopt-backup"
		if opts.DryRun {
			action = "would upload"
			adoptAction = "would adopt"
			duplicateAction = "would retire to adopt-backup"
		}
		r.Set(a.Name, box.FullName, fmt.Sprintf("upload-existing: %d local untagged; %s %d; %s %d existing remote; elsewhere=%d ambiguous=%d duplicate-local=%d %s (exact=%d message-id=%d header=%d)", len(locals), action, uploaded, adoptAction, adopted, elsewhere, ambiguous, duplicates, duplicateAction, duplicateExact, duplicateMessageID, duplicateHeader))
		flushMailboxSummary()
	}
	return out, nil
}

func addUploadSummary(dst *UploadExistingSummary, src UploadExistingSummary) {
	dst.Uploaded += src.Uploaded
	dst.Adopted += src.Adopted
	dst.Elsewhere += src.Elsewhere
	dst.Ambiguous += src.Ambiguous
	dst.Duplicates += src.Duplicates
	dst.DuplicateExact += src.DuplicateExact
	dst.DuplicateMessageID += src.DuplicateMessageID
	dst.DuplicateHeader += src.DuplicateHeader
	dst.Skipped += src.Skipped
}

func countDuplicateMethod(method string, exact, messageID, header *int) {
	switch method {
	case "exact":
		(*exact)++
	case "message-id":
		(*messageID)++
	case "header":
		(*header)++
	}
}

func buildExistingRemoteIndex(ctx context.Context, c *jmap.Client, a *config.Account, boxes []jmap.Mailbox, r status.Reporter) (*existingRemoteIndex, map[string]jmap.Mailbox, error) {
	index := &existingRemoteIndex{
		locations: map[string]map[string]bool{},
		migrated:  map[string]bool{},
	}
	resolved := map[string]jmap.Mailbox{}
	seenID := map[string]bool{}
	for _, mapping := range a.Mailboxes {
		box, err := jmap.ResolveMailbox(mapping.Remote, boxes)
		if err != nil {
			return nil, nil, err
		}
		resolved[mapping.Remote] = box
		r.Set(a.Name, box.FullName, "indexing existing server mail for duplicate protection")
		ids, err := c.QueryEmailIDs(ctx, box.ID)
		if err != nil {
			return nil, nil, err
		}
		metadata, err := c.GetEmails(ctx, ids)
		if err != nil {
			return nil, nil, err
		}
		for _, id := range ids {
			if index.locations[id] == nil {
				index.locations[id] = map[string]bool{}
			}
			index.locations[id][box.ID] = true
			if seenID[id] {
				continue
			}
			e, ok := metadata[id]
			if !ok {
				continue
			}
			raw, err := c.DownloadEmail(ctx, e.BlobID)
			if err != nil {
				return nil, nil, err
			}
			seenID[id] = true
			index.messages = append(index.messages, remoteAdoptMessage{ID: id, Raw: raw})
		}
	}
	return index, resolved, nil
}

func localMaildirSeen(path string) bool {
	base := filepath.Base(path)
	idx := strings.LastIndex(base, ":2,")
	if idx < 0 {
		return false
	}
	return strings.Contains(base[idx+3:], "S")
}
