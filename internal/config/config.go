package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	StateDir string    `toml:"state_dir"`
	Accounts []Account `toml:"accounts"`
}

type Account struct {
	Name             string    `toml:"name"`
	Protocol         string    `toml:"protocol"`
	LocalRoot        string    `toml:"local_root"`
	PropagateDeletes bool      `toml:"propagate_deletes"`
	Mailboxes        []Mailbox `toml:"mailboxes"`
	IMAP             *IMAP     `toml:"imap,omitempty"`
	JMAP             *JMAP     `toml:"jmap,omitempty"`
}

type Mailbox struct {
	Remote string `toml:"remote"`
	Local  string `toml:"local"`
}

type IMAP struct {
	Address                    string `toml:"address"`
	Username                   string `toml:"username"`
	Password                   string `toml:"password,omitempty"`
	PasswordEnv                string `toml:"password_env,omitempty"`
	PasswordCommand            string `toml:"password_command,omitempty"`
	Security                   string `toml:"security,omitempty"`
	AllowExpungeWithoutUIDPlus bool   `toml:"allow_expunge_without_uidplus,omitempty"`
}

type JMAP struct {
	SessionURL         string `toml:"session_url"`
	Auth               string `toml:"auth,omitempty"`
	Username           string `toml:"username,omitempty"`
	Password           string `toml:"password,omitempty"`
	PasswordEnv        string `toml:"password_env,omitempty"`
	PasswordCommand    string `toml:"password_command,omitempty"`
	BearerToken        string `toml:"bearer_token,omitempty"`
	BearerTokenEnv     string `toml:"bearer_token_env,omitempty"`
	BearerTokenCommand string `toml:"bearer_token_command,omitempty"`
	AccountID          string `toml:"account_id,omitempty"`
	IdentityID         string `toml:"identity_id,omitempty"`
	DraftsMailbox      string `toml:"drafts_mailbox,omitempty"`
	SentMailbox        string `toml:"sent_mailbox,omitempty"`
}

func Load(path string) (*Config, error) {
	path = ExpandPath(path)
	var cfg Config
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) != 0 {
		return nil, fmt.Errorf("parse config: unknown fields: %v", undecoded)
	}
	if cfg.StateDir == "" {
		if x := os.Getenv("XDG_STATE_HOME"); x != "" {
			cfg.StateDir = filepath.Join(x, "MailSalonSync")
		} else {
			cfg.StateDir = "~/.local/state/MailSalonSync"
		}
	}
	cfg.StateDir = ExpandPath(cfg.StateDir)
	for i := range cfg.Accounts {
		cfg.Accounts[i].LocalRoot = ExpandPath(cfg.Accounts[i].LocalRoot)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) Validate() error {
	if len(c.Accounts) == 0 {
		return errors.New("config contains no accounts")
	}
	seen := map[string]bool{}
	for i := range c.Accounts {
		a := &c.Accounts[i]
		if a.Name == "" {
			return fmt.Errorf("account %d has no name", i+1)
		}
		if seen[a.Name] {
			return fmt.Errorf("duplicate account name %q", a.Name)
		}
		seen[a.Name] = true
		a.Protocol = strings.ToLower(a.Protocol)
		if a.Protocol != "imap" && a.Protocol != "jmap" {
			return fmt.Errorf("account %q: protocol must be imap or jmap", a.Name)
		}
		if a.LocalRoot == "" {
			return fmt.Errorf("account %q: local_root is required", a.Name)
		}
		if len(a.Mailboxes) == 0 {
			return fmt.Errorf("account %q: at least one mailbox mapping is required", a.Name)
		}
		locals := map[string]bool{}
		for _, m := range a.Mailboxes {
			if m.Remote == "" || m.Local == "" {
				return fmt.Errorf("account %q: mailbox remote and local are required", a.Name)
			}
			if locals[m.Local] {
				return fmt.Errorf("account %q: local mailbox %q is mapped more than once", a.Name, m.Local)
			}
			locals[m.Local] = true
		}
		if a.Protocol == "imap" {
			if a.IMAP == nil {
				return fmt.Errorf("account %q: imap settings are required", a.Name)
			}
			if a.IMAP.Address == "" || a.IMAP.Username == "" {
				return fmt.Errorf("account %q: imap address and username are required", a.Name)
			}
			if a.IMAP.Security == "" {
				a.IMAP.Security = "tls"
			}
			switch a.IMAP.Security {
			case "tls", "starttls", "plain":
			default:
				return fmt.Errorf("account %q: imap security must be tls, starttls, or plain", a.Name)
			}
		} else {
			if a.JMAP == nil || a.JMAP.SessionURL == "" {
				return fmt.Errorf("account %q: jmap.session_url is required", a.Name)
			}
			u, err := url.Parse(a.JMAP.SessionURL)
			if err != nil || !u.IsAbs() || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
				return fmt.Errorf("account %q: jmap.session_url must be an absolute https URL", a.Name)
			}
			if a.JMAP.Auth == "" {
				a.JMAP.Auth = "basic"
			}
			if a.JMAP.Auth != "basic" && a.JMAP.Auth != "bearer" {
				return fmt.Errorf("account %q: jmap auth must be basic or bearer", a.Name)
			}
			if a.JMAP.Auth == "basic" && a.JMAP.Username == "" {
				return fmt.Errorf("account %q: jmap username is required for basic auth", a.Name)
			}
			if a.JMAP.DraftsMailbox == "" {
				a.JMAP.DraftsMailbox = "role:drafts"
			}
			if a.JMAP.SentMailbox == "" {
				a.JMAP.SentMailbox = "role:sent"
			}
		}
	}
	return nil
}

func (c *Config) Account(name string) (*Account, error) {
	for i := range c.Accounts {
		if c.Accounts[i].Name == name {
			return &c.Accounts[i], nil
		}
	}
	return nil, fmt.Errorf("no account named %q", name)
}

func (a *Account) IMAPPassword() (string, error) {
	if a.IMAP == nil {
		return "", errors.New("not an IMAP account")
	}
	return resolveSecret(a.IMAP.Password, a.IMAP.PasswordEnv, a.IMAP.PasswordCommand)
}

func (a *Account) JMAPSecret() (string, error) {
	if a.JMAP == nil {
		return "", errors.New("not a JMAP account")
	}
	if a.JMAP.Auth == "bearer" {
		return resolveSecret(a.JMAP.BearerToken, a.JMAP.BearerTokenEnv, a.JMAP.BearerTokenCommand)
	}
	return resolveSecret(a.JMAP.Password, a.JMAP.PasswordEnv, a.JMAP.PasswordCommand)
}

func resolveSecret(value, env, command string) (string, error) {
	if value != "" {
		return value, nil
	}
	if env != "" {
		if v, ok := os.LookupEnv(env); ok {
			return v, nil
		}
		return "", fmt.Errorf("environment variable %s is not set", env)
	}
	if command != "" {
		out, err := exec.Command("sh", "-c", command).Output()
		if err != nil {
			return "", fmt.Errorf("secret command failed: %w", err)
		}
		return strings.TrimRight(string(out), "\r\n"), nil
	}
	return "", errors.New("no secret configured")
}

func ExpandPath(path string) string {
	path = os.ExpandEnv(path)
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}
