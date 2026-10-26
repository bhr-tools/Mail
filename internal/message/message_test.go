package message

import (
	"strings"
	"testing"
	"time"

	"github.com/bhr-tools/mail/internal/db"
)

const newsletter = "From: Shop <news@shop.example>\r\n" +
	"To: ben@example.org\r\n" +
	"Subject: =?utf-8?q?Soldes_d=C3=A9but?=\r\n" +
	"Message-ID: <n1@shop.example>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/related; boundary=R\r\n" +
	"\r\n" +
	"--R\r\n" +
	"Content-Type: multipart/alternative; boundary=A\r\n" +
	"\r\n" +
	"--A\r\n" +
	"Content-Type: text/plain; charset=iso-8859-1\r\n" +
	"Content-Transfer-Encoding: quoted-printable\r\n" +
	"\r\n" +
	"Les soldes d=E9butent.\r\n" +
	"--A\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	`<html><head><style>.x{background:url("https://track.example/bg.png")}</style></head>` +
	`<body onload="evil()"><script>alert(1)</script>` +
	`<p class="x" style="color:red;background-image:url('https://track.example/s.png')">Hello</p>` +
	`<img src="https://track.example/pixel.gif" onerror="evil()" width="1">` +
	`<img src="cid:logo@shop">` +
	`<a href="javascript:evil()">bad</a> <a href="https://shop.example/">good</a>` +
	`<form action="https://phish.example"><input name="p"></form>` +
	`<iframe src="https://evil.example"></iframe>` +
	"</body></html>\r\n" +
	"--A--\r\n" +
	"--R\r\n" +
	"Content-Type: image/png\r\n" +
	"Content-ID: <logo@shop>\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"iVBORw0KGgo=\r\n" +
	"--R\r\n" +
	"Content-Type: application/pdf; name=\"facture.pdf\"\r\n" +
	"Content-Disposition: attachment; filename=\"facture.pdf\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"JVBERi0=\r\n" +
	"--R--\r\n"

func TestParse(t *testing.T) {
	p, err := Parse([]byte(newsletter))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(p.Text) != "Les soldes débutent." {
		t.Errorf("text = %q", p.Text)
	}
	if !strings.Contains(p.HTML, "Hello") {
		t.Errorf("html = %q", p.HTML)
	}
	if len(p.Parts) != 2 {
		t.Fatalf("parts = %+v", p.Parts)
	}
	logo, pdf := p.Parts[0], p.Parts[1]
	if !logo.Inline || logo.ContentID != "logo@shop" {
		t.Errorf("logo = %+v", logo)
	}
	if pdf.Inline || pdf.Filename != "facture.pdf" || pdf.ContentType != "application/pdf" {
		t.Errorf("pdf = %+v", pdf)
	}
	part, data, err := PartData([]byte(newsletter), pdf.Index)
	if err != nil || part.Filename != "facture.pdf" || string(data) != "%PDF-" {
		t.Errorf("part data = %+v %q %v", part, data, err)
	}
	if got := p.Snippet(); got != "Les soldes débutent." {
		t.Errorf("snippet = %q", got)
	}
}

func TestSanitizeBlocksRemoteContent(t *testing.T) {
	p, _ := Parse([]byte(newsletter))
	s := Sanitize(p.HTML, SanitizeOptions{CID: func(cid string) string { return "/api/messages/7/cid/" + cid }})
	if !s.RemoteImages {
		t.Error("remote images not reported")
	}
	for _, bad := range []string{"<script", "alert(1)", "onload", "onerror", "javascript:", "<form", "<input", "<iframe", "track.example", "phish.example"} {
		if strings.Contains(s.HTML, bad) {
			t.Errorf("sanitized HTML still contains %q:\n%s", bad, s.HTML)
		}
	}
	for _, good := range []string{"Hello", `color:red`, `class="x"`, `src="/api/messages/7/cid/logo@shop"`, `href="https://shop.example/"`, `target="_blank"`, "<style>"} {
		if !strings.Contains(s.HTML, good) {
			t.Errorf("sanitized HTML lost %q:\n%s", good, s.HTML)
		}
	}
}

