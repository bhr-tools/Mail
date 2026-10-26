package db

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SessionLifetime is how long a login stays valid.
const SessionLifetime = 30 * 24 * time.Hour

// MinPasswordLength is the shortest accepted master password.
const MinPasswordLength = 4

const (
	pbkdf2Iterations = 600_000
	saltLength       = 16
	keyLength        = 32
)

// Stored password format (standard PBKDF2-HMAC-SHA256):
//
//	v2$<iterations>$<salt hex>$<key hex>
//
// A hash with fewer iterations than today's is replaced on the next login.

func hashPassword(password string) (string, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, keyLength)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("v2$%d$%s$%s", pbkdf2Iterations, hex.EncodeToString(salt), hex.EncodeToString(key)), nil
}

// checkPassword reports whether password matches stored, and whether stored
// uses an outdated format that should be rehashed.
func checkPassword(password, stored string) (ok, outdated bool) {
	parts := strings.Split(stored, "$")
	if len(parts) == 4 && parts[0] == "v2" {
		iterations, err := strconv.Atoi(parts[1])
		salt, errSalt := hex.DecodeString(parts[2])
		want, errKey := hex.DecodeString(parts[3])
		if err != nil || errSalt != nil || errKey != nil || iterations <= 0 {
			return false, false
		}
		got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
		if err != nil {
			return false, false
		}
		return subtle.ConstantTimeCompare(got, want) == 1, iterations < pbkdf2Iterations
	}
	return false, false
}

func validatePassword(password string) error {
	if strings.TrimSpace(password) == "" || len([]rune(password)) < MinPasswordLength {
		return invalid("The password must be at least %d characters long", MinPasswordLength)
	}
	return nil
}

// HasPassword reports whether a master password has been set.
func (s *Store) HasPassword() (bool, error) {
	return exists(s.db, `SELECT 1 FROM auth_settings WHERE key = 'password_hash'`)
}

// SetPassword sets the master password and signs out every session.
func (s *Store) SetPassword(password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	hashed, err := hashPassword(password)
	if err != nil {
		return err
	}
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO auth_settings (key, value) VALUES ('password_hash', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, hashed); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM auth_sessions`)
		return err
	})
}

// SetInitialPassword sets the master password only if none exists yet, so two
// concurrent first-run setups cannot overwrite each other.
func (s *Store) SetInitialPassword(password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	hashed, err := hashPassword(password)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO auth_settings (key, value) VALUES ('password_hash', ?)`, hashed)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return invalid("A password is already configured")
	}
	return nil
}

// VerifyPassword checks the master password, upgrading an outdated hash.
func (s *Store) VerifyPassword(password string) (bool, error) {
	var stored string
	err := s.db.QueryRow(`SELECT value FROM auth_settings WHERE key = 'password_hash'`).Scan(&stored)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ok, outdated := checkPassword(password, stored)
	if ok && outdated {
		if hashed, err := hashPassword(password); err == nil {
			_, _ = s.db.Exec(`UPDATE auth_settings SET value = ? WHERE key = 'password_hash'`, hashed)
		}
	}
	return ok, nil
}

// Session tokens are random; only their SHA-256 is stored, so a copy of the
// database does not hand out valid sessions.
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateSession starts a session and returns its token.
func (s *Store) CreateSession() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	if _, err := s.db.Exec(`DELETE FROM auth_sessions WHERE created_at <= ?`, sessionCutoff()); err != nil {
		return "", err
	}
	if _, err := s.db.Exec(`INSERT INTO auth_sessions (token, created_at) VALUES (?, ?)`,
		tokenHash(token), time.Now().UTC().Format(time.DateTime)); err != nil {
		return "", err
	}
	return token, nil
}

// ValidateSession reports whether token belongs to an unexpired session.
func (s *Store) ValidateSession(token string) bool {
	if token == "" {
		return false
	}
	ok, err := exists(s.db, `SELECT 1 FROM auth_sessions WHERE token = ? AND created_at > ?`, tokenHash(token), sessionCutoff())
	return err == nil && ok
}

// DeleteSession ends a session.
func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM auth_sessions WHERE token = ?`, tokenHash(token))
	return err
}

// Sessions are stored as UTC "YYYY-MM-DD HH:MM:SS" text, which compares in
// chronological order.
func sessionCutoff() string {
	return time.Now().UTC().Add(-SessionLifetime).Format(time.DateTime)
}

// PasswordStamp returns an opaque value that changes whenever the master
// password does ("" when none is set), so callers can cache a successful
// check until then.
func (s *Store) PasswordStamp() (string, error) {
	var stored string
	err := s.db.QueryRow(`SELECT value FROM auth_settings WHERE key = 'password_hash'`).Scan(&stored)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return tokenHash(stored), err
}
