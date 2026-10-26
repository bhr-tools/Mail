package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/bhr-tools/mail/internal/db"
)

// failedLoginDelay slows down password guessing.
const failedLoginDelay = time.Second

func (a *API) authStatus(w http.ResponseWriter, r *http.Request) {
	if a.noAuth {
		writeJSON(w, http.StatusOK, map[string]bool{"has_password": false, "authenticated": true, "no_auth": true})
		return
	}
	hasPassword, err := a.store.HasPassword()
	if err != nil {
		fail(w, r, err)
		return
	}
	authenticated := false
	if hasPassword {
		if c, err := r.Cookie(sessionCookie); err == nil {
			authenticated = a.store.ValidateSession(c.Value)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"has_password": hasPassword, "authenticated": authenticated, "no_auth": false})
}

type passwordRequest struct {
	Password    string `json:"password"`
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

func (a *API) authSetup(w http.ResponseWriter, r *http.Request) {
	if a.noAuth {
		writeError(w, http.StatusBadRequest, "Authentication is disabled on this instance")
		return
	}
	var req passwordRequest
	if !decode(w, r, &req) {
		return
	}
	if err := a.store.SetInitialPassword(req.Password); err != nil {
		fail(w, r, err)
		return
	}
	a.startSession(w, r)
}

func (a *API) authLogin(w http.ResponseWriter, r *http.Request) {
	var req passwordRequest
	if !decode(w, r, &req) {
		return
	}
	if !a.checkPassword(w, r, req.Password) {
		return
	}
	a.startSession(w, r)
}

func (a *API) authLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = a.store.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r)})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (a *API) authChangePassword(w http.ResponseWriter, r *http.Request) {
	if a.noAuth {
		writeError(w, http.StatusBadRequest, "Authentication is disabled on this instance")
		return
	}
	var req passwordRequest
	if !decode(w, r, &req) {
		return
	}
	if hasPassword, err := a.store.HasPassword(); err != nil {
		fail(w, r, err)
		return
	} else if hasPassword && !a.checkPassword(w, r, req.OldPassword) {
		return
	}
	if err := a.store.SetPassword(req.NewPassword); err != nil {
		fail(w, r, err)
		return
	}
	// Every other session was signed out; keep this one signed in.
	a.startSession(w, r)
}

// checkPassword verifies the master password, answering 401 when it is wrong.
func (a *API) checkPassword(w http.ResponseWriter, r *http.Request, password string) bool {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	ok, err := a.store.VerifyPassword(password)
	if err != nil {
		fail(w, r, err)
		return false
	}
	if !ok {
		time.Sleep(failedLoginDelay)
		writeError(w, http.StatusUnauthorized, "Incorrect password")
		return false
	}
	return true
}

func (a *API) startSession(w http.ResponseWriter, r *http.Request) {
	token, err := a.store.CreateSession()
	if err != nil {
		fail(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(db.SessionLifetime.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

// isHTTPS reports whether the browser reached us over HTTPS, directly or
// through a reverse proxy.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
