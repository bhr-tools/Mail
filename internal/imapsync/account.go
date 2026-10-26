package imapsync

import (
	"context"
	"errors"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/bhr-tools/mail/internal/config"
	"github.com/bhr-tools/mail/internal/db"
	"github.com/bhr-tools/mail/internal/message"
)

const (
	// fullSyncEvery is how often every folder is synchronized; the inbox
	// is followed live with IDLE.
	fullSyncEvery = 5 * time.Minute
	// idleRestart renews IDLE before servers drop it (RFC 9051: 29 minutes).
	idleRestart = 20 * time.Minute
	maxBackoff  = 5 * time.Minute
	// maxAttempts drops an action the server keeps refusing.
	maxAttempts = 5
	// Bodies of recent inbox messages are downloaded ahead, so they open
	// at once and their first line shows in the list.
	prefetchCount = 25
	prefetchMax   = 3 << 20
)

// Status is the state of an account, shown in the status bar.
type Status struct {
	State    string    `json:"state"` // connecting, syncing, ok, error
	Error    string    `json:"error,omitempty"`
	SyncedAt time.Time `json:"synced_at"`
	Pending  int       `json:"pending"`
}

// Account follows one mail account.
type Account struct {
	cfg    *config.Account
	store  *db.Store
	window time.Duration

	worker conn // synchronization and actions
	reader conn // what the app waits for

	wake chan struct{}

	mu       sync.Mutex
	status   Status
	roles    map[string]bool // folders to synchronize at the next wake
	full     bool
	resolved bool
	all      []string // every folder of the account
}

// Manager runs the accounts.
type Manager struct {
	store    *db.Store
	accounts []*Account
}

// New prepares the accounts. Window limits how far back the archive, sent
// mail and trash are copied; the inbox and drafts are copied whole.
func New(store *db.Store, accounts []*config.Account, window time.Duration) *Manager {
	m := &Manager{store: store}
	for _, cfg := range accounts {
		m.accounts = append(m.accounts, &Account{
			cfg:    cfg,
			store:  store,
			window: window,
			worker: conn{account: cfg},
			reader: conn{account: cfg},
			wake:   make(chan struct{}, 1),
			status: Status{State: "connecting"},
			roles:  map[string]bool{},
			full:   true,
		})
	}
	return m
}

// Start runs every account until ctx ends.
func (m *Manager) Start(ctx context.Context) {
	ids := make([]string, len(m.accounts))
	for i, a := range m.accounts {
		ids[i] = a.cfg.ID
	}
	if err := m.store.DropAccountsExcept(ids); err != nil {
		log.Printf("forget removed accounts: %v", err)
	}
	for _, a := range m.accounts {
		go a.run(ctx)
		go a.watch(ctx)
	}
}

// Accounts returns the accounts in configuration order.
func (m *Manager) Accounts() []*Account {
	return m.accounts
}

// Account returns an account by ID, or nil.
func (m *Manager) Account(id string) *Account {
	for _, a := range m.accounts {
		if a.cfg.ID == id {
			return a
		}
	}
	return nil
}

// Config returns the account's settings.
func (a *Account) Config() *config.Account {
	return a.cfg
}

// Status returns the account's state.
func (a *Account) Status() Status {
	a.mu.Lock()
	s := a.status
	a.mu.Unlock()
	s.Pending, _ = a.store.PendingOps(a.cfg.ID)
	return s
}

// Folders returns the names of every folder of the account (for moves).
func (a *Account) Folders() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string{}, a.all...)
}

// Wake runs the actions waiting for the server and synchronizes the given
// folders (every folder if none is given).
func (a *Account) Wake(roles ...string) {
	a.mu.Lock()
	if len(roles) == 0 {
		a.full = true
	}
	for _, r := range roles {
		a.roles[r] = true
	}
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *Account) setStatus(state string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if state != a.status.State || (err != nil) != (a.status.Error != "") {
		defer a.store.Changed()
	}
	a.status.State = state
	a.status.Error = ""
	if err != nil {
		a.status.Error = err.Error()
	}
	if state == "ok" {
		a.status.SyncedAt = time.Now()
	}
}

// run is the synchronization loop.
func (a *Account) run(ctx context.Context) {
	defer a.worker.close()
	defer a.reader.close()
	ticker := time.NewTicker(fullSyncEvery)
	defer ticker.Stop()
	backoff := 5 * time.Second
	for {
		a.mu.Lock()
		full, roles := a.full, a.roles
		a.full, a.roles = false, map[string]bool{}
		a.mu.Unlock()

		err := a.cycle(ctx, full, roles)
		var retry <-chan time.Time
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("%s: %v", a.cfg.ID, err)
			a.setStatus("error", err)
			// Everything is tried again after a pause.
			a.mu.Lock()
			a.full = true
			a.mu.Unlock()
			retry = time.After(backoff)
			backoff = min(backoff*2, maxBackoff)
		} else {
			a.setStatus("ok", nil)
			backoff = 5 * time.Second
		}

		select {
		case <-ctx.Done():
			return
		case <-a.wake:
		case <-retry:
		case <-ticker.C:
			a.Wake()
		}
	}
}

