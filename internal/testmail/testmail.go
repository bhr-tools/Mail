// Package testmail runs an IMAP and an SMTP server in memory, filled with sample
// mail, so the app can be tried and tested without a real account. Mail sent
// to the demo address lands in its inbox.
package testmail

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"

	"github.com/bhr-tools/mail/internal/config"
)

const (
	Address  = "ben@demo.test"
	password = "demo"
)

// Server is a running pair of mail servers in memory.
type Server struct {
	user     *imapmemserver.User
	imapAddr string
	smtpAddr string
	// ImageURL is a remote image served locally, for newsletters.
	ImageURL string

	mu   sync.Mutex
	sent [][]byte
}

// Start runs the servers on loopback ports.
func Start() (*Server, error) {
	s := &Server{user: imapmemserver.NewUser(Address, password)}
	for _, name := range []string{"INBOX", "Archive", "Sent", "Drafts", "Trash", "Junk", "Receipts", "Projects", "Projects/Robotics"} {
		if err := s.user.Create(name, nil); err != nil {
			return nil, err
		}
	}
	mem := imapmemserver.New()
	mem.AddUser(s.user)
	imapSrv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		InsecureAuth: true,
	})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s.imapAddr = l.Addr().String()
	go imapSrv.Serve(l)

	smtpSrv := gosmtp.NewServer(backend{s})
	smtpSrv.Domain = "demo.test"
	smtpSrv.AllowInsecureAuth = true
	sl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s.smtpAddr = sl.Addr().String()
	go smtpSrv.Serve(sl)

	hl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s.ImageURL = "http://" + hl.Addr().String() + "/banner.svg"
	go http.Serve(hl, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		io.WriteString(w, banner)
	}))
	return s, nil
}

const banner = `<svg xmlns="http://www.w3.org/2000/svg" width="560" height="120" viewBox="0 0 560 120">` +
	`<rect width="560" height="120" rx="12" fill="#286983"/>` +
	`<text x="32" y="72" font-family="sans-serif" font-size="34" fill="#faf4ed">Autumn reading list</text></svg>`

// Account returns the settings of the sample account.
func (s *Server) Account(id, name string) *config.Account {
	host, port, _ := net.SplitHostPort(s.imapAddr)
	smtpHost, smtpPort, _ := net.SplitHostPort(s.smtpAddr)
	var ip, sp int
	fmt.Sscan(port, &ip)
	fmt.Sscan(smtpPort, &sp)
	return &config.Account{
		ID: id, Name: name, Address: Address, RealName: "Ben", Username: Address, Password: password,
		IMAP:    config.Server{Host: host, Port: ip, Security: config.Insecure},
		SMTP:    config.Server{Host: smtpHost, Port: sp, Security: config.Insecure},
		Folders: map[string]string{},
	}
}

type literal struct{ *bytes.Reader }

func (l literal) Size() int64 { return l.Reader.Size() }

// Deliver adds a message to a folder.
func (s *Server) Deliver(folder string, raw []byte, flags ...imap.Flag) error {
	_, err := s.user.Append(folder, literal{bytes.NewReader(raw)}, &imap.AppendOptions{Flags: flags, Time: time.Now()})
	return err
}

// Sent returns the messages received by the SMTP server.
func (s *Server) Sent() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte{}, s.sent...)
}

type backend struct{ s *Server }

func (b backend) NewSession(*gosmtp.Conn) (gosmtp.Session, error) {
	return &session{s: b.s}, nil
}

type session struct {
	s     *Server
	auth  bool
	toBen bool
}

func (*session) AuthMechanisms() []string { return []string{sasl.Plain} }

func (se *session) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, username, pw string) error {
		if username != Address || pw != password {
			return gosmtp.ErrAuthFailed
		}
		se.auth = true
		return nil
	}), nil
}

func (se *session) Mail(string, *gosmtp.MailOptions) error {
	if !se.auth {
		return gosmtp.ErrAuthRequired
	}
	return nil
}

func (se *session) Rcpt(to string, _ *gosmtp.RcptOptions) error {
	if strings.EqualFold(to, Address) {
		se.toBen = true
	}
	return nil
}

func (se *session) Data(r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	se.s.mu.Lock()
	se.s.sent = append(se.s.sent, raw)
	se.s.mu.Unlock()
	if se.toBen {
		return se.s.Deliver("INBOX", raw)
	}
	return nil
}

func (se *session) Reset()        { se.toBen = false }
func (se *session) Logout() error { return nil }

