package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The file written by the NixOS module.
const nixFile = `{"accounts":[
 {"id":"perso","name":"Perso","address":"vault.bhr.42@gmail.com","real_name":"Ben","username":"vault.bhr.42@gmail.com",
  "password_file":"PASSWORD","folders":{},
  "imap":{"host":"imap.gmail.com","port":993,"security":"tls"},"smtp":{"host":"smtp.gmail.com","port":465,"security":"tls"}},
 {"id":"inp","name":"INP","address":"benjamin.hourlier@etu.toulouse-inp.fr","real_name":"","username":"benjamin.hourlier@etu.toulouse-inp.fr",
  "password_file":"PASSWORD","folders":{"archive":"Archive"},
  "imap":{"host":"partage.toulouse-inp.fr","port":993,"security":"tls"},"smtp":{"host":"partage.toulouse-inp.fr","port":587,"security":"starttls"}}
]}`

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "pw")
	os.WriteFile(pw, []byte("app password\n"), 0o600)
	path := filepath.Join(dir, "accounts.json")
	os.WriteFile(path, []byte(strings.ReplaceAll(nixFile, "PASSWORD", pw)), 0o600)

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	perso, inp := c.Accounts[0], c.Accounts[1]
	if !perso.IsGmail() || perso.ShouldSaveSent() {
		t.Error("Gmail saves sent mail itself")
	}
	if inp.IsGmail() || !inp.ShouldSaveSent() || inp.Folders[Archive] != "Archive" || inp.SMTP.Security != StartTLS {
		t.Errorf("inp = %+v", inp)
	}
	if got, _ := perso.ReadPassword(); got != "app password" {
		t.Errorf("password = %q", got)
	}
	if perso.From() != `"Ben" <vault.bhr.42@gmail.com>` && perso.From() != "Ben <vault.bhr.42@gmail.com>" {
		t.Errorf("from = %q", perso.From())
	}

	bad := &Config{Accounts: []*Account{{ID: "Perso!", Address: "a@b"}}}
	if bad.Validate() == nil {
		t.Error("accepted an invalid id")
	}
}
