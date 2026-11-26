{ self, kit }:

# Multi-instance NixOS module: services.webmail.instances.<name>, the kit's
# (kit/nix/app-module.nix) plus the mail accounts. The accounts are written to
# a JSON file in the store, without secrets; each password is handed to the
# service as a systemd credential, read from a file only root can read (a sops
# secret). To keep the addresses out of the store (and of a public
# repository), render accountsJSON with sops placeholders into a sops template
# and point accountsFile to it.
kit.lib.mkAppModule {
  name = "webmail";
  title = "Mail";
  bin = "mail-server";
  package = pkgs: self.packages.${pkgs.stdenv.hostPlatform.system}.default;
  dbFile = "mail.db";
  dataDir = name: if name == "default" || name == "perso" then "/var/lib/webmail" else "/var/lib/webmail-${name}";
  network = true; # IMAP and SMTP servers, remote images

  extraOptions = { lib, pkgs, name, config }:
    let
      serverType = defaultPort: lib.types.submodule {
        options = {
          host = lib.mkOption {
            type = lib.types.str;
            description = "Host name of the server.";
          };
          port = lib.mkOption {
            type = lib.types.port;
            default = defaultPort;
            description = "Port of the server.";
          };
          security = lib.mkOption {
            type = lib.types.enum [ "tls" "starttls" "none" ];
            default = "tls";
            description = "Implicit TLS, STARTTLS, or none (tests only).";
          };
        };
      };

      accountType = lib.types.submodule ({ name, ... }: {
        options = {
          name = lib.mkOption {
            type = lib.types.str;
            default = name;
            description = "Name shown in the app.";
          };
          address = lib.mkOption {
            type = lib.types.str;
            description = "E-mail address of the account.";
          };
          realName = lib.mkOption {
            type = lib.types.str;
            default = "";
            description = "Name written in the From of sent mail.";
          };
          userName = lib.mkOption {
            type = lib.types.nullOr lib.types.str;
            default = null;
            description = "Login of the IMAP and SMTP servers (the address by default).";
          };
          passwordFile = lib.mkOption {
            # A string, not a path: a path would copy the secret to the store.
            type = lib.types.str;
            example = lib.literalExpression ''config.sops.secrets."mail/perso".path'';
            description = "Absolute path of the file holding the password (an app password for Gmail), for instance a sops secret.";
          };
          imap = lib.mkOption {
            type = serverType 993;
            description = "IMAP server.";
          };
          smtp = lib.mkOption {
            type = serverType 465;
            description = "SMTP server (port 587 goes with security = \"starttls\").";
          };
          folders = lib.mkOption {
            type = lib.types.attrsOf lib.types.str;
            default = { };
            example = { archive = "[Gmail]/All Mail"; };
            description = ''
              Folder of each role (inbox, archive, sent, drafts, trash, junk). Roles left
              out are found from the server (special-use attributes, usual names).
            '';
          };
          saveSent = lib.mkOption {
            type = lib.types.nullOr lib.types.bool;
            default = null;
            description = "Keep a copy of sent mail in Sent. Default: yes, except for Gmail, which does it itself.";
          };
        };
      });

      accountsJSON = name: accounts: builtins.toJSON {
        accounts = lib.mapAttrsToList (id: a: {
          inherit id;
          inherit (a) name address folders;
          real_name = a.realName;
          username = if a.userName == null then a.address else a.userName;
          password_file = "/run/credentials/${serviceName name}.service/account-${id}";
          imap = { inherit (a.imap) host port security; };
          smtp = { inherit (a.smtp) host port security; };
        } // lib.optionalAttrs (a.saveSent != null) { save_sent = a.saveSent; }) accounts;
      };

      serviceName = name: if name == "default" then "webmail" else "webmail-${name}";
    in
    {
      window = lib.mkOption {
        type = lib.types.str;
        default = "8760h";
        description = "How far back the archive, sent mail and trash are copied (Go duration). The inbox is copied whole.";
      };
      accounts = lib.mkOption {
        type = lib.types.attrsOf accountType;
        default = { };
        description = "Mail accounts, by short ID (lowercase letters, digits, dashes).";
      };
      accountsJSON = lib.mkOption {
        type = lib.types.str;
        readOnly = true;
        default = accountsJSON name config.accounts;
        defaultText = lib.literalMD "the accounts, in the format of the `-accounts` file";
        description = ''
          Content of the accounts file built from `accounts`. With sops
          placeholders as addresses, it is the content of a sops template
          for `accountsFile`.
        '';
      };
      accountsFile = lib.mkOption {
        # A string, not a path: a path would copy the file to the store.
        type = lib.types.str;
        default = "${pkgs.writeText "webmail-${name}-accounts.json" config.accountsJSON}";
        defaultText = lib.literalMD "`accountsJSON`, written to the store";
        example = lib.literalExpression ''config.sops.templates."webmail-accounts.json".path'';
        description = ''
          Absolute path of the accounts file, handed to the service as a
          systemd credential. Point it to a sops template rendering
          `accountsJSON` to keep the addresses out of the store; give that
          template `restartUnits`, its path does not change with its content.
        '';
      };
    };

  # %d: the service's credentials directory (/run/credentials/<unit>).
  extraArgs = inst: [ "-window" inst.window "-accounts" "%d/accounts" ];

  # Accounts and passwords, readable by the service only, under /run/credentials.
  extraServiceConfig = inst: {
    LoadCredential = [ "accounts:${inst.accountsFile}" ]
      ++ map (id: "account-${id}:${inst.accounts.${id}.passwordFile}") (builtins.attrNames inst.accounts);
  };
}
