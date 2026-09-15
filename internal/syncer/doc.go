// Package syncer coordinates MailSalonSync's high-level synchronization flows.
//
// It connects protocol clients, Maildir storage, and persistent state. The
// normal sync path reconciles local deletions, remote deletions, and missing
// downloads. Additional flows adopt pre-existing local mail, upload local-only
// messages, and detect tracked moves between configured mailboxes.
//
// A key invariant is that persistent state is updated only after the related
// local or remote operation succeeds. This keeps interrupted runs recoverable:
// rerunning the same command should continue from the last durable state rather
// than assuming work completed when it did not.
package syncer
