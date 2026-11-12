package syncer

import (
	"fmt"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/jmap"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/maildir"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/state"
)

// reconcileJMAPMove records a verified server outcome. Another device may have
// removed the message or created an independently tracked copy in the target
// mailbox while MailSalon was closed. Preserve unsynchronized local bytes in
// adopt-backup rather than overwriting an existing destination or dropping them.
func reconcileJMAPMove(cfg *config.Config, a *config.Account, s *state.State, path, oldKey string, e state.Entry, destination config.Mailbox, outcome jmap.MoveOutcome) error {
	newKey := state.Key("jmap", destination.Remote, e.RemoteID)
	movedDir := maildir.Open(localMailboxPath(a, destination.Local))
	if outcome == jmap.MoveGone || outcome == jmap.MoveMovedElsewhere {
		// The server is authoritative for membership, but the locally moved
		// file may be the only remaining copy: keep it in a recoverable backup.
		if err := backupJMAPMoveFile(cfg, a, destination, e.FileKey); err != nil {
			return err
		}
		delete(s.Entries, oldKey)
		return s.Save(path)
	}

	if existing, ok := s.Entries[newKey]; ok && newKey != oldKey && existing.FileKey != e.FileKey {
		// The same JMAP id is already tracked in the destination. Prefer a
		// healthy destination copy and save the moved duplicate separately.
		trackedDir := maildir.Open(localMailboxPath(a, existing.LocalMailbox))
		_, trackedExists, err := trackedDir.Find(existing.FileKey)
		if err != nil {
			return err
		}
		if trackedExists {
			if err := backupJMAPMoveFile(cfg, a, destination, e.FileKey); err != nil {
				return err
			}
			delete(s.Entries, oldKey)
			return s.Save(path)
		}
		// The destination index points to a missing file. Reuse the local
		// moved message instead; never leave it untracked.
	}
	_, exists, err := movedDir.Find(e.FileKey)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("cannot reconcile JMAP move for email %s: local destination file %s missing", e.RemoteID, e.FileKey)
	}
	delete(s.Entries, oldKey)
	s.Entries[newKey] = state.Entry{
		Protocol: "jmap", RemoteMailbox: destination.Remote, RemoteID: e.RemoteID,
		LocalMailbox: destination.Local, FileKey: e.FileKey,
	}
	return s.Save(path)
}

// backupJMAPMoveFile leaves no active orphan in a mapped Maildir after the
// source state is retired, but preserves its original contents for recovery.
func backupJMAPMoveFile(cfg *config.Config, a *config.Account, destination config.Mailbox, fileKey string) error {
	dir := maildir.Open(localMailboxPath(a, destination.Local))
	file, exists, err := dir.Find(fileKey)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return backupLegacyDuplicate(cfg.StateDir, a.Name, destination.Local, file)
}
