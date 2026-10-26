package message

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-message/mail"

	"github.com/bhr-tools/mail/internal/db"
)

// Attachment is a file sent with a message.
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// Outgoing is a message to send or to save as a draft.
type Outgoing struct {
	From        db.Address
	To, Cc, Bcc []db.Address
	Subject     string
	Text        string
	InReplyTo   string
	References  []string
	Attachments []Attachment
	Date        time.Time
	// MessageID is generated when empty.
	MessageID string
	// KeepBcc writes Bcc in the header: drafts only.
	KeepBcc bool
}

func toMail(list []db.Address) []*mail.Address {
	out := make([]*mail.Address, 0, len(list))
	for _, a := range list {
		out = append(out, &mail.Address{Name: a.Name, Address: a.Address})
	}
	return out
}

// Build writes the message (RFC 5322). Bcc is left out of the header.
func (o *Outgoing) Build() ([]byte, string, error) {
	var h mail.Header
	if o.Date.IsZero() {
		o.Date = time.Now()
	}
	h.SetDate(o.Date)
	h.SetAddressList("From", toMail([]db.Address{o.From}))
	if len(o.To) > 0 {
		h.SetAddressList("To", toMail(o.To))
	}
	if len(o.Cc) > 0 {
		h.SetAddressList("Cc", toMail(o.Cc))
	}
	if o.KeepBcc && len(o.Bcc) > 0 {
		h.SetAddressList("Bcc", toMail(o.Bcc))
	}
	h.SetSubject(o.Subject)
	if o.MessageID != "" {
		h.SetMessageID(o.MessageID)
	} else {
		domain := "localhost"
		if at := strings.LastIndex(o.From.Address, "@"); at >= 0 {
			domain = o.From.Address[at+1:]
		}
		if err := h.GenerateMessageIDWithHostname(domain); err != nil {
			return nil, "", err
		}
	}
	if o.InReplyTo != "" {
		h.SetMsgIDList("In-Reply-To", []string{o.InReplyTo})
	}
	if len(o.References) > 0 {
		h.SetMsgIDList("References", o.References)
	}
	h.Set("MIME-Version", "1.0")
	id, _ := h.MessageID()

	text := normalizeNewlines(o.Text)
	var buf bytes.Buffer
	if len(o.Attachments) == 0 {
		h.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
		w, err := mail.CreateSingleInlineWriter(&buf, h)
		if err != nil {
			return nil, "", err
		}
		if _, err := io.WriteString(w, text); err != nil {
			return nil, "", err
		}
		if err := w.Close(); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), id, nil
	}

	mw, err := mail.CreateWriter(&buf, h)
	if err != nil {
		return nil, "", err
	}
	var th mail.InlineHeader
	th.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
	tw, err := mw.CreateSingleInline(th)
	if err != nil {
		return nil, "", err
	}
	if _, err := io.WriteString(tw, text); err != nil {
		return nil, "", err
	}
	if err := tw.Close(); err != nil {
		return nil, "", err
	}
	for _, a := range o.Attachments {
		var ah mail.AttachmentHeader
		ct := a.ContentType
		if ct == "" {
			ct = mime.TypeByExtension(filepath.Ext(a.Filename))
		}
		if ct == "" {
			ct = "application/octet-stream"
		}
		if mediaType, params, err := mime.ParseMediaType(ct); err == nil {
			ah.SetContentType(mediaType, params)
		} else {
			ah.SetContentType("application/octet-stream", nil)
		}
		ah.SetFilename(a.Filename)
		aw, err := mw.CreateAttachment(ah)
		if err != nil {
			return nil, "", err
		}
		if _, err := aw.Write(a.Data); err != nil {
			return nil, "", err
		}
		if err := aw.Close(); err != nil {
			return nil, "", err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), id, nil
}

func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// Draft prefills the composer.
type Draft struct {
	Account   string       `json:"account"`
	To        []db.Address `json:"to"`
	Cc        []db.Address `json:"cc"`
	Bcc       []db.Address `json:"bcc"`
	Subject   string       `json:"subject"`
	Text      string       `json:"text"`
	InReplyTo int64        `json:"in_reply_to,omitempty"`
	ForwardOf int64        `json:"forward_of,omitempty"`
	Forwarded []Part       `json:"forwarded,omitempty"`
	// DraftID is the draft being finished, replaced when saved or sent.
	DraftID int64 `json:"draft_id,omitempty"`
}

var replyPrefix = regexp.MustCompile(`(?i)^\s*((re|aw|fw|fwd|tr)\s*(\[\d+\])?\s*:\s*)+`)

func prefixed(prefix, subject string) string {
	return prefix + " " + strings.TrimSpace(replyPrefix.ReplaceAllString(subject, ""))
}

