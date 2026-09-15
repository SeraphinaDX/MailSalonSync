// Package state persists the mapping between remote message identities and
// local Maildir files.
//
// Each configured account has its own state file. Entries remember the remote
// protocol, mailbox selector, remote message ID, local mailbox, and stable
// Maildir key. IMAP state also records UIDVALIDITY so stale UIDs are never
// mistaken for current messages after a server-side mailbox reset.
//
// State files use a versioned JSON format and are written atomically through a
// temporary file followed by rename.
package state
