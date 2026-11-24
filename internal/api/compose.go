package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"

	"github.com/bhr-tools/mail/internal/addy"
	"github.com/bhr-tools/mail/internal/config"
	"github.com/bhr-tools/mail/internal/db"
	"github.com/bhr-tools/mail/internal/message"
	"github.com/bhr-tools/mail/internal/smtp"
)

// compose prefills the composer: a reply (mode=reply or all), a forward, or
// a draft to finish (mode=draft).
func (a *API) compose(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id, err := strconv.ParseInt(q.Get("id"), 10, 64)
	if err != nil {
		fail(w, r, invalid("Missing message"))
		return
	}
	m, err := a.store.Message(id)
	if err != nil {
		fail(w, r, err)
		return
	}
	acc, err := a.account(r, m.Account)
	if err != nil {
		fail(w, r, err)
		return
	}
	raw, err := acc.LoadBody(m)
	if err != nil {
		fail(w, r, serverError(err))
		return
	}
	p, err := message.Parse(raw)
	if err != nil {
		p = &message.Parsed{Text: string(raw)}
	}
	cfg := acc.Config()
	var d message.Draft
	switch q.Get("mode") {
	case "reply", "all":
		d = message.Reply(m, p.PlainText(), []string{cfg.Address}, q.Get("mode") == "all")
	case "forward":
		d = message.Forward(m, p.PlainText(), p.Parts)
	case "draft":
		d = message.Draft{Account: m.Account, To: m.To, Cc: m.Cc, Bcc: message.ReadBcc(raw), Subject: m.Subject, Text: p.PlainText(), DraftID: m.ID}
		if m.InReplyTo != "" {
			if parent := a.findByMessageID(m.Account, m.InReplyTo); parent != nil {
				d.InReplyTo = parent.ID
			}
		}
		for _, part := range p.Parts {
			if !part.Inline {
				d.Forwarded = append(d.Forwarded, part)
			}
		}
		if len(d.Forwarded) > 0 {
			d.ForwardOf = m.ID
		}
	default:
		fail(w, r, invalid("Unknown mode"))
		return
	}
	a.readAliases(&d, m)
	if d.To == nil {
		d.To = []db.Address{}
	}
	if d.Cc == nil {
		d.Cc = []db.Address{}
	}
	if d.Bcc == nil {
		d.Bcc = []db.Address{}
	}
	writeJSON(w, http.StatusOK, d)
}

// readAliases shows the real people behind addy.io reverse aliases, and
// picks the alias to send from: the one of these reverse aliases, or else the
// alias the original message was sent to.
func (a *API) readAliases(d *message.Draft, orig *db.Message) {
	settings, err := a.store.AddySettings()
	if err != nil || !settings.Enabled(d.Account) {
		return
	}
	domain := settings.Domain
	var fromReverse, fromAlias string
	read := func(list []db.Address) []db.Address {
		out := []db.Address{}
		for _, addr := range list {
			if alias, recipient, ok := addy.Decode(addr.Address, domain); ok {
				if fromReverse == "" {
					fromReverse = alias
				}
				name := addr.Name
				// addy.io names the sender after its address.
				if strings.Contains(name, "@") {
					name = ""
				}
				out = append(out, db.Address{Name: name, Address: recipient})
			} else if addy.InDomain(addr.Address, domain) {
				// One's own alias: not a person to write to.
				if fromAlias == "" {
					fromAlias = addy.Local(addr.Address)
				}
			} else {
				out = append(out, addr)
			}
		}
		return out
	}
	d.To, d.Cc, d.Bcc = read(d.To), read(d.Cc), read(d.Bcc)
	if d.InReplyTo != 0 && orig != nil && fromAlias == "" {
		read(append(append([]db.Address{}, orig.To...), orig.Cc...))
	}
	switch {
	case fromReverse != "":
		d.Alias = fromReverse
	case d.InReplyTo != 0 || d.DraftID != 0:
		d.Alias = fromAlias
	}
}

