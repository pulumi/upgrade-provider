{
  description = "A flake defining upgrade-provider build-from-source package";

  inputs = {
    nixpkgs.url = github:NixOS/nixpkgs/nixos-23.05;
  };

  outputs = { self, nixpkgs }: let

    package = { system }: let
      pkgs = import nixpkgs { system = system; };
    in pkgs.buildGo120Module rec {
      name = "upgrade-provider";
      version = ''${self.rev or "dirty"}'';
      src = ./.;
      # subPackages = [ "cmd/pulumictl" ];
      doCheck = false;
      vendorSha256 = "sha256-YBteVgcWvIE10ojd9W4grMK8kJ0zXXQiBuEHto3sABI=";
      ldflags = [];
    };

  in {
    packages.aarch64-darwin.default = package {  system = "aarch64-darwin"; };
    packages.aarch64-linux.default = package {  system = "aarch64-linux"; };
    packages.x86_64-darwin.default = package { system = "x86_64-darwin"; };
    packages.x86_64-linux.default = package { system = "x86_64-linux"; };
  };
}
