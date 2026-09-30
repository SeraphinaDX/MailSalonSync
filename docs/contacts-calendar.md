# Contacts and calendars (0.6.0)

MailSalonSync now implements the core vdirsyncer workflow: discover remote
collections, map each one to a local directory, then synchronize individual
items in both directions. Existing mail account configuration keeps working.
Collections can also be used without any mail accounts.

## Storage and protocols

| Protocol | `remote` | Local item format |
| --- | --- | --- |
| `carddav` | HTTPS address-book collection URL | One vCard per `.vcf` file |
| `caldav` | HTTPS calendar collection URL | One UID/series per `.ics` file |
| `jmap-contacts` | AddressBook ID | One full ContactCard/JSContact `.json` file |
| `jmap-calendars` | Calendar ID | One full CalendarEvent/JSCalendar `.json` file |

The DAV layout can be read by tools such as khard and khal. JMAP JSON is kept
native, rather than converted into a subset of vCard/iCalendar fields. These
JSON directories are not vdirsyncer-compatible exports. Unknown properties,
recurrence definitions, attendee data, and other native fields remain in files.
Each directory belongs to exactly one remote collection and protocol.

## CardDAV and CalDAV configuration

Add independent top-level collections to `~/.config/MailSalonSync/config.toml`:

```toml
[[collections]]
name = "personal-contacts"
protocol = "carddav"
local_dir = "~/PIM/contacts/personal"
remote = "https://dav.example.com/addressbooks/me/personal/"
propagate_deletes = false

[collections.dav]
username = "me@example.com"
password_env = "DAV_PASSWORD"

[[collections]]
name = "personal-calendar"
protocol = "caldav"
local_dir = "~/PIM/calendars/personal"
remote = "https://dav.example.com/calendars/me/personal/"
propagate_deletes = false

[collections.dav]
username = "me@example.com"
password_env = "DAV_PASSWORD"
```

DAV authentication supports `password`, `password_env`, and `password_command`
in the same precedence order as mail. HTTPS and strong ETags are required.
Only authenticated-origin URLs are followed. Use a collection URL for syncing,
not an entire principal or home-set URL.

To find URLs, initially set `remote` to the server's DAV endpoint or principal
URL and run:

```sh
MailSalonSync -config=./config.toml discover -collection personal-contacts
MailSalonSync -config=./config.toml discover -collection personal-calendar
```

Discovery follows current-user-principal and addressbook/calendar-home-set
properties, then prints JSON containing names and URLs. Set `remote` to one
returned `id`; make a separate collection mapping for each book/calendar you
want. Discovery does not create, subscribe to, or delete remote collections.
If your server only supports discovery from its advertised service URL, put
that URL in `remote` directly; DNS SRV and automatic well-known probing are not
implemented yet.

## JMAP configuration

```toml
[[collections]]
name = "jmap-contacts"
protocol = "jmap-contacts"
local_dir = "~/PIM/jmap/contacts"
remote = "ADDRESS_BOOK_ID"
propagate_deletes = false

[collections.jmap]
session_url = "https://mail.example.com/.well-known/jmap"
username = "me@example.com"
password_env = "JMAP_PASSWORD"

[[collections]]
name = "jmap-calendar"
protocol = "jmap-calendars"
local_dir = "~/PIM/jmap/calendar"
remote = "CALENDAR_ID"
propagate_deletes = false

[collections.jmap]
session_url = "https://mail.example.com/.well-known/jmap"
username = "me@example.com"
password_env = "JMAP_PASSWORD"
```

Run `discover -collection jmap-contacts` or `discover -collection jmap-calendar`
to replace the placeholder `remote` with a returned ID. JMAP uses the existing
basic/bearer authentication settings and secret sources. `account_id` is
optional; the primary account is selected independently for the relevant
contacts/calendar capability, which may differ from the mail account. A
contacts/calendar-only server does not need to advertise JMAP Mail.

