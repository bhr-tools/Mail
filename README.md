# Mail

Minimal, keyboard-driven mail, built on [aerc](https://aerc-mail.org).

- **Rosé Pine**: Rosé Pine (dark) and Rosé Pine Dawn (light) stylesets. The
  `mail` command follows `~/.config/theme_state` while aerc runs, so a theme
  change applies live.
- **Same reflexes as Get Done and yazi**: `j`/`k` to browse, `l` to open, `h`
  to go back, `d` for done (archive), `Space` to select, `?` for the
  cheatsheet.
- **Quiet**: no bell or notification on new mail.
- **Accounts stay in your config**: any `accounts.email.accounts.<name>` with
  `aerc.enable = true`. The keys that depend on an account's folders (trash,
  drafts) are generated from it.

## Keyboard shortcuts

| Key | Action |
|---|---|
| `j` / `k`, `gg` / `G` | Next / previous, first / last message |
| `l`, `e` or `Enter` | Open the message (in drafts: resume it) |
| `h`, `q` or `Esc` | Back to the list |
| `J` / `K` | Next / previous folder (in a message: next / previous message) |
| `gi` / `gf` | Go to the inbox / pick a folder |
| `d` | Done: archive |
| `D` | Move to the trash |
| `Space` | Select / unselect (then act on the whole selection) |
| `m` | Move to a folder |
| `u` | Toggle read / unread |
| `s` | Toggle the star |
| `c` | Compose |
| `r` / `R` | Reply / reply all |
| `f` | Forward |
| `/` | Search |
| `&` `é` `"` | Switch account |
| `gx` / `yy` | Open / copy a link |
| `o` | Open the part (HTML, attachment) with the default application |
| `?` | Cheatsheet |

## Installation (home-manager)

```nix
inputs.mail.url = "path:/home/ben/Dev/Mail";

# home.nix
imports = [ inputs.mail.homeManagerModules.default ];
programs.mail.enable = true;

accounts.email.accounts.Perso = {
  primary = true;
  flavor = "gmail.com";
  address = "me@gmail.com";
  realName = "Me";
  passwordCommand = "...";
  folders = { inbox = "INBOX"; sent = null; drafts = "[Gmail]/Drafts"; trash = "[Gmail]/Trash"; };
  aerc = { enable = true; extraAccounts.archive = "[Gmail]/All Mail"; };
};
```

`mail` opens aerc; the desktop entry runs it in kitty and handles `mailto:`
links.
