#!/bin/sh
# Stands in for gh while mygo build -upload runs (see build-desktop.sh): every
# "gh release upload <tag>" first checks that the release is still a draft, so
# assets of a release published meanwhile are never replaced. Other commands
# pass through unchanged.
#   KIKO_REAL_GH   path of the real gh
set -eu
: "${KIKO_REAL_GH:?KIKO_REAL_GH is not set}"
if [ "${1:-}" = release ] && [ "${2:-}" = upload ]; then
  tag=${3:?release upload needs a tag}
  repo=
  prev=
  for arg in "$@"; do
    [ "$prev" = --repo ] && repo=$arg
    prev=$arg
  done
  draft=$("$KIKO_REAL_GH" release view "$tag" ${repo:+--repo "$repo"} --json isDraft --jq .isDraft)
  if [ "$draft" != true ]; then
    echo "error: release $tag is no longer a draft; not uploading over it" >&2
    exit 1
  fi
fi
exec "$KIKO_REAL_GH" "$@"
