#!/bin/sh
# One-step install, upgrade and uninstall of the Kiko server (kiko-server + kiko-core) on systemd Linux.
#
#   curl -fsSL https://github.com/@RELEASE_REPO@/releases/latest/download/install-server.sh | sudo sh
#   curl -fsSL …/install-server.sh | sudo sh -s -- --version 0.3.0
#   curl -fsSL …/install-server.sh | sudo sh -s -- --uninstall [--purge]
#
# Running it again upgrades to the latest version, keeping configuration and data.
# Environment: REMOTE_LISTEN (remote access address on first install, default :9443), KIKO_DOWNLOAD_URL (download directory for testing).
set -eu

REPO=${KIKO_REPO:-@RELEASE_REPO@}
PREFIX=/opt/kiko-server
DATA_DIR=/var/lib/kiko-server
CONFIG=/etc/default/kiko-server
SERVICE=kiko-server
UNIT=/etc/systemd/system/$SERVICE.service
COMMAND=/usr/local/bin/kiko
# Early versions installed the management command as /usr/local/bin/kiko-server. Such a file
# written by this script is removed on install and uninstall, unless $COMMAND belongs to another
# program: then the management command is installed under this name instead.
OLD_COMMAND=/usr/local/bin/kiko-server
RUN_USER=kiko

say() { printf '%s\n' "$*"; }
die() { printf '错误: %s\n' "$*" >&2; exit 1; }

version=
action=install
purge=0
force=0
while [ $# -gt 0 ]; do
  case "$1" in
    --version) version=${2:?--version 需要版本号}; shift ;;
    --version=*) version=${1#*=} ;;
    --uninstall) action=uninstall ;;
    --purge) purge=1 ;;
    --force) force=1 ;;
    -h | --help) sed -n '2,10p' "$0" 2>/dev/null || true; exit 0 ;;
    *)
      die "未知参数: $1" ;;
  esac
  shift
done
version=${version#v}

[ "$(id -u)" -eq 0 ] || die "需要 root 权限，请用 sudo 运行"
[ "$(uname -s)" = Linux ] || die "此脚本只用于 Linux；macOS 与 Windows 请安装 Kiko 桌面应用"
command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ] || die "需要 systemd"

# written_by_us reports whether a file was written by this script: the management
# commands it writes carry a marker comment (Chinese in early versions, English later).
written_by_us() {
  [ -f "$1" ] && grep -qE '由 install-server\.sh 安装|installed by install-server\.sh' "$1" 2>/dev/null
}

# remove_old_command deletes the old management command only if this script wrote it.
remove_old_command() {
  if written_by_us "$OLD_COMMAND"; then
    rm -f "$OLD_COMMAND"
  fi
}

# remove_command deletes the management command only if this script wrote it.
remove_command() {
  if written_by_us "$COMMAND"; then
    rm -f "$COMMAND"
  elif [ -e "$COMMAND" ]; then
    say "保留 $COMMAND：该文件不是本脚本安装的。"
  fi
}

uninstall() {
  systemctl disable --now "$SERVICE" >/dev/null 2>&1 || true
  rm -f "$UNIT"
  remove_command
  remove_old_command
  systemctl daemon-reload
  rm -rf "$PREFIX" "$PREFIX.new" "$PREFIX.old"
  if [ "$purge" = 1 ]; then
    rm -rf "$DATA_DIR" "$CONFIG"
    id "$RUN_USER" >/dev/null 2>&1 && userdel "$RUN_USER" 2>/dev/null || true
    say "已卸载 Kiko 服务器，并删除了配置与数据。"
  else
    say "已卸载 Kiko 服务器。配置与数据保留在 $DATA_DIR，彻底删除请加 --purge。"
  fi
}

if [ "$action" = uninstall ]; then
  uninstall
  exit 0
fi

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *)
    die "不支持的 CPU 架构: $(uname -m)" ;;
esac

if [ -n "${KIKO_DOWNLOAD_URL:-}" ]; then
  base=${KIKO_DOWNLOAD_URL%/}
