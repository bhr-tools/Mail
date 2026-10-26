package db

import (
	"database/sql"
	"errors"
	"time"
)

// Folder is the local copy of one IMAP folder of an account.
type Folder struct {
	ID          int64
	Account     string
	Role        string
	Name        string
	UIDValidity uint32
	ModSeq      uint64
	SyncedAt    time.Time
}

const folderColumns = `id, account, role, name, uidvalidity, modseq, synced_at`

func scanFolder(row interface{ Scan(...any) error }) (*Folder, error) {
	var f Folder
	var synced sql.NullInt64
	if err := row.Scan(&f.ID, &f.Account, &f.Role, &f.Name, &f.UIDValidity, &f.ModSeq, &synced); err != nil {
		return nil, err
	}
	if synced.Valid {
		f.SyncedAt = time.Unix(synced.Int64, 0)
	}
	return &f, nil
}

// EnsureFolder returns the folder of a role, creating it. When the role now
// points to another folder name, the old copy is dropped.
func (s *Store) EnsureFolder(account, role, name string) (*Folder, error) {
	var f *Folder
	err := s.tx(func(tx *sql.Tx) error {
		var err error
		f, err = scanFolder(tx.QueryRow(`SELECT `+folderColumns+` FROM folders WHERE account = ? AND role = ?`, account, role))
		if err == nil && f.Name == name {
			return nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM folders WHERE account = ? AND role = ?`, account, role); err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT INTO folders (account, role, name) VALUES (?, ?, ?)`, account, role, name)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		f = &Folder{ID: id, Account: account, Role: role, Name: name}
		return nil
	})
	return f, err
}

// DropFolder forgets the folder of a role (it no longer exists).
func (s *Store) DropFolder(account, role string) error {
	_, err := s.db.Exec(`DELETE FROM folders WHERE account = ? AND role = ?`, account, role)
	return err
}

// DropAccountsExcept forgets the accounts removed from the configuration.
func (s *Store) DropAccountsExcept(ids []string) error {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	if _, err := s.db.Exec(`DELETE FROM folders WHERE account NOT IN (`+placeholders(len(ids))+`)`, args...); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM ops WHERE account NOT IN (`+placeholders(len(ids))+`)`, args...)
	return err
}

// DropCustomFoldersExcept forgets the folders made by the person that are
// gone from the server (their roles start with "folder:").
func (s *Store) DropCustomFoldersExcept(account string, keep []string) error {
	query := `DELETE FROM folders WHERE account = ? AND role LIKE 'folder:%'`
	args := []any{account}
	if len(keep) > 0 {
		query += ` AND role NOT IN (` + placeholders(len(keep)) + `)`
		for _, r := range keep {
			args = append(args, r)
		}
	}
	_, err := s.db.Exec(query, args...)
	return err
}

// Folders returns the folders of an account.
func (s *Store) Folders(account string) ([]*Folder, error) {
	rows, err := s.db.Query(`SELECT `+folderColumns+` FROM folders WHERE account = ? ORDER BY id`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*Folder
	for rows.Next() {
		f, err := scanFolder(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, f)
	}
	return list, rows.Err()
}

// FolderByRole returns the folder of a role, or ErrNotFound.
func (s *Store) FolderByRole(account, role string) (*Folder, error) {
	f, err := scanFolder(s.db.QueryRow(`SELECT `+folderColumns+` FROM folders WHERE account = ? AND role = ?`, account, role))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return f, err
}

// ResetFolder empties a folder whose UIDs changed meaning (new UIDVALIDITY).
func (s *Store) ResetFolder(id int64, uidValidity uint32) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM messages WHERE folder_id = ?`, id); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE folders SET uidvalidity = ?, modseq = 0, synced_at = NULL WHERE id = ?`, uidValidity, id)
		return err
	})
}

// FolderSynced records the end of a successful synchronization.
func (s *Store) FolderSynced(id int64, modSeq uint64) error {
	_, err := s.db.Exec(`UPDATE folders SET modseq = ?, synced_at = ? WHERE id = ?`, modSeq, time.Now().Unix(), id)
	return err
}

// FolderCounts returns the unread and total number of messages of a folder.
func (s *Store) FolderCounts(id int64) (unread, total int, err error) {
	err = s.db.QueryRow(`SELECT COALESCE(SUM(seen = 0), 0), COUNT(*) FROM messages WHERE folder_id = ? AND hidden = 0`, id).Scan(&unread, &total)
	return
}
