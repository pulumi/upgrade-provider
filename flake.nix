{
  description = "A flake defining upgrade-provider build-from-source package";

  inputs = {
    nixpkgs.url = github:NixOS/nixpkgs/nixos-23.11;
  };

  outputs = { self, nixpkgs }: let

    package = { system }: let
      pkgs = import nixpkgs { system = system; };
    in pkgs.buildGo121Module rec {
      name = "upgrade-provider";
      version = ''${self.rev or "dirty"}'';
      src = ./.;
      doCheck = false;
      vendorHash = "sha256-0InHprcsXT9I1foDSFKEXzmOxl7LC0FxRe7wOsv6BTo=";
      ldflags = [];
    };

  in {
    packages.aarch64-darwin.default = package {  system = "aarch64-darwin"; };
    packages.aarch64-linux.default = package {  system = "aarch64-linux"; };
    packages.x86_64-darwin.default = package { system = "x86_64-darwin"; };
    packages.x86_64-linux.default = package { system = "x86_64-linux"; };
  };
}