elif [ -n "$version" ]; then
  base="https://github.com/$REPO/releases/download/v$version"
else
  base="https://github.com/$REPO/releases/latest/download"
fi

fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    die "需要 curl 或 wget"
  fi
}

# A manual install from before the rename to Kiko (the sbcfg-server service from install-service.sh) uses the same ports.
if systemctl is-active --quiet sbcfg-server 2>/dev/null; then
  die "检测到旧的 sbcfg-server 服务正在运行，请先停用：sudo systemctl disable --now sbcfg-server"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
archive="kiko-server-linux-$arch.tar.gz"

say "下载 $base/$archive"
fetch "$base/$archive" "$tmp/$archive" || die "下载失败: $base/$archive"
fetch "$base/server-SHA256SUMS" "$tmp/SHA256SUMS" || die "下载校验文件失败"
expected=$(awk -v f="$archive" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")
[ -n "$expected" ] || die "校验文件里没有 $archive"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$archive" | awk '{print $1}')
else
  actual=$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')
fi
[ "$actual" = "$expected" ] || die "$archive 校验失败"

tar -xzf "$tmp/$archive" -C "$tmp"
new_version=$(cat "$tmp/kiko-server/VERSION")
old_version=$(cat "$PREFIX/VERSION" 2>/dev/null || true)

if [ "$old_version" = "$new_version" ] && [ "$force" = 0 ] && [ -f "$UNIT" ]; then
  systemctl enable --now "$SERVICE" >/dev/null 2>&1 || true
  say "Kiko 服务器 $new_version 已是最新版本。"
  exit 0
fi

# The service runs as a dedicated system user; with the network capabilities below it can use TUN.
if ! id "$RUN_USER" >/dev/null 2>&1; then
  nologin=$(command -v nologin || echo /usr/sbin/nologin)
  useradd --system --home-dir "$DATA_DIR" --no-create-home --shell "$nologin" "$RUN_USER" 2>/dev/null ||
    adduser --system --home "$DATA_DIR" --no-create-home --shell "$nologin" "$RUN_USER" ||
    die "创建用户 $RUN_USER 失败"
fi
mkdir -p "$DATA_DIR"
chown "$RUN_USER:" "$DATA_DIR"
chmod 700 "$DATA_DIR"

# The remote access address is written only on first install; later the user's edits are kept.
if [ ! -f "$CONFIG" ]; then
  cat > "$CONFIG" <<EOF
# Kiko server startup parameters; run systemctl restart $SERVICE after editing.
LISTEN=127.0.0.1:9099
REMOTE_LISTEN=${REMOTE_LISTEN-:9443}
DATA_DIR=$DATA_DIR
EOF
fi

rm -rf "$PREFIX.new" "$PREFIX.old"
cp -R "$tmp/kiko-server" "$PREFIX.new"

cat > "$UNIT" <<EOF
[Unit]
Description=Kiko server (kiko-server + kiko-core)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$RUN_USER
# Network and process capabilities needed for TUN, auto-route and process detection, passed to the kiko-core started by kiko-server.
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE CAP_SYS_PTRACE CAP_DAC_READ_SEARCH
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE CAP_SYS_PTRACE CAP_DAC_READ_SEARCH
EnvironmentFile=$CONFIG
WorkingDirectory=$DATA_DIR
ExecStart=$PREFIX/start.sh --auto-start
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

# Another program may already be installed as $COMMAND; it is left alone and the
# management command goes to the old name $OLD_COMMAND instead, unless that is
# taken by another program too.
command_path=$COMMAND
if [ -e "$COMMAND" ] && ! written_by_us "$COMMAND"; then
  if [ -e "$OLD_COMMAND" ] && ! written_by_us "$OLD_COMMAND"; then
    command_path=
    say "未安装管理命令：$COMMAND 与 $OLD_COMMAND 均已被其他程序占用。"
  else
    command_path=$OLD_COMMAND
    say "$COMMAND 已被其他程序占用，管理命令安装为 $OLD_COMMAND。"
  fi
fi
command_name=$(basename "${command_path:-kiko}")
[ -z "$command_path" ] || cat > "$command_path" <<EOF
#!/bin/sh
# Kiko server management command, installed by install-server.sh.
set -eu
case "\${1:-}" in
  pair)
    shift
    [ "\$(id -u)" -eq 0 ] || { echo "请用 sudo 运行"; exit 1; }
    . $CONFIG
    exec setpriv --reuid=$RUN_USER --regid=\$(id -g $RUN_USER) --init-groups \\
      env DATA_DIR="\$DATA_DIR" $PREFIX/start.sh pair "\$@" ;;
  upgrade)
    shift
    curl -fsSL https://github.com/$REPO/releases/latest/download/install-server.sh | sh -s -- "\$@" ;;
  uninstall)
    shift
    curl -fsSL https://github.com/$REPO/releases/latest/download/install-server.sh | sh -s -- --uninstall "\$@" ;;
  status) exec systemctl status $SERVICE --no-pager ;;
  restart) exec systemctl restart $SERVICE ;;
  logs) exec journalctl -u $SERVICE -f ;;
  version) cat $PREFIX/VERSION ;;
  *)
    echo "用法: $command_name <命令>"
    echo "  pair       生成配对码，给客户端添加本服务器（sudo）"
    echo "  upgrade    升级到最新版本（sudo）"
    echo "  uninstall  卸载，保留数据；加 --purge 一并删除（sudo）"
    echo "  status     查看服务状态"
    echo "  restart    重启服务（sudo）"
    echo "  logs       查看实时日志"
    echo "  version    当前版本"
    exit 1 ;;
