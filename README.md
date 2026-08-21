# MailSalonSync

![Logo](logo.avif)

`MailSalonSync` is a small, Maildir-first replacement for the common OfflineIMAP workflow, written in Go. It can mirror configured folders from either IMAP or JMAP accounts, propagate deletions in both directions, handle multiple accounts and mailbox-to-directory mappings, and submit raw RFC 5322 messages with JMAP.

While it works, an interactive terminal gets a Charm Bubble Tea/Bubbles spinner with the current account, mailbox, operation, and running add/delete counts. When stdout is redirected, it falls back to normal line-oriented status output.

## Features

- Multiple accounts in one config file.
- IMAP and JMAP accounts can be mixed.
- Any number of remote mailbox -> local Maildir mappings per account.
- Remote messages are stored as normal Maildir files (`new/`, `cur/`, `tmp/`).
- Stable per-message state survives Maildir moves from `new/` to `cur/` and normal Maildir flag suffix changes.
- Remote deletion removes the corresponding local Maildir file.
- With `propagate_deletes: true`, deleting the local Maildir file propagates back to the server.
- IMAP deletion uses UIDPLUS `UID EXPUNGE` when available. A broad `EXPUNGE` is refused by default on servers without UIDPLUS.
- JMAP deletion removes the message from the mapped mailbox; if that is its final mailbox, the Email is destroyed.
- JMAP send accepts a complete RFC 5322 message from a file or stdin.
- Secrets can come from environment variables or commands such as `pass`, so they do not need to live in the config file.
- IMAP supports implicit TLS, STARTTLS, and explicit plaintext mode.
- JMAP session/API/upload/download endpoints are required to use HTTPS.
- Ctrl-C or `q` in the interactive status UI cancels the underlying sync/send operation.

## Build

The project targets Go 1.23 and uses the Go-1.23-compatible Charm v1 packages.

```sh
go mod tidy
go build -o MailSalonSync ./cmd/MailSalonSync
```

The only non-standard-library dependencies are Charm Bubble Tea and Bubbles.

## Commands

Sync every configured account:

```sh
MailSalonSync -config ~/.config/MailSalonSync/config.json sync
```

Sync one account, or a comma-separated subset:

```sh
MailSalonSync -config ~/.config/MailSalonSync/config.json sync -account personal-imap
MailSalonSync -config ~/.config/MailSalonSync/config.json sync -account personal-imap,work-jmap
```

Validate the configuration:

```sh
MailSalonSync -config ~/.config/MailSalonSync/config.json check-config
```

Print an example configuration:

```sh
MailSalonSync example-config
```

Send a raw message via JMAP:

```sh
MailSalonSync -config ~/.config/MailSalonSync/config.json \
  jmap-send -account work-jmap -file message.eml
```

Or make it act like a simple sendmail-style sink:

```sh
cat message.eml | MailSalonSync -config ~/.config/MailSalonSync/config.json \
  jmap-send -account work-jmap
```

## Configuration

The default config path is `$XDG_CONFIG_HOME/MailSalonSync/config.json`, falling back to `~/.config/MailSalonSync/config.json`.

See [`config.example.json`](config.example.json) for a complete two-account example.

A mailbox mapping has two fields:

```json
{ "remote": "INBOX", "local": "INBOX" }
```

`local` is relative to the account's `local_root`, unless it is an absolute path. Use `"."` to use `local_root` itself as the Maildir.

For JMAP, a remote mailbox can be selected three ways:

```json
{ "remote": "role:inbox", "local": "INBOX" }
{ "remote": "Archive/Projects", "local": "Projects" }
{ "remote": "id:server-mailbox-id", "local": "Special" }
```

`role:` is usually the most robust choice for Inbox, Sent, Drafts, Trash, and similar special-use mailboxes because it does not depend on a localized display name.

### IMAP account

```json
{
  "name": "personal-imap",
  "protocol": "imap",
  "local_root": "~/Mail/personal",
  "propagate_deletes": true,
  "mailboxes": [
    { "remote": "INBOX", "local": "INBOX" },
    { "remote": "Archive", "local": "Archive" }
  ],
  "imap": {
    "address": "imap.example.com:993",
    "username": "me@example.com",
    "password_env": "PERSONAL_IMAP_PASSWORD",
    "security": "tls"
  }
}
```

`security` may be `tls`, `starttls`, or `plain`. `plain` should only be used on a trusted local/tunneled connection.

If an IMAP server does not advertise UIDPLUS, local-to-remote deletion is refused because ordinary `EXPUNGE` removes *all* messages already marked `\\Deleted` in the selected mailbox. If you explicitly accept that behavior:

```json
"allow_expunge_without_uidplus": true
```

### JMAP account

```json
{
  "name": "work-jmap",
  "protocol": "jmap",
  "local_root": "~/Mail/work",
  "propagate_deletes": true,
  "mailboxes": [
    { "remote": "role:inbox", "local": "INBOX" },
    { "remote": "role:sent", "local": "Sent" }
  ],
  "jmap": {
    "session_url": "https://mail.example.net/.well-known/jmap",
    "auth": "basic",
    "username": "me@example.net",
    "password_command": "pass show mail/example.net",
    "drafts_mailbox": "role:drafts",
    "sent_mailbox": "role:sent"
  }
}
```

For bearer authentication use `"auth": "bearer"` and one of `bearer_token`, `bearer_token_env`, or `bearer_token_command`.

`account_id` and `identity_id` are optional. If omitted, the primary JMAP Mail account is used, and sending selects an identity matching the message's `From:` header when possible.

## Deletion semantics

State is stored per account under `state_dir`. Each remote message is paired with a deterministic marker embedded in its Maildir filename. This is why an MUA may safely move a message from `new/` to `cur/` or add Maildir flags without breaking identity tracking.

On each sync:

1. Previously tracked files that have disappeared locally are treated as local deletions. If `propagate_deletes` is enabled, the corresponding remote message is deleted/removed.
2. The remote mailbox is listed.
3. Tracked messages that are no longer in the remote mailbox are removed locally.
4. Remote messages that have no local file are downloaded.

For JMAP, a message may belong to several mailboxes at once. Local deletion from one mapped Maildir removes only that mailbox membership when other memberships remain; if it was the final mailbox, the Email is destroyed.

## JMAP send

`jmap-send` deliberately accepts a *complete* message rather than inventing another message-composition syntax. The flow is:

1. Upload the RFC 5322 message as a JMAP blob.
2. Import it into the configured Drafts mailbox with `Email/import`.
3. Create an `EmailSubmission` to send it.
4. On success, clear the draft keyword and move the message to the Sent mailbox when a Sent mailbox is available.

The server derives the SMTP envelope from the Sender/From and To/Cc/Bcc headers, as defined by JMAP Mail.

## Current scope

This first version intentionally focuses on reliable message mirroring and deletion. It initializes Maildir seen state from IMAP `\\Seen` / JMAP `$seen`, but it does **not yet perform bidirectional flag/keyword synchronization**. It also does not upload arbitrary new Maildir files back into IMAP/JMAP; outbound JMAP mail is handled explicitly by `jmap-send`.

The IMAP implementation is a deliberately small client covering LOGIN, TLS/STARTTLS, SELECT, UID SEARCH, UID FETCH, UID STORE, UID EXPUNGE/EXPUNGE, CAPABILITY, and LOGOUT. Servers requiring OAuth/SASL or unusual legacy mailbox encodings will need an additional authentication/encoding layer.
