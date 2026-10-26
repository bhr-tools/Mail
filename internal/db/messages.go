package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Address is a mailbox of a header (From, To…).
type Address struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

// String formats the address for display: the name, or the address.
func (a Address) String() string {
	if a.Name != "" {
		return a.Name
	}
	return a.Address
}

// Message is the header of one copy of a message.
type Message struct {
	ID          int64
	FolderID    int64
	Account     string
	Role        string
	UID         uint32
	MessageID   string
	Thread      string
	InReplyTo   string
	Refs        []string
	Subject     string
	From        Address
	To          []Address
	Cc          []Address
	ReplyTo     []Address
	Date        time.Time
	Seen        bool
	Flagged     bool
	Answered    bool
	Attachments bool
	Size        int64
	Snippet     string
	HasBody     bool
}

// NewMessage is a message fetched from the server, before it is stored.
type NewMessage struct {
	UID       uint32
	MessageID string
	InReplyTo string
	Refs      []string
	Subject   string
	From      Address
	To        []Address
	Cc        []Address
	ReplyTo   []Address
	Date      time.Time
	Seen      bool
	Flagged   bool
	Answered  bool
	// Attachments is a guess from the structure, refined when the body is
	// downloaded.
	Attachments bool
	Size        int64
}

// Flags are the IMAP flags the app follows.
type Flags struct {
	Seen, Flagged, Answered bool
}

const messageColumns = `m.id, m.folder_id, f.account, f.role, m.uid, m.message_id, m.thread, m.in_reply_to, m.refs,
	m.subject, m.from_name, m.from_addr, m.to_list, m.cc_list, m.reply_to, m.date, m.seen, m.flagged, m.answered,
	m.attachments, m.size, m.snippet, EXISTS (SELECT 1 FROM bodies b WHERE b.message_id = m.id)`

func scanMessage(row interface{ Scan(...any) error }) (*Message, error) {
	var m Message
	var refs, to, cc, replyTo string
	var date int64
	if err := row.Scan(&m.ID, &m.FolderID, &m.Account, &m.Role, &m.UID, &m.MessageID, &m.Thread, &m.InReplyTo, &refs,
		&m.Subject, &m.From.Name, &m.From.Address, &to, &cc, &replyTo, &date, &m.Seen, &m.Flagged, &m.Answered,
		&m.Attachments, &m.Size, &m.Snippet, &m.HasBody); err != nil {
		return nil, err
	}
	m.Refs = strings.Fields(refs)
	m.Date = time.Unix(date, 0)
	_ = json.Unmarshal([]byte(to), &m.To)
	_ = json.Unmarshal([]byte(cc), &m.Cc)
	_ = json.Unmarshal([]byte(replyTo), &m.ReplyTo)
	return &m, nil
}

