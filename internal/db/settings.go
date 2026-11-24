package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/bhr-tools/mail/internal/addy"
)

// Addy is the addy.io setting: the domain of the aliases, and the accounts
// whose address is a verified addy.io mailbox, which can send from an alias.
type Addy struct {
	Domain   string   `json:"domain"`
	Accounts []string `json:"accounts"`
}

// Enabled reports whether account can send through addy.io.
func (a Addy) Enabled(account string) bool {
	if a.Domain == "" {
		return false
	}
	for _, id := range a.Accounts {
		if id == account {
			return true
		}
	}
	return false
}

func (s *Store) setting(key string, dst any) error {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(value), dst)
}

func (s *Store) setSetting(key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, string(data))
	return err
}

// AddySettings returns the addy.io setting (empty when never set).
func (s *Store) AddySettings() (Addy, error) {
	a := Addy{Accounts: []string{}}
	err := s.setting("addy", &a)
	if a.Accounts == nil {
		a.Accounts = []string{}
	}
	return a, err
}

// SetAddySettings records the addy.io setting. The domain is given without
// "@"; an empty domain turns addy.io off.
func (s *Store) SetAddySettings(a Addy) error {
	a.Domain = strings.ToLower(strings.Trim(strings.TrimSpace(a.Domain), "@."))
	if a.Domain != "" && !addy.ValidDomain(a.Domain) {
		return invalid("%q is not a domain name", a.Domain)
	}
	if a.Accounts == nil {
		a.Accounts = []string{}
	}
	return s.setSetting("addy", a)
}
