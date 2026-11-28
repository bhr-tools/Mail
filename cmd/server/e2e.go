//go:build e2e

package main

import (
	"os"
	"strings"
	"time"

	"github.com/bhr-tools/mail/internal/config"
	"github.com/bhr-tools/mail/internal/testmail"
)

// MAIL_E2E_ACCOUNTS="perso:Perso,inp:INP" serves several sample accounts
// (each with its own copy of the sample mail); one "Demo" account otherwise.
func init() {
	sampleAccounts = func() ([]*config.Account, error) {
		spec := os.Getenv("MAIL_E2E_ACCOUNTS")
		if spec == "" {
			spec = "demo:Demo"
		}
		var accounts []*config.Account
		for _, item := range strings.Split(spec, ",") {
			id, name, _ := strings.Cut(item, ":")
			srv, err := testmail.Start()
			if err != nil {
				return nil, err
			}
			if err := srv.Seed(time.Now()); err != nil {
				return nil, err
			}
			accounts = append(accounts, srv.Account(id, name))
		}
		return accounts, nil
	}
}
