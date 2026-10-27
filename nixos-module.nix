self:

# Native multi-instance NixOS module: services.webmail.instances.<name>.
# The accounts are written to a JSON file in the store, without secrets; each
# password is handed to the service as a systemd credential, read from a file
# only root can read (a sops secret). To keep the addresses out of the store
# (and of a public repository), render accountsJSON with sops placeholders
# into a sops template and point accountsFile to it.
{ config, lib, pkgs, ... }:

let
  cfg = config.services.webmail;

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
          Folder of each role (inbox, archive, sent, drafts, trash). Roles left
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
  options.services.webmail = {
    enable = lib.mkEnableOption "the Mail web app (IMAP and SMTP client)";

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      description = "The Mail package to use.";
    };

    instances = lib.mkOption {
      type = lib.types.attrsOf (lib.types.submodule ({ name, config, ... }: {
        options = {
          port = lib.mkOption {
            type = lib.types.port;
            description = "HTTP listening port of this instance.";
          };
          host = lib.mkOption {
            type = lib.types.str;
            default = "127.0.0.1";
            description = "IP address on which the instance listens.";
          };
          dataDir = lib.mkOption {
            type = lib.types.path;
            default = if (name == "default" || name == "perso") then "/var/lib/webmail" else "/var/lib/webmail-${name}";
            description = "Data directory of the instance (the local copy of the mail).";
          };
          dbPath = lib.mkOption {
            type = lib.types.path;
            default = "${config.dataDir}/mail.db";
            description = "Path to the SQLite database of this instance.";
          };
          window = lib.mkOption {
            type = lib.types.str;
            default = "8760h";
            description = "How far back the archive, sent mail and trash are copied (Go duration). The inbox is copied whole.";
          };
          noAuth = lib.mkOption {
            type = lib.types.bool;
            default = false;
            description = "Disable password authentication.";
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
          user = lib.mkOption {
            type = lib.types.str;
            default = "webmail";
            description = "System user under which to run the instance.";
          };
          group = lib.mkOption {
            type = lib.types.str;
            default = "webmail";
            description = "System group under which to run the instance.";
          };
          openFirewall = lib.mkOption {
            type = lib.types.bool;
            default = false;
            description = "Automatically open the port in the NixOS firewall.";
          };
        };
      }));
      default = { };
      description = "Definition of independent Mail instances.";
    };
  };

  config = lib.mkIf (cfg.enable && cfg.instances != { }) {
    networking.firewall.allowedTCPPorts = lib.concatLists (
      lib.mapAttrsToList (_: inst: lib.optional inst.openFirewall inst.port) cfg.instances
    );

    users.users = lib.mkIf (lib.any (inst: inst.user == "webmail") (lib.attrValues cfg.instances)) {
      webmail = {
        isSystemUser = true;
        group = "webmail";
        home = "/var/lib/webmail";
        createHome = true;
        description = "Mail daemon user";
      };
    };

    users.groups = lib.mkIf (lib.any (inst: inst.group == "webmail") (lib.attrValues cfg.instances)) {
      webmail = { };
    };

    systemd.services = lib.mapAttrs' (name: inst:
      lib.nameValuePair (serviceName name) {
        description = "Mail (${name})";
        after = [ "network-online.target" ];
        wants = [ "network-online.target" ];
        wantedBy = [ "multi-user.target" ];

        serviceConfig = {
          ExecStart = "${lib.getExe cfg.package} -host ${inst.host} -port ${toString inst.port} -data-dir ${toString inst.dataDir} -db ${toString inst.dbPath} -window ${inst.window} -accounts /run/credentials/${serviceName name}.service/accounts"
            + lib.optionalString inst.noAuth " -no-auth";
          # Accounts and passwords, readable by the service only, under /run/credentials.
          LoadCredential = [ "accounts:${inst.accountsFile}" ]
            ++ lib.mapAttrsToList (id: a: "account-${id}:${a.passwordFile}") inst.accounts;
          Restart = "always";
          RestartSec = 3;
          User = inst.user;
          Group = inst.group;
          WorkingDirectory = toString inst.dataDir;
          ReadWritePaths = [ (toString inst.dataDir) ];
          # Hardening: the service only needs its data directory and the
          # network (mail servers, remote images).
          NoNewPrivileges = true;
          PrivateTmp = true;
          PrivateDevices = true;
          ProtectSystem = "strict";
          ProtectHome = true;
          ProtectKernelTunables = true;
          ProtectKernelModules = true;
          ProtectControlGroups = true;
          RestrictAddressFamilies = [ "AF_INET" "AF_INET6" "AF_UNIX" ];
          RestrictNamespaces = true;
          LockPersonality = true;
          MemoryDenyWriteExecute = true;
          SystemCallArchitectures = "native";
        } // lib.optionalAttrs (lib.hasPrefix "/var/lib/" (toString inst.dataDir)) {
          # systemd creates and owns the directory when it lives under /var/lib.
          StateDirectory = lib.removePrefix "/var/lib/" (toString inst.dataDir);
        };
      }
    ) cfg.instances;
  };
}
