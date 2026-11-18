#!/bin/sh
# Packages the Linux server (kiko-server + kiko-core) and writes the install
# script; artifacts go to app/build/release/:
#   kiko-server-linux-<arch>.tar.gz, server-SHA256SUMS, install-server.sh
#   scripts/release/package-server.sh [arch...]
# The install script downloads from RELEASE_REPO, which defaults to
# GITHUB_REPOSITORY (this repository in Actions).
set -eu

repo=${RELEASE_REPO:-${GITHUB_REPOSITORY:-}}
: "${repo:?RELEASE_REPO (or GITHUB_REPOSITORY) is not set, e.g. getkiko/kiko}"
[ $# -gt 0 ] || set -- amd64 arm64

root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
version=$(jq -r .version "$root/app/mygo/mygo.json")
dist="$root/app/build/release"
mkdir -p "$dist"

for arch in "$@"; do
  "$root/scripts/release/build-binaries.sh" linux "$arch" "$version"
  stage="$root/app/build/server-linux-$arch/kiko-server"
  rm -rf "${stage%/*}"
  mkdir -p "$stage"
  cp "$root/app/build/package-linux-$arch/kiko-server" "$root/app/build/package-linux-$arch/kiko-core" \
    "$root/app/packaging/linux/start.sh" "$stage/"
  printf '%s\n' "$version" > "$stage/VERSION"
  chmod +x "$stage/kiko-server" "$stage/kiko-core" "$stage/start.sh"
  tar -C "${stage%/*}" -czf "$dist/kiko-server-linux-$arch.tar.gz" kiko-server
done

sed "s|@RELEASE_REPO@|$repo|g" "$root/app/packaging/linux/install-server.sh" > "$dist/install-server.sh"
chmod +x "$dist/install-server.sh"
(cd "$dist" && sha256sum kiko-server-linux-*.tar.gz install-server.sh > server-SHA256SUMS)
ls -l "$dist"