// sendingAlias rewrites the recipients of o to the addy.io reverse aliases
// that send from alias. Without an alias, o is left as it is.
func (a *API) sendingAlias(account, alias string, o *message.Outgoing) error {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return nil
	}
	settings, err := a.store.AddySettings()
	if err != nil {
		return err
	}
	if !settings.Enabled(account) {
		return invalid("This account does not send through addy.io")
	}
	if !addy.ValidAlias(alias) {
		return invalid("Alias: letters, digits, dots, hyphens and underscores only")
	}
	for _, list := range []*[]db.Address{&o.To, &o.Cc, &o.Bcc} {
		for i, addr := range *list {
			(*list)[i].Address = addy.Encode(alias, settings.Domain, addr.Address)
		}
	}
	return nil
}

func (a *API) findByMessageID(account, messageID string) *db.Message {
	m, err := a.store.MessageByMessageID(account, messageID)
	if err != nil {
		return nil
	}
	return m
}

// outgoingRequest is the "message" field of a send or a draft.
type outgoingRequest struct {
	Account   string `json:"account"`
	To        string `json:"to"`
	Cc        string `json:"cc"`
	Bcc       string `json:"bcc"`
	Subject   string `json:"subject"`
	Text      string `json:"text"`
	InReplyTo int64  `json:"in_reply_to"`
	ForwardOf int64  `json:"forward_of"`
	// Attachments of ForwardOf to send again (part indexes).
	ForwardParts []int `json:"forward_parts"`
	// DraftID is the draft this message replaces.
	DraftID int64 `json:"draft_id"`
	// Alias is the root of the addy.io alias to send from ("": none).
	Alias string `json:"alias"`

	// people are the recipients as typed, before addy.io rewrites them.
	people []db.Address
}

// readOutgoing reads a multipart form: the message as JSON, and files.
func (a *API) readOutgoing(w http.ResponseWriter, r *http.Request) (*outgoingRequest, *message.Outgoing, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, nil, invalid("The attachments are too large (35 MB at most)")
		}
		return nil, nil, invalid("Invalid form")
	}
	var req outgoingRequest
	if err := json.Unmarshal([]byte(r.FormValue("message")), &req); err != nil {
		return nil, nil, invalid("Invalid message")
	}
	acc := a.mail.Account(req.Account)
	if acc == nil {
		return nil, nil, invalid("Unknown account")
	}
	cfg := acc.Config()
	o := &message.Outgoing{From: db.Address{Name: cfg.RealName, Address: cfg.Address}, Subject: strings.TrimSpace(req.Subject), Text: req.Text}
	var err error
	for _, field := range []struct {
		value string
		dst   *[]db.Address
		label string
	}{{req.To, &o.To, "To"}, {req.Cc, &o.Cc, "Cc"}, {req.Bcc, &o.Bcc, "Bcc"}} {
		if *field.dst, err = message.ParseAddressList(field.value); err != nil {
			return nil, nil, invalid(field.label + ": " + err.Error())
		}
	}
	for _, list := range [][]db.Address{o.To, o.Cc, o.Bcc} {
		req.people = append(req.people, list...)
	}
	if err := a.sendingAlias(req.Account, req.Alias, o); err != nil {
		return nil, nil, err
	}
	if req.InReplyTo != 0 {
		if parent, err := a.store.Message(req.InReplyTo); err == nil {
			o.InReplyTo, o.References = message.ReplyHeaders(parent)
		}
	}
	if req.ForwardOf != 0 && len(req.ForwardParts) > 0 {
		orig, err := a.store.Message(req.ForwardOf)
		if err != nil {
			return nil, nil, err
		}
		raw, err := acc.LoadBody(orig)
		if err != nil {
			return nil, nil, serverError(err)
		}
		for _, index := range req.ForwardParts {
			part, data, err := message.PartData(raw, index)
			if err != nil {
				return nil, nil, invalid("An attachment of the original message is missing")
			}
			o.Attachments = append(o.Attachments, message.Attachment{Filename: part.Filename, ContentType: part.ContentType, Data: data})
		}
	}
	if r.MultipartForm != nil {
		for _, fh := range r.MultipartForm.File["files"] {
			f, err := fh.Open()
			if err != nil {
				return nil, nil, err
			}
			data, err := io.ReadAll(f)
			f.Close()
			if err != nil {
				return nil, nil, err
			}
			o.Attachments = append(o.Attachments, message.Attachment{Filename: fh.Filename, ContentType: fh.Header.Get("Content-Type"), Data: data})
		}
	}
	return &req, o, nil
}

