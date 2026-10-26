package imapsync

import (
	"context"
	"log"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/bhr-tools/mail/internal/config"
	"github.com/bhr-tools/mail/internal/db"
)

// fetchBatch is how many headers are fetched per command.
const fetchBatch = 100

// headerOptions are the items fetched for a new message.
var headerOptions = &imap.FetchOptions{
	UID:           true,
	Envelope:      true,
	Flags:         true,
	InternalDate:  true,
	RFC822Size:    true,
	BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	BodySection: []*imap.FetchItemBodySection{{
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: []string{"References"},
		Peek:         true,
	}},
}

// syncFolder brings the local copy of a folder in step with the server:
// new messages, flags, and messages gone.
func (a *Account) syncFolder(c *imapclient.Client, f *db.Folder) error {
	condStore := c.Caps().Has(imap.CapCondStore)
	sel, err := c.Select(f.Name, &imap.SelectOptions{CondStore: condStore}).Wait()
	if err != nil {
		if isServerRefusal(err) {
			// The folder was renamed or deleted: look for it again.
			a.mu.Lock()
			a.resolved = false
			a.mu.Unlock()
		}
		return err
	}
	if sel.UIDValidity != f.UIDValidity {
		if err := a.store.ResetFolder(f.ID, sel.UIDValidity); err != nil {
			return err
		}
		f.UIDValidity, f.ModSeq = sel.UIDValidity, 0
	}

	// Every UID in the window, from the server.
	criteria := &imap.SearchCriteria{}
	if a.window > 0 && f.Role != config.Inbox && f.Role != config.Drafts {
		criteria.Since = time.Now().Add(-a.window)
	}
	var server []imap.UID
	if sel.NumMessages > 0 {
		data, err := c.UIDSearch(criteria, nil).Wait()
		if err != nil {
			return err
		}
		server = data.AllUIDs()
	}
	known, err := a.store.KnownFlags(f.ID)
	if err != nil {
		return err
	}
	onServer := make(map[uint32]bool, len(server))
	var fresh []imap.UID
	for _, uid := range server {
		onServer[uint32(uid)] = true
		if _, ok := known[uint32(uid)]; !ok {
			fresh = append(fresh, uid)
		}
	}
	var gone []uint32
	var kept imap.UIDSet
	for uid := range known {
		if onServer[uid] {
			kept.AddNum(imap.UID(uid))
		} else {
			gone = append(gone, uid)
		}
	}
	changed := len(gone) > 0
	if err := a.store.DeleteUIDs(f.ID, gone); err != nil {
		return err
	}

	// New messages, the most recent first, so the list fills from the top.
	slices.SortFunc(fresh, func(x, y imap.UID) int { return int(y) - int(x) })
	for start := 0; start < len(fresh); start += fetchBatch {
		batch := fresh[start:min(start+fetchBatch, len(fresh))]
		msgs, err := c.Fetch(imap.UIDSetNum(batch...), headerOptions).Collect()
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
		a.store.Changed()
	}

	// Flags changed elsewhere (read on the phone…).
	if len(kept) > 0 {
		options := &imap.FetchOptions{UID: true, Flags: true}
		set := kept
		if condStore && f.ModSeq > 0 {
			options.ChangedSince = f.ModSeq
		}
		msgs, err := c.Fetch(set, options).Collect()
		if err != nil {
			return err
		}
		updates := map[uint32]db.Flags{}
		for _, m := range msgs {
			old, ok := known[uint32(m.UID)]
			now := flagsOf(m.Flags)
			if ok && now != old {
				updates[uint32(m.UID)] = now
			}
		}
		if err := a.store.UpdateFlags(f.ID, updates); err != nil {
			return err
		}
		changed = changed || len(updates) > 0
	}
	if err := a.store.FolderSynced(f.ID, sel.HighestModSeq); err != nil {
		return err
	}
	if changed {
		a.store.Changed()
	}
	return nil
}

func flagsOf(flags []imap.Flag) db.Flags {
	var f db.Flags
	for _, flag := range flags {
		switch flag {
		case imap.FlagSeen:
			f.Seen = true
		case imap.FlagFlagged:
			f.Flagged = true
		case imap.FlagAnswered:
			f.Answered = true
		}
	}
	return f
}

