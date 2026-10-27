package db

// AccountOrder returns the position of each account arranged in the
// sidebar; accounts never moved are missing.
func (s *Store) AccountOrder() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT account, position FROM account_order`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	order := map[string]int{}
	for rows.Next() {
		var id string
		var position int
		if err := rows.Scan(&id, &position); err != nil {
			return nil, err
		}
		order[id] = position
	}
	return order, rows.Err()
}

// SetAccountOrder records the order of the accounts.
func (s *Store) SetAccountOrder(ids []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM account_order`); err != nil {
		return err
	}
	for i, id := range ids {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO account_order (account, position) VALUES (?, ?)`, id, i); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.Changed()
	return nil
}