// send sends a message, keeps a copy in Sent (unless the server does) and
// marks the message answered.
func (a *API) send(w http.ResponseWriter, r *http.Request) {
	req, o, err := a.readOutgoing(w, r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var recipients []string
	for _, list := range [][]db.Address{o.To, o.Cc, o.Bcc} {
		for _, addr := range list {
			recipients = append(recipients, addr.Address)
		}
	}
	if len(recipients) == 0 {
		fail(w, r, invalid("Add at least one recipient"))
		return
	}
	acc := a.mail.Account(req.Account)
	raw, _, err := o.Build()
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := smtp.Send(acc.Config(), recipients, raw); err != nil {
		fail(w, r, serverError(err))
		return
	}
	// The message is sent: what follows must not report a failure.
	if acc.Config().ShouldSaveSent() {
		if err := acc.Append(config.Sent, raw, []imap.Flag{imap.FlagSeen}); err != nil {
			log.Printf("%s: keep a copy of sent mail: %v", req.Account, err)
		}
	} else {
		acc.Wake(config.Sent)
	}
	if req.InReplyTo != 0 {
		if parent, err := a.store.Message(req.InReplyTo); err == nil && parent.Account == req.Account {
			a.store.Apply(req.Account, db.Op{Kind: db.OpFlag, MessageIDs: []string{parent.MessageID}, From: parent.Role, Flag: "answered", Value: true})
		}
	}
	if req.DraftID != 0 {
		a.deleteDraft(req.Account, req.DraftID)
	}
	if err := a.store.NoteSent(req.people); err != nil {
		log.Printf("contacts: %v", err)
	}
	acc.Wake(config.Inbox, config.Drafts)
	writeJSON(w, http.StatusOK, map[string]bool{"sent": true})
}

func (a *API) deleteDraft(account string, id int64) {
	draft, err := a.store.Message(id)
	if err != nil || draft.Account != account || draft.Role != config.Drafts {
		return
	}
	if _, err := a.store.Apply(account, db.Op{Kind: db.OpDelete, MessageIDs: []string{draft.MessageID}, From: config.Drafts}); err != nil {
		log.Printf("%s: delete draft: %v", account, err)
	}
}

// saveDraft stores the message in the Drafts folder, in place of the draft
// it was opened from.
func (a *API) saveDraft(w http.ResponseWriter, r *http.Request) {
	req, o, err := a.readOutgoing(w, r)
	if err != nil {
		fail(w, r, err)
		return
	}
	acc := a.mail.Account(req.Account)
	if _, err := a.store.FolderByRole(req.Account, config.Drafts); err != nil {
		fail(w, r, invalid("This account has no Drafts folder"))
		return
	}
	// Bcc is kept in a draft, so it is not lost when finishing it.
	o.KeepBcc = true
	raw, _, err := o.Build()
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := acc.Append(config.Drafts, raw, []imap.Flag{imap.FlagSeen, imap.FlagDraft}); err != nil {
		fail(w, r, serverError(err))
		return
	}
	if req.DraftID != 0 {
		a.deleteDraft(req.Account, req.DraftID)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"saved": true})
}

// contacts completes addresses.
func (a *API) contacts(w http.ResponseWriter, r *http.Request) {
	const limit = 8
	list, err := a.store.Contacts(r.URL.Query().Get("q"), 2*limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	// Senders reached through addy.io are suggested by their real address;
	// one's own aliases are left out.
	settings, _ := a.store.AddySettings()
	out := []db.Contact{}
	seen := map[string]bool{}
	for _, c := range list {
		if _, recipient, ok := addy.Decode(c.Address, settings.Domain); ok {
			c.Address = recipient
			if strings.Contains(c.Name, "@") {
				c.Name = ""
			}
		} else if addy.InDomain(c.Address, settings.Domain) {
			continue
		}
		key := strings.ToLower(c.Address)
		if seen[key] || len(out) == limit {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, out)
}
