#!/bin/sh
set -eu

helper_source=$1
core_source=$2
helper_label=$3
helper_target=$4
core_target=$5
plist_target=$6
socket_path=$7
helper_log=$8
core_log=$9
versions_source=${10}
versions_target=${11}

# The privileged components installed before the rename to Kiko (io.sagernet.poppy.*): stop the old
# helper and the core it started, and delete the old launchd config, binaries, socket, logs and version manifest.
legacy=io.sagernet.poppy
if [ "$helper_label" != "$legacy.helper" ]; then
  /bin/launchctl bootout "system/$legacy.helper" 2>/dev/null || true
  /usr/bin/pkill -f "^/Library/PrivilegedHelperTools/$legacy.sing-box( |\$)" 2>/dev/null || true
  /bin/rm -f "/Library/LaunchDaemons/$legacy.helper.plist" \
    "/Library/PrivilegedHelperTools/$legacy.helper" \
    "/Library/PrivilegedHelperTools/$legacy.sing-box" \
    "/Library/PrivilegedHelperTools/$legacy.versions.json" \
    "/var/run/$legacy.helper.sock"
  for log in "/var/log/$legacy.helper.log" "/var/log/$legacy.sing-box.log"; do
    /bin/rm -f "$log" "$log.1" "$log.2"
  done
fi

/bin/launchctl bootout "system/$helper_label" 2>/dev/null || true
# Kiko's core was installed as <prefix>.sing-box before it was renamed kiko-core: stop a core
# still running from the old path and delete the old binary and its logs.
previous_core="${helper_target%.helper}.sing-box"
previous_core_log="${helper_log%.helper.log}.sing-box.log"
if [ "$core_target" != "$previous_core" ]; then
  /usr/bin/pkill -f "^$previous_core( |\$)" 2>/dev/null || true
  /bin/rm -f "$previous_core"
  if [ "$core_log" != "$previous_core_log" ]; then
    /bin/rm -f "$previous_core_log" "$previous_core_log.1" "$previous_core_log.2"
  fi
fi
/bin/mkdir -p "$(/usr/bin/dirname "$helper_target")" "$(/usr/bin/dirname "$plist_target")"
/usr/bin/install -o root -g wheel -m 0755 "$helper_source" "$helper_target"
/usr/bin/install -o root -g wheel -m 0755 "$core_source" "$core_target"
/bin/cat > "$plist_target" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$helper_label</string>
  <key>ProgramArguments</key><array><string>$helper_target</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Interactive</string>
  <key>StandardOutPath</key><string>$helper_log</string>
  <key>StandardErrorPath</key><string>$helper_log</string>
</dict>
</plist>
EOF
/usr/sbin/chown root:wheel "$plist_target"
/bin/chmod 0644 "$plist_target"
/usr/bin/touch "$core_log"
/usr/sbin/chown root:wheel "$core_log"
/bin/chmod 0644 "$core_log"
/bin/rm -f "$socket_path"
/bin/launchctl bootstrap system "$plist_target"
# Record revisions only after the binaries and launchd job were installed.
versions_temporary="$versions_target.tmp.$$"
trap '/bin/rm -f "$versions_temporary"' EXIT
/usr/bin/install -o root -g wheel -m 0644 "$versions_source" "$versions_temporary"
/bin/mv -f "$versions_temporary" "$versions_target"
# RunAtLoad starts the new helper. It reaps verified orphan cores before
# exposing its socket; a second kickstart could interrupt that cleanup.
