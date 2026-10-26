package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Op kinds.
const (
	OpMove = "move" // move messages from one folder to another
	OpFlag = "flag" // set or clear a flag
	// OpDelete deletes messages for good (a draft replaced or sent).
	OpDelete = "delete"
)

// Op is an action done in the app, waiting to be replayed on the server.
// Messages are named by Message-ID, so an op still applies after the folder
// was synchronized again.
type Op struct {
	ID         int64    `json:"-"`
	Account    string   `json:"-"`
	Kind       string   `json:"kind"`
	MessageIDs []string `json:"message_ids"`
	From       string   `json:"from"`         // folder role
	To         string   `json:"to,omitempty"` // folder role, for moves
	// ToFolder names a folder without a role (a Gmail label…).
	ToFolder string `json:"to_folder,omitempty"`
	Flag     string `json:"flag,omitempty"` // seen, flagged or answered
	Value    bool   `json:"value,omitempty"`
	Attempts int    `json:"-"`
}

// Apply shows the effect of an op at once and queues it for the server.
// It returns the op's ID, which can undo it.
func (s *Store) Apply(account string, op Op) (int64, error) {
	if len(op.MessageIDs) == 0 {
		return 0, invalid("No message selected")
	}
	switch op.Kind {
	case OpMove:
		if op.ToFolder != "" {
			op.To = ""
		} else if op.From == op.To || op.To == "" {
			return 0, invalid("Invalid destination")
		}
	case OpFlag:
		if op.Flag != "seen" && op.Flag != "flagged" && op.Flag != "answered" {
			return 0, invalid("Unknown flag %q", op.Flag)
		}
	case OpDelete:
		op.To = ""
	default:
		return 0, invalid("Unknown action %q", op.Kind)
	}
	data, _ := json.Marshal(op)
	var id int64
	err := s.tx(func(tx *sql.Tx) error {
		if err := applyLocal(tx, account, op, false); err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT INTO ops (account, data, created_at) VALUES (?, ?, ?)`, account, string(data), time.Now().Unix())
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		return nil
	})
	if err == nil {
		s.Changed()
	}
	return id, err
}

// applyLocal shows an op in the local copy, or reverts it.
func applyLocal(tx *sql.Tx, account string, op Op, revert bool) error {
	args := []any{}
	for _, id := range op.MessageIDs {
		args = append(args, id)
	}
	in := `message_id IN (` + placeholders(len(op.MessageIDs)) + `)`
	switch op.Kind {
	case OpMove, OpDelete:
		// Every copy outside the destination disappears (Gmail drops a
		// message from All Mail too when it goes to the trash).
		_, err := tx.Exec(`UPDATE messages SET hidden = ? WHERE folder_id IN
			(SELECT id FROM folders WHERE account = ? AND role != ?) AND `+in,
			append([]any{!revert, account, op.To}, args...)...)
		return err
	case OpFlag:
		value := op.Value
		if revert {
			value = !value
		}
		_, err := tx.Exec(`UPDATE messages SET `+op.Flag+` = ? WHERE folder_id IN
			(SELECT id FROM folders WHERE account = ?) AND `+in, append([]any{value, account}, args...)...)
		return err
	}
	return nil
}

func scanOp(row interface{ Scan(...any) error }) (*Op, error) {
	var op Op
	var data string
	if err := row.Scan(&op.ID, &op.Account, &data, &op.Attempts); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(data), &op); err != nil {
		return nil, err
	}
	return &op, nil
}

// NextOp returns the oldest op of an account not applied yet and marks it
// started, or nil.
func (s *Store) NextOp(account string) (*Op, error) {
	op, err := scanOp(s.db.QueryRow(`SELECT id, account, data, attempts FROM ops
		WHERE account = ? AND done = 0 ORDER BY id LIMIT 1`, account))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(`UPDATE ops SET started = 1 WHERE id = ?`, op.ID)
	return op, err
}

// OpDone records that the server applied an op.
func (s *Store) OpDone(op *Op) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE ops SET done = 1 WHERE id = ?`, op.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM ops WHERE done = 1 AND created_at < ?`, time.Now().Add(-24*time.Hour).Unix()); err != nil {
			return err
		}
		if op.Kind != OpMove && op.Kind != OpDelete {
			return nil
		}
		// The copies hidden by the move are gone; the next synchronization
		// brings back whatever is still on the server.
		args := []any{op.Account, op.To}
		for _, id := range op.MessageIDs {
			args = append(args, id)
		}
		_, err := tx.Exec(`DELETE FROM messages WHERE hidden = 1 AND folder_id IN
			(SELECT id FROM folders WHERE account = ? AND role != ?) AND message_id IN (`+placeholders(len(op.MessageIDs))+`)`, args...)
		return err
	})
}

// OpFailed records a failed attempt. After maxAttempts, the op is dropped
// and its effect reverted; dropped reports it.
func (s *Store) OpFailed(op *Op, cause error, maxAttempts int) (dropped bool, err error) {
	err = s.tx(func(tx *sql.Tx) error {
		if op.Attempts+1 < maxAttempts {
			_, err := tx.Exec(`UPDATE ops SET attempts = attempts + 1, error = ? WHERE id = ?`, cause.Error(), op.ID)
			return err
		}
		dropped = true
		if _, err := tx.Exec(`DELETE FROM ops WHERE id = ?`, op.ID); err != nil {
			return err
		}
		return applyLocal(tx, op.Account, *op, true)
	})
	if err == nil && dropped {
		s.Changed()
	}
	return dropped, err
}

// Undo cancels an op: one the server has not started yet is dropped, one
// already on its way is reversed by a new op. It returns the account.
func (s *Store) Undo(id int64) (string, error) {
	var op *Op
	var started bool
	err := s.tx(func(tx *sql.Tx) error {
		var err error
		op, err = scanOp(tx.QueryRow(`SELECT id, account, data, attempts FROM ops WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT started FROM ops WHERE id = ?`, id).Scan(&started); err != nil {
			return err
		}
		if op.Kind == OpDelete {
			return invalid("A deletion cannot be undone")
		}
		if started && op.ToFolder != "" {
			return invalid("This move cannot be undone")
		}
		if started {
			// Undone once: a second undo must not reverse it again.
			_, err := tx.Exec(`DELETE FROM ops WHERE id = ? AND done = 1`, id)
			return err
		}
		if _, err := tx.Exec(`DELETE FROM ops WHERE id = ?`, id); err != nil {
			return err
		}
		return applyLocal(tx, op.Account, *op, true)
	})
	if err != nil {
		return "", err
	}
	if started {
		_, err = s.Apply(op.Account, Reverse(*op))
		return op.Account, err
	}
	s.Changed()
	return op.Account, nil
}

// Reverse returns the op that undoes op.
func Reverse(op Op) Op {
	switch op.Kind {
	case OpMove:
		op.From, op.To = op.To, op.From
	case OpFlag:
		op.Value = !op.Value
	}
	return op
}

// PendingOps returns how many ops of an account wait for the server.
func (s *Store) PendingOps(account string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM ops WHERE account = ? AND done = 0`, account).Scan(&n)
	return n, err
}
