// Package db keeps a local copy of the mail folders (headers, flags, and
// the messages already opened), the queue of actions waiting for the IMAP
// servers, the address book and authentication data in a single SQLite file.
package db

import (
	"context"
	"database/sql"
	"sync"

	"github.com/bhr-tools/kit/httpx"
	kitstore "github.com/bhr-tools/kit/store"
)

// ErrNotFound is returned when the requested row does not exist (the kit's,
// so httpx.Fail answers 404).
var ErrNotFound = httpx.ErrNotFound

// ValidationError reports rejected input. Its message is meant for the user.
type ValidationError = httpx.ValidationError

func invalid(format string, args ...any) error {
	return httpx.Invalid(format, args...)
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
	sqlDB, err := kitstore.Open(path, migrations)
	if err != nil {
		return nil, err
	}
	return &Store{db: sqlDB, version: 1, wake: make(chan struct{})}, nil
}

// DB is the database, shared with the kit's auth.
func (s *Store) DB() *sql.DB { return s.db }

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
func (s *Store) tx(fn func(tx *sql.Tx) error) error { return kitstore.Tx(s.db, fn) }

// migrations are applied in order by the kit (PRAGMA user_version records
// how many ran). The auth tables of the first one are the kit's.
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

	// The order of the accounts, as arranged in the sidebar.
	`CREATE TABLE account_order (
		account TEXT PRIMARY KEY,
		position INTEGER NOT NULL
	);`,

	// Settings changed in the app (JSON values).
	`CREATE TABLE settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`,
}

func exists(q querier, query string, args ...any) (bool, error) {
	return kitstore.Exists(q, query, args...)
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
