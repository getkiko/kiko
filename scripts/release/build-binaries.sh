#!/bin/sh
# Builds kiko-server and kiko-core (plus kiko-helper on macOS) for one platform
# into app/build/package-<os>-<arch>/, for desktop app packaging and the
# server package.
#   scripts/release/build-binaries.sh <darwin|linux|windows> <amd64|arm64> <version>
set -eu

goos=${1:?os: darwin, linux or windows}
goarch=${2:?arch: amd64 or arm64}
version=${3:?version, e.g. 0.3.0}

root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
case "$goos" in
  darwin) package_os=macos ;;
  linux | windows) package_os=$goos ;;
  *) echo "unsupported os: $goos" >&2; exit 1 ;;
esac
case "$goarch" in amd64 | arm64) ;; *) echo "unsupported arch: $goarch" >&2; exit 1 ;; esac
exe=
[ "$goos" = windows ] && exe=.exe
# macOS features such as processes and the keychain need cgo; other platforms
# build statically.
cgo=0
[ "$goos" = darwin ] && cgo=1

out="$root/app/build/package-$package_os-$goarch"
mkdir -p "$out"

core="$root/core"
core_version=$(cd "$core" && CGO_ENABLED=0 GOOS= GOARCH= go run ./cmd/internal/read_tag)
# Release builds must carry the core's real version; a checkout without the
# core's git history reports "unknown".
if [ "${CI:-}" = true ]; then
  case "$core_version" in
    "" | unknown) echo "cannot read the core version; check out core/ with its history and tags" >&2; exit 1 ;;
  esac
fi
core_tags=$(cat "$core/release/DEFAULT_BUILD_TAGS_OTHERS")
core_ldflags=$(cat "$core/release/LDFLAGS")

echo "==> kiko-server $version ($goos/$goarch)"
(cd "$root/app" && CGO_ENABLED=$cgo GOOS=$goos GOARCH=$goarch go build -trimpath \
  -ldflags "-s -w -buildid= -X main.version=$version" \
  -o "$out/kiko-server$exe" .)

echo "==> kiko-core $core_version ($goos/$goarch)"
(cd "$core" && CGO_ENABLED=$cgo GOOS=$goos GOARCH=$goarch go build -trimpath \
  -tags "$core_tags" \
  -ldflags "-X github.com/sagernet/sing-box/constant.Version=$core_version $core_ldflags -s -w -buildid=" \
  -o "$out/kiko-core$exe" ./cmd/sing-box)

if [ "$goos" = darwin ]; then
  echo "==> kiko-helper ($goos/$goarch)"
  (cd "$root/app" && CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -buildvcs=false -trimpath \
    -ldflags "-s -w -buildid=" -o "$out/kiko-helper" ./helper)
fi

chmod +x "$out"/kiko-server"$exe" "$out"/kiko-core"$exe"
echo "$out"
