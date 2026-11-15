package imapsync

import (
	"fmt"
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/bhr-tools/mail/internal/config"
	"github.com/bhr-tools/mail/internal/db"
)

// The person's folders: created, renamed and deleted from the app. Each
// change is made on the server at once, then the folders are listed again.

func refuse(format string, args ...any) error {
	return &db.ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// fullName joins a parent folder and a name with the server's separator.
func (a *Account) fullName(parent, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", refuse("The folder needs a name")
	}
	a.mu.Lock()
	delim := a.delim
	a.mu.Unlock()
	if parent == "" {
		return name, nil
	}
	if delim == 0 {
		return "", refuse("This server has no subfolders")
	}
	if strings.ContainsRune(name, delim) {
		return "", refuse("A folder name cannot hold %q here", string(delim))
	}
	return parent + string(delim) + name, nil
}

func (a *Account) exists(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.ContainsFunc(a.all, func(n string) bool { return strings.EqualFold(n, name) })
}

// customFolder returns the folder of one of the person's own roles.
func (a *Account) customFolder(role string) (*db.Folder, error) {
	if !config.IsCustom(role) {
		return nil, refuse("Only your own folders can be changed")
	}
	return a.store.FolderByRole(a.cfg.ID, role)
}

// CreateFolder makes a folder, inside parent (a role) or at the top, and
// returns its role.
func (a *Account) CreateFolder(parent, name string) (string, error) {
	parentName := ""
	if parent != "" {
		f, err := a.store.FolderByRole(a.cfg.ID, parent)
		if err != nil {
			return "", err
		}
		parentName = f.Name
	}
	full, err := a.fullName(parentName, name)
	if err != nil {
		return "", err
	}
	if a.exists(full) {
		return "", refuse("A folder named %s already exists", full)
	}
	err = a.reader.with(func(c *imapclient.Client) error {
		a.listing.Lock()
		defer a.listing.Unlock()
		if err := c.Create(full, nil).Wait(); err != nil {
			return err
		}
		// Some servers only list a folder once it is subscribed.
		_ = c.Subscribe(full).Wait()
		return a.resolveFoldersLocked(c)
	})
	if err != nil {
		return "", err
	}
	a.store.Changed()
	return config.CustomRole(full), nil
}

// RenameFolder gives a folder of the person another name, in the same
// parent, and returns its new role.
func (a *Account) RenameFolder(role, name string) (string, error) {
	f, err := a.customFolder(role)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	delim := a.delim
	a.mu.Unlock()
	parent := ""
	if delim != 0 {
		if i := strings.LastIndexByte(f.Name, byte(delim)); i >= 0 {
			parent = f.Name[:i]
		}
	}
	full, err := a.fullName(parent, name)
	if err != nil {
		return "", err
	}
	if full == f.Name {
		return role, nil
	}
	if !strings.EqualFold(full, f.Name) && a.exists(full) {
		return "", refuse("A folder named %s already exists", full)
	}
	err = a.reader.with(func(c *imapclient.Client) error {
		a.listing.Lock()
		defer a.listing.Unlock()
		if err := c.Rename(f.Name, full, nil).Wait(); err != nil {
			return err
		}
		_ = c.Unsubscribe(f.Name).Wait()
		_ = c.Subscribe(full).Wait()
		return a.resolveFoldersLocked(c)
	})
	if err != nil {
		return "", err
	}
	a.Wake(config.CustomRole(full))
	a.store.Changed()
	return config.CustomRole(full), nil
}

// DeleteFolder removes an empty folder of the person (a Gmail label even
// when it holds mail: the messages stay in All Mail).
func (a *Account) DeleteFolder(role string) error {
	f, err := a.customFolder(role)
	if err != nil {
		return err
	}
	a.mu.Lock()
	delim := a.delim
	a.mu.Unlock()
	if delim != 0 && slices.ContainsFunc(a.Folders(), func(n string) bool { return strings.HasPrefix(n, f.Name+string(delim)) }) {
		return refuse("%s holds other folders: delete them first", f.Name)
	}
	err = a.reader.with(func(c *imapclient.Client) error {
		a.listing.Lock()
		defer a.listing.Unlock()
		if !a.cfg.IsGmail() {
			sel, err := c.Select(f.Name, &imap.SelectOptions{ReadOnly: true}).Wait()
			if err != nil {
				return err
			}
			if err := c.Unselect().Wait(); err != nil {
				return err
			}
			if sel.NumMessages > 0 {
				return refuse("%s still holds %d messages: move them out first", f.Name, sel.NumMessages)
			}
		}
		if err := c.Delete(f.Name).Wait(); err != nil {
			return err
		}
		_ = c.Unsubscribe(f.Name).Wait()
		return a.resolveFoldersLocked(c)
	})
	if err != nil {
		return err
	}
	a.store.Changed()
	return nil
}
