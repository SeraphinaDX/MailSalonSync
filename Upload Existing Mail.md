# Upload Existing Mail

MailSalonSync 0.5.0 adds a one-shot `upload-existing` command for older local Maildir messages that predate MailSalonSync tracking and are not present on the JMAP server.

Start with a dry run:

```sh
MailSalonSync -plain upload-existing -dry-run
```

To limit the migration to one account:

```sh
MailSalonSync -plain upload-existing -account personal-jmap -dry-run
```

If the dry run looks correct, apply it:

```sh
MailSalonSync -plain upload-existing
```

## What it does

For every configured JMAP mailbox mapping, `upload-existing` scans local `new/` and `cur/` files that do not already contain a `mailsalonsync-*` stable key.

Before uploading, MailSalonSync indexes messages in all configured server mailboxes for the account and uses conservative exact-content, Message-ID, and header matching to avoid duplicate imports.

- No server match: upload the RFC 5322 message and import it directly into the mapped JMAP mailbox.
- One match in the same server mailbox: adopt that server message instead of uploading another copy.
- Match in another mapped server mailbox: skip it rather than create a duplicate in the wrong place.
- Ambiguous match: skip it.
- Duplicate local-only copies encountered during the same migration: only one is selected for upload.

The Maildir `S` flag is imported as the JMAP `$seen` keyword.

After a successful import or adoption, the existing local file is renamed in place with the normal `mailsalonsync-*` stable key and MailSalonSync state is saved immediately. The message then participates in normal deletion and tracked-folder-move synchronization, including moves such as `INBOX -> Archive`.

## Interrupted migrations

The command is designed to be rerunnable. If a previous run successfully imported a message but stopped before the local file was tagged, the next run detects the unique server copy and adopts it instead of uploading another copy.

## Scope

`upload-existing` currently performs server upload/import for JMAP accounts. When run without `-account`, non-JMAP accounts are skipped. Selecting a non-JMAP account explicitly returns an error rather than silently doing something unsafe.

`adopt-existing` remains available for cases where the local message already exists on the server and only local tracking needs repair.