esac
EOF
if [ -n "$command_path" ]; then
  chmod 755 "$command_path"
fi
if [ "$command_path" != "$OLD_COMMAND" ]; then
  remove_old_command
fi

# Replace the program with the service stopped; if the new version fails to start, switch back to the old one.
systemctl stop "$SERVICE" >/dev/null 2>&1 || true
if [ -d "$PREFIX" ]; then
  mv "$PREFIX" "$PREFIX.old"
fi
mv "$PREFIX.new" "$PREFIX"
systemctl daemon-reload
systemctl enable "$SERVICE" >/dev/null 2>&1
systemctl restart "$SERVICE" || true

started=0
i=0
while [ $i -lt 10 ]; do
  if systemctl is-active --quiet "$SERVICE"; then started=1; break; fi
  sleep 1
  i=$((i + 1))
done

if [ "$started" = 0 ]; then
  if [ -d "$PREFIX.old" ]; then
    say "新版本启动失败，换回 ${old_version:-旧版本}。日志：journalctl -u $SERVICE"
    rm -rf "$PREFIX"
    mv "$PREFIX.old" "$PREFIX"
    systemctl restart "$SERVICE" || true
  else
    say "服务启动失败，查看日志：journalctl -u $SERVICE"
  fi
  exit 1
fi
rm -rf "$PREFIX.old"

# pair_hint prints how to pair a device: the management command, or the full
# command it would run when it could not be installed.
pair_hint() {
  if [ -n "$command_path" ]; then
    say "配对新设备：sudo $command_path pair"
  else
    say "配对新设备：sudo setpriv --reuid=$RUN_USER --regid=$(id -g "$RUN_USER") --init-groups env DATA_DIR=$DATA_DIR $PREFIX/start.sh pair"
  fi
}

if [ -n "$old_version" ]; then
  say "Kiko 服务器已从 $old_version 升级到 $new_version。"
  if [ "$command_path" != "$COMMAND" ]; then
    . "$CONFIG"
    pair_hint
  fi
else
  . "$CONFIG"
  say "Kiko 服务器 $new_version 已安装并启动。"
  say ""
  pair_hint
  [ -n "$REMOTE_LISTEN" ] && say "远程访问端口 ${REMOTE_LISTEN##*:}/tcp，请在防火墙中放行。"
  [ -z "$command_path" ] || say "更多命令：$command_path help"
fi
