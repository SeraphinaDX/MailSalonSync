// Package jmap implements MailSalonSync's JMAP Mail and submission client.
//
// A Client begins with JMAP session discovery, validates every advertised
// endpoint as HTTPS, then uses the selected mail account for mailbox, email,
// import, move, quota, and submission operations.
//
// Mailbox selectors accepted by higher layers may use a stable JMAP role
// (for example role:inbox), an explicit mailbox ID (id:...), or a display path.
// Roles and IDs are preferred because display names can be renamed or localized.
//
// JMAP messages may belong to more than one mailbox at once. Code in this
// package therefore distinguishes removing a mailbox membership from destroying
// an Email object entirely.
package jmap
