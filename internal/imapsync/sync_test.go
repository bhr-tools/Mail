package imapsync

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/bhr-tools/mail/internal/config"
	"github.com/bhr-tools/mail/internal/db"
	"github.com/bhr-tools/mail/internal/testmail"
)

func setup(t *testing.T) (*db.Store, *Account, *testmail.Server) {
	t.Helper()
	srv, err := testmail.Start()
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Seed(time.Now()); err != nil {
		t.Fatal(err)
	}
	store, err := db.Open(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	m := New(store, []*config.Account{srv.Account("demo", "Demo")}, 365*24*time.Hour)
	return store, m.Accounts()[0], srv
}

// syncNow runs one full cycle.
func syncNow(t *testing.T, a *Account) {
	t.Helper()
	if err := a.cycle(context.Background(), true, nil); err != nil {
		t.Fatal(err)
	}
}

func threads(t *testing.T, store *db.Store, role string) []*db.Thread {
	t.Helper()
	list, err := store.Threads(db.Filter{Roles: []string{role}})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func find(list []*db.Thread, subject string) *db.Thread {
	for _, th := range list {
		if th.Latest.Subject == subject {
			return th
		}
	}
	return nil
}

func TestSyncFoldersThreadsAndBodies(t *testing.T) {
	store, a, _ := setup(t)
	syncNow(t, a)

	inbox := threads(t, store, config.Inbox)
	if len(inbox) != 4 {
		t.Fatalf("inbox has %d threads", len(inbox))
	}
	if inbox[0].Latest.Subject != "Climbing on Saturday?" || !inbox[0].Flagged {
		t.Errorf("latest thread = %+v", inbox[0].Latest)
	}
	lab := find(inbox, "Re: Lab report review")
	if lab == nil || lab.Count != 2 || lab.Unread != 1 {
		t.Fatalf("lab thread = %+v", lab)
	}
	// The conversation includes the reply from Sent.
	msgs, err := store.ThreadMessages("demo", lab.Thread, config.Inbox)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("thread messages = %d %v", len(msgs), err)
	}
	if msgs[1].Role != config.Sent {
		t.Errorf("middle message from %s", msgs[1].Role)
	}
	invoice := find(inbox, "Invoice for the gala hall")
	if invoice == nil || !invoice.Attachments {
		t.Errorf("invoice = %+v", invoice)
	}
	if len(threads(t, store, config.Drafts)) != 1 || len(threads(t, store, config.Trash)) != 1 || len(threads(t, store, config.Archive)) != 2 {
		t.Error("drafts, trash or archive not synchronized")
	}
	if folders := a.Folders(); len(folders) != 9 {
		t.Errorf("folders = %v", folders)
	}
	// The person's folders are kept too; junk is not.
	var own []string
	all, _ := store.Folders("demo")
	for _, f := range all {
		if config.IsCustom(f.Role) {
			own = append(own, f.Name)
		}
	}
	if len(own) != 3 || own[0] != "Receipts" && own[1] != "Receipts" && own[2] != "Receipts" {
		t.Errorf("own folders = %v", own)
	}
	robotics := threads(t, store, config.CustomRole("Projects/Robotics"))
	if len(robotics) != 1 || robotics[0].Latest.Subject != "Motor driver datasheet" || robotics[0].Unread != 1 {
		t.Errorf("robotics = %+v", robotics)
	}

	// Recent inbox bodies were downloaded ahead, with their first line.
	latest, _ := store.Message(lab.Latest.ID)
	if !latest.HasBody || latest.Snippet != "Great, thanks! I added two figures since." {
		t.Errorf("snippet = %q (body %v)", latest.Snippet, latest.HasBody)
	}
	raw, err := a.LoadBody(msgs[0])
	if err != nil || len(raw) == 0 {
		t.Fatalf("body: %v", err)
	}
}

func TestActionsReachTheServer(t *testing.T) {
	store, a, srv := setup(t)
	syncNow(t, a)
	climb := find(threads(t, store, config.Inbox), "Climbing on Saturday?")

	// Done: the message leaves the inbox at once, then reaches the archive.
	id, err := store.Apply("demo", db.Op{Kind: db.OpMove, MessageIDs: []string{climb.Latest.MessageID}, From: config.Inbox, To: config.Archive})
	if err != nil {
		t.Fatal(err)
	}
	if find(threads(t, store, config.Inbox), "Climbing on Saturday?") != nil {
		t.Fatal("still shown in the inbox")
	}
	syncNow(t, a)
	if find(threads(t, store, config.Archive), "Climbing on Saturday?") == nil {
		t.Fatal("not in the archive after sync")
	}
	if n, _ := store.PendingOps("demo"); n != 0 {
		t.Fatalf("%d ops pending", n)
	}

	// Undo after the server applied it: moved back.
	if _, err := store.Undo(id); err != nil {
		t.Fatal(err)
	}
	syncNow(t, a)
	if find(threads(t, store, config.Inbox), "Climbing on Saturday?") == nil {
		t.Fatal("undo did not bring it back")
	}
	if find(threads(t, store, config.Archive), "Climbing on Saturday?") != nil {
		t.Fatal("still archived after undo")
	}

	// Undo before the server saw it: nothing reaches the server.
	climb = find(threads(t, store, config.Inbox), "Climbing on Saturday?")
	id, _ = store.Apply("demo", db.Op{Kind: db.OpMove, MessageIDs: []string{climb.Latest.MessageID}, From: config.Inbox, To: config.Trash})
	if _, err := store.Undo(id); err != nil {
		t.Fatal(err)
	}
	syncNow(t, a)
	if find(threads(t, store, config.Inbox), "Climbing on Saturday?") == nil {
		t.Fatal("cancelled trash still applied")
	}

	// Flags.
	lab := find(threads(t, store, config.Inbox), "Re: Lab report review")
	store.Apply("demo", db.Op{Kind: db.OpFlag, MessageIDs: []string{lab.Latest.MessageID}, From: config.Inbox, Flag: "seen", Value: true})
	syncNow(t, a)
	if lab = find(threads(t, store, config.Inbox), "Re: Lab report review"); lab.Unread != 0 {
		t.Fatal("seen flag lost after sync")
	}

	// New mail delivered on the server shows after a sync.
	srv.Deliver("INBOX", []byte("From: x@y.test\r\nTo: "+testmail.Address+"\r\nSubject: Hello\r\nMessage-ID: <new@y.test>\r\n\r\nHi\r\n"), imap.FlagSeen)
	a.Wake(config.Inbox)
	syncNow(t, a)
	if find(threads(t, store, config.Inbox), "Hello") == nil {
		t.Fatal("new message not synchronized")
	}
}

func TestIdleWakesTheSynchronization(t *testing.T) {
	store, a, srv := setup(t)
	syncNow(t, a)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.watch(ctx)
	time.Sleep(300 * time.Millisecond)
	select {
	case <-a.wake:
	default:
	}
	srv.Deliver("INBOX", []byte("From: x@y.test\r\nSubject: Ping\r\nMessage-ID: <ping@y.test>\r\n\r\nHi\r\n"))
	select {
	case <-a.wake:
	case <-time.After(5 * time.Second):
		t.Fatal("new mail did not wake the synchronization")
	}
	syncNow(t, a)
	if find(threads(t, store, config.Inbox), "Ping") == nil {
		t.Fatal("not synchronized")
	}
}

func TestRefusedPasswordRests(t *testing.T) {
	srv, err := testmail.Start()
	if err != nil {
		t.Fatal(err)
	}
	cfg := srv.Account("demo", "Demo")
	cfg.Password = "wrong"
	refused := &refusals{}
	if _, err := dial(cfg, refused, nil); err == nil || !strings.Contains(err.Error(), "login refused") {
		t.Fatalf("wrong password: %v", err)
	}
	// Not sent again: the refusal is returned without a new login.
	if refused.check("wrong") == nil {
		t.Error("refused password tried again at once")
	}
	cfg.Password = srv.Account("demo", "Demo").Password
	c, err := dial(cfg, refused, nil)
	if err != nil {
		t.Fatalf("new password: %v", err)
	}
	c.Close()
}

func TestMissingFolderSetByHandIsCreated(t *testing.T) {
	store, a, _ := setup(t)
	a.cfg.Folders = map[string]string{config.Archive: "Kept"}
	syncNow(t, a)
	f, err := store.FolderByRole(a.cfg.ID, config.Archive)
	if err != nil || f.Name != "Kept" {
		t.Fatalf("archive = %+v, %v", f, err)
	}
	if !slices.Contains(a.Folders(), "Kept") {
		t.Errorf("folders = %v", a.Folders())
	}
}
