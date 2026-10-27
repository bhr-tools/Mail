package api

import (
	"fmt"
	"html"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bhr-tools/mail/internal/config"
	"github.com/bhr-tools/mail/internal/db"
	"github.com/bhr-tools/mail/internal/imapsync"
	"github.com/bhr-tools/mail/internal/imgproxy"
	"github.com/bhr-tools/mail/internal/message"
)

type folderJSON struct {
	Role   string `json:"role"`
	Name   string `json:"name"`
	Unread int    `json:"unread"`
	Total  int    `json:"total"`
}

type accountJSON struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Address string          `json:"address"`
	Status  imapsync.Status `json:"status"`
	Folders []folderJSON    `json:"folders"`
	Gmail   bool            `json:"gmail"`
}

// accounts returns every account with its folders and state.
func (a *API) accounts(w http.ResponseWriter, r *http.Request) {
	list := []accountJSON{}
	for _, acc := range a.mail.Accounts() {
		cfg := acc.Config()
		item := accountJSON{ID: cfg.ID, Name: cfg.Name, Address: cfg.Address, Status: acc.Status(), Folders: []folderJSON{}, Gmail: cfg.IsGmail()}
		folders, err := a.store.Folders(cfg.ID)
		if err != nil {
			fail(w, r, err)
			return
		}
		// The five roles in their order, then the person's folders by name.
		rank := func(role string) int {
			for i, r := range config.Roles {
				if r == role {
					return i
				}
			}
			return len(config.Roles)
		}
		sort.SliceStable(folders, func(i, j int) bool {
			ri, rj := rank(folders[i].Role), rank(folders[j].Role)
			if ri != rj {
				return ri < rj
			}
			return strings.ToLower(folders[i].Name) < strings.ToLower(folders[j].Name)
		})
		for _, f := range folders {
			unread, total, err := a.store.FolderCounts(f.ID)
			if err != nil {
				fail(w, r, err)
				return
			}
			item.Folders = append(item.Folders, folderJSON{Role: f.Role, Name: f.Name, Unread: unread, Total: total})
		}
		list = append(list, item)
	}
	// In the order arranged in the sidebar; accounts never moved keep the
	// configuration order, after the others.
	order, err := a.store.AccountOrder()
	if err != nil {
		fail(w, r, err)
		return
	}
	position := func(id string) int {
		if p, ok := order[id]; ok {
			return p
		}
		return len(order)
	}
	sort.SliceStable(list, func(i, j int) bool { return position(list[i].ID) < position(list[j].ID) })
	writeJSON(w, http.StatusOK, map[string]any{"accounts": list, "version": a.store.Version(), "no_auth": a.noAuth})
}

// setAccountOrder records the order of the accounts in the sidebar.
func (a *API) setAccountOrder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Accounts []string `json:"accounts"`
	}
	if !decode(w, r, &body) {
		return
	}
	for _, id := range body.Accounts {
		if a.mail.Account(id) == nil {
			writeError(w, http.StatusBadRequest, "unknown account "+id)
			return
		}
	}
	if err := a.store.SetAccountOrder(body.Accounts); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) account(r *http.Request, id string) (*imapsync.Account, error) {
	acc := a.mail.Account(id)
	if acc == nil {
		return nil, db.ErrNotFound
	}
	return acc, nil
}

