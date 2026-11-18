# Changelog

## 0.3.0

First public release of Kiko: a desktop client based on sing-box for macOS and Windows, and `kiko-server`, a headless server for Linux that the desktop app manages remotely.

### Desktop app

- Native console drawn by MyGo on the GPU, with no webview or bundled web frontend.
- Overview with traffic charts, policy groups, unapplied changes and a Tailscale status card.
- "Apply without restart": configuration changes are applied to the running core in place; when a rule's outbound changes, you can choose to interrupt the connections it already carries.
- Configurable time zone for every timestamp and exported file name.
- Signed in-app updates, checked daily.

### Proxies and policy groups

- Subscriptions, inbounds, outbounds and endpoints as compact cards, with search, section jump and usage meters for subscriptions.
- Policy groups with manual, fastest, failover, load-balance and smart selection. Failed members are re-probed and failover returns to the preferred member once it recovers; nested groups fail over as a whole.
- Region detection from flag emoji, keyword-based member selection and a two-pane group editor that shows the final member list.
- Parallel latency tests from any server entry, with a unified latency mode that measures a warmed-up request.

### Routing and DNS

- Per-device routing with several profiles per device, preview on click and switch with confirmation.
- Topology view of devices, rules and outbounds, with live activity, path testing and the connections each rule currently carries.
- LAN scan that identifies devices by name, type and model, with device images.
- Linux per-device matching by MAC address or DHCP hostname, from the neighbor table and DHCP leases.
- DNS split rules that query several DNS servers in parallel, each with its own outbound, using the first valid answer.
- Rule sets as a settings page: browse and search contents (including very large sets), add or remove rules on top of any rule set, and create your own.
- Draggable and resizable columns in the rule tables, with unsaved-change protection in the rule editor.

### Monitoring

- Connections, rejected connections and DNS queries with faceted filters, grouping by domain or root domain, and jumps between connections, rules and DNS.
- Usage history recorded per minute, with grouping, cross-breakdowns, time ranges and CSV export.
- Process monitor with app, parent and flat grouping, per-process CPU, memory, energy, disk and network, and quit or reveal actions.
- Shared app icon cache across Connections, Processes and Usage.

### HTTPS inspection and scripting

- Opt-in HTTPS decryption for listed hosts, with built-in exclusions for banking and system services and automatic skipping of hosts that reject the certificate.
- Root certificate generated locally, with the private key in the system credential store (Keychain, DPAPI, Secret Service, or a 0600 file on headless hosts). Export as `.crt`, `.p12` or `.mobileconfig` and a time-limited LAN download page with QR code.
- HTTP/1.1, HTTP/2, gRPC, WebSocket and SSE with streaming preserved. QUIC is blocked for decrypted hosts so traffic falls back to TCP.
- HTTP request inspector with timing waterfall, headers and bodies, copy as cURL, edit and replay, and HAR 1.2 export. Optional on-disk capture with size limits and redaction of sensitive headers.
- Rewrites: URL reject and redirect, header rewrites, local mapping, body rewrites (regex and JSONPath) and internal host rewrites.
- Surge-compatible JavaScript scripts (request, response, generic, cron, rule and DNS) with editor, dry run, logs, storage and notifications.
- Modules: import a `.sgmodule` subset or `.kikomodule` after reviewing the hosts, scripts and rules they add; automatic update checks.

### Remote management

- `kiko-server` for Linux, installed with one command and managed with the `kiko` command (`pair`, `status`, `restart`, `logs`, `upgrade`, `uninstall`).
- Remote access over TLS with certificate pinning, one-time pairing codes and per-device tokens that can be revoked.
- Write operations are serialized across clients, with the current holder shown to other clients.
- `--auto-start` for unattended hosts, retrying until the core is running.

### Tailscale and devices

- Tailscale endpoint with browser or auth key login, network diagnostics, UDP path control through a proxy, Taildrop and SSH.
- USB/IP device export and import.

### Backup and sync

- End-to-end encrypted configuration backup and sync over WebDAV, S3 or a self-hosted Cloudflare KV Worker with multi-user access tokens.

### Platforms

- macOS (Apple silicon and Intel) with a privileged helper for TUN.
- Windows (x64 and ARM64) desktop app and portable server package.
- Linux server (x86_64 and ARM64) with system proxy support through GSettings.

### Known limitations

- The user interface is in Chinese only.
- The macOS app is ad-hoc signed and the Windows app is not code-signed.
- The local API has no access token; it listens on loopback only.
- Certificate installation and the LAN download page have not been verified on real Windows, Linux, iOS and Android devices.