// cycle replays the pending actions, then synchronizes folders.
func (a *Account) cycle(ctx context.Context, full bool, roles map[string]bool) error {
	return a.worker.with(func(c *imapclient.Client) error {
		if full || !a.isResolved() {
			a.setStatus("syncing", nil)
			if err := a.resolveFolders(c); err != nil {
				return err
			}
		}
		touched, err := a.runOps(c)
		if err != nil {
			return err
		}
		if roles == nil {
			roles = map[string]bool{}
		}
		for r := range touched {
			roles[r] = true
		}
		folders, err := a.store.Folders(a.cfg.ID)
		if err != nil {
			return err
		}
		// The inbox first: it is what is waited for.
		sort.SliceStable(folders, func(i, j int) bool { return folders[i].Role == config.Inbox && folders[j].Role != config.Inbox })
		for _, f := range folders {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !full && !roles[f.Role] {
				continue
			}
			a.setStatus("syncing", nil)
			if err := a.syncFolder(c, f); err != nil {
				return err
			}
		}
		return a.prefetch(c)
	})
}

func (a *Account) isResolved() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.resolved
}

// Usual folder names, by role, for servers without special-use attributes.
var usualNames = map[string][]string{
	config.Archive: {"archive", "archives", "all mail", "tous les messages"},
	config.Sent:    {"sent", "sent items", "sent messages", "sent mail", "envoyés", "messages envoyés", "éléments envoyés"},
	config.Drafts:  {"drafts", "brouillons"},
	config.Trash:   {"trash", "deleted items", "deleted messages", "bin", "corbeille", "éléments supprimés"},
}

// noise are folders some servers show over IMAP that hold no mail to read
// (Zimbra's contacts and chats), or junk.
var noise = map[string]bool{
	"junk": true, "spam": true, "chats": true, "contacts": true, "emailed contacts": true,
	"calendar": true, "tasks": true, "briefcase": true, "outbox": true, "templates": true,
	"courrier indésirable": true, "pourriel": true,
}

var specialUse = map[imap.MailboxAttr]string{
	imap.MailboxAttrAll:     config.Archive,
	imap.MailboxAttrArchive: config.Archive,
	imap.MailboxAttrSent:    config.Sent,
	imap.MailboxAttrDrafts:  config.Drafts,
	imap.MailboxAttrTrash:   config.Trash,
}

// resolveFolders finds the folder of each role and lists the others.
func (a *Account) resolveFolders(c *imapclient.Client) error {
	var options *imap.ListOptions
	if c.Caps().Has(imap.CapListExtended) && c.Caps().Has(imap.CapSpecialUse) {
		options = &imap.ListOptions{ReturnSpecialUse: true}
	}
	list, err := c.List("", "*", options).Collect()
	if err != nil {
		return err
	}
	found := map[string]string{config.Inbox: "INBOX"}
	byName := map[string]string{}
	var all []string
	// Candidates for the person's own folders: not special, not system.
	var own []string
	for _, l := range list {
		selectable := true
		special := false
		for _, attr := range l.Attrs {
			switch attr {
			case imap.MailboxAttrAll, imap.MailboxAttrArchive, imap.MailboxAttrSent, imap.MailboxAttrDrafts,
				imap.MailboxAttrTrash, imap.MailboxAttrJunk, imap.MailboxAttrFlagged, imap.MailboxAttrImportant:
				special = true
			}
			if attr == imap.MailboxAttrNoSelect || attr == imap.MailboxAttrNonExistent {
				selectable = false
			}
			if role, ok := specialUse[attr]; ok {
				// Gmail marks All Mail \All: it is the archive.
				if found[role] == "" || attr == imap.MailboxAttrAll {
					found[role] = l.Mailbox
				}
			}
		}
		if !selectable {
			continue
		}
		all = append(all, l.Mailbox)
		last := l.Mailbox
		if l.Delim != 0 {
			if i := strings.LastIndexByte(last, byte(l.Delim)); i >= 0 {
				last = last[i+1:]
			}
		}
		byName[strings.ToLower(last)] = l.Mailbox
		// Gmail keeps its system folders under [Gmail] (or [Google Mail]).
		system := strings.HasPrefix(l.Mailbox, "[Gmail]") || strings.HasPrefix(l.Mailbox, "[Google Mail]")
		if !special && !system && !strings.EqualFold(l.Mailbox, "INBOX") && !noise[strings.ToLower(last)] {
			own = append(own, l.Mailbox)
		}
	}
	for role, names := range usualNames {
		if found[role] != "" {
			continue
		}
		for _, n := range names {
			if name, ok := byName[n]; ok {
				found[role] = name
				break
			}
		}
	}
	for role, name := range a.cfg.Folders {
		found[role] = name
	}
	for _, role := range config.Roles {
		if found[role] == "" {
			if err := a.store.DropFolder(a.cfg.ID, role); err != nil {
				return err
			}
			continue
		}
		if _, err := a.store.EnsureFolder(a.cfg.ID, role, found[role]); err != nil {
			return err
		}
	}
	// The person's folders, minus those holding a role.
	taken := map[string]bool{}
	for _, name := range found {
		taken[name] = true
	}
	var keep []string
	for _, name := range own {
		if taken[name] {
			continue
		}
		role := config.CustomRole(name)
		if _, err := a.store.EnsureFolder(a.cfg.ID, role, name); err != nil {
			return err
		}
		keep = append(keep, role)
	}
	if err := a.store.DropCustomFoldersExcept(a.cfg.ID, keep); err != nil {
		return err
	}
	sort.Strings(all)
	a.mu.Lock()
	a.all = all
	a.resolved = true
	a.mu.Unlock()
	return nil
}

