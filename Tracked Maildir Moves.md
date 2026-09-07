# Tracked Maildir moves

MailSalonSync 0.3.0 can propagate moves of already-tracked messages between configured Maildir folders.

This is intended for Maildir clients such as MailSalon and mu4e. For example, if a tracked message is moved locally from `INBOX` to `Archive`, MailSalonSync recognizes the stable message key in the destination folder and performs the corresponding server-side move.

## Requirements

The source and destination must both be configured mailbox mappings, and `propagate_deletes = true` must be enabled for the account.

For a JMAP account using a normal manually-created Archive mailbox:

```toml
[[accounts]]
name = "personal-jmap"
protocol = "jmap"
local_root = "~/Maildir"
propagate_deletes = true

[[accounts.mailboxes]]
remote = "role:inbox"
local = "INBOX"

[[accounts.mailboxes]]
remote = "Archive"
local = "Archive"
```

A special server-side archive role is not required. If the server does provide one, `remote = "role:archive"` may be used instead.

## Behavior

When a tracked file disappears from its current mapped Maildir, MailSalonSync checks the other mapped Maildirs for the same stable key before treating it as a deletion.

- Exactly one destination match: propagate a move.
- No destination match: use the existing local-deletion behavior.
- More than one destination match: stop with an error rather than guessing.
- Untracked `.eml` files are not uploaded.

For JMAP, MailSalonSync updates the Email's mailbox membership without disturbing unrelated memberships.

For IMAP, MailSalonSync uses `UID MOVE` when the server supports it. Otherwise it uses `UID COPY` followed by safe UID-based deletion. IMAP move propagation requires UIDPLUS so MailSalonSync can learn the destination UID and keep its state consistent.

After the move reaches the server, another computer running MailSalonSync will observe the new mailbox membership on its next sync.
