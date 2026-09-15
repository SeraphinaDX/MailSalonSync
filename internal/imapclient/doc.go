// Package imapclient implements the small subset of IMAP that MailSalonSync
// needs for mailbox mirroring and tracked moves.
//
// It is intentionally not a general-purpose IMAP library. Commands are issued
// synchronously over one connection, and operations that act on messages assume
// the caller has selected the appropriate mailbox first.
//
// Message identity is based on UID plus UIDVALIDITY. Operations that would lose
// a reliable destination UID, or could expunge unrelated messages, are refused
// unless the server advertises the capabilities needed to do them safely.
package imapclient