// Seed fills the mailbox with sample mail, dated relative to now.
func (s *Server) Seed(now time.Time) error {
	day := 24 * time.Hour
	type sample struct {
		folder string
		raw    string
		flags  []imap.Flag
	}
	samples := []sample{
		{"Archive", plain(now.Add(-40*day), "Camille Martin <camille@inp.test>", Address, "Project kickoff", "kickoff@inp.test", "",
			"Hi Ben,\n\nThe project kickoff is moved to Thursday 10:00, room B204.\n\nCamille"), []imap.Flag{imap.FlagSeen}},
		{"Archive", plain(now.Add(-12*day), "Bank <noreply@bank.test>", Address, "Your October statement", "stmt@bank.test", "",
			"Your statement is available in your online space."), []imap.Flag{imap.FlagSeen}},
		{"INBOX", plain(now.Add(-3*day), "Camille Martin <camille@inp.test>", Address, "Lab report review", "lab1@inp.test", "",
			"Hi Ben,\n\nCould you review section 3 of the lab report before Friday?\nThe draft is on the shared drive.\n\nThanks,\nCamille"), []imap.Flag{imap.FlagSeen}},
		{"Sent", plain(now.Add(-2*day), "Ben <"+Address+">", "Camille Martin <camille@inp.test>", "Re: Lab report review", "lab2@demo.test", "lab1@inp.test",
			"Sure, I will read it tonight.\n\nOn Fri, Camille Martin wrote:\n> Could you review section 3 of the lab report before Friday?"), []imap.Flag{imap.FlagSeen}},
		{"INBOX", plain(now.Add(-26*time.Hour), "Camille Martin <camille@inp.test>", Address, "Re: Lab report review", "lab3@inp.test", "lab2@demo.test",
			"Great, thanks! I added two figures since.\n\n> Sure, I will read it tonight."), nil},
		{"INBOX", withAttachment(now.Add(-5*time.Hour), "Association AEN7 <bureau@aen7.test>", Address, "Invoice for the gala hall",
			"gala@aen7.test", "Hello,\n\nPlease find the invoice for the hall attached.\n\nThe board", "invoice-gala.pdf", "%PDF-1.4\n% demo invoice\n"), nil},
		{"INBOX", newsletter(now.Add(-2*time.Hour), s.ImageURL), nil},
		{"INBOX", plain(now.Add(-40*time.Minute), "Léa Dubois <lea@friends.test>", Address, "Climbing on Saturday?", "climb@friends.test", "",
			"Hey! A few of us are going to the climbing gym on Saturday afternoon. Want to join?\n\nLéa"), []imap.Flag{imap.FlagFlagged}},
		{"Drafts", plain(now.Add(-6*time.Hour), "Ben <"+Address+">", "Léa Dubois <lea@friends.test>", "Photos from the hike", "draft1@demo.test", "",
			"Here are the photos from"), []imap.Flag{imap.FlagSeen, imap.FlagDraft}},
		{"Projects/Robotics", plain(now.Add(-9*day), "Hugo Bernard <hugo@club.test>", Address, "Motor driver datasheet", "robot1@club.test", "",
			"Hi Ben,\n\nHere is the reference of the motor driver we talked about: DRV8833.\n\nHugo"), nil},
		{"Junk", plain(now.Add(-1*day), "Prize <win@spam.test>", Address, "You won!", "spam1@spam.test", "",
			"Claim it now."), nil},
		{"Trash", plain(now.Add(-4*day), "Deals <promo@shop.test>", Address, "Last chance: 70% off", "promo@shop.test", "",
			"Everything must go."), []imap.Flag{imap.FlagSeen}},
	}
	for _, m := range samples {
		if err := s.Deliver(m.folder, []byte(m.raw), m.flags...); err != nil {
			return err
		}
	}
	return nil
}

func header(date time.Time, from, to, subject, id, inReplyTo string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Date: %s\r\nFrom: %s\r\nTo: %s\r\nSubject: %s\r\nMessage-ID: <%s>\r\nMIME-Version: 1.0\r\n",
		date.Format(time.RFC1123Z), from, to, subject, id)
	if inReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: <%s>\r\nReferences: <%s>\r\n", inReplyTo, inReplyTo)
	}
	return b.String()
}

func crlf(s string) string {
	return strings.ReplaceAll(s, "\n", "\r\n")
}

func plain(date time.Time, from, to, subject, id, inReplyTo, text string) string {
	return header(date, from, to, subject, id, inReplyTo) +
		"Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + crlf(text) + "\r\n"
}

func withAttachment(date time.Time, from, to, subject, id, text, filename, data string) string {
	return header(date, from, to, subject, id, "") +
		"Content-Type: multipart/mixed; boundary=MIX\r\n\r\n" +
		"--MIX\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + crlf(text) + "\r\n" +
		"--MIX\r\nContent-Type: application/pdf; name=\"" + filename + "\"\r\nContent-Disposition: attachment; filename=\"" + filename + "\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString([]byte(data)) + "\r\n--MIX--\r\n"
}

func newsletter(date time.Time, image string) string {
	html := `<html><head><style>.card{max-width:560px;margin:0 auto;font-family:Georgia,serif}` +
		`.btn{display:inline-block;padding:10px 18px;background:#d7827e;color:#fff;border-radius:6px;text-decoration:none}</style></head>` +
		`<body><div class="card"><img src="` + image + `" width="560" height="120" alt="Autumn reading list">` +
		`<h1>Five books for the evenings</h1><p>Short days, long reads. Our picks for October, from essays on slow ` +
		`technology to a novel set in a lighthouse.</p><p><a class="btn" href="https://books.test/autumn">See the list</a></p>` +
		`<img src="` + strings.Replace(image, "banner.svg", "open.gif", 1) + `" width="1" height="1" alt="">` +
		`<p style="color:#797593;font-size:12px">You receive this letter because you subscribed on books.test.</p></div></body></html>`
	return header(date, "Bookshop Letters <letters@books.test>", Address, "Five books for the evenings", "news-oct@books.test", "") +
		"Content-Type: multipart/alternative; boundary=ALT\r\n\r\n" +
		"--ALT\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nFive books for the evenings. See https://books.test/autumn\r\n" +
		"--ALT\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" + html + "\r\n--ALT--\r\n"
}
