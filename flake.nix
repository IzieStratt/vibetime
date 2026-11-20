{
  description = "vibetime - count attended (human-in-the-loop) AI coding time";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        vibetime = pkgs.buildGoModule {
          pname = "vibetime";
          version = "0.1.0";
          src = self;

          # Maintained automatically by .github/workflows/nix.yml: on any change to the Go
          # dependencies the workflow rebuilds, reads the correct hash out of the build
          # failure, and commits the new value on the line tagged below. Keep the tag.
          vendorHash = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="; # vibetime:vendor-hash

          # both the local agent and the server; pure Go SQLite, so no cgo.
          subPackages = [ "cmd/vibetime" "cmd/vibetime-server" ];
          env.CGO_ENABLED = 0;
          ldflags = [ "-s" "-w" ];

          meta = {
            description = "Passive, privacy-friendly attended-time tracking for building with AI";
            homepage = "https://github.com/IzieStratt/vibetime";
            license = pkgs.lib.licenses.agpl3Plus;
            mainProgram = "vibetime";
          };
        };
      in
      {
        packages = {
          default = vibetime;
          vibetime = vibetime;
        };

        apps = rec {
          default = vibetime;
          vibetime = {
            type = "app";
            program = "${self.packages.${system}.vibetime}/bin/vibetime";
          };
          vibetime-server = {
            type = "app";
            program = "${self.packages.${system}.vibetime}/bin/vibetime-server";
          };
        };

        devShells.default = pkgs.mkShell {
          packages = [ pkgs.go pkgs.gopls pkgs.gotools ];
        };
      }
    );
}