The server must advertise `urn:ietf:params:jmap:contacts` (RFC 9610) or
`urn:ietf:params:jmap:calendars` (calendar draft), both at session and account
level. An unsupported capability produces an explicit error. This does not
implement older vendor-specific `Contact/get` or `CalendarEvent` APIs under
unrelated capabilities. JMAP calendars follows
[draft-ietf-jmap-calendars-29](https://www.ietf.org/archive/id/draft-ietf-jmap-calendars-29.html);
older server draft implementations may need interoperability adjustments.

## Synchronization commands

```sh
# All mail accounts and PIM collections:
MailSalonSync -plain sync

# PIM only:
MailSalonSync -plain sync -collection personal-contacts,personal-calendar

# Selected mail plus selected PIM collections:
MailSalonSync -plain sync -account personal-jmap -collection jmap-contacts,jmap-calendar
```

With no selectors, `sync` processes everything. With either selector, only the
named accounts/collections are selected. Collection and mail-account names are
globally unique. The existing `-account` selector also accepts collection names.
`adopt-existing`, `upload-existing`, and `jmap-send` remain mail-only commands.
Root flags precede commands; `-config=...` also works in fish.

New valid local items upload automatically. Each must have a UID, and calendar
resources must contain only one UID/series. Matching UID and identical content
can be adopted instead of duplicated. JMAP objects are queried in pages; a
stable object state is required throughout the snapshot. This first version
uses full snapshots, not sync-token or JMAP `/changes` optimization.

## Deletions, conflicts, and recovery

`propagate_deletes` defaults to false for collections. Removing a tracked local
file then restores it on the next sync. Set it to true explicitly when you want
local deletion to remove the remote item. Remote deletion removes the local
file and preserves its previous content under `.mss-backup`. JMAP removal only
unlinks the mapped membership when the item also belongs to another collection.

If local and remote content both change differently, or an edit races a
deletion, sync stops before mutating that conflicted collection. Local edits
remain in place; available remote copies are saved in `.mss-conflicts`. For
example, to accept a remote edit, copy the matching remote conflict file over
the original local item, then sync again. To keep or merge a local edit, retain
both copies and reconcile the server item using your server's editing client;
then put matching content in the local file and rerun. There is no automatic
conflict winner or force-resolution command in this version. Do not erase the
state file to force a conflict through.

DAV PUT/DELETE use conditional ETags; JMAP `/set` uses `ifInState`. A concurrent
server edit is rejected. Partial/invalid listings do not imply remote deletions.
State is checkpointed after each successful item; a failure may leave earlier
successful items synchronized, with the remaining changes available for retry.
An interrupted create is handled conservatively through UID matching; server
normalization can require explicit conflict reconciliation on retry.

`.mss-state.json`, `.mss-lock`, `.mss-conflicts`, `.mss-backup`, and `.mss-trash`
are internal files/directories. Keep the state file with its collection. A lock
prevents overlapping MailSalon/MailSalonSync edits. After a hard kill, remove
`.mss-lock` only after confirming neither process is using that collection.
State is bound to the remote configuration and actual resolved JMAP account;
retarget a collection using a new local directory.

## Using MailSalon

MailSalon 0.4.0 reads these directories. See its
[contacts/calendar guide](https://github.com/SeraphinaDX/MailSalon/blob/main/docs/contacts-calendar.md).
Set the account's `receive` command to `MailSalonSync -plain sync` so both
manual and periodic updates include collections. A mail-only `-account` selector
does not automatically include unrelated PIM collections.

## Migrating from vdirsyncer and current scope

Stop overlapping sync jobs, back up the collection directory, then configure
one mapping per existing vdirsyncer collection. MailSalonSync does not import
vdirsyncer's status database. Matching files are adopted conservatively by UID
and content; differing versions require reconciliation. Do not run two sync
engines against the same directory concurrently.

This is item synchronization, not complete vdirsyncer feature parity. Collection
creation/deletion, metadata sync, OAuth provider flows, DNS discovery, tasks via
JMAP Tasks, and optimized incremental sync are not included. CalDAV VTODO and
recurrence resources are retained as native files. Separate JMAP event objects
with the same UID but different recurrence IDs are conservatively rejected in
this initial engine. Cross-protocol conversion and automatic account-to-account
bridging are also not included. Sync does not request JMAP scheduling messages;
DAV servers may apply their own scheduling policies.

Protocol references: [CardDAV, RFC 6352](https://www.rfc-editor.org/rfc/rfc6352.html),
[CalDAV, RFC 4791](https://www.rfc-editor.org/rfc/rfc4791.html),
[JMAP Contacts, RFC 9610](https://www.rfc-editor.org/rfc/rfc9610.html).
