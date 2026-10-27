// Package imapsync keeps the local copy of each account in step with its
// IMAP server: one connection waits in IDLE on the inbox for new mail,
// another synchronizes the folders and replays the actions done in the app,
// a third answers what the app needs right away (opening a message).
package imapsync

import (
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	gomessage "github.com/emersion/go-message"

	"github.com/bhr-tools/mail/internal/config"
)

const dialTimeout = 20 * time.Second

var wordDecoder = &mime.WordDecoder{CharsetReader: gomessage.CharsetReader}

// refusedRetry is how long a refused password rests before it is tried
// again: servers lock accounts (or ask for a captcha) after repeated
// failures, and the three connections of an account would otherwise try it
// every few minutes.
const refusedRetry = time.Hour

// refusals remembers the last password the server refused.
type refusals struct {
	mu       sync.Mutex
	password string
	until    time.Time
	err      error
}

// check returns the refusal of password, if it is still resting.
func (r *refusals) check(password string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil && r.password == password && time.Now().Before(r.until) {
		return r.err
	}
	return nil
}

func (r *refusals) note(password string, err error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.password, r.until = password, time.Now().Add(refusedRetry)
	r.err = fmt.Errorf("%w; check the password, not tried again before %s", err, r.until.Format("15:04"))
	return r.err
}

// dial opens an authenticated connection.
func dial(a *config.Account, refused *refusals, handler *imapclient.UnilateralDataHandler) (*imapclient.Client, error) {
	password, err := a.ReadPassword()
	if err != nil {
		return nil, err
	}
	if err := refused.check(password); err != nil {
		return nil, err
	}
	options := &imapclient.Options{
		WordDecoder:           wordDecoder,
		UnilateralDataHandler: handler,
		Dialer:                &net.Dialer{Timeout: dialTimeout},
		TLSConfig:             &tls.Config{ServerName: a.IMAP.Host},
	}
	var c *imapclient.Client
	switch a.IMAP.Security {
	case config.TLS:
		c, err = imapclient.DialTLS(a.IMAP.Address(), options)
	case config.StartTLS:
		c, err = imapclient.DialStartTLS(a.IMAP.Address(), options)
	default:
		c, err = imapclient.DialInsecure(a.IMAP.Address(), options)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", a.IMAP.Address(), err)
	}
	if err := c.Login(a.Username, password).Wait(); err != nil {
		c.Close()
		var imapErr *imap.Error
		if errors.As(err, &imapErr) {
			return nil, refused.note(password, fmt.Errorf("login refused by %s: %s", a.IMAP.Host, imapErr.Text))
		}
		return nil, fmt.Errorf("login: %w", err)
	}
	return c, nil
}

// conn is a lazily opened connection, reopened after an error. Commands
// on it are serialized.
type conn struct {
	account *config.Account
	refused *refusals
	mu      sync.Mutex
	client  *imapclient.Client
}

// with runs fn on the connection, opening it if needed. A failed command
// closes the connection; the next call opens a new one.
func (c *conn) with(fn func(*imapclient.Client) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		client, err := dial(c.account, c.refused, nil)
		if err != nil {
			return err
		}
		c.client = client
	}
	err := fn(c.client)
	var imapErr *imap.Error
	if err != nil && !errors.As(err, &imapErr) {
		// A network error: the connection is likely broken.
		c.client.Close()
		c.client = nil
	}
	return err
}

func (c *conn) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		c.client.Close()
		c.client = nil
	}
}
