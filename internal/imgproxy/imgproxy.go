// Package imgproxy loads the remote images of a message on behalf of the
// browser, so senders see the server's address instead of the reader's, and
// no cookie or referrer. Only public addresses are reached: a message cannot
// use the server to probe the local network.
package imgproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

const (
	maxImageSize = 15 << 20
	timeout      = 15 * time.Second
	userAgent    = "Mozilla/5.0 (compatible; Mail image proxy)"
)

// Proxy serves /api/img?u=<address>.
type Proxy struct {
	client *http.Client
}

// ErrForbidden is returned for addresses the proxy refuses to reach.
var ErrForbidden = errors.New("address not allowed")

// New returns a proxy. allowPrivate lets it reach loopback and private
// addresses (the demo serves its images locally).
func New(allowPrivate bool) *Proxy {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		// The check runs on the address actually dialed, after DNS: a name
		// resolving to a private address is refused too.
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || (!allowPrivate && !public(ip)) {
				return ErrForbidden
			}
			return nil
		},
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          20,
		IdleConnTimeout:       time.Minute,
	}
	return &Proxy{client: &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return checkURL(req.URL)
		},
	}}
}

func public(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() ||
		// Carrier-grade NAT and the IPv4 "this network" block.
		(ip.To4() != nil && (ip.To4()[0] == 0 || (ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64))))
}

func checkURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return ErrForbidden
	}
	if u.User != nil || u.Host == "" {
		return ErrForbidden
	}
	return nil
}

// URL returns the proxied address of a remote image.
func URL(remote string) string {
	return "/api/img?u=" + url.QueryEscape(remote)
}

// ServeHTTP fetches the image named by the u parameter.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target, err := url.Parse(r.URL.Query().Get("u"))
	if err != nil || checkURL(target) != nil {
		http.Error(w, "invalid image address", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		http.Error(w, "invalid image address", http.StatusBadRequest)
		return
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "image/avif,image/webp,image/png,image/svg+xml,image/*;q=0.8")
	res, err := p.client.Do(req)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, ErrForbidden) {
			status = http.StatusForbidden
		}
		http.Error(w, "image unavailable", status)
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		http.Error(w, "image unavailable", http.StatusBadGateway)
		return
	}
	ct := strings.ToLower(strings.TrimSpace(strings.Split(res.Header.Get("Content-Type"), ";")[0]))
	if !strings.HasPrefix(ct, "image/") {
		http.Error(w, "not an image", http.StatusUnsupportedMediaType)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", "private, max-age=86400")
	// An SVG opened on its own must not run scripts.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	if res.ContentLength > maxImageSize {
		http.Error(w, "image too large", http.StatusRequestEntityTooLarge)
		return
	}
	io.Copy(w, io.LimitReader(res.Body, maxImageSize))
}
