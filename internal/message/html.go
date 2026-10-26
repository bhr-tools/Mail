package message

import (
	"html"
	"net/url"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
)

// SanitizeOptions says how to rewrite the addresses of images.
type SanitizeOptions struct {
	// Remote returns the address of a remote image through the image proxy,
	// or "" while remote images are blocked.
	Remote func(u string) string
	// CID returns the address of an image attached to the message.
	CID func(cid string) string
}

// Sanitized is HTML safe to show in the mail frame.
type Sanitized struct {
	HTML string
	// RemoteImages reports images (or CSS backgrounds) loaded from the
	// internet: blocked, or going through the proxy.
	RemoteImages bool
}

// policy keeps what mail layouts need (tables, inline styles, <style>
// blocks) and drops everything active: scripts, forms, frames, event
// handlers. The frame also runs without scripts, under a CSP that only
// allows images from this server.
func policy(rewrite func(*url.URL)) *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowElements("center", "font", "style", "span", "div", "section", "header", "footer", "article", "main", "u", "s", "small", "big", "mark")
	p.AllowAttrs("style", "class", "dir", "lang", "align", "valign", "width", "height", "bgcolor", "color", "border",
		"cellpadding", "cellspacing", "face", "size").Globally()
	p.AllowAttrs("role", "aria-hidden").Globally()
	p.AllowURLSchemes("http", "https", "mailto", "tel", "cid")
	p.AllowDataURIImages()
	// <style> content is CSS, kept as is: the frame's CSP keeps it from
	// loading anything that is not proxied.
	p.AllowUnsafe(true)
	p.AllowElementsContent("style")
	p.RequireNoFollowOnLinks(false)
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	p.RewriteSrc(rewrite)
	return p
}

var cssURL = regexp.MustCompile(`(?i)url\(\s*([^)]*?)\s*\)`)

// cssQuotes are the quotes around a CSS url(), escaped or not (inside a
// style attribute).
var cssQuotes = strings.NewReplacer(`&#34;`, ``, `&#39;`, ``, `&quot;`, ``, `"`, ``, `'`, ``)
var cssImport = regexp.MustCompile(`(?i)@import[^;]*;?`)

// Sanitize makes the HTML of a message safe to show.
func Sanitize(raw string, opts SanitizeOptions) Sanitized {
	var out Sanitized
	rewriteURL := func(u string) string {
		u = strings.TrimSpace(u)
		lower := strings.ToLower(u)
		switch {
		case strings.HasPrefix(lower, "cid:"):
			if opts.CID != nil {
				return opts.CID(strings.Trim(u[4:], "<>"))
			}
			return ""
		case strings.HasPrefix(lower, "data:image/"):
			return u
		case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"), strings.HasPrefix(lower, "//"):
			out.RemoteImages = true
			if strings.HasPrefix(u, "//") {
				u = "https:" + u
			}
			if opts.Remote != nil {
				return opts.Remote(u)
			}
			return ""
		}
		return ""
	}
	p := policy(func(u *url.URL) {
		next := rewriteURL(u.String())
		parsed, err := url.Parse(next)
		if next == "" || err != nil {
			*u = url.URL{}
			return
		}
		*u = *parsed
	})
	safe := p.Sanitize(raw)
	// Images and fonts named in CSS: inline styles and <style> blocks.
	safe = cssImport.ReplaceAllString(safe, "")
	safe = cssURL.ReplaceAllStringFunc(safe, func(m string) string {
		inner := html.UnescapeString(cssQuotes.Replace(cssURL.FindStringSubmatch(m)[1]))
		next := rewriteURL(inner)
		if next == "" {
			return "none"
		}
		// Proxied and attached images have no quotes nor ampersands.
		return "url(" + next + ")"
	})
	out.HTML = safe
	return out
}
