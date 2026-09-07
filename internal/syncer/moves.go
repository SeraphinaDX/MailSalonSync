package syncer

import (
	"fmt"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/maildir"
)

// findTrackedMove looks for the stable local file key in another configured
// Maildir for the same account. Exactly one destination is required; finding
// more than one copy is ambiguous and must not trigger a remote mutation.
func findTrackedMove(a *config.Account, current config.Mailbox, fileKey string) (*config.Mailbox, error) {
	var found *config.Mailbox
	for i := range a.Mailboxes {
		m := a.Mailboxes[i]
		if m.Local == current.Local {
			continue
		}
		dir := maildir.Open(localMailboxPath(a, m.Local))
		_, exists, err := dir.Find(fileKey)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("tracked message %s appears in multiple mapped Maildirs (%s and %s)", fileKey, found.Local, m.Local)
		}
		copy := m
		found = &copy
	}
	return found, nil
}
