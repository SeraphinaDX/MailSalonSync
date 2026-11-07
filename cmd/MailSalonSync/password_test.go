package main

import (
	"testing"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
)

func TestPasswordTarget(t *testing.T) {
	cfg := &config.Config{
		Accounts:    []config.Account{{Name: "mail", Protocol: "imap", IMAP: &config.IMAP{Address: "imap.example:993", Username: "user", PasswordCommand: "pass show mail"}}},
		Collections: []config.Collection{{Name: "contacts", Protocol: "carddav", Remote: "https://example/contacts", DAV: &config.DAV{Username: "user"}}},
	}
	for _, names := range [][2]string{{"", ""}, {"mail", "contacts"}, {"missing", ""}, {"", "missing"}} {
		if _, _, err := passwordTarget(cfg, names[0], names[1]); err == nil {
			t.Fatalf("accepted selectors %v", names)
		}
	}
	target, external, err := passwordTarget(cfg, "mail", "")
	if err != nil || !external || target.Endpoint != "imap.example:993" {
		t.Fatalf("unexpected mail target: %+v, %v, %v", target, external, err)
	}
	target, external, err = passwordTarget(cfg, "", "contacts")
	if err != nil || external || target.Protocol != "carddav" {
		t.Fatalf("unexpected collection target: %+v, %v, %v", target, external, err)
	}
}

func TestPasswordGroupsSharedCollections(t *testing.T) {
	cfg := &config.Config{Collections: []config.Collection{
		{Name: "contacts", Protocol: "carddav", Remote: "https://example/contacts", DAV: &config.DAV{Username: "user"}},
		{Name: "calendar", Protocol: "caldav", Remote: "https://example/calendar", DAV: &config.DAV{Username: "user"}},
		{Name: "other", Protocol: "caldav", Remote: "https://example/other", DAV: &config.DAV{Username: "other"}},
		{Name: "external", Protocol: "caldav", Remote: "https://external/calendar", DAV: &config.DAV{Username: "user", PasswordEnv: "DAV_PASSWORD"}},
	}}
	groups := passwordGroups(cfg)
	if len(groups) != 2 || groups[0].Name != "contacts, calendar" || groups[1].Name != "other" {
		t.Fatalf("unexpected groups: %+v", groups)
	}
	if !groups[0].SameLogin(cfg.Collections[0].Credential()) {
		t.Fatal("group display name changed credential identity")
	}
}
