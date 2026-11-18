# Development guide

This guide covers building and running Kiko from source. For installing a release, see the [README](../README.md); for publishing one, see [RELEASING.md](../RELEASING.md).

## Prerequisites

- Go 1.27.3 or later. The toolchain is selected automatically from `go.work`.
- Git, with the `core` submodule checked out.
- macOS desktop builds: Xcode Command Line Tools.
- Android builds: the Android NDK.

```sh
git clone --recursive https://github.com/getkiko/kiko.git
cd kiko
git submodule update --init --recursive   # if the clone was not recursive
```

## Workspace layout

`go.work` ties the Go modules together:

| Module | Contents |
| --- | --- |
| `app` | `kiko-server`, the macOS privileged helper and supporting packages |
| `app/mygo` | The desktop app (see [app/mygo/README.md](../app/mygo/README.md)) |
| `core` | `kiko-core`, a sing-box fork; a submodule tracking the `kiko` branch of [getkiko/kiko-core](https://github.com/getkiko/kiko-core) |
| `scripts/release/site` | Generator for the download site |

The core is based on upstream sing-box plus Kiko's changes (HTTPS inspection, scripting, DNS monitoring, policy group selection modes, Burrow NAT traversal and others). Those changes are maintained in `getkiko/kiko-core`; this repository pins the exact commit through the submodule gitlink. The core's Go module path stays `github.com/sagernet/sing-box`, and it depends on [getkiko/burrow](https://github.com/getkiko/burrow) by version (see [RELEASING.md](../RELEASING.md#first-release)).

## Build and test

From the repository root:

```sh
make test        # app and desktop app tests
make core        # kiko-core from the submodule -> app/build/core/kiko-core
make macos-app   # core + Kiko.app
```

`make core` checks that the built binary reports the version recorded in the submodule. The core keeps the upstream version output (`sing-box version <x>`); kiko-server accepts it as is.

Build output lives under `app/build/`, which is ignored by Git:

| Path | Contents |
| --- | --- |
| `app/build/darwin-arm64/` | `Kiko.app` and the DMG (`darwin-amd64` on Intel Macs) |
| `app/build/core/` | `kiko-core` built from the workspace |
| `app/build/bin/` | Standalone `kiko-server` builds |
| `app/build/package-<os>-<arch>/` | Staging directories for platform packages |
| `app/build/deploy-<os>-<arch>/` | Binaries for direct deployment |
| `app/mygo/resources/<os>-<arch>/` | Desktop app resources staged by `make prepare` |

Platform archives are written to `app/build/`.

### kiko-server targets

Run these from `app/` (or with `make -C app`):

| Target | Result |
| --- | --- |
| `build` | `build/bin/kiko-server` for the host |
| `darwin-amd64`, `darwin-arm64`, `linux-amd64`, `linux-arm64`, `windows-amd64`, `windows-arm64` | Cross-compiled `kiko-server` in `build/bin/` |
| `package-linux` | `kiko-server-linux-{amd64,arm64}.tar.gz` with `kiko-core` and the scripts in `packaging/linux` |
| `package-windows` | `kiko-server-windows-{amd64,arm64}.zip` with `kiko-core.exe` and `start.cmd` |
| `package-macos` | `kiko-server-macos-{amd64,arm64}.tar.gz` without `kiko-core` |
| `oneclick-macos` | A package for the current Mac including `kiko-core` and `kiko-helper` (`KIKO_CORE=/path/to/kiko-core` to supply your own core) |
| `android-arm64` | `kiko-server` and `kiko-core` for Android in `build/deploy-android-arm64/` |
| `mygo-dev`, `mygo-app`, `mygo-test` | Shortcuts for `make -C mygo dev/build/test` |

### Android arm64

```sh
make -C app android-arm64
```

The NDK is picked up from the usual Android SDK locations on macOS and Linux. Both `kiko-server` and `kiko-core` (with the default build tags) are built. To choose the NDK and API level:

```sh
make -C app android-arm64 ANDROID_NDK=/absolute/path/to/ndk ANDROID_API=28
```

`ANDROID_CC` overrides the full path to the NDK compiler.

## Running kiko-server

```sh
kiko-server --core /path/to/kiko-core --data-dir ./data
```

| Flag | Meaning |
| --- | --- |
| `--core` | Absolute path to the `kiko-core` binary (required; `--sing-box` is still accepted) |
| `--listen` | Loopback address for local clients, default `127.0.0.1:9099`; only loopback addresses are accepted |
| `--remote-listen` | Enable remote TLS access on this address (for example `:9443`); persisted in `remote.json` |
| `--data-dir` | Data directory |
| `--auto-start` | Start the core on launch and keep retrying until stopped |
| `--privileged-helper` | Start the core through the macOS privileged helper |

`kiko-server pair` prints a one-time pairing string and code for adding the server to a desktop app.

### Unattended start

With `--auto-start`, kiko-server starts the core at launch. If the runtime configuration has not been generated yet, fails validation, or the process exits, the intent to run is kept and the start is retried with a back-off of up to 30 seconds. Stopping the core from the app cancels the retries for the current run; the next launch with `--auto-start` tries again. After a configuration is applied successfully, a waiting service starts on its next retry.

On Linux, `sudo ./install-service.sh` from the server package enables this flag on the systemd unit and restarts the service. Rerun it after updating the script to refresh `ExecStart`.

## Desktop app

```sh
make macos-app
```

This builds the core from the current `core` submodule, checks that the bundled core version matches the source, and writes the app to `app/build/darwin-$(go env GOHOSTARCH)/Kiko.app`. See [app/mygo/README.md](../app/mygo/README.md) for the desktop app itself.

The Clash API address and secret are generated the first time a configuration is applied and reused across later applies, core restarts and app restarts, so an open Connections workspace reconnects on its own once the core is running again.

Runtime data (including `cache.db`, the runtime configuration and logs) is stored in the user data directory:

```text
~/Library/Application Support/Kiko/kiko-server/
```

When the app starts the core it uses this writable directory as the working directory, since the app bundle's resources are read-only.

### TUN on macOS

On first launch the app offers to install a helper, and macOS asks for administrator approval. The installer copies `kiko-helper`, `kiko-core` and the launchd job to fixed locations:

```text
/Library/PrivilegedHelperTools/io.github.getkiko.kiko.helper
/Library/PrivilegedHelperTools/io.github.getkiko.kiko.core
/Library/LaunchDaemons/io.github.getkiko.kiko.helper.plist
```

The desktop app and kiko-server run as the logged-in user. The privileged helper only starts and stops the bundled core, which lets it create a `utun` interface while configuration and app state stay in the user's directory.

When the bundled core changes, the app prompts to reinstall the helper so the system copy stays in sync; it can also be reinstalled from the app menu.

## Windows

### Portable server package

```sh
make -C app package-windows
```

This produces `app/build/kiko-server-windows-amd64.zip` and `app/build/kiko-server-windows-arm64.zip`, each with `kiko-server.exe`, `kiko-core.exe` and `start.cmd`. Extract the archive for your architecture, run `start.cmd`, and connect from the Kiko app at `127.0.0.1:9099`. Configuration and logs are kept in `%APPDATA%\kiko-server`. Run `start.cmd` as administrator to use a TUN inbound.

### Desktop app

Prepare the `build/package-windows-{amd64,arm64}` binaries for the target architecture, then:

```sh
make -C app/mygo build TARGET_OS=windows TARGET_ARCH=amd64
```

The app is written to `app/build/windows-amd64/`. Keep `Kiko.exe` next to its `bin/` directory. On Windows the app uses the system title bar, and closing the last window quits it.

For TUN on Windows, DNS is configured on the TUN adapter by the core (sing-tun). With `auto_route` enabled and `dns_mode` at its default or `native`, an empty `dns_address` derives the DNS server from the TUN interface address; `dns_mode=disabled` turns this off. On macOS, kiko-server sets and restores the network service DNS itself.

### Manual test checklist

CI covers Windows console control events, restoring temporary registry proxy settings, and builds both architectures. Driver-level behavior needs a real Windows machine of each architecture:

1. Launch the desktop app and check that Settings shows the run privileges. A second launch focuses the existing window; with port 9099 taken, launch reports a port conflict.
2. Create or import a configuration, enable TUN with `auto_route`, clear `dns_address`, and apply. Check the TUN adapter DNS with `Get-DnsClientServerAddress`, then verify name resolution, proxied traffic, IPv4 and IPv6.
3. In the process picker, choose paths containing non-ASCII characters, spaces and parentheses. Check icons, path rules and actual traffic matching. Test Taildrop send, receive and open folder.
4. Record the initial system proxy, enable the mixed system proxy, then switch to PAC. Quit and check that the original value is restored and that `Get-Process kiko-core,kiko-server -ErrorAction SilentlyContinue` returns nothing. Relaunch and check that the configuration is kept.
5. Export and import a device over USB/IP, quit and check that the device is released, then relaunch and reconnect. Also try cancelling UAC, stop then restart, closing the last window, and quitting from the menu.

## Linux: per-device routing

In a routing rule's match conditions, add a source device and pick one or more devices by name, then choose the outbound. Matching uses the MAC address by default and can switch to the DHCP hostname; hostnames are case-sensitive and devices with the same name match together. Device labels are part of the configuration, so they are backed up and synced with it. Exported sing-box configurations use `source_mac_address` or `source_hostname`.

The device list merges the neighbor table of the server's network namespace with DHCP leases, read when the picker opens or on refresh. On OpenWrt `/tmp/dhcp.leases` is detected by default; on other distributions the common ISC dhcpd and Kea lease paths are checked. Additional lease files can be listed (one absolute path per line) under device discovery on the routing page; they are also exported as `route.dhcp_lease_files` so the core can use them for matching. The list is available while the core is stopped.

Each device shows whether it came from the neighbor table or a DHCP lease. When a device disappears temporarily, the selected identifier and saved name are kept. Per-device routing works when LAN traffic is forwarded through the host with the source address preserved; which traffic is captured depends on the inbounds and system routes. Wi-Fi devices that use a fixed private MAC address keep a stable identity.

The discovery API is `GET /api/v1/lan-devices`, with repeatable `lease_file` query parameters. The response contains `devices` (MAC, DHCP hostname, IP addresses and source) and `warnings`. `capabilities.lanDevices` in `GET /api/v1/info` reports whether automatic discovery is available; on other platforms it is off, and existing rules can still be viewed and edited by hand.

## Process monitor

See [app/PROCESS_MONITOR.md](../app/PROCESS_MONITOR.md) for the process monitor's behavior, metrics, platform collectors and HTTP API.

## Self-hosted sync

See [app/syncworker/README.md](../app/syncworker/README.md) for deploying the Cloudflare Worker sync backend.