func TestSanitizeProxiesRemoteImages(t *testing.T) {
	p, _ := Parse([]byte(newsletter))
	proxy := func(u string) string { return "/api/img?u=" + strings.ReplaceAll(u, "/", "%2F") }
	s := Sanitize(p.HTML, SanitizeOptions{Remote: proxy})
	for _, want := range []string{
		`src="/api/img?u=https:%2F%2Ftrack.example%2Fpixel.gif"`,
		`url(/api/img?u=https:%2F%2Ftrack.example%2Fbg.png)`,
		`url(/api/img?u=https:%2F%2Ftrack.example%2Fs.png)`,
	} {
		if !strings.Contains(s.HTML, want) {
			t.Errorf("missing %q in:\n%s", want, s.HTML)
		}
	}
	if strings.Contains(s.HTML, `"https://track.example`) {
		t.Errorf("direct remote address left:\n%s", s.HTML)
	}
}

func TestReplyAndForward(t *testing.T) {
	m := &db.Message{
		ID: 3, Account: "perso", MessageID: "b@x", Refs: []string{"a@x"},
		Subject: "Re: Lunch", From: db.Address{Name: "Alice", Address: "alice@x.org"},
		To:   []db.Address{{Address: "ben@example.org"}, {Name: "Carol", Address: "carol@x.org"}},
		Cc:   []db.Address{{Address: "BEN@example.org"}, {Address: "dave@x.org"}},
		Date: time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC),
	}
	own := []string{"ben@example.org"}
	d := Reply(m, "See you at noon.\n> earlier", own, true)
	if d.Subject != "Re: Lunch" {
		t.Errorf("subject = %q", d.Subject)
	}
	if len(d.To) != 1 || d.To[0].Address != "alice@x.org" {
		t.Errorf("to = %+v", d.To)
	}
	if len(d.Cc) != 2 || d.Cc[0].Address != "carol@x.org" || d.Cc[1].Address != "dave@x.org" {
		t.Errorf("cc = %+v", d.Cc)
	}
	if !strings.Contains(d.Text, "On Mon 26 Oct 2026 at 09:30, Alice wrote:\n> See you at noon.\n>> earlier") {
		t.Errorf("text = %q", d.Text)
	}
	inReplyTo, refs := ReplyHeaders(m)
	if inReplyTo != "b@x" || strings.Join(refs, " ") != "a@x b@x" {
		t.Errorf("headers = %q %v", inReplyTo, refs)
	}

	f := Forward(m, "Body", []Part{{Index: 2, Filename: "a.pdf"}, {Index: 3, Inline: true}})
	if f.Subject != "Fwd: Lunch" || len(f.Forwarded) != 1 || !strings.Contains(f.Text, "From: Alice <alice@x.org>") {
		t.Errorf("forward = %+v", f)
	}
}

func TestBuild(t *testing.T) {
	o := &Outgoing{
		From:        db.Address{Name: "Ben", Address: "ben@example.org"},
		To:          []db.Address{{Name: "Alice", Address: "alice@x.org"}},
		Bcc:         []db.Address{{Address: "secret@x.org"}},
		Subject:     "Réunion",
		Text:        "Bonjour,\nà demain.",
		InReplyTo:   "b@x",
		References:  []string{"a@x", "b@x"},
		Attachments: []Attachment{{Filename: "notes.txt", Data: []byte("hello")}},
	}
	raw, id, err := o.Build()
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if id == "" || !strings.Contains(s, "In-Reply-To: <b@x>") || !strings.Contains(s, "References: <a@x> <b@x>") {
		t.Errorf("headers:\n%s", s)
	}
	if strings.Contains(s, "secret@x.org") {
		t.Error("Bcc leaked into the message")
	}
	p, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(p.Text) != "Bonjour,\r\nà demain." && strings.TrimSpace(p.Text) != "Bonjour,\nà demain." {
		t.Errorf("text = %q", p.Text)
	}
	if len(p.Parts) != 1 || p.Parts[0].Filename != "notes.txt" {
		t.Errorf("parts = %+v", p.Parts)
	}
	if h := ReadHeader(raw); h.MessageID != id || h.InReplyTo != "b@x" {
		t.Errorf("read header = %+v", h)
	}
}

func TestParseAddressList(t *testing.T) {
	list, err := ParseAddressList(`Alice <alice@x.org>, bob@y.org,`)
	if err != nil || len(list) != 2 || list[0].Name != "Alice" || list[1].Address != "bob@y.org" {
		t.Errorf("got %+v %v", list, err)
	}
	if _, err := ParseAddressList("not an address"); err == nil {
		t.Error("accepted an invalid address")
	}
}
