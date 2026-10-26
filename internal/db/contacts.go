package db

import (
	"strings"
	"time"
)

// Contact is an address to complete when composing.
type Contact struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

func noteContact(q querier, a Address, sent bool, when time.Time) error {
	address := strings.TrimSpace(a.Address)
	if address == "" || !strings.Contains(address, "@") {
		return nil
	}
	column := "received"
	if sent {
		column = "sent"
	}
	_, err := q.Exec(`INSERT INTO contacts (address, name, `+column+`, last) VALUES (?, ?, 1, ?)
		ON CONFLICT (address) DO UPDATE SET `+column+` = `+column+` + 1,
			name = CASE WHEN excluded.name != '' THEN excluded.name ELSE name END,
			last = MAX(last, excluded.last)`, address, a.Name, when.Unix())
	return err
}

// NoteSent records the recipients of a sent message: they come first when
// completing.
func (s *Store) NoteSent(list []Address) error {
	now := time.Now()
	for _, a := range list {
		if err := noteContact(s.db, a, true, now); err != nil {
			return err
		}
	}
	return nil
}

// Contacts returns the addresses matching a prefix of a word of the name or
// the address: people written to first, then the most frequent senders.
func (s *Store) Contacts(query string, limit int) ([]Contact, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []Contact{}, nil
	}
	pattern := like(query)
	prefix := strings.TrimPrefix(pattern, "%")
	rows, err := s.db.Query(`SELECT name, address FROM contacts
		WHERE address LIKE ? ESCAPE '\' OR name LIKE ? ESCAPE '\' OR name LIKE ? ESCAPE '\'
		ORDER BY (sent > 0) DESC, sent * 5 + received DESC, last DESC LIMIT ?`,
		prefix, prefix, "% "+prefix, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Contact{}
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.Name, &c.Address); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

// AllowImages always shows the remote images of a sender.
func (s *Store) AllowImages(address string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO image_senders (address) VALUES (?)`, strings.TrimSpace(address))
	return err
}

// ImagesAllowed reports whether the remote images of a sender are shown.
func (s *Store) ImagesAllowed(address string) bool {
	ok, _ := exists(s.db, `SELECT 1 FROM image_senders WHERE address = ?`, strings.TrimSpace(address))
	return ok
}
