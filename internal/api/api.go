// Package api exposes the mail over a JSON HTTP API, for the web app.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/bhr-tools/mail/internal/db"
	"github.com/bhr-tools/mail/internal/imapsync"
	"github.com/bhr-tools/mail/internal/imgproxy"
)

const (
	maxJSONBody   = 1 << 20  // 1 MiB
	maxUploadBody = 35 << 20 // attachments of a message
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
	noAuth bool
	// loginMu serializes password checks; a failed attempt holds it for a
	// moment, which caps brute-force attempts without any bookkeeping.
	loginMu sync.Mutex
}

// New returns the API. With noAuth, every request is accepted without login.
func New(store *db.Store, mail *imapsync.Manager, images *imgproxy.Proxy, noAuth bool) *API {
	return &API{store: store, mail: mail, images: images, noAuth: noAuth}
}

// Handler returns the API routes, wrapped in their middlewares.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	open := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, h) }
	auth := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, a.requireAuth(h)) }

	open("GET /api/auth/status", a.authStatus)
	open("POST /api/auth/setup", a.authSetup)
	open("POST /api/auth/login", a.authLogin)
	open("POST /api/auth/logout", a.authLogout)
	auth("POST /api/auth/change-password", a.authChangePassword)

	auth("GET /api/accounts", a.accounts)
	auth("PUT /api/accounts/order", a.setAccountOrder)
	auth("GET /api/accounts/{account}/folders", a.folders)
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
	return rejectCrossSite(mux)
}

// rejectCrossSite refuses state-changing requests sent by another site
// (CSRF). Browsers label every request with Sec-Fetch-Site; requests without
// it (curl, scripts) are not browser-driven and are let through.
func rejectCrossSite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeError(w, http.StatusForbidden, "Cross-site request refused")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.authenticated(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Authentication required", "auth_required": true})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authenticated reports whether r may use the API. Until a password is set,
// the instance is open (first-run setup).
func (a *API) authenticated(r *http.Request) bool {
	if a.noAuth {
		return true
	}
	hasPassword, err := a.store.HasPassword()
	if err != nil {
		return false
	}
	if !hasPassword {
		return true
	}
	c, err := r.Cookie(sessionCookie)
	return err == nil && a.store.ValidateSession(c.Value)
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// fail answers with the status matching err: 404, 400 for invalid input, 502
// when the mail server is unreachable or refuses, or 500 (logged, with a
// generic message).
func fail(w http.ResponseWriter, r *http.Request, err error) {
	var invalidInput *db.ValidationError
	var mailErr *mailError
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, "Not found")
	case errors.Is(err, imapsync.ErrGone):
		writeError(w, http.StatusGone, "This message is no longer on the server")
	case errors.As(err, &invalidInput):
		writeError(w, http.StatusBadRequest, invalidInput.Msg)
	case errors.As(err, &mailErr):
		log.Printf("%s %s: %v", r.Method, r.URL.Path, mailErr.err)
		writeError(w, http.StatusBadGateway, mailErr.err.Error())
	default:
		log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
		writeError(w, http.StatusInternalServerError, "Internal server error")
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

// decode reads a JSON request body into dst. On failure it answers the
// request and returns false.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	body := http.MaxBytesReader(w, r.Body, maxJSONBody)
	if err := json.NewDecoder(body).Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, "Request body too large")
		case errors.Is(err, io.EOF):
			writeError(w, http.StatusBadRequest, "Request body is empty")
		default:
			writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		}
		return false
	}
	return true
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, db.ErrNotFound
	}
	return id, nil
}