// folders lists every folder of an account, as targets of a move.
func (a *API) folders(w http.ResponseWriter, r *http.Request) {
	acc, err := a.account(r, r.PathValue("account"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, acc.Folders())
}

func (a *API) syncAccount(w http.ResponseWriter, r *http.Request) {
	if id := r.PathValue("account"); id == "all" {
		for _, acc := range a.mail.Accounts() {
			acc.Wake()
		}
	} else if acc, err := a.account(r, id); err != nil {
		fail(w, r, err)
		return
	} else {
		acc.Wake()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------------------
// Lists
// ---------------------------------------------------------------------------

type messageJSON struct {
	ID          int64        `json:"id"`
	Account     string       `json:"account"`
	Role        string       `json:"role"`
	Subject     string       `json:"subject"`
	From        db.Address   `json:"from"`
	To          []db.Address `json:"to"`
	Cc          []db.Address `json:"cc"`
	ReplyTo     []db.Address `json:"reply_to"`
	Date        time.Time    `json:"date"`
	Seen        bool         `json:"seen"`
	Flagged     bool         `json:"flagged"`
	Answered    bool         `json:"answered"`
	Attachments bool         `json:"attachments"`
	Snippet     string       `json:"snippet"`
	Draft       bool         `json:"draft"`
}

func toMessageJSON(m *db.Message) messageJSON {
	nonNil := func(l []db.Address) []db.Address {
		if l == nil {
			return []db.Address{}
		}
		return l
	}
	return messageJSON{
		ID: m.ID, Account: m.Account, Role: m.Role, Subject: m.Subject, From: m.From,
		To: nonNil(m.To), Cc: nonNil(m.Cc), ReplyTo: nonNil(m.ReplyTo), Date: m.Date,
		Seen: m.Seen, Flagged: m.Flagged, Answered: m.Answered, Attachments: m.Attachments, Snippet: m.Snippet,
		Draft: m.Role == config.Drafts,
	}
}

type threadJSON struct {
	Account     string      `json:"account"`
	Thread      string      `json:"thread"`
	Latest      messageJSON `json:"latest"`
	People      []string    `json:"people"`
	Date        time.Time   `json:"date"`
	Count       int         `json:"count"`
	Unread      int         `json:"unread"`
	Flagged     bool        `json:"flagged"`
	Attachments bool        `json:"attachments"`
}

// viewFilter returns the filter of a folder view: one role, of one account
// or every account.
func (a *API) viewFilter(view, account string) (db.Filter, error) {
	if !isView(view) {
		return db.Filter{}, invalid("Unknown folder")
	}
	f := db.Filter{Roles: []string{view}, Account: account}
	if account != "" && a.mail.Account(account) == nil {
		return f, db.ErrNotFound
	}
	if view == config.Archive {
		// Gmail's All Mail holds the inbox and sent mail too.
		f.NotInInbox = true
		for _, acc := range a.mail.Accounts() {
			if acc.Config().IsGmail() {
				f.ExcludeFrom = append(f.ExcludeFrom, acc.Config().Address)
			}
		}
	}
	return f, nil
}

// isView reports whether a list can show a folder role: one of the five,
// or a folder made by the person.
func isView(role string) bool {
	return isRole(role) || config.IsCustom(role)
}

func isRole(role string) bool {
	for _, r := range config.Roles {
		if r == role {
			return true
		}
	}
	return false
}

func (a *API) writeThreads(w http.ResponseWriter, r *http.Request, f db.Filter) {
	f.Offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	f.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := a.store.Threads(f)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := make([]threadJSON, 0, len(list))
	for _, t := range list {
		if t.Latest == nil {
			continue
		}
		people := t.People
		if people == nil {
			people = []string{}
		}
		out = append(out, threadJSON{
			Account: t.Account, Thread: t.Thread, Latest: toMessageJSON(t.Latest), People: people,
			Date: t.Date, Count: t.Count, Unread: t.Unread, Flagged: t.Flagged, Attachments: t.Attachments,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// threads lists the conversations of a folder.
func (a *API) threads(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, err := a.viewFilter(q.Get("view"), q.Get("account"))
	if err != nil {
		fail(w, r, err)
		return
	}
	a.writeThreads(w, r, f)
}

// search lists the conversations matching a query.
func (a *API) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := ParseQuery(q.Get("q")).Filter
	if f.Account == "" {
		f.Account = q.Get("account")
	}
	if f.Account != "" && a.mail.Account(f.Account) == nil {
		// "account:" names an account by its ID or display name.
		for _, acc := range a.mail.Accounts() {
			if strings.EqualFold(acc.Config().Name, f.Account) {
				f.Account = acc.Config().ID
			}
		}
	}
	a.writeThreads(w, r, f)
}

// searchServer looks on the servers for mail older than the local copy.
func (a *API) searchServer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query   string `json:"q"`
		Account string `json:"account"`
	}
	if !decode(w, r, &req) {
		return
	}
	text := ParseQuery(req.Query).Text
	if text == "" {
		writeError(w, http.StatusBadRequest, "Type words to look for")
		return
	}
	found := 0
	for _, acc := range a.mail.Accounts() {
		if req.Account != "" && acc.Config().ID != req.Account {
			continue
		}
		n, err := acc.SearchServer(text, 50)
		if err != nil {
			fail(w, r, serverError(err))
			return
		}
		found += n
	}
	writeJSON(w, http.StatusOK, map[string]int{"found": found})
}

// thread returns the messages of a conversation.
func (a *API) thread(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	view := q.Get("view")
	msgs, err := a.store.ThreadMessages(q.Get("account"), q.Get("id"), view)
	if err != nil {
		fail(w, r, err)
		return
	}
	if len(msgs) == 0 {
		fail(w, r, db.ErrNotFound)
		return
	}
	out := make([]messageJSON, len(msgs))
	for i, m := range msgs {
		out[i] = toMessageJSON(m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"subject": msgs[len(msgs)-1].Subject, "messages": out})
}

// ---------------------------------------------------------------------------
// One message
// ---------------------------------------------------------------------------

// load returns a message and its whole content, downloading it if needed.
func (a *API) load(r *http.Request) (*db.Message, []byte, *message.Parsed, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, nil, nil, err
	}
	m, err := a.store.Message(id)
	if err != nil {
		return nil, nil, nil, err
	}
	acc, err := a.account(r, m.Account)
	if err != nil {
		return nil, nil, nil, err
	}
	raw, err := acc.LoadBody(m)
	if err != nil {
		return nil, nil, nil, serverError(err)
	}
	p, err := message.Parse(raw)
	if err != nil {
		p = &message.Parsed{Text: string(raw)}
	}
	return m, raw, p, nil
}

type fullMessageJSON struct {
	messageJSON
	Text          string         `json:"text"`
	HTML          bool           `json:"html"`
	RemoteImages  bool           `json:"remote_images"`
	ImagesAllowed bool           `json:"images_allowed"`
	Parts         []message.Part `json:"parts"`
}

// message returns a message's content: its text, whether it has HTML (shown
// from /html in a frame), and its attachments.
func (a *API) message(w http.ResponseWriter, r *http.Request) {
	m, _, p, err := a.load(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := fullMessageJSON{messageJSON: toMessageJSON(m), Text: p.PlainText(), Parts: []message.Part{}}
	if strings.TrimSpace(p.HTML) != "" {
		out.HTML = true
		out.RemoteImages = message.Sanitize(p.HTML, message.SanitizeOptions{}).RemoteImages
		out.ImagesAllowed = a.store.ImagesAllowed(m.From.Address)
	}
	for _, part := range p.Parts {
		if !part.Inline {
			out.Parts = append(out.Parts, part)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// frameCSP keeps the mail frame from running scripts or loading anything
// but its styles and the images of this server.
const frameCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; font-src data:; " +
	"frame-ancestors 'self'; base-uri 'none'; form-action 'none'"

// frameStyle is the paper the message is shown on: mail is designed for a
// light background, so it stays light in dark mode.
const frameStyle = `<style>
html { background: #fffaf3; color: #26233a; color-scheme: light; }
body { margin: 0; padding: 20px 24px; font: 15px/1.55 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; overflow-wrap: anywhere; }
img { max-width: 100%; height: auto; }
table { max-width: 100%; }
a { color: #286983; }
blockquote { margin: 0 0 0 4px; padding-left: 12px; border-left: 3px solid #dfdad9; color: #575279; }
pre { white-space: pre-wrap; }
</style>`

// messageHTML serves the HTML of a message as a page of its own, for the
// sandboxed frame of the reader. Remote images go through the proxy when
// asked for (images=1) or allowed for the sender.
func (a *API) messageHTML(w http.ResponseWriter, r *http.Request) {
	m, _, p, err := a.load(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	opts := message.SanitizeOptions{
		CID: func(cid string) string {
			return fmt.Sprintf("/api/messages/%d/cid/%s", m.ID, url.PathEscape(cid))
		},
	}
	if r.URL.Query().Get("images") == "1" || a.store.ImagesAllowed(m.From.Address) {
		opts.Remote = imgproxy.URL
	}
	body := p.HTML
	if strings.TrimSpace(body) == "" {
		body = "<pre>" + html.EscapeString(p.Text) + "</pre>"
	}
	safe := message.Sanitize(body, opts)
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", frameCSP)
	h.Set("X-Frame-Options", "SAMEORIGIN")
	fmt.Fprintf(w, "<!DOCTYPE html><html><head><meta charset=\"utf-8\"><base target=\"_blank\">%s</head><body>%s</body></html>", frameStyle, safe.HTML)
}

// servePart sends an attachment. Images, PDF and text open in the browser
// (in a sandbox), anything else is downloaded.
func servePart(w http.ResponseWriter, part message.Part, data []byte, download bool) {
	ct := part.ContentType
	inline := !download && (strings.HasPrefix(ct, "image/") || ct == "application/pdf" || ct == "text/plain")
	if !inline && !download {
		ct = "application/octet-stream"
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	if strings.HasPrefix(ct, "text/") {
		ct += "; charset=utf-8"
	}
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": part.Filename}))
	// Chrome's PDF viewer does not run in a sandbox.
	if ct != "application/pdf" {
		h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; sandbox")
	}
	h.Set("Cache-Control", "private, max-age=3600")
	w.Write(data)
}

func (a *API) part(w http.ResponseWriter, r *http.Request) {
	_, raw, _, err := a.load(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		fail(w, r, db.ErrNotFound)
		return
	}
	part, data, err := message.PartData(raw, index)
	if err != nil {
		fail(w, r, db.ErrNotFound)
		return
	}
	servePart(w, part, data, r.URL.Query().Get("download") == "1")
}

func (a *API) cid(w http.ResponseWriter, r *http.Request) {
	_, raw, p, err := a.load(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	part, ok := p.PartByContentID(r.PathValue("cid"))
	if !ok || !strings.HasPrefix(part.ContentType, "image/") {
		fail(w, r, db.ErrNotFound)
		return
	}
	_, data, err := message.PartData(raw, part.Index)
	if err != nil {
		fail(w, r, db.ErrNotFound)
		return
	}
	servePart(w, part, data, false)
}

// allowImages always shows the remote images of the sender of a message.
func (a *API) allowImages(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	m, err := a.store.Message(id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := a.store.AllowImages(m.From.Address); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sender": m.From.Address})
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

type threadRef struct {
	Account string `json:"account"`
	Thread  string `json:"thread"`
}

type actionRequest struct {
	Action string `json:"action"`
	// The conversations acted on, as shown in a folder (view).
	Threads []threadRef `json:"threads"`
	// Or single messages.
	Messages []int64 `json:"messages"`
	View     string  `json:"view"`
	Folder   string  `json:"folder"` // for "move"
}

// Actions: where a conversation goes, or which flag changes.
var moves = map[string]string{"done": config.Archive, "trash": config.Trash, "inbox": config.Inbox}
var flags = map[string]struct {
	flag  string
	value bool
}{
	"read": {"seen", true}, "unread": {"seen", false},
	"star": {"flagged", true}, "unstar": {"flagged", false},
}

// act applies an action to conversations or messages. It answers the IDs
// that undo it.
func (a *API) act(w http.ResponseWriter, r *http.Request) {
	var req actionRequest
	if !decode(w, r, &req) {
		return
	}
	if !isView(req.View) {
		fail(w, r, invalid("Unknown folder"))
		return
	}
	// Message-IDs per account.
	targets := map[string][]string{}
	for _, t := range req.Threads {
		roles := []string{req.View}
		ids, err := a.store.ThreadIDs(t.Account, t.Thread, roles)
		if err != nil {
			fail(w, r, err)
			return
		}
		// A star goes on the latest message only, like on a phone.
		if req.Action == "star" && len(ids) > 1 {
			msgs, err := a.store.ThreadMessages(t.Account, t.Thread, req.View)
			if err == nil && len(msgs) > 0 {
				ids = []string{msgs[len(msgs)-1].MessageID}
			}
		}
		targets[t.Account] = append(targets[t.Account], ids...)
	}
	if len(req.Messages) > 0 {
		msgs, err := a.store.MessagesByID(req.Messages)
		if err != nil {
			fail(w, r, err)
			return
		}
		for _, m := range msgs {
			targets[m.Account] = append(targets[m.Account], m.MessageID)
		}
	}
	var undo []int64
	for account, ids := range targets {
		if len(ids) == 0 {
			continue
		}
		op := db.Op{MessageIDs: ids, From: req.View}
		if to, ok := moves[req.Action]; ok {
			op.Kind, op.To = db.OpMove, to
		} else if f, ok := flags[req.Action]; ok {
			op.Kind, op.Flag, op.Value = db.OpFlag, f.flag, f.value
		} else if req.Action == "move" {
			op.Kind, op.ToFolder = db.OpMove, req.Folder
			if role := a.roleOf(account, req.Folder); role != "" {
				op.To, op.ToFolder = role, ""
			}
		} else if req.Action == "delete" {
			op.Kind = db.OpDelete
		} else {
			fail(w, r, invalid("Unknown action"))
			return
		}
		if op.Kind == db.OpMove && op.To == req.View {
			continue
		}
		id, err := a.store.Apply(account, op)
		if err != nil {
			fail(w, r, err)
			return
		}
		undo = append(undo, id)
		if acc := a.mail.Account(account); acc != nil {
			acc.Wake(req.View)
		}
	}
	if undo == nil {
		undo = []int64{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"undo": undo})
}

// roleOf returns the role of a folder name of an account, if it has one.
func (a *API) roleOf(account, name string) string {
	folders, err := a.store.Folders(account)
	if err != nil {
		return ""
	}
	for _, f := range folders {
		if f.Name == name {
			return f.Role
		}
	}
	return ""
}

func (a *API) undo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if !decode(w, r, &req) {
		return
	}
	for _, id := range req.IDs {
		account, err := a.store.Undo(id)
		if err != nil {
			fail(w, r, err)
			return
		}
		if acc := a.mail.Account(account); acc != nil {
			acc.Wake()
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
