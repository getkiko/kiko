# Releasing Kiko

Source code, releases and the documentation site all live in the public repository `getkiko/kiko`. Installers and install scripts are uploaded to the repository's GitHub Releases, and the documentation site is deployed to its GitHub Pages, so users can download without signing in. The workflow is `.github/workflows/release.yml` and uses the built-in Actions `GITHUB_TOKEN`.

| Platform | Install | Upgrade |
| --- | --- | --- |
| macOS | `curl -fsSL https://github.com/getkiko/kiko/releases/latest/download/install.sh \| sh` | In-app update check (daily, or from the app menu); or rerun the install command |
| Windows | `irm https://github.com/getkiko/kiko/releases/latest/download/install.ps1 \| iex` | Same as macOS |
| Linux server | `curl -fsSL https://github.com/getkiko/kiko/releases/latest/download/install-server.sh \| sudo sh` | `sudo kiko upgrade` |

On Linux servers the management command is `kiko` (`sudo kiko pair`, `kiko status`, `sudo kiko restart`, `kiko logs`, `sudo kiko uninstall`, ...); the service binary is `kiko-server`.

Documentation site: <https://getkiko.github.io/kiko/>.

## One-time setup

1. Generate the update signing key pair. Do this once: if the private key is lost, installed apps can no longer be updated.
   ```sh
   cd app/mygo && go tool mygo keygen -o ~/kiko-update-keys
   ```
   Store `mygo-update.key` in a password manager. Never commit it.
2. In `getkiko/kiko`, open Settings → Secrets and variables → Actions and add:

   | Type | Name | Value |
   | --- | --- | --- |
   | Variable | `MYGO_UPDATER_PUBLIC_KEY` | Contents of `mygo-update.pub` |
   | Secret | `MYGO_UPDATER_PRIVATE_KEY` | Contents of `mygo-update.key` |

   Or from the command line:
   ```sh
   gh variable set MYGO_UPDATER_PUBLIC_KEY --repo getkiko/kiko < ~/kiko-update-keys/mygo-update.pub
   gh secret set MYGO_UPDATER_PRIVATE_KEY --repo getkiko/kiko < ~/kiko-update-keys/mygo-update.key
   ```
3. In Settings → Pages, set **Source** to **GitHub Actions**.

The `core` submodule points to the public repository `getkiko/kiko-core` (branch `kiko`), so the workflow checks it out without an extra token.

## First release

The Burrow dependency has already been switched to its published version. Done:

1. `getkiko/burrow` was tagged `v0.1.0` and the Go module proxy resolves it.
2. `core/go.mod` (repository `getkiko/kiko-core`) requires `github.com/getkiko/burrow v0.1.0` and has no `replace` for it; `go.sum` was updated with `GOWORK=off go mod tidy`. This is a commit on the `kiko` branch, and the gitlink in this repository points to it.
3. `app/go.mod` lists `github.com/getkiko/burrow v0.1.0` as an indirect requirement; its only `replace` is `github.com/sagernet/sing-box => ../core`. `app/mygo` and `scripts/release/site` do not depend on Burrow.
4. `go.work` uses `./app`, `./app/mygo`, `./core` and `./scripts/release/site`, without a Burrow directory, so a clone of this repository builds as published.

`core` and `app` build with `GOWORK=off` as well. To test an unreleased Burrow change, check it out next to the workspace and add it temporarily with `go work use ../burrow` (do not commit that). To move to a newer Burrow tag, bump it in `core/go.mod` first (`GOWORK=off go get github.com/getkiko/burrow@<tag> && go mod tidy`), commit and push to `getkiko/kiko-core`, then run `GOWORK=off go mod tidy` in `app`, update the gitlink, and commit both here.

## Each release

1. Bump `version` in `app/mygo/mygo.json`, commit and push. The commit the core gitlink points to must already be pushed to `getkiko/kiko-core`.
2. Push the matching tag:
   ```sh
   git tag v0.3.0 && git push origin v0.3.0
   ```
   You can also run the Release workflow manually from the Actions page; the version is then read from `mygo.json`.
3. The workflow then:
   - creates a draft release;
   - builds macOS (Apple silicon, Intel) and Windows (x64, ARM64) and uploads them together with the signed update files, `install.sh` and `install.ps1`;
   - builds the Linux server (x86_64, ARM64) and uploads it with `install-server.sh`;
   - publishes the release, generates the documentation site and deploys it to GitHub Pages.

Installed desktop apps offer the update on their next check. Updates are accepted only when signed with `MYGO_UPDATER_PRIVATE_KEY`.

## Local dry run

To check the packages without uploading anything, use a test key pair. In Actions `RELEASE_REPO` defaults to `GITHUB_REPOSITORY`; locally, set it explicitly:

```sh
export RELEASE_REPO=getkiko/kiko UPLOAD=0
export MYGO_UPDATER_PUBLIC_KEY="$(cat mygo-update.pub)" MYGO_UPDATER_PRIVATE_KEY="$(cat mygo-update.key)"
scripts/release/build-desktop.sh darwin arm64   # app/build/darwin-arm64/
scripts/release/package-server.sh amd64         # app/build/release/
(cd scripts/release/site && GOWORK=off go run . -repo getkiko/kiko -version 0.3.0 -o /tmp/site)
```

## Known gaps

- The macOS app is ad-hoc signed. Installing with the install command or updating in-app works; a DMG downloaded in a browser is blocked by Gatekeeper until the app is signed with a Developer ID and notarized.
- The Windows app is not code-signed, so SmartScreen warns on first launch.
