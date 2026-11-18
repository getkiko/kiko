#!/bin/sh
set -eu

PACKAGE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# SING_BOX_PATH is the variable's name before the core was renamed kiko-core.
KIKO_CORE_PATH=${KIKO_CORE_PATH:-${SING_BOX_PATH:-$PACKAGE_DIR/kiko-core}}
LISTEN=${LISTEN:-127.0.0.1:9099}
REMOTE_LISTEN=${REMOTE_LISTEN-:9443}
DATA_DIR=${DATA_DIR:-$PACKAGE_DIR/data}

# ./start.sh pair generates a one-time pairing code for a client to add this server.
if [ "${1:-}" = "pair" ]; then
  shift
  exec "$PACKAGE_DIR/kiko-server" pair --data-dir "$DATA_DIR" "$@"
fi

if [ ! -x "$KIKO_CORE_PATH" ]; then
  echo "缺少可执行文件: $KIKO_CORE_PATH"
  exit 1
fi

mkdir -p "$DATA_DIR"
# With REMOTE_LISTEN empty, the remote access setting saved in the data directory is kept.
set -- --core "$KIKO_CORE_PATH" --listen "$LISTEN" --data-dir "$DATA_DIR" "$@"
if [ -n "$REMOTE_LISTEN" ]; then
  set -- "$@" --remote-listen "$REMOTE_LISTEN"
fi
exec "$PACKAGE_DIR/kiko-server" "$@"
