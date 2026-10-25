{
  description = "Tsuzuri development environment";

  inputs = {
    # This snapshot provides the candidate's Go 1.26.10 requirement.
    # flake.lock pins it; review compiler/module/CI alignment when updating.
    nixpkgs.url = "github:NixOS/nixpkgs/48526c34d64d9c42559a1939a69da7bc44f55392";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = {nixpkgs, flake-utils, ...}:
    flake-utils.lib.eachDefaultSystem (
      system: let
        pkgs = import nixpkgs {inherit system;};
        goVersion = builtins.replaceStrings ["\n"] [""] (builtins.readFile ./.go-version);
        packages = with pkgs; [go gopls govulncheck gnumake stdenv.cc cacert git coreutils gnutar]
          ++ lib.optionals stdenv.hostPlatform.isLinux [util-linux];
        shell = assert pkgs.go.version == goVersion;
          pkgs.mkShell {
            inherit packages;
            GOTOOLCHAIN = "local";
          };
      in {
        devShells.default = shell;
      }
    );
}
