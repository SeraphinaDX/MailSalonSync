// Command MailSalonSync synchronizes configured IMAP/JMAP mailboxes into
// Maildir directories and provides maintenance commands for adopting or
// uploading pre-existing local mail.
//
// Global flags are parsed before the subcommand. In particular, -plain forces
// synchronous line-oriented status output for parent programs such as mu4e,
// while normal terminal use keeps the interactive status display.
package main
