package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const Version = 1

type State struct {
	Version int                   `json:"version"`
	Entries map[string]Entry      `json:"entries"`
	IMAP    map[string]IMAPFolder `json:"imap"`
}

type Entry struct {
	Protocol      string `json:"protocol"`
	RemoteMailbox string `json:"remote_mailbox"`
	RemoteID      string `json:"remote_id"`
	LocalMailbox  string `json:"local_mailbox"`
	FileKey       string `json:"file_key"`
}

type IMAPFolder struct {
	UIDValidity uint32 `json:"uid_validity"`
}

func New() *State {
	return &State{Version: Version, Entries: map[string]Entry{}, IMAP: map[string]IMAPFolder{}}
}

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
	if s.Entries == nil {
		s.Entries = map[string]Entry{}
	}
	if s.IMAP == nil {
		s.IMAP = map[string]IMAPFolder{}
	}
	return &s, nil
}

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

func Key(protocol, mailbox, id string) string {
	return protocol + "\n" + mailbox + "\n" + id
}
