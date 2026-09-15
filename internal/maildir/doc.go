// Package maildir contains the filesystem operations MailSalonSync uses for
// local Maildir storage.
//
// MailSalonSync embeds a deterministic stable key in managed filenames. The key
// lets state tracking survive ordinary Maildir moves between new/ and cur/ and
// the addition or removal of :2, flag suffixes by a mail client.
//
// New files are written through tmp/ and renamed into new/ or cur/ so readers do
// not observe partially written messages.
package maildir