// isServerRefusal reports an error the server answered (NO, BAD), as
// opposed to a broken connection.
func isServerRefusal(err error) bool {
	var imapErr *imap.Error
	return errors.As(err, &imapErr)
}

// LoadBody returns a whole message, downloading it the first time.
func (a *Account) LoadBody(m *db.Message) ([]byte, error) {
	raw, err := a.store.Body(m)
	if err == nil || !errors.Is(err, db.ErrNotFound) {
		return raw, err
	}
	err = a.reader.with(func(c *imapclient.Client) error {
		raw, err = a.fetchBody(c, m)
		return err
	})
	return raw, err
}

// ErrGone is returned for a message no longer on the server.
var ErrGone = errors.New("the message is no longer on the server")

func (a *Account) fetchBody(c *imapclient.Client, m *db.Message) ([]byte, error) {
	f, err := a.store.FolderByRole(a.cfg.ID, m.Role)
	if err != nil {
		return nil, err
	}
	if _, err := c.Select(f.Name, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return nil, err
	}
	section := &imap.FetchItemBodySection{Peek: true}
	msgs, err := c.Fetch(imap.UIDSetNum(imap.UID(m.UID)), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 || len(msgs[0].BodySection) == 0 {
		a.Wake(m.Role)
		return nil, ErrGone
	}
	raw := msgs[0].BodySection[0].Bytes
	info := db.BodyInfo{}
	if p, err := message.Parse(raw); err == nil {
		info.Snippet = p.Snippet()
		info.Text = p.PlainText()
		for _, part := range p.Parts {
			if !part.Inline {
				info.Attachments = true
			}
		}
	}
	if err := a.store.SetBody(m.ID, raw, info); err != nil {
		return nil, err
	}
	return raw, nil
}

// prefetch downloads the bodies of the latest inbox messages.
func (a *Account) prefetch(c *imapclient.Client) error {
	inbox, err := a.store.FolderByRole(a.cfg.ID, config.Inbox)
	if err != nil {
		return nil
	}
	missing, err := a.store.MissingBodies(inbox.ID, prefetchCount, prefetchMax)
	if err != nil || len(missing) == 0 {
		return err
	}
	for _, m := range missing {
		if _, err := a.fetchBody(c, m); err != nil && !errors.Is(err, ErrGone) {
			return err
		}
	}
	a.store.Changed()
	return nil
}

// Append stores a message in the folder of a role (a copy of sent mail, a
// draft) and synchronizes that folder.
func (a *Account) Append(role string, raw []byte, flags []imap.Flag) error {
	f, err := a.store.FolderByRole(a.cfg.ID, role)
	if err != nil {
		return err
	}
	err = a.reader.with(func(c *imapclient.Client) error {
		cmd := c.Append(f.Name, int64(len(raw)), &imap.AppendOptions{Flags: flags, Time: time.Now()})
		if _, err := cmd.Write(raw); err != nil {
			cmd.Close()
			return err
		}
		if err := cmd.Close(); err != nil {
			return err
		}
		_, err := cmd.Wait()
		return err
	})
	if err == nil {
		a.Wake(role)
	}
	return err
}
