// Package db keeps a local copy of the mail folders (headers, flags, and
// the messages already opened), the queue of actions waiting for the IMAP
// servers, the address book and authentication data in a single SQLite file.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when the requested row does not exist.
var ErrNotFound = errors.New("not found")

// ValidationError reports rejected input. Its message is meant for the user.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// Store is safe for concurrent use.
type Store struct {
	db *sql.DB

	// version counts changes, so the web app can wait for the next one.
	mu      sync.Mutex
	version uint64
	wake    chan struct{}
}

// querier is implemented by both *sql.DB and *sql.Tx.
type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Open opens (creating it if needed) the database at path and migrates it.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// A single connection serializes every statement. For one person's mail
	// this costs nothing and rules out "database is locked" errors.
	sqlDB.SetMaxOpenConns(1)

	s := &Store{db: sqlDB, version: 1, wake: make(chan struct{})}
	if err := s.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Ping checks that the database answers.
func (s *Store) Ping() error {
	return s.db.Ping()
}

// Changed records that the mail shown by the web app changed.
func (s *Store) Changed() {
	s.mu.Lock()
	s.version++
	close(s.wake)
	s.wake = make(chan struct{})
	s.mu.Unlock()
}

// Version returns the current change counter.
func (s *Store) Version() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

// WaitChange returns the change counter once it differs from since, or when
// ctx ends.
func (s *Store) WaitChange(ctx context.Context, since uint64) uint64 {
	for {
		s.mu.Lock()
		v, wake := s.version, s.wake
		s.mu.Unlock()
		if v != since {
			return v
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return v
		}
	}
}

// tx runs fn inside a transaction, committed only if fn returns nil.
func (s *Store) tx(fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// migrations are applied in order; PRAGMA user_version records how many ran.
// Never edit a released migration: append a new one instead.
var migrations = []string{
	// 1: base schema.
	`CREATE TABLE folders (
		id INTEGER PRIMARY KEY,
		account TEXT NOT NULL,
		role TEXT NOT NULL,
		name TEXT NOT NULL,
		uidvalidity INTEGER NOT NULL DEFAULT 0,
		-- Highest CONDSTORE mod-sequence seen (0: unknown or unsupported).
		modseq INTEGER NOT NULL DEFAULT 0,
		synced_at INTEGER,
		UNIQUE (account, role)
	);

	-- One row per copy of a message on the server: Gmail shows the same
	-- message in INBOX and All Mail, with the same message_id.
	CREATE TABLE messages (
		id INTEGER PRIMARY KEY,
		folder_id INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
		uid INTEGER NOT NULL,
		message_id TEXT NOT NULL,
		-- Message-ID of the first message of the conversation.
		thread TEXT NOT NULL,
		in_reply_to TEXT NOT NULL DEFAULT '',
		refs TEXT NOT NULL DEFAULT '',
		subject TEXT NOT NULL DEFAULT '',
		from_name TEXT NOT NULL DEFAULT '',
		from_addr TEXT NOT NULL DEFAULT '',
		to_list TEXT NOT NULL DEFAULT '[]',
		cc_list TEXT NOT NULL DEFAULT '[]',
		reply_to TEXT NOT NULL DEFAULT '[]',
		date INTEGER NOT NULL,
		seen INTEGER NOT NULL DEFAULT 0,
		flagged INTEGER NOT NULL DEFAULT 0,
		answered INTEGER NOT NULL DEFAULT 0,
		attachments INTEGER NOT NULL DEFAULT 0,
		size INTEGER NOT NULL DEFAULT 0,
		snippet TEXT NOT NULL DEFAULT '',
		-- Moved away here, waiting for the server to do the same.
		hidden INTEGER NOT NULL DEFAULT 0,
		UNIQUE (folder_id, uid)
	);
	CREATE INDEX idx_messages_date ON messages(folder_id, date);
	CREATE INDEX idx_messages_mid ON messages(message_id);
	CREATE INDEX idx_messages_thread ON messages(thread);

	-- The whole message (RFC 5322), downloaded when first opened.
	CREATE TABLE bodies (
		message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
		raw BLOB NOT NULL
	);

	-- Full-text index; rowid is messages.id.
	CREATE VIRTUAL TABLE search USING fts5(subject, people, body, tokenize = 'unicode61 remove_diacritics 2');
	CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
		DELETE FROM search WHERE rowid = old.id;
	END;

	-- Actions done in the app, replayed on the IMAP server in order.
	CREATE TABLE ops (
		id INTEGER PRIMARY KEY,
		account TEXT NOT NULL,
		data TEXT NOT NULL,
		started INTEGER NOT NULL DEFAULT 0,
		-- Kept a while once applied, so that it can still be undone.
		done INTEGER NOT NULL DEFAULT 0,
		attempts INTEGER NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL
	);

	-- Addresses seen or written to, for completion when composing.
	CREATE TABLE contacts (
		address TEXT PRIMARY KEY COLLATE NOCASE,
		name TEXT NOT NULL DEFAULT '',
		sent INTEGER NOT NULL DEFAULT 0,
		received INTEGER NOT NULL DEFAULT 0,
		last INTEGER NOT NULL DEFAULT 0
	);

	-- Senders whose remote images are always shown.
	CREATE TABLE image_senders (
		address TEXT PRIMARY KEY COLLATE NOCASE
	);

	CREATE TABLE auth_settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	CREATE TABLE auth_sessions (
		token TEXT PRIMARY KEY,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`,
}

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		err := s.tx(func(tx *sql.Tx) error {
			if _, err := tx.Exec(migrations[i]); err != nil {
				return err
			}
			_, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1))
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	return nil
}

func exists(q querier, query string, args ...any) (bool, error) {
	var one int
	err := q.QueryRow(query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// placeholders returns "?, ?, ?" for n values.
func placeholders(n int) string {
	if n == 0 {
		return "NULL"
	}
	b := make([]byte, 0, n*3)
	for i := range n {
		if i > 0 {
			b = append(b, ", "...)
		}
		b = append(b, '?')
	}
	return string(b)
}
