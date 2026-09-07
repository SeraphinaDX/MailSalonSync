package syncer

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/jmap"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/maildir"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/state"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/status"
)

type UploadExistingOptions struct {
	DryRun bool
}

type UploadExistingSummary struct {
	Uploaded int
	Adopted  int
	Skipped  int
}

// UploadExisting is a one-shot migration for local Maildir messages that are
// not present on the server. It currently supports JMAP accounts. Before
// importing a local message, it compares it against the current remote mailbox
// using the same conservative exact / Message-ID / header matching used by
// adopt-existing. A unique remote match is adopted instead of uploaded, making
// reruns safe after partial/interrupted migrations.
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
			return total, fmt.Errorf("account %s: upload-existing currently supports JMAP accounts only", a.Name)
		}
		summary, err := uploadExistingJMAP(ctx, cfg, a, opts, r)
		total.Uploaded += summary.Uploaded
		total.Adopted += summary.Adopted
		total.Skipped += summary.Skipped
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

	var out UploadExistingSummary
	for _, mapping := range a.Mailboxes {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		box, err := jmap.ResolveMailbox(mapping.Remote, boxes)
		if err != nil {
			return out, err
		}
		r.Set(a.Name, box.FullName, "scanning local-only mail for upload")
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
		metadata, err := c.GetEmails(ctx, ids)
		if err != nil {
			return out, err
		}
		remote := make([]remoteAdoptMessage, 0, len(ids))
		for _, id := range ids {
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

		uploaded, adopted, skipped := 0, 0, 0
		for _, local := range locals {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			matches, _ := matchRemoteDetailed(local.Raw, remote, map[string]bool{})
			if len(matches) > 1 {
				skipped++
				continue
			}

			if len(matches) == 1 {
				remoteID := matches[0].ID
				stateKey := state.Key("jmap", mapping.Remote, remoteID)
				fileKey := maildir.StableKey(a.Name + "\n" + stateKey)
				if existing, ok := s.Entries[stateKey]; ok && existing.FileKey != "" {
					fileKey = existing.FileKey
				}
				if !opts.DryRun {
					if _, err := maildir.EnsureStableKey(local.Path, fileKey); err != nil {
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
						return out, err
					}
				}
				adopted++
				continue
			}

			if opts.DryRun {
				uploaded++
				continue
			}

			seen := localMaildirSeen(local.Path)
			remoteID, err := c.ImportRaw(ctx, local.Raw, box.ID, seen)
			if err != nil {
				return out, fmt.Errorf("import %s: %w", filepath.Base(local.Path), err)
			}
			stateKey := state.Key("jmap", mapping.Remote, remoteID)
			fileKey := maildir.StableKey(a.Name + "\n" + stateKey)
			if _, err := maildir.EnsureStableKey(local.Path, fileKey); err != nil {
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
				return out, err
			}
			remote = append(remote, remoteAdoptMessage{ID: remoteID, Raw: local.Raw})
			uploaded++
		}

		action := "uploaded"
		adoptAction := "adopted"
		if opts.DryRun {
			action = "would upload"
			adoptAction = "would adopt"
		}
		r.Set(a.Name, box.FullName, fmt.Sprintf("upload-existing: %d local untagged; %s %d; %s %d existing remote; skipped %d ambiguous", len(locals), action, uploaded, adoptAction, adopted, skipped))
		out.Uploaded += uploaded
		out.Adopted += adopted
		out.Skipped += skipped
	}
	return out, nil
}

func localMaildirSeen(path string) bool {
	base := filepath.Base(path)
	idx := strings.LastIndex(base, ":2,")
	if idx < 0 {
		return false
	}
	return strings.Contains(base[idx+3:], "S")
}
