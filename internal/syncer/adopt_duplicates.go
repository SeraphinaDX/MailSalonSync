package syncer

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/maildir"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/state"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/status"
)

type trackedLocalAdoptMessage struct {
	RemoteID string
	FileKey  string
	Path     string
	Raw      []byte
}

func trackedLocalAdoptMessages(a *config.Account, mapping config.Mailbox, protocol string, s *state.State) ([]trackedLocalAdoptMessage, error) {
	dir := maildir.Open(localMailboxPath(a, mapping.Local))
	var out []trackedLocalAdoptMessage
	for _, e := range s.Entries {
		if e.Protocol != protocol || e.RemoteMailbox != mapping.Remote || e.LocalMailbox != mapping.Local {
			continue
		}
		path, exists, err := dir.Find(e.FileKey)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		out = append(out, trackedLocalAdoptMessage{
			RemoteID: e.RemoteID,
			FileKey:  e.FileKey,
			Path:     path,
			Raw:      raw,
		})
	}
	return out, nil
}

// reconcileLegacyDuplicates removes legacy untagged duplicates from the active
// Maildir when the same message already has a healthy tracked MailSalonSync
// copy. The legacy copy is moved into state_dir/adopt-backup rather than
// deleted. This leaves exactly one active tracked copy for Maildir clients.
func reconcileLegacyDuplicates(cfg *config.Config, a *config.Account, mapping config.Mailbox, protocol string, locals []localAdoptMessage, s *state.State, dryRun bool, r status.Reporter) ([]localAdoptMessage, int, error) {
	tracked, err := trackedLocalAdoptMessages(a, mapping, protocol, s)
	if err != nil {
		return nil, 0, err
	}
	if len(tracked) == 0 || len(locals) == 0 {
		return locals, 0, nil
	}

	candidates := make([]remoteAdoptMessage, 0, len(tracked))
	byID := make(map[string]trackedLocalAdoptMessage, len(tracked))
	for _, t := range tracked {
		// RemoteID is unique within one mapped mailbox. It also lets us reuse the
		// conservative exact / Message-ID / header matcher.
		candidates = append(candidates, remoteAdoptMessage{ID: t.RemoteID, Raw: t.Raw})
		byID[t.RemoteID] = t
	}

	used := map[string]bool{}
	remaining := make([]localAdoptMessage, 0, len(locals))
	retired := 0
	exact, messageIDMatches, header := 0, 0, 0
	ambiguous := 0

	for _, local := range locals {
		matches, method := matchRemoteDetailed(local.Raw, candidates, used)
		if len(matches) != 1 {
			if len(matches) > 1 {
				ambiguous++
			}
			remaining = append(remaining, local)
			continue
		}
		trackedCopy, ok := byID[matches[0].ID]
		if !ok {
			remaining = append(remaining, local)
			continue
		}

		if !dryRun {
			if err := backupLegacyDuplicate(cfg.StateDir, a.Name, mapping.Local, local.Path); err != nil {
				return nil, retired, err
			}
		}
		used[trackedCopy.RemoteID] = true
		retired++
		switch method {
		case "exact":
			exact++
		case "message-id":
			messageIDMatches++
		case "header":
			header++
		}
	}

	if retired > 0 || ambiguous > 0 {
		action := "would retire"
		if !dryRun {
			action = "retired"
		}
		r.Set(a.Name, mapping.Remote, fmt.Sprintf("legacy duplicate reconciliation: %s %d duplicate(s) to adopt-backup (exact=%d message-id=%d header=%d ambiguous=%d); %d untagged remain", action, retired, exact, messageIDMatches, header, ambiguous, len(remaining)))
	}
	return remaining, retired, nil
}

func backupLegacyDuplicate(stateDir, accountName, localMailbox, src string) error {
	sub := filepath.Base(filepath.Dir(src))
	root := filepath.Join(stateDir, "adopt-backup", safeBackupComponent(accountName), safeBackupComponent(localMailbox), sub)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	dst := filepath.Join(root, filepath.Base(src))
	for n := 1; ; n++ {
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			break
		} else if err != nil {
			return err
		}
		dst = filepath.Join(root, fmt.Sprintf("%s.%d", filepath.Base(src), n))
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	// state_dir may live on a different filesystem from local_root. Fall back to
	// copy+remove so the one-shot migration still works across mount points.
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = os.Remove(dst)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		return err
	}
	ok = true
	return nil
}

func safeBackupComponent(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "." {
		return "root"
	}
	s = strings.NewReplacer("/", "_", "\\", "_", ":", "_", "..", "_").Replace(s)
	return s
}
