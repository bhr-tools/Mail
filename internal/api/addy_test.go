package api

import (
	"path/filepath"
	"testing"

	"github.com/bhr-tools/mail/internal/db"
	"github.com/bhr-tools/mail/internal/message"
)

func TestAddyAliases(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := &API{store: store}
	if err := store.SetAddySettings(db.Addy{Domain: " @Me.AnonAddy.com ", Accounts: []string{"perso"}}); err != nil {
		t.Fatal(err)
	}

	o := &message.Outgoing{To: []db.Address{{Name: "Bob", Address: "bob@example.com"}}, Cc: []db.Address{{Address: "ann@example.org"}}}
	if err := a.sendingAlias("perso", "shop", o); err != nil {
		t.Fatal(err)
	}
	if o.To[0].Address != "shop+bob=example.com@me.anonaddy.com" || o.To[0].Name != "Bob" || o.Cc[0].Address != "shop+ann=example.org@me.anonaddy.com" {
		t.Errorf("rewritten: %+v %+v", o.To, o.Cc)
	}
	if err := a.sendingAlias("work", "shop", &message.Outgoing{}); err == nil {
		t.Error("an account without addy.io sends from an alias")
	}
	if err := a.sendingAlias("perso", "a+b", &message.Outgoing{}); err == nil {
		t.Error("invalid alias accepted")
	}
	if err := a.requireAlias("perso", " "); err == nil {
		t.Error("an addy.io account sends from its own address")
	}
	if err := a.requireAlias("perso", "shop"); err != nil {
		t.Error(err)
	}
	if err := a.requireAlias("work", ""); err != nil {
		t.Error(err)
	}

	// A reply to mail forwarded by addy.io: the real sender, the same alias,
	// and one's own alias left out of the copies.
	d := message.Draft{Account: "perso", InReplyTo: 1,
		To: []db.Address{{Name: "bob@example.com", Address: "shop+bob=example.com@me.anonaddy.com"}},
		Cc: []db.Address{{Address: "shop@me.anonaddy.com"}, {Address: "ann@example.org"}}}
	a.readAliases(&d, &db.Message{})
	if d.Alias != "shop" || len(d.To) != 1 || d.To[0] != (db.Address{Address: "bob@example.com"}) || len(d.Cc) != 1 || d.Cc[0].Address != "ann@example.org" {
		t.Errorf("reply: %+v", d)
	}

	// A reply to mail sent straight to an alias.
	d = message.Draft{Account: "perso", InReplyTo: 1, To: []db.Address{{Address: "bob@example.com"}}}
	a.readAliases(&d, &db.Message{To: []db.Address{{Address: "news@me.anonaddy.com"}}})
	if d.Alias != "news" || d.To[0].Address != "bob@example.com" {
		t.Errorf("reply to an alias: %+v", d)
	}

	// Other accounts are left alone.
	d = message.Draft{Account: "work", To: []db.Address{{Address: "shop+bob=example.com@me.anonaddy.com"}}}
	a.readAliases(&d, nil)
	if d.Alias != "" || d.To[0].Address != "shop+bob=example.com@me.anonaddy.com" {
		t.Errorf("other account: %+v", d)
	}
}
