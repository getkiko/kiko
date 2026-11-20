# Native console (MyGo `ui`)

The desktop console is drawn by [MyGo](https://github.com/egoist/mygo)'s
native `ui` package (GPU-rendered, no webview). It replaces the former web
console with the same workspaces, data and server API. kiko-server does not
serve a web console; this client controls it, on the loopback address or
remotely over TLS, one window (and one `console.Console`) per server.

## Packages

| Package | Role |
| --- | --- |
| `api` | Typed client of kiko-server (`/api/v1`): JSON requests, the SSE event stream, NDJSON streams. `api.New` is the trusted loopback client; `api.NewWith(url, Options{Token, Fingerprint})` a remote server's, pinning its certificate (`*FingerprintError`) and sending the Bearer token on every request, streams and the SSH WebSocket (`TLSConfig`, `Header`) included; `Reconfigure` swaps them after pairing again. 401 matches `ErrUnauthorized`, a 423 of the write gate carries `Error.Lock`; `Pair`, `FetchFingerprint` and the `/remote*`, `/lock` endpoints (`remote.go`) |
| `servers` | The app's server directory (`servers.json` in the app data directory): the local server (fixed, never stored) and the paired remote servers with their pinned fingerprints; tokens in the credential store (`SecretStore`, the desktop's `mygo.Secrets.Service("kiko-servers")`), else in `server-tokens.json` (0600). Pairing from a `kiko-pair://` string or by hand with trust on first use, pairing again, rename, remove (revoking the device), the windows to restore |
| `model` | The configuration document (`Doc`), ids, ordering, defaults and migrations (`defaults.go`: `EmptyDoc`, `Make*`, `FillDefaults`, `DecodeDoc`), route / DNS device policies (`routepolicy.go`, `devicepolicy.go`, `dnspolicy.go`, `conditions.go`), shared network/TLS configs (`network.go`), DNS servers and addresses, the public DNS presets every document lists (`dnspresets.go`: `AddDNSPresets` adds the missing ones disabled, once per `DNSPresetsVersion`), reference checks and page warnings (`references.go`), rule set catalog and fetching (`rulesets.go`), input parsing (`matchinput.go`, `proxyaddress.go`, `lan.go`), the node region table after FlClash (`regions.go`: `Doc.NodeRegions` or `DefaultRegions`, `RegionOf`, info nodes, `Outbound.Filter` and `GroupMembers`, the members a group's filter adds at emit time; RE2, FlClash's lookarounds rewritten) |
| `schema` | Protocol field tables driving forms and output (`tables_gen.go`, generated once from the former TypeScript and maintained by hand), form visibility and validation, list item formats, Burrow signal addresses; `schema/jsv` gives JSON values JavaScript's coercions |
| `emit` | `Doc` → sing-box configuration JSON (key order as the web console writes it), dependency cycle checks |
| `store` | The edited document, synced with `GET/PUT /store` (revisions, conflicts, remote updates), apply (`Apply` restarts the core's services, `ApplyLive` applies without restarting), unapplied changes against `GET /config/applied` by workspace (`unapplied.go`), speed tests; loading (and `SetProfiles`) completes the document and the profiles with the DNS presets |
| `env` | What workspaces share: API client, live server state (events, runtime, logs, the write gate's `Lock`, the last connection `Failure`, `Info` with who the server takes this console for), desktop shell, UI-thread posting, toasts, navigation (`Navigate` with `RouteRule`, `NewRouteRule`, `ConnectionsView`, `UsageView` for the pages that take them). `Target` is the server the console shows (`Remote` for a paired one); a shell that keeps the server list implements `ServerDirectory`, which the settings use from any window |
| `kit` | Component library: theme tokens, controls (`Search`: Escape empties it, and in an empty field goes on to the dialog or page around it; `BeginTextFocus` / `TextFocused`: whether an `Input`, `Search` or `TextArea` built since has the focus, so that a page's Escape can leave the field first; `SegmentedFill`, the segmented control spanning its container in equal segments; `SearchSelect` and `SearchMultiSelect`, the searchable popover choosing one value or several in the order chosen; `OnQuery` tells the search text for options that come from a server), toolbar, filter panes (resizable by `ResizeHandle`, widths in `PaneWidth`), tables (`TableOptions.HasMenu` keeps the context menu off rows it has nothing for; `MenuFreeze` holds a list's derived view still while a row's popover menu shows, the data arriving behind it and showing as the menu closes: `Begin`, `Hold`, `Menu` wrapping `TableOptions.Menu`, `End` each pass), `FacetFilter` (faceted filter counts in one pass: each group's options over the records passing every condition but its own, `Miss` / `Counts`; the server's `app/facetcount.go` is its twin; `FacetChoice`, the chosen values of each group, several per group: any of a group's values passes it and every group must pass, `Toggle` / `Add` / `Has` / `Clone`, `Only` keeping one value alone in its group (or unchoosing the group when it already is, as "Show only this" does) and `IsOnly`, `Match` the group's condition), inspector, dialogs, confirm popover (with an optional `Body`, such as a `ChoiceList`), editor sidebar, dot chart (Usage, Processes), smooth area chart (`AreaChart`: monotone line over a fading gradient, gaps for nil samples, hover readout; the Overview metric cards), side sheet (`SideSheet`: slides in from the right edge, at once with less motion; Escape and a press outside close it, `Refocus` telling the view to return the focus after Escape or its close button), `CapsuleTag`, `TokenInput` (whole-field tag input: click / Cmd-click / Shift-click choose chips, Cmd+A, Cmd+C (one value a line, all when none chosen), Backspace / Delete and Escape on the choice in the empty input; `Paste` takes a paste of several values split by `Split` (`SplitTokens`, `SplitTokenLines` keeping spaces); `Token.Clip` + `ClipKind` paste an in-app copy back losslessly; a context menu on the field and chips copies, pastes, chooses all and clears; `RemoveAll` removes several at once), hero cards (`HeroCard`: the Components page's compact card, about 104 DIPs, one button labeled by the caller, a type-colored glow in the top right, header / main / footer slots with `HeroDot`, `HeroInitial`, `HeroName`, `HeroTag`, `HeroValue`, `HeroMono`, `HeroLatency`, an optional background `Spark`, a hover-revealed "More actions" button returned for anchoring confirmations, `ContextMenu` listing the same actions on a secondary click, `EntityMenuItem.Disabled` showing an action in Label3 without clicks; `HeroColumns` 1–4 columns at 640 / 900 / 1200 DIPs; `AddCard`, the dashed "+ Add..." cell; `CardSurface`, the surface shared with the Overview cards), `TypeLook` (symbol and system glow color of an inbound, outbound, endpoint or service type, light or dark), `QRCode` / `ParseQR` (a QR matrix of '1' / '0' rows drawn as dark modules on a white tile that stays white in dark mode, 4-module quiet zone, whole-DIP module size `QRModuleSize` for a 176 DIP tile, runs of modules merged into one rectangle), `Spinner` / `SpinnerIn` (work under way: AppKit's twelve spokes turning every 75 ms, size DIPs square, Label3 or a given color; it asks for its next step in its draw callback (`Painter.After`) without building the view and stops asking once gone; with less motion a still ellipsis), `BusyCover` (a disabled, empty button over a control busy with its work: takes the clicks and keeps the name without the half opacity MyGo paints disabled elements at), `IconButton` `Busy` / `BusyHint` (the spinner in the symbol's place and color, no clicks, unfaded; with less motion its own symbol in Label on the Fill, no ellipsis), `Sparkline` (a faint monotone trend, 10% area under a 1.5 DIP line at 55%, painted only, never asking for frames), settings layout, `Hint` (the only hover tip: a long rest, for icon-only buttons, status symbols and notes shown nowhere else), `CodeEditor` / `CodeState` (editable code on a solid 9 DIP surface with a hairline, accent outline while focused; JavaScript, JSON or plain text, 12 DIP monospaced at 18 DIPs a line, the console's palette for light and dark; `Text`, `SetText`, `Reset` for a new document, `Changed` for edits made since the previous build; Tab indents and Shift+Tab outdents, Escape closes the find bar, then collapses a selection, then reaches the dialog; click elsewhere to leave) and `CodeDiff` (read-only side-by-side or unified diff with word highlights and collapsed unchanged runs; side by side below `DiffNarrowWidth` falls back to unified), Lucide symbols |
| `nodeview` | What the views show of outbound nodes, shared by the Overview chooser and Components: region sections (`Sections`, the chooser's order), `Members` (short name, protocol, subscription), `ProtocolTag`, `LatencyTag` ("Timeout" / "Unavailable" in red on failure), `TestingMark` (the spinner in Label2 while a test runs, "Testing" in words with less motion), `MemberTag` / `MemberBadge` / `MemberTest` (a group member's availability in its latency tag alone: the app's last test, else the core's state, down "Unavailable" and suspect "Unconfirmed"; nothing untested; no status dots), `LatencyBadge` / `LatencyTest` (the same without a core state), `ViaBadge` / `ViaTest` (a member that is a group, showing the server its choice comes down to, named in the hover text), `Groups` (`NewGroups` of the running core's groups or `DocGroups`, the configuration's with their default choices: `Final` follows a group's choices to its server, at most 8 groups, stopping at a loop; `Servers` unfolds groups into all their servers, each once, loops cut; `Chosen` the servers members stand for now), `SectionHeader` (a region's header as a button testing its servers: hover fill, pointing hand and gauge, the spinner while they are tested, context menu "Test this group"), `WithoutFlags`, `GroupInitial` |
| `forms` | Renders `schema.Field` lists as native forms (every field type, visibility, validation, advanced section, generator actions, reference options); `Context.Inline` lays fields out label left (`InlineLabelWidth` 96), control right, numbers, durations and selects at most `InlineShortWidth`, switches ending their row, hints on hover; `Context.Narrow` stands the labels above their controls; `LabeledRow` is such a row for controls of the caller, stacked when not inline, and `RowLabel` the label of both (13 DIPs beside, 12 above, secondary color) (the policy group editor) |
| `profiles` | Profile bundles: import, export, URL import, preview, sharing, sing-box import |
| `subscription` | Share-link parsing and formatting, subscription traffic headers, and a native subscription converter (sing-box JSON, Clash YAML, share links) |
| `timefmt` | The times and durations the console shows, one entry for every workspace: `Duration(start, end, now)` (end zero for still running) and `Seconds`, in Chinese units: seconds under a minute, then minutes, hours and minutes, days and hours; the Connections inspector's start time and the Processes inspector's run time under a minute. The display time zone (Settings › General › Time Zone; an app preference of this computer, `PrefZone` in `Env.AppPrefs` = the shell's `app-preferences.json` shared by every window, empty to follow the system; the shell calls `SetZone` at launch, the settings row on a choice, so it shows on the next frame; `ZoneName` is the current choice): every shown time goes through `Location`, `In`, `Now`, `Milli`, `Format(t, layout)` and `FormatMilli(ms, layout)`, which equal `time.Local` while it follows the system; storage and transport stay RFC 3339 / Unix. `Zones` lists the IANA zones of the embedded `zones.txt` (tzdata's zone.tab, with `time/tzdata` bundled for Windows) by current offset, Chinese-named cities first, then IANA name; it never blocks: built on a goroutine at launch, offsets refreshed each hour, nil until built; with `City` (Chinese names for common cities, else the IANA city) and `Offset` (UTC+08:00, UTC−05:00) |
| `srcname` | Names for the source addresses of Connections (Network Connections, HTTP Requests, DNS Queries) and Usage, in filters, lists and inspectors: `New`/`FromDoc` build the lookup from the route devices (IP or CIDR addresses; exact over range, narrower over wider) and the host's addresses (the local server wins): the detected interface addresses, loopback, every TUN inbound's host addresses (configured or the platform defaults, from the document, core running or not) and the supplementary local addresses (single IPs, whatever the local scope switch); `Name`, `Text` (the "IP name" the group search matches). Display only: filter values, sorting, search and copies stay the IP. Drawn by `kit.FilterOptionNamed` (filter option: IP, secondary-colour single-line name with a full-text hint, count or total), `kit.NamedCell` (clipping table cell: the address in tabular digits, then the name in the cell's muted colour on one line; the name shrinks first, and only a cell too narrow for the whole address drops the name and ends the address with an ellipsis; an "IP · name" hint when either is clipped; an address without a name takes the same path, alone, so a column lines up; an optional second line) and `kit.NamedProperty` (inspector row: address and name on one line where they fit, else the name below; only the address is selectable), all in `kit/filtername.go` and `kit/namedcell.go`; `kit.FilterOptionIconDetail` is `FilterOptionDetail` (option with its total) with an icon before the label, Usage's process options; `kit.FilterOptionIconNamed` is `FilterOptionNamed` with an icon before the label, the Connections PID options (PID, then the process name; the filter value stays the PID) |
| `procicon` | The process icons of Connections, Processes and Usage, one store per console (`For(env)`): keyed by App bundle (`app:<bundle>`, the bundle taken from the path when the caller has none, so every process of an App and its long-gone usage rows share one icon) else executable path (`path:<path>`). Lookups run on goroutines, at most 4 at once, one per key while pending: the server's drawing of a running App process (`ProcessIcon`), the desktop's icon of the path (this host only, never for `Target.Remote`), else the server's icon of an existing executable's App (`GET /api/v1/process-icon?path=`). Decoded bitmaps stay in memory (512, least recently shown dropped first); their bytes on disk under `Env.CacheDir/process-icons/<local or remote-hash>` (1000 files, 32 MB, oldest written evicted; read on the next launch with no lookup, looked up again after 7 days while the kept one shows). Misses are memory-only and stay through the LRU while shown: asked again after 10 minutes, or at once by another running process of the App (PID and start time known). `Forget` deletes a removed remote server's disk directory; `For` then gives that console a store without a disk, so a last frame cannot write it back. Workspaces keep the store they got when built. `View` draws the icon or the kind's glyph (JS for Node, a terminal for shells, a window for Apps, a gear; the network symbol of Connections when nothing identifies a process) in the `Plain` (Connections, Usage) or `Tinted` (Processes) look; `FromPath` turns a Usage process value into a `Process` |
| `ruleactions` | "Add to rule": a connection's, usage group's or process's domain, suffix, keyword, destination IP, port, process name, path or macOS App as the conditions of a new traffic rule; `Menu` / `Items` add them to a context menu for the source's device (`model.DeviceForSource`) and the other devices, opening the rule unsaved in Routing (`env.NewRouteRule`) |
| `console` | App navigation and workspace switching; the sidebar's server badge (name, address, connection dot, a 3 DIP accent bar on remote windows), the notices for a down server, another device holding the write gate ("X is <operation>...") and a remote server needing pairing again (changed certificate, revoked token); blue dots on workspaces with unapplied changes |
| `workspaces/*` | One package per page; `rewrites` (Rewrites: URL, header and local-mapping rules, and `Editors`, the rule editor dialogs the HTTP inspector opens too); `scripts` (the Scripts tab of Rewrites: the script list with run counters polled every 3 s, `Editors`, the script editor dialog with its source editor, validation, leave guard and test area that dry-runs the draft through `ScriptEvaluate` on a captured or hand-written request and shows diffs, logs and timing, `Seed` / `TestSeed` for the inspector's "Add script for this request"; `ActionLabel`, `Duration`, `SkipLabel` shared with the inspector; cron scripts (the scheduled type with `NextRunText` for the list's next-run time) and "Run now" (`runnow.go`: result popover over `ScriptRun`, blocked unless the counters list the script); the store, log and notification pages it opens are `StorePage`, `LogsPage` and `NotesPage`, the last two following their streams through the generic `feed`; imports no workspace, `rewrites` and `connections` import it); `subscriptions` (Components: Subscriptions, Inbounds, Outbounds, Endpoints) and `settings`, whose Settings › DNS subpage (`settings/dnsservers`) holds the DNS settings and every DNS server |
| `model/tsgolden` | Replays the frozen calls recorded from the former TypeScript (its tests and a document corpus) against the Go port: `testdata/ts-calls.jsonl.gz` of model, schema and emit; the recorder is gone with the web console |
| `supervisor` | Keeps a child process running: restarts on unexpected exits with exponential backoff and jitter, resets after a stable run, stops in a crash loop; injectable clock and launcher. The desktop shell supervises kiko-server with it (`server.go`), and the console shows its status (`console/process.go`) |
| `workspaces/modules` | Modules tab of Rewrites: module cards, add / pending-confirmation dialog, parameters, content view, export, update queue and hourly schedule (`StartUpdates`); downloads through `api.Fetch`, conversion in `internal/modules` |
| `testkit` | Fake kiko-server and helpers for headless view tests: `/scripts/*` endpoints (`SetScriptStatus`, `SetEvaluate`, `ScriptEvalRequests`, `PushScriptLog`, `ScriptStore`, `SetScriptRunResult` / `SetScriptRunDelay` / `SetScriptRunFailure`); `/fetch` (`SetFetch` / `SetFetchBody` / `SetFetchHook` with status, delay and failure, `FetchCalls`, `FetchPeak`); `NewTLSServer` is a remote server taking a Bearer token, with remote access, pairing, devices and the write gate (`SetLock`); `Directory` / `DirectoryShell` fake the server directory |

Dependency direction: `api`, `schema` ← `model` ← `emit` ← `env` ← `store` ←
`kit` (UI only) ← `nodeview`, `forms`, `profiles`, `subscription`, `ruleactions`, `procicon` ← `workspaces/*` ← `console` ← `main`;
`api` ← `servers` ← `env`.

## Threading

Views build on the UI thread and read state directly. Never touch view state
from a goroutine: run requests with `env.Go` / `Env.Run` (results arrive on the
UI thread) or post with `Env.Post`. Workspaces that stream (connections, DNS,
processes, usage, logs) implement `env.Activator` and start their streams in
`SetActive(true)`, stopping them in `SetActive(false)` (hidden page or window).

Every window builds its frames on the main thread, one after another, so
the few frame-scoped globals of `kit` (the dialogs built in a frame, the row
colors on the accent) are safe across the windows of several consoles. Each
console has a `ui.View` of its own: windows sharing one `ui.Content` would
show the same console. Windows open on the main thread (`mygo.RunOnMain`),
never from inside a frame; `env.ServerDirectory.OpenServer` returns at once.

## Remote servers

- `servers.Directory` lists the local server and the paired servers; the shell opens a
  `serverWindow` per server with its own env, store and preferences
  (`console-preferences-<id>.json`). The local server's services restart the server
  process and install the macOS helper; a remote window's do neither, and
  local-only settings (helper, "Open log directory") and local icon lookups are
  hidden or skipped where `env.Target.Remote`.
- `api.NewWith` pins the certificate and sends the token on every request.
  The event stream stops retrying on `api.Fatal` failures; the console then
  offers "Pair again" (Settings › Servers, `servers:repair:<id>`), and the shell swaps
  the client's options with `Reconfigure` after pairing.
- Writes may wait in the server's FIFO write gate; a 423 reply names the
  holder. The store keeps a refused save and retries it after 1, 2, 4… s
  (five times), showing "Waiting for X to finish", before reporting a save error.
- Settings › Remote Access manages the shown server's own TLS listener, pairing
  codes and paired devices through `/api/v1/remote*`.

## Conventions

- Compose pages from `kit`; page code styles only its own content. Extend
  `kit` when a control is shared, keeping its look in one place.
- Colors from `kit.T(c)`, sizes from the `kit` constants (12 DIP table text,
  32/48 DIP rows, 30 DIP controls, 64 DIP toolbar…).
- Keep state that lasts in the workspace struct; keys (`c.Key`) for stateful
  controls inside loops.
- Change the document only through `Store.Update(func(doc *model.Doc))`; it
  validates dependencies, keeps the active profile current and saves. Editors
  work on a copy, call `Store.BeginEdit` while open and `EndEdit` on close.
- Shared confirmations use `kit.ConfirmPopover` anchored to the trigger; rule
  editors use `kit.EditorSidebar`, other editors `kit.Dialog`. A large
  dialog laying out its own header, independently scrolling columns and
  footer (the policy group editor) uses `kit.DialogFrame(c, open, label,
  width, height, fn)`: a fixed-size panel (capped at the window less 16 DIPs
  a side) whose body is a plain column, no outer scroll; the wheel over the
  backdrop scrolls nothing. With unsaved
  changes, `kit.EditorSidebarGuarded` and its `EditorGuard` (the caller sets
  only `Dirty` each frame) ask "Discard changes?" in a bar inside the sidebar,
  over the footer actions, when a press outside or a switch the caller routes
  through `Ask` would drop the draft: "Keep editing" (focused, and Escape) keeps it,
  "Discard changes" closes or runs the latest switch asked. The close button, Escape
  and the caller's Cancel (`EditorGuard.Discard`) close at once, without
  asking. This in-sidebar question is the editor's exception to anchoring.
- Popovers beside a button: grouped action menus use `kit.ActionMenu`
  (`ActionItem.Hint` for a hover hint; `Disabled` items show in the
  tertiary text color and do nothing, `Unavailable` ones look the same but
  stay focusable and run, as to lead to the setting that enables them). A
  hand-built `ui.PopoverBase` places its panel with
  `kit.AttachPopover(c, panel, anchor, at, self, gap, limit)` and puts its
  content in `kit.PopoverBody` (or its own `ui.Scroll`): MyGo flips a
  popover above its anchor without moving it along that side, so the
  height is held to the larger room (`kit.PopoverRoom`) and the content
  scrolls, keeping the panel inside the window.
- Reorderable table columns: keep a `kit.ColumnOrder` per table, pass it as
  `TableOptions.ColumnOrder` (or use its header helpers in a hand-drawn
  table), persist it in `OnReorder`; reset goes through `ConfirmPopover`.
- Resizable table columns: `TableOptions.Resizable` keeps the dragged
  widths in `TableState.Widths` (by column ID), persisted in `OnResize`. A
  hand-drawn table keeps a `kit.ColumnWidths`, sizes its header and row
  cells with `ColumnWidths.Width`, ends each header cell (filling the
  header's height, `PassThrough`) with `kit.ColumnGrip`, and pads cells
  with `kit.TableCellInset`. Grips drag (at least 60 DIPs, or a narrower
  column's own width; at most 900), double-click to the default, and take
  Left / Right when focused. `OrderView.WithWidths` makes the order's reset
  button ("Restore default columns") show for changed widths too and restore both.
- Tests run views headless with `ui.NewTester` against `testkit.NewServer`;
  `testkit.Until` waits for asynchronous results.

## Commands

```sh
go build ./... && go vet ./... && go test ./...      # from app/mygo
KIT_PREVIEW_DIR=/tmp go test ./internal/kit/          # PNGs of the kit preview
```
