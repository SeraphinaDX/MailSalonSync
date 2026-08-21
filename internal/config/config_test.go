package config

import "testing"

func TestValidateJMAPRequiresHTTPS(t *testing.T) {
	cfg := &Config{Accounts: []Account{{
		Name: "a", Protocol: "jmap", LocalRoot: "/tmp/mail",
		Mailboxes: []Mailbox{{Remote: "role:inbox", Local: "INBOX"}},
		JMAP:      &JMAP{SessionURL: "http://mail.example/.well-known/jmap", Auth: "bearer", BearerToken: "x"},
	}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected non-HTTPS JMAP URL to be rejected")
	}
}

func TestValidateJMAPBasicRequiresUsername(t *testing.T) {
	cfg := &Config{Accounts: []Account{{
		Name: "a", Protocol: "jmap", LocalRoot: "/tmp/mail",
		Mailboxes: []Mailbox{{Remote: "role:inbox", Local: "INBOX"}},
		JMAP:      &JMAP{SessionURL: "https://mail.example/.well-known/jmap", Auth: "basic", Password: "x"},
	}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing basic-auth username to be rejected")
	}
}
