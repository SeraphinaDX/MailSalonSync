package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Version identifies the on-disk state schema. Bump this when a change can no
// longer be read safely by older versions of MailSalonSync.
const Version = 1

// State is the durable per-account index that ties remote messages to local
// Maildir files.
type State struct {
	Version int                   `json:"version"`
	Entries map[string]Entry      `json:"entries"`
	IMAP    map[string]IMAPFolder `json:"imap"`
}

// Entry records one synchronized remote message and the stable key embedded in
// its local Maildir filename.
type Entry struct {
	Protocol      string `json:"protocol"`
	RemoteMailbox string `json:"remote_mailbox"`
	RemoteID      string `json:"remote_id"`
	LocalMailbox  string `json:"local_mailbox"`
	FileKey       string `json:"file_key"`
}

// IMAPFolder stores mailbox metadata that affects the meaning of message UIDs.
type IMAPFolder struct {
	UIDValidity uint32 `json:"uid_validity"`
}

// New returns an empty state object using the current schema version.
func New() *State {
	return &State{Version: Version, Entries: map[string]Entry{}, IMAP: map[string]IMAPFolder{}}
}

// Load reads an account state file. A missing file is treated as a first run,
// while an unknown schema version is rejected rather than guessed at.
func Load(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return New(), nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", path, err)
	}
	if s.Version != Version {
		return nil, fmt.Errorf("unsupported state version %d", s.Version)
	}
	// Older state files may omit empty maps. Normalize them so callers can write
	// to the maps without nil checks.
	if s.Entries == nil {
		s.Entries = map[string]Entry{}
	}
	if s.IMAP == nil {
		s.IMAP = map[string]IMAPFolder{}
	}
	return &s, nil
}

// Save writes state atomically: the complete JSON document is written to a
// sibling temporary file and then renamed over the old state file.
func (s *State) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Key constructs the map key used for a remote message. The mailbox is part of
// the key because JMAP messages may have memberships in several mailboxes and
// IMAP UIDs are scoped to a selected mailbox.
func Key(protocol, mailbox, id string) string {
	return protocol + "\n" + mailbox + "\n" + id
}
