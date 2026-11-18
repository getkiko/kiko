# Kiko MyGo desktop app

The desktop app uses [MyGo](https://github.com/egoist/mygo) v0.7.3. Its
console is native UI drawn by MyGo's `ui` package on the GPU (Metal,
Direct3D 11, OpenGL), written in Go under [`internal/`](internal/README.md):
no webview and no bundled frontend. The app starts kiko-server, talks to its
API on the loopback address, and bundles kiko-server, kiko-core and the
macOS helper.

It also controls remote kiko-servers, such as a headless Linux host: each
server opens in a window of its own (the Servers menu, or Settings › Servers). A remote
server is reached over TLS with its certificate pinned at pairing and a
per-device Bearer token. Pair with a string printed by `kiko-server pair` on
the host (or shown under Settings › Remote Access › Pair New Device on a
device already connected), or with its address and an 8-character code, comparing the
certificate fingerprint. Tokens live in the system credential store
(Keychain, Secret Service, Credential Manager) — or, where there is none,
in `server-tokens.json` readable by the user only. The server list is
`servers.json` in the app data directory.

Run from this directory with Go 1.27.3+:

```sh
make dev     # run the app with live reload
make build   # package the app
make test    # go vet and go test, including headless view tests
```

The console's views are tested headless with `ui.Tester` against a fake
server (`internal/testkit`), as fast as unit tests.

For a Windows desktop build on macOS, prepare the matching binaries under
`../build/package-windows-amd64` or `../build/package-windows-arm64`, then run:

```sh
make build TARGET_OS=windows TARGET_ARCH=amd64
```

Always package through `make build`: `mygo.json` has no build command,
and `mygo build` ships whatever `make prepare` staged in
`resources/<os>-<arch>/`.

`make prepare` copies the prepared binaries. It checks the worktree first,
then the shared checkout located through `git rev-parse --git-common-dir`.
Explicit paths work as well:

```sh
make build BINARY_DIR=/path/to/build/package-macos-arm64 \
  BUILD_FLAGS=-skip-dmg
```

The app is written to `../build/darwin-arm64/Kiko.app` on Apple silicon.
`make build` also creates a DMG by default. macOS builds use ad hoc signing;
set `MACOS_SIGNING_IDENTITY` to use a Developer ID.

The hidden macOS title bar keeps the traffic lights at (16, 16); the app
navigation draws under them, clear of their room (`ui.Context.TitleBar`), with
the small collapse button beside them, and page toolbars drag the window. On
macOS the sidebar shows the sidebar material (`VibrancySidebar`) behind it,
none while Reduce Transparency is on; the content stays opaque.

Set `KIKO_DEBUG_PROBE=/tmp/kiko-probe.json` when launching to record the local
server's console state (page, document load, server connection, core state) and a
PNG of the window beside the JSON.

kiko-server has no web console: it is controlled with this app, locally
or remotely.

MyGo documentation: https://github.com/egoist/mygo/blob/main/docs/README.md