func scanMessages(rows *sql.Rows, err error) ([]*Message, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

func addressesJSON(list []Address) string {
	if list == nil {
		list = []Address{}
	}
	data, _ := json.Marshal(list)
	return string(data)
}

// people is the text indexed for the addresses of a message.
func people(m *NewMessage) string {
	var b strings.Builder
	for _, a := range append(append([]Address{m.From}, m.To...), m.Cc...) {
		b.WriteString(a.Name)
		b.WriteByte(' ')
		b.WriteString(a.Address)
		b.WriteByte(' ')
	}
	return b.String()
}

// Message returns one message.
func (s *Store) Message(id int64) (*Message, error) {
	m, err := scanMessage(s.db.QueryRow(`SELECT `+messageColumns+` FROM messages m JOIN folders f ON f.id = m.folder_id WHERE m.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// MessageByMessageID returns a copy of a message of an account.
func (s *Store) MessageByMessageID(account, messageID string) (*Message, error) {
	m, err := scanMessage(s.db.QueryRow(`SELECT `+messageColumns+` FROM messages m JOIN folders f ON f.id = m.folder_id
		WHERE f.account = ? AND m.message_id = ? AND m.hidden = 0 ORDER BY f.role = 'inbox' DESC LIMIT 1`, account, messageID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// MessagesByID returns the given messages, in no particular order.
func (s *Store) MessagesByID(ids []int64) ([]*Message, error) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return scanMessages(s.db.Query(`SELECT `+messageColumns+` FROM messages m JOIN folders f ON f.id = m.folder_id
		WHERE m.id IN (`+placeholders(len(ids))+`)`, args...))
}

// KnownFlags returns the UIDs stored for a folder, with their flags.
func (s *Store) KnownFlags(folderID int64) (map[uint32]Flags, error) {
	rows, err := s.db.Query(`SELECT uid, seen, flagged, answered FROM messages WHERE folder_id = ?`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	known := map[uint32]Flags{}
	for rows.Next() {
		var uid uint32
		var f Flags
		if err := rows.Scan(&uid, &f.Seen, &f.Flagged, &f.Answered); err != nil {
			return nil, err
		}
		known[uid] = f
	}
	return known, rows.Err()
}

// threadOf finds the conversation of a new message: the one of a message it
// replies to, if known, else the first message it refers to.
func threadOf(q querier, account string, m *NewMessage) (string, error) {
	parents := append([]string{}, m.Refs...)
	if m.InReplyTo != "" {
		parents = append(parents, m.InReplyTo)
	}
	if len(parents) > 0 {
		args := []any{account}
		for _, p := range parents {
			args = append(args, p)
		}
		var thread string
		err := q.QueryRow(`SELECT m.thread FROM messages m JOIN folders f ON f.id = m.folder_id
			WHERE f.account = ? AND m.message_id IN (`+placeholders(len(parents))+`) LIMIT 1`, args...).Scan(&thread)
		if err == nil {
			return thread, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		return parents[0], nil
	}
	// Replies that arrived before this message adopted it as their root.
	return m.MessageID, nil
}

// InsertMessages stores new messages of a folder (existing UIDs are skipped).
func (s *Store) InsertMessages(folder *Folder, list []*NewMessage) error {
	if len(list) == 0 {
		return nil
	}
	return s.tx(func(tx *sql.Tx) error {
		for _, m := range list {
			if m.MessageID == "" {
				// Without a Message-ID, the copy is its own conversation.
				m.MessageID = syntheticID(folder, m.UID)
			}
			thread, err := threadOf(tx, folder.Account, m)
			if err != nil {
				return err
			}
			res, err := tx.Exec(`INSERT OR IGNORE INTO messages (folder_id, uid, message_id, thread, in_reply_to, refs, subject,
				from_name, from_addr, to_list, cc_list, reply_to, date, seen, flagged, answered, attachments, size)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				folder.ID, m.UID, m.MessageID, thread, m.InReplyTo, strings.Join(m.Refs, " "), m.Subject,
				m.From.Name, m.From.Address, addressesJSON(m.To), addressesJSON(m.Cc), addressesJSON(m.ReplyTo),
				m.Date.Unix(), m.Seen, m.Flagged, m.Answered, m.Attachments, m.Size)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
			id, _ := res.LastInsertId()
			// Replies stored before this message took it as the start of
			// their conversation: they join the conversation it belongs to.
			if thread != m.MessageID {
				if _, err := tx.Exec(`UPDATE messages SET thread = ? WHERE thread = ?
					AND folder_id IN (SELECT id FROM folders WHERE account = ?)`, thread, m.MessageID, folder.Account); err != nil {
					return err
				}
			}
			// A copy of the same message elsewhere may already have its body.
			if _, err := tx.Exec(`UPDATE messages SET snippet = (SELECT o.snippet FROM messages o JOIN folders f ON f.id = o.folder_id
				WHERE f.account = ? AND o.message_id = ? AND o.snippet != '' LIMIT 1) WHERE id = ? AND EXISTS
				(SELECT 1 FROM messages o JOIN folders f ON f.id = o.folder_id WHERE f.account = ? AND o.message_id = ? AND o.snippet != '')`,
				folder.Account, m.MessageID, id, folder.Account, m.MessageID); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO search (rowid, subject, people, body) VALUES (?, ?, ?, '')`, id, m.Subject, people(m)); err != nil {
				return err
			}
			if m.From.Address != "" {
				if err := noteContact(tx, m.From, false, m.Date); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func syntheticID(folder *Folder, uid uint32) string {
	return fmt.Sprintf("local.%s.%s.%d.%d", folder.Account, folder.Role, folder.UIDValidity, uid)
}

// UpdateFlags stores the flags the server reports for some UIDs.
func (s *Store) UpdateFlags(folderID int64, flags map[uint32]Flags) error {
	if len(flags) == 0 {
		return nil
	}
	return s.tx(func(tx *sql.Tx) error {
		for uid, f := range flags {
			if _, err := tx.Exec(`UPDATE messages SET seen = ?, flagged = ?, answered = ? WHERE folder_id = ? AND uid = ?`,
				f.Seen, f.Flagged, f.Answered, folderID, uid); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteUIDs forgets messages that left a folder on the server.
func (s *Store) DeleteUIDs(folderID int64, uids []uint32) error {
	if len(uids) == 0 {
		return nil
	}
	return s.tx(func(tx *sql.Tx) error {
		for _, uid := range uids {
			if _, err := tx.Exec(`DELETE FROM messages WHERE folder_id = ? AND uid = ?`, folderID, uid); err != nil {
				return err
			}
		}
		return nil
	})
}

// Body returns the whole message, or ErrNotFound if not downloaded yet. A
// copy of the same message in another folder of the account will do.
func (s *Store) Body(m *Message) ([]byte, error) {
	var raw []byte
	err := s.db.QueryRow(`SELECT b.raw FROM bodies b JOIN messages o ON o.id = b.message_id JOIN folders f ON f.id = o.folder_id
		WHERE o.id = ? OR (f.account = ? AND o.message_id = ?) LIMIT 1`, m.ID, m.Account, m.MessageID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return raw, err
}

// BodyInfo is what is learnt by reading a message's body.
type BodyInfo struct {
	Snippet     string
	Text        string // for the search index
	Attachments bool
}

// SetBody stores a downloaded message.
func (s *Store) SetBody(id int64, raw []byte, info BodyInfo) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO bodies (message_id, raw) VALUES (?, ?)`, id, raw); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE messages SET snippet = ?, attachments = ? WHERE id = ?`, info.Snippet, info.Attachments, id); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE search SET body = ? WHERE rowid = ?`, info.Text, id)
		return err
	})
}

