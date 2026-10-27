//go:build e2e

package main

import (
	"time"

	"github.com/bhr-tools/mail/internal/config"
	"github.com/bhr-tools/mail/internal/testmail"
)

func init() {
	sampleAccounts = func() ([]*config.Account, error) {
		srv, err := testmail.Start()
		if err != nil {
			return nil, err
		}
		if err := srv.Seed(time.Now()); err != nil {
			return nil, err
		}
		return []*config.Account{srv.Account("demo", "Demo")}, nil
	}
}
