package imapsync

import (
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/bhr-tools/mail/internal/db"
)

// runOps replays the actions waiting for the server, in order. It returns
// the folders they touched. It stops at the first broken connection; an
// action the server refuses is tried again later, then dropped.
func (a *Account) runOps(c *imapclient.Client) (map[string]bool, error) {
	touched := map[string]bool{}
	for {
		op, err := a.store.NextOp(a.cfg.ID)
		if err != nil || op == nil {
			return touched, err
		}
		err = a.execOp(c, op)
		if err != nil && !isServerRefusal(err) && !errors.Is(err, errOpInvalid) {
			return touched, err
		}
		if err != nil {
			log.Printf("%s: %s refused: %v", a.cfg.ID, op.Kind, err)
			dropped, ferr := a.store.OpFailed(op, err, maxAttempts)
			if ferr != nil {
				return touched, ferr
			}
			if dropped {
				a.mu.Lock()
				a.status.Error = fmt.Sprintf("The server refused an action: %v", err)
				a.mu.Unlock()
			}
			// Later ops may depend on this one: try again at the next cycle.
			touched[op.From] = true
			return touched, nil
		}
		if err := a.store.OpDone(op); err != nil {
			return touched, err
		}
		touched[op.From] = true
		if op.To != "" {
			touched[op.To] = true
		}
	}
}

var errOpInvalid = errors.New("invalid action")

func (a *Account) execOp(c *imapclient.Client, op *db.Op) error {
	from, err := a.store.FolderByRole(a.cfg.ID, op.From)
	if err != nil {
		return fmt.Errorf("%w: no %s folder", errOpInvalid, op.From)
	}
	if _, err := c.Select(from.Name, nil).Wait(); err != nil {
		return err
	}
	uids, err := a.locate(c, from, op.MessageIDs)
	if err != nil {
		return err
	}
	if len(uids) == 0 {
		return nil // already gone: nothing to do
	}
	set := imap.UIDSetNum(uids...)
	switch op.Kind {
	case db.OpFlag:
		flag := map[string]imap.Flag{"seen": imap.FlagSeen, "flagged": imap.FlagFlagged, "answered": imap.FlagAnswered}[op.Flag]
		mode := imap.StoreFlagsDel
		if op.Value {
			mode = imap.StoreFlagsAdd
		}
		return c.Store(set, &imap.StoreFlags{Op: mode, Silent: true, Flags: []imap.Flag{flag}}, nil).Close()
	case db.OpDelete:
		return a.expunge(c, set)
	case db.OpMove:
		target := op.ToFolder
		if target == "" {
			to, err := a.store.FolderByRole(a.cfg.ID, op.To)
			if err != nil {
				return fmt.Errorf("%w: no %s folder", errOpInvalid, op.To)
			}
			target = to.Name
		}
		if c.Caps().Has(imap.CapMove) || c.Caps().Has(imap.CapIMAP4rev2) {
			_, err := c.Move(set, target).Wait()
			return err
		}
		if _, err := c.Copy(set, target).Wait(); err != nil {
			return err
		}
		return a.expunge(c, set)
	}
	return fmt.Errorf("%w: %s", errOpInvalid, op.Kind)
}

func (a *Account) expunge(c *imapclient.Client, set imap.UIDSet) error {
	if err := c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		return err
	}
	if c.Caps().Has(imap.CapUIDPlus) || c.Caps().Has(imap.CapIMAP4rev2) {
		return c.UIDExpunge(set).Close()
	}
	return c.Expunge().Close()
}

// locate returns the UIDs of messages in the selected folder: from the local
// copy, else by asking the server (after an undo, the message has a new UID
// that was not synchronized yet).
func (a *Account) locate(c *imapclient.Client, f *db.Folder, messageIDs []string) ([]imap.UID, error) {
	local, err := a.store.UIDsOf(f.ID, messageIDs)
	if err != nil {
		return nil, err
	}
	var uids []imap.UID
	for _, uid := range local {
		uids = append(uids, imap.UID(uid))
	}
	if len(local) >= len(messageIDs) {
		return uids, nil
	}
	var criteria *imap.SearchCriteria
	for _, id := range messageIDs {
		if strings.HasPrefix(id, "local.") {
			continue
		}
		one := imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: "<" + id + ">"}}}
		if criteria == nil {
			criteria = &one
		} else {
			criteria = &imap.SearchCriteria{Or: [][2]imap.SearchCriteria{{*criteria, one}}}
		}
	}
	if criteria == nil {
		return uids, nil
	}
	data, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, err
	}
	seen := map[imap.UID]bool{}
	for _, uid := range uids {
		seen[uid] = true
	}
	for _, uid := range data.AllUIDs() {
		if !seen[uid] {
			uids = append(uids, uid)
		}
	}
	return uids, nil
}

// SearchServer looks for messages older than the local copy, in the archive
// and sent mail, and adds the matches to the local copy. It returns how many
// were found.
func (a *Account) SearchServer(text string, limit int) (int, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, nil
	}
	total := 0
	err := a.reader.with(func(c *imapclient.Client) error {
		for _, role := range []string{"archive", "sent"} {
			f, err := a.store.FolderByRole(a.cfg.ID, role)
			if err != nil {
				continue
			}
			sel, err := c.Select(f.Name, &imap.SelectOptions{ReadOnly: true}).Wait()
			if err != nil {
				return err
			}
			if sel.UIDValidity != f.UIDValidity {
				continue // not synchronized yet
			}
			data, err := c.UIDSearch(&imap.SearchCriteria{Text: []string{text}}, nil).Wait()
			if err != nil {
				return err
			}
			found := data.AllUIDs()
			total += len(found)
			if len(found) > limit {
				found = found[len(found)-limit:]
			}
			known, err := a.store.KnownFlags(f.ID)
			if err != nil {
				return err
			}
			var missing []imap.UID
			for _, uid := range found {
				if _, ok := known[uint32(uid)]; !ok {
					missing = append(missing, uid)
				}
			}
			if len(missing) == 0 {
				continue
			}
			msgs, err := c.Fetch(imap.UIDSetNum(missing...), headerOptions).Collect()
			if err != nil {
				return err
			}
			list := make([]*db.NewMessage, 0, len(msgs))
			for _, m := range msgs {
				if m.Envelope != nil {
					list = append(list, newMessage(m))
				}
			}
			if err := a.store.InsertMessages(f, list); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		a.store.Changed()
	}
	return total, err
}
