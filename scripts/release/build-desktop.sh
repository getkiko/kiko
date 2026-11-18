#!/bin/sh
# Packages the Kiko desktop app for one platform and uploads it, together with
# the signed update files, to the repository's Release.
#   scripts/release/build-desktop.sh <darwin|windows> <amd64|arm64>
# Environment variables:
#   RELEASE_REPO               release repository owner/name; defaults to GITHUB_REPOSITORY (this repository in Actions)
#   MYGO_UPDATER_PUBLIC_KEY    contents of mygo-update.pub
#   MYGO_UPDATER_PRIVATE_KEY   contents of mygo-update.key (read by mygo build)
#   GH_TOKEN                   token that can write the repository's Releases (read by mygo build -upload)
#   UPLOAD=0                   package only, do not upload
set -eu

goos=${1:?os: darwin or windows}
goarch=${2:?arch: amd64 or arm64}
RELEASE_REPO=${RELEASE_REPO:-${GITHUB_REPOSITORY:-}}
: "${RELEASE_REPO:?RELEASE_REPO (or GITHUB_REPOSITORY) is not set, e.g. getkiko/kiko}"
: "${MYGO_UPDATER_PUBLIC_KEY:?MYGO_UPDATER_PUBLIC_KEY is not set}"

root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
mygo_dir="$root/app/mygo"
version=$(jq -r .version "$mygo_dir/mygo.json")

"$root/scripts/release/build-binaries.sh" "$goos" "$goarch" "$version"

# The update source is written only into release builds: local builds have no
# updates section, so the app does not check for updates.
config="$mygo_dir/mygo.json"
cp "$config" "$config.orig"
trap 'mv "$config.orig" "$config"' EXIT
jq --arg repo "$RELEASE_REPO" --arg key "$MYGO_UPDATER_PUBLIC_KEY" \
  '.updates = {publicKey: $key, github: $repo}' "$config.orig" > "$config"

flags=
if [ "${UPLOAD:-1}" != 0 ]; then
  flags=-upload
  # mygo uploads with gh at the end of the build; the guard standing in for
  # it checks right before each upload that the release is still a draft.
  KIKO_REAL_GH=$(command -v gh) || { echo "gh is not installed" >&2; exit 1; }
  export KIKO_REAL_GH
  guard_dir=$(mktemp -d)
  trap 'mv "$config.orig" "$config"; rm -rf "$guard_dir"' EXIT
  cp "$root/scripts/release/gh-draft-guard.sh" "$guard_dir/gh"
  chmod +x "$guard_dir/gh"
  PATH="$guard_dir:$PATH"
  export PATH
fi
make -C "$mygo_dir" build TARGET_OS="$goos" TARGET_ARCH="$goarch" BUILD_FLAGS="$flags"
