#!/bin/sh
set -eu

PACKAGE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
LABEL=io.github.getkiko.kiko-server
PLIST_DIR=$HOME/Library/LaunchAgents
PLIST_PATH=$PLIST_DIR/$LABEL.plist
DATA_DIR="$HOME/Library/Application Support/kiko-server"
LOG_DIR=$DATA_DIR/launcher
# The LaunchAgent and data directory from before the rename to Kiko.
LEGACY_LABEL=io.sagernet.sbcfg-server
LEGACY_PLIST_PATH=$PLIST_DIR/$LEGACY_LABEL.plist
LEGACY_DATA_DIR="$HOME/Library/Application Support/sbcfg-server"

if [ ! -x "$PACKAGE_DIR/kiko-core" ]; then
  echo "请先将同架构 kiko-core 放入 $PACKAGE_DIR，并执行 chmod +x kiko-core"
  printf "\n按回车键关闭窗口..."
  read ignored
  exit 1
fi

launchctl bootout "gui/$(id -u)/$LEGACY_LABEL" 2>/dev/null || true
rm -f "$LEGACY_PLIST_PATH"
if [ ! -e "$DATA_DIR" ] && [ -d "$LEGACY_DATA_DIR" ]; then
  mv "$LEGACY_DATA_DIR" "$DATA_DIR"
fi

mkdir -p "$PLIST_DIR" "$LOG_DIR"
cat > "$PLIST_PATH" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key><array>
    <string>$PACKAGE_DIR/kiko-server</string>
    <string>--core</string><string>$PACKAGE_DIR/kiko-core</string>
    <string>--listen</string><string>127.0.0.1:9099</string>
    <string>--data-dir</string><string>$DATA_DIR</string>
  </array>
  <key>WorkingDirectory</key><string>$PACKAGE_DIR</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$LOG_DIR/stdout.log</string>
  <key>StandardErrorPath</key><string>$LOG_DIR/stderr.log</string>
</dict></plist>
EOF

launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$PLIST_PATH"
launchctl enable "gui/$(id -u)/$LABEL"
echo "LaunchAgent 已安装并启动。"
echo "卸载命令：launchctl bootout gui/$(id -u)/$LABEL"
printf "\n按回车键关闭窗口..."
read ignored
