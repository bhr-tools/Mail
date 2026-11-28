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

	"github.com/bhr-tools/kit/auth"
	"github.com/bhr-tools/mail/internal/config"
	"github.com/bhr-tools/mail/internal/db"
	"github.com/bhr-tools/mail/internal/imapsync"
	"github.com/bhr-tools/mail/internal/imgproxy"
	"github.com/bhr-tools/mail/internal/testmail"
)

func setup(t *testing.T) (*httptest.Server, *db.Store) {
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
	a, err := New(store, m, imgproxy.New(false), false)
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(a.Handler())
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
	locks, err := auth.New(store.DB(), sessionCookie, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := locks.SetInitialPassword("correct horse"); err != nil {
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
	if q := ParseQuery("x"); len(q.Roles) != 0 || len(q.ExcludeRoles) != 2 {
		t.Errorf("default folders = %v minus %v", q.Roles, q.ExcludeRoles)
	}
	if q := ParseQuery("in:Receipts"); len(q.Roles) != 1 || q.Roles[0] != "folder:Receipts" {
		t.Errorf("own folder = %v", q.Roles)
	}
	if q := ParseQuery(`in:"My receipts"`); len(q.Roles) != 1 || q.Roles[0] != "folder:My receipts" {
		t.Errorf("quoted folder = %v", q.Roles)
	}
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"newer:7d":         now.AddDate(0, 0, -7),
		"newer:2w":         now.AddDate(0, 0, -14),
		"newer:1m":         now.AddDate(0, -1, 0),
		"newer:1y":         now.AddDate(-1, 0, 0),
		"after:2026-01-15": time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
	} {
		if q := parseQuery(in, now); !q.After.Equal(want) || q.Match != "" {
			t.Errorf("%s: after = %v, match = %q", in, q.After, q.Match)
		}
	}
	// Days are included: on: is one day, before: keeps its own day.
	jan := func(d int) time.Time { return time.Date(2026, 1, d, 0, 0, 0, 0, time.UTC) }
	if q := parseQuery("on:2026-01-15", now); !q.After.Equal(jan(15)) || !q.Before.Equal(jan(16)) {
		t.Errorf("on: %v – %v", q.After, q.Before)
	}
	if q := parseQuery("after:2026-01-10 before:2026-01-15", now); !q.After.Equal(jan(10)) || !q.Before.Equal(jan(16)) || q.Text != "" {
		t.Errorf("range: %v – %v, text %q", q.After, q.Before, q.Text)
	}
	// A filter it can't read is looked for as words.
	if q := parseQuery("newer:soon", now); !q.After.IsZero() || q.Text != "newer:soon" {
		t.Errorf("bad span: after = %v, text = %q", q.After, q.Text)
	}
}

func TestSearchByDate(t *testing.T) {
	_, store := setup(t)
	count := func(q string) int {
		t.Helper()
		list, err := store.Threads(ParseQuery(q).Filter)
		if err != nil {
			t.Fatal(err)
		}
		return len(list)
	}
	// The lab report thread is from the last few days; the kickoff is 40 days old.
	if n := count("in:archive"); n < 2 {
		t.Fatalf("archive = %d", n)
	}
	if n := count("in:archive newer:30d"); n != count("in:archive")-1 {
		t.Errorf("archive newer than 30 days = %d", n)
	}
	if n := count("lab newer:7d"); n != 1 {
		t.Errorf("lab newer than 7 days = %d", n)
	}
	if n := count("lab newer:1d"); n != 0 {
		t.Errorf("lab newer than a day = %d", n)
	}
	monthAgo := time.Now().AddDate(0, 0, -30).Format(time.DateOnly)
	if n := count("in:archive before:" + monthAgo); n != 1 {
		t.Errorf("archive before %s = %d", monthAgo, n)
	}
	kickoff := time.Now().AddDate(0, 0, -40).Format(time.DateOnly)
	if n := count("in:archive on:" + kickoff); n != 1 {
		t.Errorf("archive on %s = %d", kickoff, n)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestAccountOrder(t *testing.T) {
	srv, _ := setup(t)
	req, _ := http.NewRequest("PUT", srv.URL+"/api/accounts/order", strings.NewReader(`{"accounts":["nope"]}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown account: %v %v", res, err)
	}
	req, _ = http.NewRequest("PUT", srv.URL+"/api/accounts/order", strings.NewReader(`{"accounts":["demo"]}`))
	req.Header.Set("Content-Type", "application/json")
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != http.StatusNoContent {
		t.Fatalf("order: %v %v", res, err)
	}
}
