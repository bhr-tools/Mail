// Package smtp sends messages through an account's SMTP server.
package smtp

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"

	"github.com/bhr-tools/mail/internal/config"
)

// Send delivers raw (a whole message) to the recipients.
func Send(a *config.Account, recipients []string, raw []byte) error {
	if len(recipients) == 0 {
		return errors.New("no recipient")
	}
	password, err := a.ReadPassword()
	if err != nil {
		return err
	}
	tlsConfig := &tls.Config{ServerName: a.SMTP.Host}
	var c *gosmtp.Client
	switch a.SMTP.Security {
	case config.TLS:
		c, err = gosmtp.DialTLS(a.SMTP.Address(), tlsConfig)
	case config.StartTLS:
		c, err = gosmtp.DialStartTLS(a.SMTP.Address(), tlsConfig)
	default:
		c, err = gosmtp.Dial(a.SMTP.Address())
	}
	if err != nil {
		return fmt.Errorf("connect to %s: %w", a.SMTP.Address(), err)
	}
	defer c.Close()
	if err := c.Auth(sasl.NewPlainClient("", a.Username, password)); err != nil {
		return fmt.Errorf("login refused by %s: %w", a.SMTP.Host, err)
	}
	if err := c.SendMail(a.Address, recipients, bytes.NewReader(raw)); err != nil {
		return fmt.Errorf("sending refused by %s: %w", a.SMTP.Host, err)
	}
	return c.Quit()
}
