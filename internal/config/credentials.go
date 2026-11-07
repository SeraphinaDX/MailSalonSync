package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/zalando/go-keyring"
)

const credentialService = "MailSalonSync"

// Credential identifies a secret by server, username and auth mode.
// Changing a connection cannot silently reuse a different server's credential.
type Credential struct {
	Name     string
	Protocol string
	Endpoint string
	Username string
	Auth     string
}

func (c Credential) key() string {
	// Display names and collection paths do not identify a login.
	c.Name = ""
	if c.Protocol == "carddav" || c.Protocol == "caldav" {
		c.Protocol = "dav"
		if u, err := url.Parse(c.Endpoint); err == nil {
			c.Endpoint = strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
		}
	}
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SameLogin reports whether two targets share a keyring entry.
func (c Credential) SameLogin(other Credential) bool { return c.key() == other.key() }

func (c Credential) Store(secret string) error {
	if secret == "" {
		return fmt.Errorf("password or token must not be empty")
	}
	if err := keyring.Set(credentialService, c.key(), secret); err != nil {
		return fmt.Errorf("store credential for %q in OS keyring: %w", c.Name, err)
	}
	return nil
}

func (c Credential) Delete() error {
	if err := keyring.Delete(credentialService, c.key()); err != nil {
		return fmt.Errorf("delete credential for %q from OS keyring: %w", c.Name, err)
	}
	return nil
}

func (c Credential) secret(value, env, command string) (string, error) {
	if value != "" || env != "" || command != "" {
		return resolveSecret(value, env, command)
	}
	secret, err := keyring.Get(credentialService, c.key())
	if err != nil {
		return "", fmt.Errorf("read credential for %q from OS keyring (register it with MailSalonSync password): %w", c.Name, err)
	}
	return secret, nil
}

func (a *Account) Credential() Credential {
	if a.Protocol == "imap" || a.IMAP != nil {
		return Credential{a.Name, "imap", a.IMAP.Address, a.IMAP.Username, "password"}
	}
	return jmapCredential(a.Name, a.JMAP)
}

func jmapCredential(name string, j *JMAP) Credential {
	auth := j.Auth
	if auth == "" {
		auth = "basic"
	}
	return Credential{name, "jmap", j.SessionURL, j.Username, auth}
}

func (c *Collection) Credential() Credential {
	if c.DAV != nil {
		return Credential{c.Name, c.Protocol, c.Remote, c.DAV.Username, "password"}
	}
	return jmapCredential(c.Name, c.JMAP)
}

// ExternalSecret reports whether configuration overrides the OS keyring.
func (a *Account) ExternalSecret() bool {
	if a.IMAP != nil {
		return a.IMAP.Password != "" || a.IMAP.PasswordEnv != "" || a.IMAP.PasswordCommand != ""
	}
	return jmapExternal(a.JMAP)
}
func jmapExternal(j *JMAP) bool {
	if j.Auth == "bearer" {
		return j.BearerToken != "" || j.BearerTokenEnv != "" || j.BearerTokenCommand != ""
	}
	return j.Password != "" || j.PasswordEnv != "" || j.PasswordCommand != ""
}
func (c *Collection) ExternalSecret() bool {
	if c.DAV != nil {
		return c.DAV.Password != "" || c.DAV.PasswordEnv != "" || c.DAV.PasswordCommand != ""
	}
	return jmapExternal(c.JMAP)
}
