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

// dial opens an authenticated connection.
func dial(a *config.Account, handler *imapclient.UnilateralDataHandler) (*imapclient.Client, error) {
	password, err := a.ReadPassword()
	if err != nil {
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
			return nil, fmt.Errorf("login refused by %s: %s", a.IMAP.Host, imapErr.Text)
		}
		return nil, fmt.Errorf("login: %w", err)
	}
	return c, nil
}

// conn is a lazily opened connection, reopened after an error. Commands
// on it are serialized.
type conn struct {
	account *config.Account
	mu      sync.Mutex
	client  *imapclient.Client
}

// with runs fn on the connection, opening it if needed. A failed command
// closes the connection; the next call opens a new one.
func (c *conn) with(fn func(*imapclient.Client) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		client, err := dial(c.account, nil)
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
