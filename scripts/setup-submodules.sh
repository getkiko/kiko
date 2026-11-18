#!/bin/sh
# Checks out the core submodule (getkiko/kiko-core, branch kiko) at the commit
# recorded in this repository's gitlink.
set -eu
workspace_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
git -C "$workspace_dir" submodule sync -- core
git -C "$workspace_dir" submodule update --init --recursive -- core
