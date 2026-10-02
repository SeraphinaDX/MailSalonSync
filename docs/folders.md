# Remote folders and subscriptions

MailSalonSync 0.7.0 lists, creates, subscribes to and unsubscribes from IMAP and
JMAP mail folders. Server subscriptions and local sync mappings are separate:
subscribing enables both; unsubscribing disables the mapping and keeps cached
mail. These commands do not delete server folders or messages.

## Commands

List remote folders, including unsubscribed ones:

```sh
MailSalonSync folders -account personal-jmap
```

The JSON response contains `version: 1`, `account`, `local_root` and `folders`.
Each folder has `id`, `name`, `subscribed`, `syncing`, `local`, `selectable` and
`can_create_child`. Use the returned ID for operations. IMAP IDs are decoded
mailbox names; JMAP IDs have an `id:` prefix.

Create a top-level folder, or a child using its parent's ID:

```sh
MailSalonSync folders -account personal-jmap -action create -name Projects
MailSalonSync folders -account personal-jmap -action create \
  -parent id:parent-mailbox -name Work -local Projects/Work
```

Names are single components; choose parents separately. IMAP respects the
server's hierarchy delimiter, uses modified UTF-7 with IMAP4rev1 and UTF-8 with
IMAP4rev2. JMAP uses parent IDs and server creation permissions.

Subscribe and download mail, or unsubscribe while keeping local mail:

```sh
MailSalonSync folders -account personal-jmap -action subscribe -mailbox id:work-mailbox
MailSalonSync -plain sync -account personal-jmap
MailSalonSync folders -account personal-jmap -action unsubscribe -mailbox id:work-mailbox
```

Subscribing again restores the original local mapping. Existing mappings keep
their remote selectors and local paths, including root-as-INBOX (`local = "."`),
so tracked message identities remain intact. All mappings may be disabled.

Unmapped folders default to `Folders/<remote-name>` under `local_root`.
Characters unsafe in paths are percent-encoded. `-local` selects another relative
path. Traversal, Maildir control directories, symlink parents, existing paths
and mapping collisions are rejected before server mutations. Existing tracked
mappings cannot be redirected through this command. Accounts with no enabled
mail mappings are skipped by normal mail sync.

## Storage and recovery

Managed mappings are saved atomically as private TOML files named
`folders-<account-identity-hash>.toml` in `state_dir`. Records contain `remote`,
`local` and `enabled`. The identity includes the account, protocol, endpoint and
local root. The main config, including credentials and comments, is not rewritten.
Back up these files with the sync state. Removing them removes managed mappings
and restores mappings that were disabled from the main config.

Folder operations, sync, adopt-existing and upload-existing share
`state_dir/mail-operation.lock`. Concurrent mail operations fail with a clear
error. On Linux, macOS, BSD and Windows the OS releases the lock when the process
exits, including when the GUI cancels its helper. The `.lock` file remains;
do not delete it while operations run. Other platforms use a conservative
`.lock.held` fallback; after a crash, remove that file only after verifying no
MailSalonSync mail operation is running.

Server errors never enable an unconfirmed mapping. If creation succeeds but
subscription, mapping persistence or Maildir initialization fails, the error
reports the completed step. Refresh and subscribe to the existing folder to
finish rather than creating it again. A saved mapping survives a failed Maildir
initialization, allowing its original path to be retried.

## MailSalonGUI

Install MailSalonSync 0.7.0+ and configure the GUI account:

```toml
[[accounts]]
name = "personal"
maildir = "~/Maildir"
from = "Your Name <you@example.com>"
receive = "MailSalonSync -plain sync"
send = "MailSalonSync -plain jmap-send -account personal-jmap"
sync_account = "personal-jmap"
# sync_config = "~/.config/MailSalonSync/config.toml"
# sync_executable = "~/bin/MailSalonSync"
```

`sync_account` must match this tool's account name; its `local_root` must match
the GUI's `maildir`. Use the same sync config for `receive` and `sync_config`
when using a custom file. The executable setting is a binary path, not a shell
command. Credentials stay in MailSalonSync; password commands/environment
variables continue to work.

The GUI sends one JSON request to `folders -account NAME -request-stdin`.
Supported keys are `action`, `mailbox`, `name`, `parent`, `local` and
`expected_local_root`. The expected root is checked before connecting or changing
the server. Values travel on stdin and are never expanded by a shell. Successful
stdout contains only the versioned JSON response. Failures return a nonzero
status and diagnostics on stderr.

Creating/subscribing initializes a local folder immediately. Run Sync to download
its messages. Dragging tracked messages into mapped folders uses the existing
move propagation behavior and its `propagate_deletes` setting.

## Validation

Protocol fixtures cover IMAP LIST/LSUB, quoted/literal names, Unicode, rev2
subscribed listings, and JMAP create/update confirmations and set errors.
Mapping tests cover nested create/disable/resume, retained cached mail,
root-as-INBOX, traversal/collisions/symlinks, mismatched roots, corrupt metadata,
locking and recovery from a partial local failure. A disposable IMAP server test
exercises the real CLI, reloads persisted mappings in normal sync, and verifies
unsubscribe stops folder syncing without losing cached mail. No live mail server
was changed. Full tests, race checks and vet passed, along with native Linux
and Windows cross builds.

This release handles mail folders. Folder rename/delete, offline queued
operations and contacts/calendar collection creation are separate features.
