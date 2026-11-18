# Kiko

Kiko is a desktop client based on [sing-box](https://github.com/SagerNet/sing-box), plus a headless server you can manage remotely from the same app.

The desktop app (macOS and Windows) runs its core, `kiko-core` (a sing-box fork), on your machine and gives you a native console for routing, DNS, connections and traffic usage. `kiko-server` runs the same engine on a Linux host without a display, such as a home gateway or a VPS, and the desktop app pairs with it over TLS to manage it from anywhere on your network.

> The Kiko user interface is currently available in Chinese only.

<!-- Screenshots: add images of the Overview, Routing topology and Connections workspaces here. -->

## Features

**Proxy and routing**

- Native console built with [MyGo](https://github.com/egoist/mygo): no webview, no bundled web frontend.
- Subscriptions, inbounds, outbounds and endpoints in one place, with latency testing that runs in parallel and a unified latency mode.
- Policy groups with manual, fastest, failover, load-balance and smart selection; nested groups fail over as a whole.
- Routing rules per device, each device with several switchable profiles, plus a live topology view that shows which rules are carrying traffic.
- "Apply without restart": most configuration changes are applied to the running core without dropping unrelated connections.
- DNS split rules that can race several DNS servers, each with its own outbound.
- Rule sets with in-app browsing, search, per-rule overrides and user-created rule sets.

**Monitoring**

- Connections, DNS queries and rejected connections, with faceted filters, grouping and jump-to-rule.
- Traffic usage history recorded per minute, with grouping, cross-breakdowns, time ranges and CSV export.
- Process monitor with per-app grouping, CPU, memory, energy and per-process network usage.
- LAN scan that identifies devices on the local network for per-device routing.

**HTTPS inspection and rewriting**

- Opt-in HTTPS decryption for listed hosts, with a locally generated root certificate stored in the system keychain.
- HTTP/1.1, HTTP/2, gRPC, WebSocket and SSE capture, request replay and HAR 1.2 export.
- URL, header and body rewriting, local mapping, and Surge-compatible JavaScript scripts.
- Module import (`.sgmodule` subset and `.kikomodule`) with a review step before anything is installed.

**Remote management and sync**

- `kiko-server` for Linux hosts, managed through a pinned TLS connection with per-device tokens.
- Tailscale endpoint with network diagnostics and Taildrop.
- End-to-end encrypted configuration backup and sync over WebDAV, S3 or Cloudflare KV. Kiko runs no public sync service: for Cloudflare KV, deploy the sync Worker to your own account ([guide](app/syncworker/README.md), optional [pages.dev entry](app/syncworker/pages/README.md)).
- Configuration sharing links. The share service is part of the same self-hosted Worker as Cloudflare KV sync: one deployment provides both, and you enter its address in Settings ([guide](app/syncworker/README.md#sharing)).
- Signed in-app updates for the desktop app.

See [app/CHANGELOG.md](app/CHANGELOG.md) for the full list.

## Install

Installers and the documentation site are at **<https://getkiko.github.io/kiko/>**.

| Platform | Install | Upgrade |
| --- | --- | --- |
| macOS | `curl -fsSL https://github.com/getkiko/kiko/releases/latest/download/install.sh \| sh` | In-app update check, or rerun the install command |
| Windows | `irm https://github.com/getkiko/kiko/releases/latest/download/install.ps1 \| iex` | In-app update check, or rerun the install command |
| Linux server | `curl -fsSL https://github.com/getkiko/kiko/releases/latest/download/install-server.sh \| sudo sh` | `sudo kiko upgrade` |

On a Linux server, `kiko` is the management command (`sudo kiko pair`, `kiko status`, `sudo kiko restart`, `kiko logs`, `sudo kiko uninstall`). Run `sudo kiko pair` and add the server from the desktop app with the pairing string it prints.

The macOS app is ad-hoc signed and the Windows app is not code-signed yet. The install commands are not affected; a DMG downloaded in a browser is blocked by Gatekeeper, and Windows SmartScreen may warn on first launch.

## Build from source

Requirements:

- Go 1.27.3 or later
- Git, to fetch the core submodule
- macOS: Xcode Command Line Tools for the desktop app

Clone with the core submodule ([getkiko/kiko-core](https://github.com/getkiko/kiko-core), a sing-box fork):

```sh
git clone --recursive https://github.com/getkiko/kiko.git
cd kiko
# or, in an existing clone:
git submodule update --init --recursive
```

Common targets:

| Command | Result |
| --- | --- |
| `make test` | Run the server and desktop app tests |
| `make core` | Build `kiko-core` from the submodule into `app/build/core/` |
| `make macos-app` | Build the core and `Kiko.app` into `app/build/darwin-<arch>/` |
| `make -C app build` | Build `kiko-server` into `app/build/bin/` |
| `make -C app package-linux` | Linux server archives (amd64, arm64) |
| `make -C app package-windows` | Windows portable archives (amd64, arm64) |
| `make -C app android-arm64` | `kiko-server` and `kiko-core` for Android arm64 (needs the Android NDK) |
| `make -C app/mygo dev` | Run the desktop app with live reload |

Build output goes to `app/build/`, which is ignored by Git. See [docs/development.md](docs/development.md) for platform notes and [RELEASING.md](RELEASING.md) for the release pipeline.

## Repository layout

| Path | Contents |
| --- | --- |
| `app/` | `kiko-server`: HTTP/gRPC API, core lifecycle, monitoring, HTTPS inspection, sync |
| `app/mygo/` | The desktop app and its native console |
| `app/helper/`, `app/helperpaths/` | macOS privileged helper that runs `kiko-core` for TUN |
| `app/syncbackend/`, `app/syncvault/` | Sync backends (WebDAV, S3, Cloudflare KV) and the encrypted vault |
| `app/syncworker/` | Self-hosted Cloudflare Worker for multi-user sync |
| `app/lanscan/` | LAN device discovery |
| `app/packaging/` | Per-platform packaging scripts and READMEs |
| `core/` | `kiko-core`, the core (submodule, `getkiko/kiko-core`, a sing-box fork) |
| `scripts/release/` | Release build scripts and the download site generator |
| `docs/` | Developer documentation |

## Related projects

- [getkiko/kiko-core](https://github.com/getkiko/kiko-core): Kiko's core, a fork of sing-box.
- [getkiko/burrow](https://github.com/getkiko/burrow): the Burrow NAT traversal library behind the core's `burrow` inbound and outbound.
- [SagerNet/sing-box](https://github.com/SagerNet/sing-box): the upstream universal proxy platform.

## License

Kiko is licensed under the GNU General Public License v3.0 or later (GPL-3.0-or-later), the same license as sing-box. See [LICENSE](LICENSE).
