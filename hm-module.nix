self:

{ config, lib, pkgs, ... }:

let
  cfg = config.programs.mail;

  accounts = lib.filterAttrs (_: a: a.aerc.enable) config.accounts.email.accounts;

  # Sections that depend on each account's folders. The trash is reached with
  # an explicit move: Gmail expunges deleted mail to the archive instead.
  accountBinds = lib.concatStrings (lib.mapAttrsToList (name: account: ''

    [messages:account=${name}]
    D = :move ${account.folders.trash}<Enter>

    [view:account=${name}]
    D = :move ${account.folders.trash}<Enter>
  '') accounts);

  # Drafts are matched on the last component of their path ("[Gmail]/Drafts"
  # becomes "Drafts"): brackets would end the section name.
  draftNames = lib.unique (lib.concatMap (account:
    lib.optional (account.folders.drafts != null)
      (lib.last (lib.splitString "/" account.folders.drafts))
  ) (lib.attrValues accounts));

  draftBinds = lib.concatMapStrings (folder: ''

    [messages:folder=~(^|/)${folder}$]
    <Enter> = :recall<Enter>
    l = :recall<Enter>
  '') draftNames;
in {
  options.programs.mail = {
    enable = lib.mkEnableOption "the aerc-based mail client";

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.mail;
      description = "The mail package to use.";
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    # Accounts come from accounts.email.accounts.<name>.aerc.enable.
    programs.aerc = {
      enable = true;

      extraConfig = {
        general = {
          # accounts.conf is a read-only symlink to the store, without secrets.
          unsafe-accounts-conf = true;
        };

        ui = {
          # Symlinked by the mail command from theme_state.
          styleset-name = "user-theme";
          threading-enabled = true;
          dirlist-tree = true;
          fuzzy-complete = true;
          sidebar-width = 24;
          # No terminal bell on new mail: it is read when it is opened, not pushed.
          new-message-bell = false;
          timestamp-format = "02/01/2006";
          this-day-time-format = "15:04";
          this-week-time-format = "Mon 15:04";
          this-year-time-format = "02 Jan";
        };

        viewer = {
          alternatives = "text/plain,text/html";
          header-layout = "From|To,Cc|Bcc,Date,Subject";
        };

        compose = {
          # Warns before sending when the text mentions an attachment that is missing.
          no-attachment-warning = "^[^>]*(attach|joint)";
        };

        filters = {
          "text/plain" = "colorize";
          "text/html" = "html | colorize";
          "text/calendar" = "calendar";
          "message/delivery-status" = "colorize";
          "message/rfc822" = "colorize";
          ".headers" = "colorize";
        };
      };
    };

    xdg.configFile."aerc/binds.conf".text =
      builtins.readFile ./config/binds.conf + accountBinds + draftBinds;

    # A kitty window with its own class, so bars, switchers and overviews get
    # the name and icon of this entry. Also the handler of mailto: links.
    xdg.desktopEntries.mail = {
      name = "Mail";
      genericName = "Mail Client";
      exec = "kitty --class mail -e mail %u";
      icon = "internet-mail";
      terminal = false;
      categories = [ "Network" "Email" ];
      mimeType = [ "x-scheme-handler/mailto" ];
      settings.StartupWMClass = "mail";
    };

    xdg.mimeApps.defaultApplications."x-scheme-handler/mailto" = [ "mail.desktop" ];
  };
}
