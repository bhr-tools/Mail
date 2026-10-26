package db

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Filter selects the messages of a list: a folder of one or every account,
// or the results of a search.
type Filter struct {
	Roles   []string // folders to look in
	Account string   // "" for every account
	// Match is an FTS5 query on the subject, the people and the text.
	Match       string
	From        string
	To          string
	Unread      bool
	Flagged     bool
	Attachments bool
	// NotInInbox leaves out the messages that are also in the inbox: Gmail's
	// All Mail is the archive, but holds the inbox too.
	NotInInbox bool
	// ExcludeFrom leaves out messages sent by these addresses (the copies of
	// sent mail in All Mail).
	ExcludeFrom []string
	Offset      int
	Limit       int
}

// Thread sums up a conversation for a list.
type Thread struct {
	Account     string
	Thread      string
	Latest      *Message // most recent message of the list
	People      []string
	Date        time.Time
	Count       int
	Unread      int
	Flagged     bool
	Attachments bool
}

func (f *Filter) where() (string, []any) {
	var conds []string
	var args []any
	if len(f.Roles) > 0 {
		conds = append(conds, `f.role IN (`+placeholders(len(f.Roles))+`)`)
		for _, r := range f.Roles {
			args = append(args, r)
		}
	}
	if f.Account != "" {
		conds = append(conds, `f.account = ?`)
		args = append(args, f.Account)
	}
	conds = append(conds, `m.hidden = 0`)
	if f.Match != "" {
		conds = append(conds, `m.id IN (SELECT rowid FROM search WHERE search MATCH ?)`)
		args = append(args, f.Match)
	}
	if f.From != "" {
		conds = append(conds, `(m.from_addr LIKE ? ESCAPE '\' OR m.from_name LIKE ? ESCAPE '\')`)
		args = append(args, like(f.From), like(f.From))
	}
	if f.To != "" {
		conds = append(conds, `(m.to_list LIKE ? ESCAPE '\' OR m.cc_list LIKE ? ESCAPE '\')`)
		args = append(args, like(f.To), like(f.To))
	}
	if f.Unread {
		conds = append(conds, `m.seen = 0`)
	}
	if f.Flagged {
		conds = append(conds, `m.flagged = 1`)
	}
	if f.Attachments {
		conds = append(conds, `m.attachments = 1`)
	}
	if f.NotInInbox {
		conds = append(conds, `NOT EXISTS (SELECT 1 FROM messages i JOIN folders fi ON fi.id = i.folder_id
			WHERE fi.account = f.account AND fi.role = 'inbox' AND i.hidden = 0 AND i.message_id = m.message_id)`)
	}
	if len(f.ExcludeFrom) > 0 {
		conds = append(conds, `m.from_addr COLLATE NOCASE NOT IN (`+placeholders(len(f.ExcludeFrom))+`)`)
		for _, a := range f.ExcludeFrom {
			args = append(args, a)
		}
	}
	return strings.Join(conds, " AND "), args
}

func like(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// Threads returns the conversations of a list, most recent first.
func (s *Store) Threads(f Filter) ([]*Thread, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	where, args := f.where()
	args = append(args, f.Limit, f.Offset)
	rows, err := s.db.Query(`WITH v AS (
			SELECT m.id, f.account, m.thread, m.message_id, m.date, m.seen, m.flagged, m.attachments,
				CASE WHEN m.from_name != '' THEN m.from_name ELSE m.from_addr END AS who
			FROM messages m JOIN folders f ON f.id = m.folder_id
			WHERE `+where+`
		), g AS (
			SELECT account, thread, MAX(date) AS d, COUNT(DISTINCT message_id) AS n,
				COUNT(DISTINCT CASE WHEN seen = 0 THEN message_id END) AS unread,
				MAX(flagged) AS flagged, MAX(attachments) AS attachments, json_group_array(DISTINCT who) AS people
			FROM v GROUP BY account, thread ORDER BY d DESC LIMIT ? OFFSET ?
		)
		SELECT g.account, g.thread, g.d, g.n, g.unread, g.flagged, g.attachments, g.people,
			(SELECT v.id FROM v WHERE v.account = g.account AND v.thread = g.thread ORDER BY v.date DESC, v.id DESC LIMIT 1)
		FROM g ORDER BY g.d DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*Thread
	var ids []int64
	for rows.Next() {
		var t Thread
		var date, latest int64
		var people string
		if err := rows.Scan(&t.Account, &t.Thread, &date, &t.Count, &t.Unread, &t.Flagged, &t.Attachments, &people, &latest); err != nil {
			return nil, err
		}
		t.Date = time.Unix(date, 0)
		_ = json.Unmarshal([]byte(people), &t.People)
		t.Latest = &Message{ID: latest}
		list = append(list, &t)
		ids = append(ids, latest)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	msgs, err := s.MessagesByID(ids)
	if err != nil {
		return nil, err
	}
	byID := map[int64]*Message{}
	for _, m := range msgs {
		byID[m.ID] = m
	}
	for _, t := range list {
		t.Latest = byID[t.Latest.ID]
	}
	return list, nil
}

// ThreadMessages returns the messages of a conversation, oldest first, one
// copy per message: the copy in the folder being viewed, else in the inbox.
// Messages in the trash only show from the trash.
func (s *Store) ThreadMessages(account, thread, role string) ([]*Message, error) {
	list, err := scanMessages(s.db.Query(`SELECT `+messageColumns+` FROM messages m JOIN folders f ON f.id = m.folder_id
		WHERE f.account = ? AND m.thread = ? AND m.hidden = 0 AND (f.role != 'trash' OR ? = 'trash')`, account, thread, role))
	if err != nil {
		return nil, err
	}
	rank := func(m *Message) int {
		switch m.Role {
		case role:
			return 0
		case "inbox":
			return 1
		default:
			return 2
		}
	}
	best := map[string]*Message{}
	for _, m := range list {
		if b, ok := best[m.MessageID]; !ok || rank(m) < rank(b) {
			best[m.MessageID] = m
		}
	}
	out := make([]*Message, 0, len(best))
	for _, m := range best {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Date.Equal(out[j].Date) {
			return out[i].Date.Before(out[j].Date)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ThreadIDs returns the message IDs of a conversation in some folders.
func (s *Store) ThreadIDs(account, thread string, roles []string) ([]string, error) {
	args := []any{account, thread}
	for _, r := range roles {
		args = append(args, r)
	}
	rows, err := s.db.Query(`SELECT DISTINCT m.message_id FROM messages m JOIN folders f ON f.id = m.folder_id
		WHERE f.account = ? AND m.thread = ? AND m.hidden = 0 AND f.role IN (`+placeholders(len(roles))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