func quoteDate(t time.Time) string {
	return t.Format("Mon 2 Jan 2006 at 15:04")
}

// quote prefixes every line with "> ".
func quote(text string) string {
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n ")
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, ">") {
			lines[i] = ">" + l
		} else {
			lines[i] = "> " + l
		}
	}
	return strings.Join(lines, "\n")
}

func sameAddress(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// Reply prefills an answer to m. With all, the other recipients are kept
// in copy. own lists the addresses of the account, never answered to.
func Reply(m *db.Message, text string, own []string, all bool) Draft {
	isOwn := func(a db.Address) bool {
		for _, o := range own {
			if sameAddress(a.Address, o) {
				return true
			}
		}
		return false
	}
	d := Draft{Account: m.Account, Subject: prefixed("Re:", m.Subject), InReplyTo: m.ID}
	switch {
	case isOwn(m.From):
		// Answering one's own message: write to the same people again.
		d.To = append(d.To, m.To...)
	case len(m.ReplyTo) > 0:
		d.To = append(d.To, m.ReplyTo...)
	default:
		d.To = []db.Address{m.From}
	}
	if all {
		seen := map[string]bool{}
		for _, a := range d.To {
			seen[strings.ToLower(a.Address)] = true
		}
		for _, a := range append(append([]db.Address{}, m.To...), m.Cc...) {
			key := strings.ToLower(a.Address)
			if seen[key] || isOwn(a) {
				continue
			}
			seen[key] = true
			d.Cc = append(d.Cc, a)
		}
	}
	intro := fmt.Sprintf("On %s, %s wrote:", quoteDate(m.Date), m.From.String())
	d.Text = "\n\n" + intro + "\n" + quote(text) + "\n"
	return d
}

// Forward prefills the forward of m, with its attachments.
func Forward(m *db.Message, text string, parts []Part) Draft {
	d := Draft{Account: m.Account, Subject: prefixed("Fwd:", m.Subject), ForwardOf: m.ID}
	var b strings.Builder
	b.WriteString("\n\n---------- Forwarded message ----------\n")
	fmt.Fprintf(&b, "From: %s\n", formatAddress(m.From))
	fmt.Fprintf(&b, "Date: %s\n", quoteDate(m.Date))
	fmt.Fprintf(&b, "Subject: %s\n", m.Subject)
	if len(m.To) > 0 {
		fmt.Fprintf(&b, "To: %s\n", formatList(m.To))
	}
	if len(m.Cc) > 0 {
		fmt.Fprintf(&b, "Cc: %s\n", formatList(m.Cc))
	}
	b.WriteString("\n")
	b.WriteString(strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n "))
	b.WriteString("\n")
	d.Text = b.String()
	for _, p := range parts {
		if !p.Inline {
			d.Forwarded = append(d.Forwarded, p)
		}
	}
	return d
}

func formatAddress(a db.Address) string {
	if a.Name == "" {
		return a.Address
	}
	return fmt.Sprintf("%s <%s>", a.Name, a.Address)
}

func formatList(list []db.Address) string {
	parts := make([]string, len(list))
	for i, a := range list {
		parts[i] = formatAddress(a)
	}
	return strings.Join(parts, ", ")
}

// ReplyHeaders returns In-Reply-To and References for an answer to m.
func ReplyHeaders(m *db.Message) (string, []string) {
	if strings.HasPrefix(m.MessageID, "local.") {
		return "", nil
	}
	refs := append(append([]string{}, m.Refs...), m.MessageID)
	// Keep the first and the last ones when the chain grows long.
	if len(refs) > 20 {
		refs = append(refs[:1], refs[len(refs)-19:]...)
	}
	return m.MessageID, refs
}

// ParseAddressList reads addresses typed in the composer ("Name <a@b>, c@d").
func ParseAddressList(s string) ([]db.Address, error) {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), ","))
	if s == "" {
		return nil, nil
	}
	list, err := mail.ParseAddressList(s)
	if err != nil {
		return nil, fmt.Errorf("invalid address list %q", s)
	}
	out := make([]db.Address, len(list))
	for i, a := range list {
		out[i] = db.Address{Name: a.Name, Address: a.Address}
	}
	return out, nil
}

// ReadBcc returns the Bcc of a draft.
func ReadBcc(raw []byte) []db.Address {
	r, err := mail.CreateReader(bytes.NewReader(raw))
	if r == nil {
		return nil
	}
	_ = err
	list, _ := r.Header.AddressList("Bcc")
	out := make([]db.Address, 0, len(list))
	for _, a := range list {
		out = append(out, db.Address{Name: a.Name, Address: a.Address})
	}
	return out
}
