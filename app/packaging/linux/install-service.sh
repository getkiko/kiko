#!/bin/sh
set -eu

PACKAGE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
SERVICE_NAME=kiko-server
SERVICE_FILE=/etc/systemd/system/$SERVICE_NAME.service
RUN_USER=${SUDO_USER:-$(id -un)}

if [ "$(id -u)" -ne 0 ]; then
  echo "请使用 sudo ./install-service.sh"
  exit 1
fi

if [ ! -x "$PACKAGE_DIR/kiko-core" ]; then
  echo "安装包中的 kiko-core 不可用: $PACKAGE_DIR/kiko-core"
  exit 1
fi

# The sbcfg-server service installed before the rename to Kiko: disable and remove it; the kiko-server service replaces it.
LEGACY_SERVICE_FILE=/etc/systemd/system/sbcfg-server.service
if [ -f "$LEGACY_SERVICE_FILE" ]; then
  systemctl disable --now sbcfg-server 2>/dev/null || true
  rm -f "$LEGACY_SERVICE_FILE"
fi

cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=Kiko server (kiko-server + kiko-core)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$RUN_USER
# TUN and auto-route need network admin capabilities; they are passed as ambient capabilities to the kiko-core started by kiko-server.
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE CAP_SYS_PTRACE CAP_DAC_READ_SEARCH
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE CAP_SYS_PTRACE CAP_DAC_READ_SEARCH
WorkingDirectory=$PACKAGE_DIR
ExecStart=$PACKAGE_DIR/start.sh --auto-start
Restart=on-failure
RestartSec=3
Environment=LISTEN=127.0.0.1:9099
Environment=REMOTE_LISTEN=${REMOTE_LISTEN:-:9443}

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable "$SERVICE_NAME"
systemctl restart "$SERVICE_NAME"
systemctl status "$SERVICE_NAME" --no-pager || true

echo
echo "远程访问已开启（TLS，端口 ${REMOTE_LISTEN:-:9443}）。配对新设备："
echo "  $PACKAGE_DIR/start.sh pair"
