// Command server runs Mail: the IMAP synchronization, the JSON API and the
// embedded web app.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"time"
	_ "time/tzdata" // time zones without relying on the system database

	"github.com/bhr-tools/kit/server"
	"github.com/bhr-tools/mail/internal/api"
	"github.com/bhr-tools/mail/internal/config"
	"github.com/bhr-tools/mail/internal/db"
	"github.com/bhr-tools/mail/internal/imapsync"
	"github.com/bhr-tools/mail/internal/imgproxy"
	"github.com/bhr-tools/mail/web"
)

// sampleAccounts serves sample mail from servers in memory, without a real
// account. It is only built for the browser tests (go build -tags e2e).
var sampleAccounts func() ([]*config.Account, error)

// csp is the kit's, plus the frame that shows a message (its own answer
// sets a stricter policy).
const csp = "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-src 'self'; manifest-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// Each flag defaults to its environment variable (kit/server), so the binary
// is configured with flags (NixOS module) or environment alike.
func main() {
	cfg := server.Flags(8080, "mail.db")
	accountsPath := flag.String("accounts", server.EnvOr("ACCOUNTS", ""), "accounts file, JSON (env ACCOUNTS)")
	window := flag.Duration("window", server.EnvDuration("WINDOW", 365*24*time.Hour), "how far back the archive, sent mail and trash are copied (env WINDOW)")
	flag.Parse()
	addr := cfg.Addr()

	var accounts []*config.Account
	switch {
	case *accountsPath != "":
		c, err := config.Load(*accountsPath)
		if err != nil {
			log.Fatalf("accounts: %v", err)
		}
		accounts = c.Accounts
	case sampleAccounts != nil:
		var err error
		if accounts, err = sampleAccounts(); err != nil {
			log.Fatalf("sample mail: %v", err)
		}
	default:
		log.Fatal("no accounts: pass -accounts accounts.json")
	}

	store, err := db.Open(cfg.DBPath())
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer store.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := imapsync.New(store, accounts, *window)
	manager.Start(ctx)

	a, err := api.New(store, manager, imgproxy.New(sampleAccounts != nil), cfg.NoAuth)
	if err != nil {
		log.Fatal(err)
	}
	server.Timeout = 2 * time.Minute // attachments
	log.Printf("database %s, %d accounts", cfg.DBPath(), len(accounts))
	server.Run("Mail", addr, newHandler(store, a))
}

// newHandler routes the API, the health check, the kit files and the web
// app (kit/server).
func newHandler(store *db.Store, a *api.API) http.Handler {
	return server.Handler(server.Options{API: a.Handler(), Web: web.Files, Ping: store.Ping, CSP: csp})
}
