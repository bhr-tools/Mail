package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bhr-tools/mail/internal/config"
	"github.com/bhr-tools/mail/internal/db"
	"github.com/bhr-tools/mail/internal/demo"
	"github.com/bhr-tools/mail/internal/imapsync"
	"github.com/bhr-tools/mail/internal/imgproxy"
)

func setup(t *testing.T) (*httptest.Server, *db.Store) {
	t.Helper()
	srv, err := demo.Start()
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
	m := imapsync.New(store, []*config.Account{srv.Account("demo", "Demo")}, 365*24*time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m.Start(ctx)
	// Wait for the first synchronization.
	deadline := time.Now().Add(5 * time.Second)
	for {
		list, _ := store.Threads(db.Filter{Roles: []string{config.Inbox}})
		if len(list) == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("inbox not synchronized")
		}
		time.Sleep(50 * time.Millisecond)
	}
	api := httptest.NewServer(New(store, m, imgproxy.New(false), false).Handler())
	t.Cleanup(api.Close)
	return api, store
}

func get(t *testing.T, url string, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestLockedOnceAPasswordIsSet(t *testing.T) {
	srv, store := setup(t)
	if err := store.SetInitialPassword("correct horse"); err != nil {
		t.Fatal(err)
	}
	newsletter, err := store.Threads(db.Filter{Roles: []string{config.Inbox}, Match: `"books"*`})
	if err != nil || len(newsletter) != 1 {
		t.Fatalf("newsletter: %v %v", newsletter, err)
	}
	id := newsletter[0].Latest.ID
	for _, path := range []string{"/api/accounts", "/api/threads?view=inbox", "/api/img?u=https://example.org/a.png",
		"/api/messages/" + itoa(id), "/api/messages/" + itoa(id) + "/html"} {
		if res := get(t, srv.URL+path, nil); res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without a session: %d", path, res.StatusCode)
		}
	}

	res, err := http.Post(srv.URL+"/api/auth/login", "application/json", strings.NewReader(`{"password":"correct horse"}`))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("login: %v %v", res, err)
	}
	cookie := res.Cookies()[0]

	// The frame of a message: its own strict policy, no remote address.
	res = get(t, srv.URL+"/api/messages/"+itoa(id)+"/html", cookie)
	body, _ := io.ReadAll(res.Body)
	csp := res.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "img-src 'self' data:") || strings.Contains(csp, "script-src") {
		t.Errorf("frame CSP = %q", csp)
	}
	if res.Header.Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Errorf("frame options = %q", res.Header.Get("X-Frame-Options"))
	}
	if strings.Contains(string(body), "http://127.0.0.1") {
		t.Error("remote image address left in the frame")
	}

	var msg fullMessageJSON
	json.NewDecoder(get(t, srv.URL+"/api/messages/"+itoa(id), cookie).Body).Decode(&msg)
	if !msg.HTML || !msg.RemoteImages || msg.ImagesAllowed {
		t.Errorf("message = %+v", msg)
	}
}

func TestQuery(t *testing.T) {
	q := ParseQuery(`rapport from:camille "lab report" is:unread has:attachment in:all`)
	if q.From != "camille" || !q.Unread || !q.Attachments || len(q.Roles) != 0 || len(q.ExcludeRoles) != 0 {
		t.Errorf("filters = %+v", q.Filter)
	}
	if q.Match != `"rapport"* "lab report"` || q.Text != "rapport lab report" {
		t.Errorf("match = %q, text = %q", q.Match, q.Text)
	}
	if q := ParseQuery(`he said "hi`); q.Match == "" {
		t.Error("unbalanced quote dropped the query")
	}
	if q := ParseQuery("x"); len(q.Roles) != 0 || len(q.ExcludeRoles) != 1 {
		t.Errorf("default folders = %v minus %v", q.Roles, q.ExcludeRoles)
	}
	if q := ParseQuery("in:Receipts"); len(q.Roles) != 1 || q.Roles[0] != "folder:Receipts" {
		t.Errorf("own folder = %v", q.Roles)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