// MissingBodies returns the most recent messages of a folder whose body was
// not downloaded yet, smaller than maxSize bytes.
func (s *Store) MissingBodies(folderID int64, limit int, maxSize int64) ([]*Message, error) {
	return scanMessages(s.db.Query(`SELECT `+messageColumns+` FROM messages m JOIN folders f ON f.id = m.folder_id
		WHERE m.folder_id = ? AND m.hidden = 0 AND m.size <= ?
		AND NOT EXISTS (SELECT 1 FROM bodies b JOIN messages o ON o.id = b.message_id WHERE o.message_id = m.message_id)
		ORDER BY m.date DESC LIMIT ?`, folderID, maxSize, limit))
}

// SetLocalFlag changes a flag on every copy of messages, before the server
// does: the app shows the change at once.
func (s *Store) SetLocalFlag(account string, messageIDs []string, column string, value bool) error {
	if column != "seen" && column != "flagged" && column != "answered" {
		return invalid("unknown flag %q", column)
	}
	args := []any{value, account}
	for _, id := range messageIDs {
		args = append(args, id)
	}
	_, err := s.db.Exec(`UPDATE messages SET `+column+` = ? WHERE folder_id IN (SELECT id FROM folders WHERE account = ?)
		AND message_id IN (`+placeholders(len(messageIDs))+`)`, args...)
	return err
}

// HideMessages hides every copy of messages outside the folder they move to.
func (s *Store) HideMessages(account string, messageIDs []string, keepRole string, hidden bool) error {
	args := []any{hidden, account, keepRole}
	for _, id := range messageIDs {
		args = append(args, id)
	}
	_, err := s.db.Exec(`UPDATE messages SET hidden = ? WHERE folder_id IN (SELECT id FROM folders WHERE account = ? AND role != ?)
		AND message_id IN (`+placeholders(len(messageIDs))+`)`, args...)
	return err
}

// DeleteHidden forgets the copies hidden by a move the server has done: the
// next synchronization brings back what is still there.
func (s *Store) DeleteHidden(account string, messageIDs []string) error {
	args := []any{account}
	for _, id := range messageIDs {
		args = append(args, id)
	}
	_, err := s.db.Exec(`DELETE FROM messages WHERE hidden = 1 AND folder_id IN (SELECT id FROM folders WHERE account = ?)
		AND message_id IN (`+placeholders(len(messageIDs))+`)`, args...)
	return err
}

// UIDsOf returns the UIDs of messages in a folder, hidden or not.
func (s *Store) UIDsOf(folderID int64, messageIDs []string) ([]uint32, error) {
	args := []any{folderID}
	for _, id := range messageIDs {
		args = append(args, id)
	}
	rows, err := s.db.Query(`SELECT uid FROM messages WHERE folder_id = ? AND message_id IN (`+placeholders(len(messageIDs))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var uids []uint32
	for rows.Next() {
		var uid uint32
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		uids = append(uids, uid)
	}
	return uids, rows.Err()
}
