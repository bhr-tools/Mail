{ writeShellApplication, aerc, inotify-tools }:

# Starts aerc with the Rosé Pine styleset matching ~/.config/theme_state, and
# keeps following that file while aerc runs, so a theme change applies live.
# Arguments go straight to aerc (mailto: links, :commands over IPC).
writeShellApplication {
  name = "mail";
  runtimeInputs = [ aerc inotify-tools ];
  text = ''
    config="''${XDG_CONFIG_HOME:-$HOME/.config}"
    stylesets="$config/aerc/stylesets"

    apply_theme() {
      if [ "$(cat "$config/theme_state" 2>/dev/null)" = light ]; then
        name=rose-pine-dawn
      else
        name=rose-pine
      fi
      mkdir -p "$stylesets"
      ln -sfn "${./config/stylesets}/$name" "$stylesets/user-theme"
    }

    # theme-switch rewrites the file in place: watch the directory, so a
    # recreated file is caught too.
    follow_theme() {
      inotifywait -m -q -e close_write,moved_to --format %f "$config" |
        while read -r file; do
          if [ "$file" = theme_state ]; then
            apply_theme
            aerc :reload > /dev/null 2>&1 || true
          fi
        done
    }

    apply_theme
    follow_theme &
    # Closing the kitty window hangs up the script: stop the watcher then too.
    trap 'trap - EXIT HUP INT TERM; kill 0' EXIT HUP INT TERM
    aerc "$@"
  '';
}
