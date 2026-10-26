{
  description = "Minimal keyboard-driven mail, built on aerc with Rosé Pine themes";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in {
      packages = forAllSystems (pkgs: rec {
        mail = pkgs.callPackage ./package.nix { };
        default = mail;
      });

      homeManagerModules.default = import ./hm-module.nix self;
    };
}
