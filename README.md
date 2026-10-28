# Mail

A minimalist, self-hosted mail client in the spirit of Get Done, Focus and Calendar:

- **Your accounts, one place:** Gmail (with an app password), any IMAP/SMTP server. Every inbox together, or one account at a time. Your own folders and Gmail labels are there too.
- **Minimal sidebar:** each account folds to its inbox; unfold it to reach the other folders. Drag an account by its name to reorder the accounts (kept on the server, so every device shows the same order).
- **Images, safely:** HTML mail is shown as designed (on a phone, a layout made for a desktop is scaled down to the width of the screen), in a frame that runs no script and loads nothing from the internet. Remote images, which tell senders when you read, stay hidden until you press `i`, and then go through the server, so senders never see your address. Senders can be trusted for good.
- **Done means archived:** `d` takes a conversation out of the inbox, `z` brings it back. Actions show at once and reach the server in the background.
- **Conversations:** the reader always shows the conversation under the cursor, every message unfolded; replies are grouped with the messages they answer, your own answers (from Sent) included. Quotes are folded. A conversation counts as read once it has been shown a second.
- **Plain text writing:** reply, reply all, forward (with the attachments), drafts kept on the server, addresses completed from the people you write to. A missing attachment is caught before sending.
- **Full Vim navigation:** fully operable from the keyboard, AZERTY included.
- **At home on a phone:** installable on the home screen. Swipe a conversation left to throw it away, right to move it; pull the list down to check for new mail; swipe from the left edge of a conversation to go back.
- **Quiet:** no notification, no badge, no tracking. New mail shows when you look.
- **Rosé Pine palette:** Moon (dark) and Dawn (light) themes, following the system setting.
- **One binary:** a single Go binary with embedded SQLite and web app, packaged with Nix Flakes (multi-instance NixOS module).

## Keyboard shortcuts

Press `?` in the app to show the cheatsheet.

| Key | Action |
|---|---|
| `j` / `k`, `gg` / `G` | Next / previous, first / last conversation, shown beside the list (on a phone, tap to open) |
| `Ctrl+d` / `Ctrl+u` | Scroll the conversation down / up |
| `J` / `K` | Next / previous folder of the sidebar, as in aerc |
| `+` | Fold / unfold the account: inbox only, or every folder |
| `&` `é` `"` `'` | Every inbox, then each account |
| `d` | Done: archive |
| `D` (or `#`) | Trash |
| `I` | Back to the inbox |
| `u` / `s` | Read / unread, star |
| `m` | Move to a folder (type to filter) |
| `Space` | Select, then act on the selection |
| `z` | Undo |
| `c` | New message |
| `r` / `R` / `f` | Reply / reply all / forward |
| `e` | Finish a draft |
| `i` | Show the remote images |
| `t` | Plain text / formatted |
| `o` | Open an attachment |
| `/` | Search: words, `from:`, `to:`, `is:unread`, `is:starred`, `has:attachment`, `in:archive`, `in:Receipts`, `in:all`, `account:` |
| `Ctrl+Enter` / `Ctrl+S` | Send / save the draft (in the composer) |
| `Esc` | Close (an unsent message is kept as a draft) |

## How it works

The server keeps a copy of each account in SQLite: the inbox and drafts whole, the archive, sent mail, trash and your own folders for the last year (`-window`). Junk and Gmail's system folders (Important, Starred…) are left out. Older mail is found with **Search older mail on the servers** at the end of search results. One connection per account waits in IMAP IDLE on the inbox, so new mail shows within seconds; the other folders are checked every five minutes. Flags changed elsewhere (read on a phone) follow, with CONDSTORE when the server has it.

Actions done in the app (done, trash, read, star, move) are applied to the local copy at once and queued for the server; a refused action is tried again, then undone with a message in the status bar. The folders of each role (inbox, archive, sent, drafts, trash) are found from the server's special-use attributes (Gmail's All Mail is the archive) or usual names, and can be set by hand.

Message bodies are downloaded when first opened (the latest of the inbox ahead of time). HTML is cleaned (no script, form, frame, event handler), then served from its own address under a Content-Security-Policy that only allows images from this server, into a frame without scripts. Remote images are rewritten to the image proxy only when asked for; the proxy refuses private and local addresses.

## Installation (NixOS)

```nix
# flake.nix
inputs.mail = {
  url = "github:bhr-tools/Mail";
  inputs.nixpkgs.follows = "nixpkgs";
};
# modules
mail.nixosModules.default
```

```nix
# Passwords: sops secrets readable by root only. The service receives them
# as systemd credentials; nothing secret goes to the Nix store.
sops.secrets = {
  "mail-perso" = { };
  "mail-inp" = { };
};

services.webmail = {
  enable = true;
  instances.ben = {
    port = 9890;
    accounts = {
      perso = {
        name = "Perso";
        address = "me@gmail.com";
        realName = "Me";
        passwordFile = config.sops.secrets."mail-perso".path; # a Gmail app password
        imap.host = "imap.gmail.com";
        smtp.host = "smtp.gmail.com";
      };
      school = {
        name = "School";
        address = "me@school.example";
        passwordFile = config.sops.secrets."mail-inp".path;
        imap.host = "mail.school.example";
        smtp = { host = "mail.school.example"; port = 587; security = "starttls"; };
        folders.archive = "Archive"; # only when the server does not say
      };
    };
  };
};

services.caddy.virtualHosts."mail.example.com".extraConfig = "reverse_proxy localhost:9890";
```

Then open the address, choose a master password, and in the settings menu choose **Open mailto: links here** to make it the browser's mail app.

The addresses end up in the Nix store, and in the repository of the configuration. To keep them private, make them sops placeholders and render the accounts file with a sops template:

```nix
sops.secrets."mail-perso-address" = { };
sops.templates."webmail-accounts.json" = {
  content = config.services.webmail.instances.ben.accountsJSON;
  restartUnits = [ "webmail-ben.service" ];
};

services.webmail.instances.ben = {
  accountsFile = config.sops.templates."webmail-accounts.json".path;
  accounts.perso.address = config.sops.placeholder."mail-perso-address";
};
```

Options of an instance: `port`, `host` (127.0.0.1), `dataDir`, `dbPath`, `window` (`8760h`), `noAuth`, `user`, `group`, `openFirewall`, `accounts`, `accountsFile` (the generated `accountsJSON`, in the store). Options of an account: `name`, `address`, `realName`, `userName` (the address), `passwordFile`, `imap` and `smtp` (`host`, `port`, `security`: `tls`, `starttls` or `none`), `folders`, `saveSent` (yes, except for Gmail, which keeps sent mail itself).

## Development

```sh
nix develop
go test ./...
tests/e2e/run.sh   # browser test (Playwright, from nixpkgs)
```

The tests run against an IMAP and an SMTP server in memory, filled with sample mail (`internal/testmail`); the browser tests use a build of the server made for them (`-tags e2e`), never the deployed one.

## aerc (until the switch)

The flake still provides the previous client, aerc with Rosé Pine stylesets (`packages.mail`, `homeManagerModules.default` with `programs.mail.enable`), so both can run side by side until the web app has proven itself; it will then be removed.
