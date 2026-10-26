// Package message reads messages (text, HTML, attachments), makes their
// HTML safe to show, and writes new ones.
package message

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"regexp"
	"strings"
	"unicode/utf8"

	gomessage "github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // non UTF-8 messages
	"github.com/emersion/go-message/mail"
	"github.com/microcosm-cc/bluemonday"
)

// maxPartSize caps what is read of one part (larger parts are truncated).
const maxPartSize = 50 << 20

// Part is an attachment, or an image shown inside the HTML.
type Part struct {
	Index       int    `json:"index"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
	ContentID   string `json:"-"`
	// Inline parts are images referenced by the HTML (cid:).
	Inline bool `json:"inline"`
}

// Parsed is the readable content of a message.
type Parsed struct {
	Text  string
	HTML  string // as sent: run it through Sanitize before showing it
	Parts []Part
}

// Parse reads a whole message.
func Parse(raw []byte) (*Parsed, error) {
	p := &Parsed{}
	err := walk(raw, func(index int, h gomessage.Header, body []byte) {
		mediaType, params, _ := h.ContentType()
		disposition, dparams, _ := h.ContentDisposition()
		filename := dparams["filename"]
		if filename == "" {
			filename = params["name"]
		}
		filename = decodeWord(filename)
		cid := strings.Trim(h.Get("Content-Id"), "<> ")
		attachment := disposition == "attachment" || (filename != "" && !strings.HasPrefix(mediaType, "text/"))
		switch {
		case mediaType == "text/plain" && !attachment && p.Text == "":
			p.Text = string(body)
		case mediaType == "text/html" && !attachment && p.HTML == "":
			p.HTML = string(body)
		case strings.HasPrefix(mediaType, "multipart/"):
		default:
			if filename == "" {
				filename = defaultName(mediaType, index)
			}
			p.Parts = append(p.Parts, Part{
				Index:       index,
				Filename:    filename,
				ContentType: mediaType,
				Size:        len(body),
				ContentID:   cid,
				Inline:      cid != "" && disposition != "attachment" && strings.HasPrefix(mediaType, "image/"),
			})
		}
	})
	if err != nil {
		return nil, err
	}
	// Inline images the HTML does not use are attachments after all.
	for i := range p.Parts {
		if p.Parts[i].Inline && !strings.Contains(p.HTML, "cid:"+p.Parts[i].ContentID) {
			p.Parts[i].Inline = false
		}
	}
	return p, nil
}

// PartData returns the decoded content of the part at index.
func PartData(raw []byte, index int) (Part, []byte, error) {
	p, err := Parse(raw)
	if err != nil {
		return Part{}, nil, err
	}
	for _, part := range p.Parts {
		if part.Index != index {
			continue
		}
		var data []byte
		err := walk(raw, func(i int, _ gomessage.Header, body []byte) {
			if i == index {
				data = body
			}
		})
		return part, data, err
	}
	return Part{}, nil, ErrNoPart
}

// ErrNoPart is returned for an unknown part.
var ErrNoPart = errors.New("no such part")

// PartByContentID returns the index of the part with a Content-ID.
func (p *Parsed) PartByContentID(cid string) (Part, bool) {
	for _, part := range p.Parts {
		if part.ContentID == cid {
			return part, true
		}
	}
	return Part{}, false
}

// walk calls fn for every leaf part of a message, numbered from 1 in order.
func walk(raw []byte, fn func(index int, h gomessage.Header, body []byte)) error {
	entity, err := gomessage.Read(bytes.NewReader(raw))
	if err != nil && !gomessage.IsUnknownCharset(err) && !gomessage.IsUnknownEncoding(err) {
		return err
	}
	index := 0
	return entity.Walk(func(_ []int, e *gomessage.Entity, err error) error {
		if err != nil && !gomessage.IsUnknownCharset(err) && !gomessage.IsUnknownEncoding(err) {
			return err
		}
		if mr := e.MultipartReader(); mr != nil {
			return nil
		}
		index++
		body, err := io.ReadAll(io.LimitReader(e.Body, maxPartSize))
		if err != nil && !gomessage.IsUnknownCharset(err) {
			// A broken part should not hide the rest of the message.
			body = nil
		}
		fn(index, e.Header, body)
		return nil
	})
}

var wordDecoder = &mime.WordDecoder{CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
	return gomessage.CharsetReader(charset, input)
}}

func decodeWord(s string) string {
	if d, err := wordDecoder.DecodeHeader(s); err == nil {
		return d
	}
	return s
}

func defaultName(mediaType string, index int) string {
	ext := ""
	if exts, _ := mime.ExtensionsByType(mediaType); len(exts) > 0 {
		ext = exts[0]
	}
	return fmt.Sprintf("part-%d%s", index, ext)
}

// Header is the part of a message's header shown in the app.
type Header struct {
	MessageID  string
	InReplyTo  string
	References []string
}

// ReadHeader returns the identifiers of a message.
func ReadHeader(raw []byte) Header {
	var h Header
	r, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil && r == nil {
		return h
	}
	h.MessageID, _ = r.Header.MessageID()
	if ids, _ := r.Header.MsgIDList("In-Reply-To"); len(ids) > 0 {
		h.InReplyTo = ids[0]
	}
	h.References, _ = r.Header.MsgIDList("References")
	return h
}

var (
	textPolicy  = bluemonday.StrictPolicy()
	spaces      = regexp.MustCompile(`\s+`)
	blockTags   = regexp.MustCompile(`(?i)<(br|/p|/div|/tr|/li|/h[1-6]|/table)[^>]*>`)
	hiddenParts = regexp.MustCompile(`(?is)<(style|script|head|title)[^>]*>.*?</(style|script|head|title)>`)
)

// PlainText returns the text of a message: its text part, or its HTML part
// stripped of markup.
func (p *Parsed) PlainText() string {
	if strings.TrimSpace(p.Text) != "" {
		return p.Text
	}
	return HTMLToText(p.HTML)
}

// HTMLToText strips markup, keeping line breaks between blocks.
func HTMLToText(s string) string {
	s = hiddenParts.ReplaceAllString(s, "")
	s = blockTags.ReplaceAllString(s, "\n")
	s = textPolicy.Sanitize(s)
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, l := range lines {
		l = strings.TrimSpace(strings.Map(func(r rune) rune {
			if r == ' ' || r == '‌' || r == '​' || r == '͏' {
				return ' '
			}
			return r
		}, l))
		if l != "" || (len(out) > 0 && out[len(out)-1] != "") {
			out = append(out, l)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// Snippet returns the start of the new text of a message (quotes left out),
// on one line.
func (p *Parsed) Snippet() string {
	var kept []string
	for _, line := range strings.Split(p.PlainText(), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, ">") {
			continue
		}
		if quoteIntro.MatchString(t) {
			break
		}
		kept = append(kept, t)
	}
	s := strings.TrimSpace(spaces.ReplaceAllString(strings.Join(kept, " "), " "))
	return truncate(s, 200)
}

// quoteIntro matches the line before a quoted reply ("On … wrote:").
var quoteIntro = regexp.MustCompile(`(?i)^(on .{4,120} wrote:|le .{4,120} a écrit\s?:|-{2,}\s*(original message|forwarded message|message d'origine)\s*-{2,})$`)

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}
