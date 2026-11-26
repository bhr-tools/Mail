// Package api exposes the mail over a JSON HTTP API, for the web app.
// Authentication and the JSON helpers come from the kit.
package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/bhr-tools/kit/auth"
	"github.com/bhr-tools/kit/httpx"
	"github.com/bhr-tools/mail/internal/db"
	"github.com/bhr-tools/mail/internal/imapsync"
	"github.com/bhr-tools/mail/internal/imgproxy"
)

const (
	maxUploadBody = 35 << 20 // attachments of a message
	// sessionCookie keeps its first name: renaming it would sign everybody out.
	sessionCookie = "mail_session"
	// changesWait is how long a request for changes waits before answering
	// "nothing new" (below the server's write timeout).
	changesWait = 50 * time.Second
)

// API holds the HTTP handlers.
type API struct {
	store  *db.Store
	mail   *imapsync.Manager
	images *imgproxy.Proxy
	auth   *auth.Auth
}

// New returns the API. With noAuth, every request is accepted without login.
func New(store *db.Store, mail *imapsync.Manager, images *imgproxy.Proxy, noAuth bool) (*API, error) {
	a := &API{store: store, mail: mail, images: images}
	var err error
	if a.auth, err = auth.New(store.DB(), sessionCookie, noAuth); err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	return a, nil
}

// Helpers of the kit, under the names the handlers use.
var (
	writeJSON  = httpx.WriteJSON
	writeError = httpx.WriteError
	decode     = httpx.Decode
)

// Handler returns the API routes, wrapped in their middlewares.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	open := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, h) }
	auth := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, a.auth.Require(h)) }

	a.auth.Routes(mux)

	auth("GET /api/accounts", a.accounts)
	auth("PUT /api/accounts/order", a.setAccountOrder)
	auth("PUT /api/settings/addy", a.setAddy)
	auth("GET /api/accounts/{account}/folders", a.folders)
	auth("POST /api/accounts/{account}/folders", a.createFolder)
	auth("PUT /api/accounts/{account}/folders", a.renameFolder)
	auth("DELETE /api/accounts/{account}/folders", a.deleteFolder)
	auth("POST /api/accounts/{account}/sync", a.syncAccount)
	auth("GET /api/changes", a.changes)

	auth("GET /api/threads", a.threads)
	auth("GET /api/thread", a.thread)
	auth("GET /api/search", a.search)
	auth("POST /api/search/server", a.searchServer)

	auth("GET /api/messages/{id}", a.message)
	auth("GET /api/messages/{id}/html", a.messageHTML)
	auth("GET /api/messages/{id}/parts/{index}", a.part)
	auth("GET /api/messages/{id}/cid/{cid}", a.cid)
	auth("POST /api/messages/{id}/images", a.allowImages)
	auth("GET /api/img", a.images.ServeHTTP)

	auth("POST /api/actions", a.act)
	auth("POST /api/undo", a.undo)

	auth("GET /api/compose", a.compose)
	auth("POST /api/send", a.send)
	auth("POST /api/drafts", a.saveDraft)
	auth("GET /api/contacts", a.contacts)

	open("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "Unknown API endpoint")
	})
	return httpx.RejectCrossSite(mux)
}

// changes waits until the mail changes, then returns the new version.
func (a *API) changes(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	ctx, cancel := context.WithTimeout(r.Context(), changesWait)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]uint64{"version": a.store.WaitChange(ctx, since)})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// fail answers like httpx.Fail (404, 400, 500), plus 410 for a message gone
// from the server and 502 when the mail server is unreachable or refuses.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	var mailErr *mailError
	switch {
	case errors.Is(err, imapsync.ErrGone):
		writeError(w, http.StatusGone, "This message is no longer on the server")
	case errors.As(err, &mailErr):
		log.Printf("%s %s: %v", r.Method, r.URL.Path, mailErr.err)
		writeError(w, http.StatusBadGateway, mailErr.err.Error())
	default:
		httpx.Fail(w, r, err)
	}
}

// mailError is an error of the mail server, shown to the user as is.
type mailError struct{ err error }

func (e *mailError) Error() string { return e.err.Error() }

func serverError(err error) error {
	if err == nil {
		return nil
	}
	var invalidInput *db.ValidationError
	if errors.Is(err, db.ErrNotFound) || errors.Is(err, imapsync.ErrGone) || errors.As(err, &invalidInput) {
		return err
	}
	return &mailError{err}
}

func invalid(msg string) error {
	return &db.ValidationError{Msg: msg}
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, db.ErrNotFound
	}
	return id, nil
}
