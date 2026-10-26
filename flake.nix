{
  description = "Mail — minimalist self-hosted mail client: IMAP, Rosé Pine design & Vim navigation";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in {
      packages = forAllSystems (pkgs: rec {
        # The web app: IMAP synchronization, API and interface in one binary.
        webmail = pkgs.buildGoModule {
          pname = "mail";
          version = "1.0.0";
          src = ./.;

          subPackages = [ "cmd/server" ];
          vendorHash = null;

          postInstall = ''
            mv "$out/bin/server" "$out/bin/mail-server"
          '';

          meta = with pkgs.lib; {
            description = "Minimalist self-hosted mail client with Rosé Pine design & Vim navigation";
            homepage = "https://github.com/bhr-tools/Mail";
            license = licenses.mit;
            mainProgram = "mail-server";
          };
        };

        # aerc with the Rosé Pine themes, until the web app replaces it.
        mail = pkgs.callPackage ./package.nix { };

        default = webmail;
      });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          buildInputs = with pkgs; [ go gopls sqlite nodejs ];
        };
      });

      nixosModules.default = import ./nixos-module.nix self;
      homeManagerModules.default = import ./hm-module.nix self;
    };
}
