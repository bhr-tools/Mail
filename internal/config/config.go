// Package config reads the mail accounts: a JSON file written by the NixOS
// module, without secrets. Each password is read from its own file
// (passwordFile, typically a systemd credential decrypted by sops).
package config

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"os"
	"regexp"
	"strings"
)

// Security of a connection.
const (
	TLS      = "tls"      // implicit TLS (IMAP 993, SMTP 465)
	StartTLS = "starttls" // plain connection upgraded with STARTTLS (SMTP 587)
	Insecure = "none"     // plain text: tests and the demo only
)

// Server is an IMAP or SMTP endpoint.
type Server struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"`
}

// Address returns host:port.
func (s Server) Address() string {
	return fmt.Sprintf("%s:%d", s.Host, s.Port)
}

// Folder roles. Each account has at most one folder per role; the app only
// keeps a copy of these folders.
const (
	Inbox   = "inbox"
	Archive = "archive"
	Sent    = "sent"
	Drafts  = "drafts"
	Trash   = "trash"
)

// Roles lists the folder roles in the order of the sidebar.
var Roles = []string{Inbox, Archive, Sent, Drafts, Trash}

// Account is one mailbox.
type Account struct {
	// ID is a short stable name ("perso"), used in URLs and the database.
	ID       string `json:"id"`
	Name     string `json:"name"`
	Address  string `json:"address"`
	RealName string `json:"real_name"`
	Username string `json:"username"`
	// PasswordFile holds the password (an app password for Gmail).
	PasswordFile string `json:"password_file"`
	// Password is set directly by the demo and tests only.
	Password string `json:"-"`
	IMAP     Server `json:"imap"`
	SMTP     Server `json:"smtp"`
	// Folders maps roles to folder names. Missing roles are found from the
	// server's special-use attributes (RFC 6154) or the usual names.
	Folders map[string]string `json:"folders"`
	// SaveSent appends sent mail to the Sent folder. Gmail does it on its
	// own: nil means "true unless the SMTP server is Gmail".
	SaveSent *bool `json:"save_sent"`
}

// Config is the content of the accounts file.
type Config struct {
	Accounts []*Account `json:"accounts"`
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Load reads and checks the accounts file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// Validate fills defaults and rejects incomplete accounts.
func (c *Config) Validate() error {
	seen := map[string]bool{}
	for i, a := range c.Accounts {
		if !idPattern.MatchString(a.ID) {
			return fmt.Errorf("account %d: id %q must be lowercase letters, digits or dashes", i+1, a.ID)
		}
		if seen[a.ID] {
			return fmt.Errorf("account %q is defined twice", a.ID)
		}
		seen[a.ID] = true
		if a.Name == "" {
			a.Name = a.ID
		}
		if !strings.Contains(a.Address, "@") {
			return fmt.Errorf("account %q: invalid address %q", a.ID, a.Address)
		}
		if a.Username == "" {
			a.Username = a.Address
		}
		if a.PasswordFile == "" && a.Password == "" {
			return fmt.Errorf("account %q: password_file is missing", a.ID)
		}
		for _, s := range []*Server{&a.IMAP, &a.SMTP} {
			if s.Security == "" {
				s.Security = TLS
			}
			if s.Security != TLS && s.Security != StartTLS && s.Security != Insecure {
				return fmt.Errorf("account %q: security must be tls, starttls or none", a.ID)
			}
		}
		if a.IMAP.Host == "" || a.SMTP.Host == "" {
			return fmt.Errorf("account %q: imap and smtp hosts are required", a.ID)
		}
		if a.IMAP.Port == 0 {
			a.IMAP.Port = 993
			if a.IMAP.Security != TLS {
				a.IMAP.Port = 143
			}
		}
		if a.SMTP.Port == 0 {
			a.SMTP.Port = 465
			if a.SMTP.Security != TLS {
				a.SMTP.Port = 587
			}
		}
		if a.Folders == nil {
			a.Folders = map[string]string{}
		}
		for role := range a.Folders {
			if !isRole(role) {
				return fmt.Errorf("account %q: unknown folder role %q", a.ID, role)
			}
		}
	}
	return nil
}

func isRole(role string) bool {
	for _, r := range Roles {
		if r == role {
			return true
		}
	}
	return false
}

// IsGmail reports whether the account is hosted by Google, whose folders are
// labels: "All Mail" holds every message, the inbox included.
func (a *Account) IsGmail() bool {
	h := strings.ToLower(a.IMAP.Host)
	return strings.HasSuffix(h, ".gmail.com") || strings.HasSuffix(h, ".googlemail.com")
}

// ShouldSaveSent reports whether sent mail must be appended to Sent.
func (a *Account) ShouldSaveSent() bool {
	if a.SaveSent != nil {
		return *a.SaveSent
	}
	return !a.IsGmail()
}

// ReadPassword returns the account's password.
func (a *Account) ReadPassword() (string, error) {
	if a.Password != "" {
		return a.Password, nil
	}
	data, err := os.ReadFile(a.PasswordFile)
	if err != nil {
		return "", fmt.Errorf("password of %s: %w", a.ID, err)
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

// From returns the From header of the account: "Real Name <address>".
func (a *Account) From() string {
	return (&mail.Address{Name: a.RealName, Address: a.Address}).String()
}