var msgIDPattern = regexp.MustCompile(`<([^<>\s]+)>`)

func cleanID(id string) string {
	return strings.Trim(strings.TrimSpace(id), "<>")
}

func addresses(list []imap.Address) []db.Address {
	var out []db.Address
	for _, a := range list {
		if addr := a.Addr(); addr != "" {
			out = append(out, db.Address{Name: a.Name, Address: addr})
		}
	}
	return out
}

// newMessage reads the header items of a fetched message.
func newMessage(m *imapclient.FetchMessageBuffer) *db.NewMessage {
	env := m.Envelope
	n := &db.NewMessage{
		UID:       uint32(m.UID),
		MessageID: cleanID(env.MessageID),
		Subject:   env.Subject,
		To:        addresses(env.To),
		Cc:        addresses(env.Cc),
		ReplyTo:   addresses(env.ReplyTo),
		Date:      env.Date,
		Size:      m.RFC822Size,
	}
	if from := addresses(env.From); len(from) > 0 {
		n.From = from[0]
	}
	// Reply-To equal to From says nothing.
	if len(n.ReplyTo) == 1 && strings.EqualFold(n.ReplyTo[0].Address, n.From.Address) {
		n.ReplyTo = nil
	}
	if len(env.InReplyTo) > 0 {
		n.InReplyTo = cleanID(env.InReplyTo[0])
	}
	if n.Date.IsZero() || n.Date.After(time.Now().Add(48*time.Hour)) {
		n.Date = m.InternalDate
	}
	for _, s := range m.BodySection {
		for _, id := range msgIDPattern.FindAllStringSubmatch(string(s.Bytes), -1) {
			n.Refs = append(n.Refs, id[1])
		}
	}
	f := flagsOf(m.Flags)
	n.Seen, n.Flagged, n.Answered = f.Seen, f.Flagged, f.Answered
	if m.BodyStructure != nil {
		m.BodyStructure.Walk(func(_ []int, part imap.BodyStructure) bool {
			if single, ok := part.(*imap.BodyStructureSinglePart); ok {
				d := single.Disposition()
				attached := d != nil && strings.EqualFold(d.Value, "attachment")
				named := single.Filename() != "" && !strings.EqualFold(single.Type, "text")
				// Images shown inside an HTML message are not attachments.
				inlineImage := strings.EqualFold(single.Type, "image") && single.ID != "" && !attached
				if (attached || named) && !inlineImage {
					n.Attachments = true
				}
			}
			return true
		})
	}
	return n
}

// watch keeps an IDLE connection on the inbox and wakes the synchronization
// when the server announces a change.
func (a *Account) watch(ctx context.Context) {
	backoff := 10 * time.Second
	for ctx.Err() == nil {
		err := a.idle(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == errNoIdle {
			return // the regular synchronization will do
		}
		if err != nil {
			log.Printf("%s: idle: %v", a.cfg.ID, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

type idleError string

func (e idleError) Error() string { return string(e) }

const errNoIdle = idleError("the server does not support IDLE")

func (a *Account) idle(ctx context.Context) error {
	changed := make(chan struct{}, 1)
	signal := func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	c, err := dial(a.cfg, &imapclient.UnilateralDataHandler{
		Expunge: func(uint32) { signal() },
		Mailbox: func(data *imapclient.UnilateralDataMailbox) {
			if data.NumMessages != nil {
				signal()
			}
		},
		Fetch: func(msg *imapclient.FetchMessageData) { signal() },
	})
	if err != nil {
		return err
	}
	defer c.Close()
	if !c.Caps().Has(imap.CapIdle) && !c.Caps().Has(imap.CapIMAP4rev2) {
		return errNoIdle
	}
	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return err
	}
	for {
		cmd, err := c.Idle()
		if err != nil {
			return err
		}
		woken := false
		select {
		case <-ctx.Done():
		case <-changed:
			woken = true
		case <-time.After(idleRestart):
		case <-c.Closed():
			return cmd.Wait()
		}
		if err := cmd.Close(); err != nil {
			return err
		}
		if err := cmd.Wait(); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		if woken {
			// Let a burst of notifications settle.
			time.Sleep(500 * time.Millisecond)
			select {
			case <-changed:
			default:
			}
			a.Wake(config.Inbox)
		}
	}
}
