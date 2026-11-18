#!/bin/sh
set -eu

PACKAGE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$PACKAGE_DIR"

if [ ! -x "$PACKAGE_DIR/kiko-core" ]; then
  echo "缺少可执行文件: $PACKAGE_DIR/kiko-core"
  echo "请将同架构 kiko-core 放入此文件夹，文件名保持为 kiko-core。"
  echo "随后执行: chmod +x kiko-core"
  printf "\n按回车键关闭窗口..."
  read ignored
  exit 1
fi

if [ -z "${DATA_DIR:-}" ]; then
  DATA_DIR="$HOME/Library/Application Support/kiko-server"
  # Move the data directory from before the rename to Kiko to the new location.
  LEGACY_DATA_DIR="$HOME/Library/Application Support/sbcfg-server"
  if [ ! -e "$DATA_DIR" ] && [ -d "$LEGACY_DATA_DIR" ]; then
    # The LaunchAgent from before the rename runs a server on the old directory;
    # stop and remove it first, as install-launchagent.command does.
    LEGACY_LABEL=io.sagernet.sbcfg-server
    LEGACY_PLIST_PATH=$HOME/Library/LaunchAgents/$LEGACY_LABEL.plist
    launchctl bootout "gui/$(id -u)/$LEGACY_LABEL" 2>/dev/null || true
    if [ -f "$LEGACY_PLIST_PATH" ]; then
      rm -f "$LEGACY_PLIST_PATH"
      echo "已停用旧版开机启动项；需要开机启动请运行 install-launchagent.command。"
    fi
    if ! mv "$LEGACY_DATA_DIR" "$DATA_DIR"; then
      # Keep using the old directory rather than an empty one; the move is retried next time.
      echo "数据目录迁移失败，继续使用 $LEGACY_DATA_DIR"
      DATA_DIR=$LEGACY_DATA_DIR
    fi
  fi
fi
mkdir -p "$DATA_DIR"
if [ -d "$PACKAGE_DIR/data" ] && [ ! -f "$DATA_DIR/editor.json" ]; then
  cp -R "$PACKAGE_DIR/data/." "$DATA_DIR/"
fi
echo "Kiko Server 启动中（本机 127.0.0.1:9099）。远程访问可在客户端设置中开启。"
echo "按 Control-C 停止服务。"
exec "$PACKAGE_DIR/kiko-server" \
  --core "$PACKAGE_DIR/kiko-core" \
  --listen "${LISTEN:-127.0.0.1:9099}" \
  --data-dir "$DATA_DIR" \
  "$@"
