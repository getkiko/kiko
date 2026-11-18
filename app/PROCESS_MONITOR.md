# Process monitor

The process monitor shows processes and system resources on **the machine running kiko-server**. The Go server collects raw data and serves snapshots over the HTTP API; the desktop console polls them, groups and sorts processes, and keeps chart history in memory.

## Interface

- Open it from **Network → Processes** in the desktop sidebar.
- Columns: name, CPU, memory, power, threads, CPU time, PID, user. Click a header to sort and click again to reverse; missing values sort after present ones.
- The header shows the process count and readable thread count; the footer shows how many processes the current filter covers, plus host CPU and memory.
- Desktop list rows are 28 px high. Touch targets are at least 44 px.

## Grouping, filters and search

In **All** and **My processes**, the grouping button offers three modes, defaulting to **By app**. **Apps** is always grouped by app and **System** by name; switching back to All or My processes restores the previously chosen mode.

| Mode | Grouped by | Expanded content |
| --- | --- | --- |
| By app | Same `appPath`, usually the outermost `.app` path | Summary row plus the app's other members, one level deep |
| By parent | `ppid` ancestry, bounded by launchd/init (PID 1) | Summary row plus descendants, indented by depth |
| By name (System) | Same process `name`, across apps | Summary row plus other system processes with that name |
| Flat | One row per process | Every PID, filtered and sorted |

App mode keeps helpers that were re-parented to launchd in their original app. Signed copies under a macOS `.app.bundle/` are mapped back to the installed app via the original `argv[0]`, while `path` keeps the real executable path. The app's representative is its main executable, then the earliest start time and lowest PID; summary rows use the app name. Parent mode handles missing parents, self-references and ancestry cycles, so every PID belongs to exactly one group.

Summary rows add up members' CPU, CPU time, memory, threads, power, wakeups, disk and network. If every member's value is `null`, the sum is `null`; if only some are readable, the readable values are summed. Identity fields (PID, user, command, path, start time) come from the representative, which appears only in the summary row when expanded.

Filters are **All**, **My processes** and **System**, plus **Apps** on macOS. The filter defines the member set first; grouping, sums and search then work within it:

| Filter | Members | Grouping |
| --- | --- | --- |
| All | All processes | App, parent, flat |
| My processes | `mine` is true and `system` is false | App, parent, flat |
| Apps | Processes with an `appPath` | Always by app |
| System | `system` is true | Always by name |

Search matches name, app name, path, command, PID and user, case-insensitively. A group stays visible if any member in the filter matches. Counts, detail members, search suggestions and sums all use the current filter; parent mode builds ancestry within it. Expansion is under the user's control and tracked separately per grouping mode, including while searching. **View options** can collapse all groups.

While typing in the search field, up to 8 suggestions appear, built from the current grouping and filter with icon, name and process count. Arrow keys move between them; Enter or a click selects the matching row, and an open detail pane follows the selection. Escape, blur or an outside click closes the suggestions.

## Selection and details

With the detail pane closed, a click selects a row and a double-click opens details; with it open, a click switches the details. Enter opens details, Space selects, and Left/Right collapse or expand a group. The details button toggles the pane; the close button or Escape closes it.

Details include:

1. Icon and name; `PID, user` for a single process or `N processes, user` for a group; quit, force quit and reveal in file manager.
2. CPU and memory charts with current readings.
3. CPU time, threads, resident and virtual memory, power, wakeups, disk read and write, network received and sent, start time and uptime, and state.
4. Parent process. App summaries list the app's other members, name summaries list the other processes with that name, parent groups list their members, and single processes list their direct children within the filter. Member rows show name, CPU and memory.
5. Command and path, each with a copy button. Commands longer than four lines are collapsed with a "show more" toggle; the directory prefix is dimmed, the executable name is bold, and outer quotes around the program path are removed for display. Paths are shown in full as plain text and wrap to the available width. Copying always uses the full original text.

Clicking a parent or member row clears the search, switches to All, expands the owning group and selects the row in the list. Refreshes keep the detail scroll position for the same process or app; a new selection starts a fresh view. A process that has exited keeps its last snapshot and is marked as exited.

### Formatting

- CPU is relative to one logical core (100%), so multi-threaded processes can exceed 100%. Host CPU is normalized to 0–100%.
- CPU time is shown as whole seconds in `h:mm:ss` (`29:49.79` becomes `0:29:49`); hours keep counting past a day (`25:01:01`).
- Byte counts use powers of 1024 (B, KB, MB, GB, TB). Disk and network details show "current rate and total".
- The API reports power in mW; the console switches to W from 1,000 mW (`1550` is shown as `1.55 W`). Wakeups are per second.
- Start times use the configured time zone. Dates in the snapshot's year omit the year; all include hours, minutes and seconds. Uptime under an hour is shown in minutes, then hours and minutes, then days, hours and minutes.
- Start times wider than the column scroll as a marquee, pause on hover, and fall back to static truncated text when reduced motion is on.
- `null` metrics are shown as an em dash; measured zeros are shown as zero.

## Refresh, pause and charts

The default refresh interval is 2 seconds, with 1, 2 and 5 seconds available. The console waits the chosen interval after each response before the next request, so requests from one view never overlap. Opening the page samples immediately; leaving it, hiding or minimizing the window cancels the request and stops polling, and late responses are dropped. On return it samples again and keeps the filter, selection, detail scroll, history and manual pause. The server caps a sample at 8 seconds and reuses a snapshot taken within the last 750 ms, so multiple clients share one counter baseline.

Pause takes effect immediately: the in-flight request is cancelled and later responses are dropped. Lists, details, CPU, memory, disk, network, host totals and charts stay on the current snapshot, and uptime and year formatting use the snapshot time. Processes, grouping, filters and sorting can still be changed on the frozen snapshot. Resuming sends a new request at once. Pause applies to the current console only; other clients keep polling the server.

CPU and memory history is recorded only for a new `capturedAt`. Each process and group keeps up to 60 samples; the dot chart draws the latest 44 with 6 dots per column. The CPU scale starts at 100% and memory at 1 GiB, growing with the historical peak. History lives in memory only.

A single process is identified by `pid + startedAt`, so PID reuse starts a new history. App summaries are keyed by `appPath`, so selection and history survive a change of representative; parent summaries use the root process identity and name summaries use the process name. Each filter keeps its own group history. History for processes that are gone and not selected is discarded.

## HTTP API

`capabilities.processMonitor` in `GET /api/v1/info` reports whether the process monitor is available. These routes share the server's normal API access controls.

### Snapshot

```http
GET /api/v1/process-monitor
```

Returns 200 with JSON and `Cache-Control: no-store`; collection failures return 503 with `{ "error": "..." }`.

| Top-level field | Meaning |
| --- | --- |
| `capturedAt` | Sample time, Unix milliseconds |
| `processes` | All collected processes |
| `total` / `estimated` | Process count / processes using estimated data |
| `threads` / `threadsUnavailable` | Readable thread total / processes whose thread count could not be read |
| `cpu` | Host CPU percentage; `null` until there are two samples to diff |
| `memory` / `memoryTotal` | Host used / total memory in bytes |
| `os` / `warnings` | Server OS name / collection warnings |

| Process field | Meaning and unit |
| --- | --- |
| `pid`, `ppid` | PID and parent PID |
| `name`, `path`, `command` | Name, real executable path, command line |
| `appName`, `appPath` | App name and path, when identified |
| `user`, `mine`, `system` | User; owned by the server's user; platform system account |
| `startedAt`, `status`, `estimated` | Start time in Unix ms; state; basic metrics are estimated |
| `cpu`, `cpuTime` | CPU percentage; cumulative CPU seconds |
| `memory`, `residentMemory`, `virtualMemory` | Memory footprint, resident and virtual memory in bytes |
| `threads`, `power`, `wakeups` | Thread count; mW; wakeups per second |
| `readBytes`, `writeBytes` | Cumulative disk bytes read and written |
| `receivedBytes`, `sentBytes` | Cumulative observed network bytes received and sent |
| `readRate`, `writeRate`, `receiveRate`, `sendRate` | Corresponding rates in bytes per second |

### Signal a process

```http
POST /api/v1/process-monitor/{pid}/signal
Content-Type: application/json

{ "startedAt": 1234567890000, "force": false }
```

`force: false` asks the process to quit; `force: true` terminates it. The console confirms both. Actions on an app summary row apply to the representative PID. The server re-checks the start time and protects PID 0, PID 1 and itself; the body is limited to 1 KiB.

| Status | Meaning |
| --- | --- |
| 204 | Signal sent |
| 400 | Protected PID, invalid parameters or missing start time |
| 403 | The OS refused the signal |
| 404 | The process has exited |
| 409 | The PID now belongs to a different process |

### Reveal in file manager

```http
POST /api/v1/process-monitor/{pid}/reveal?startedAt=1234567890000
```

Reveals the executable in the server machine's file manager: `open -R` on macOS, Explorer selection on Windows, and on Linux a FileManager1-capable file manager, falling back to `xdg-open` on the directory. Requires a graphical session on that machine. Returns 204 on success, 409 for a stale identity, 404 when the path is missing, 500 when the file manager fails to start, and 501 on other systems.

### App icons

```http
GET /api/v1/process-monitor/{pid}/icon?startedAt=1234567890000
```

Returns a 128 px PNG on macOS, enough for up to 3x displays, with `Content-Type: image/png` and a one-hour private cache lifetime. The app is derived from the verified process path. ICNS files are converted with `sips`; `CFBundleIconName` / `Assets.car` icons are resolved through NSWorkspace Icon Services and rendered to an 8-bit RGBA bitmap. Signed copies use the same app mapping as the process list.

The desktop server learns the current app from the kiko-core path inside the app bundle and associates the installed privileged helper and kiko-core declared in `helperpaths` with that app, so they group under it and share its icon; their real paths and commands are still shown. Command-line deployments keep separate process identities, and other apps' privileged helpers need their own install declaration to be attributed reliably.

Successful conversions are cached by app path, so helpers of the same app share an icon; the cache is cleared at 256 entries and failed conversions can be retried. A changed identity or an unavailable icon returns 404.

```http
GET /api/v1/process-icon?path=/Applications/X.app/Contents/MacOS/X
```

Returns the icon of the app that owns an executable path, without the process having to run; usage history and remote consoles use it for processes that have exited. The path must be a clean, absolute, existing executable (`.exe` on Windows). App attribution matches the process list, including privileged helper mapping, and the conversion cache is shared with the PID route. Paths outside an app or failed conversions return 404; a failure is remembered per app path for about a minute so repeated conversions do not block other icon requests.

## Platform collectors

### macOS

macOS uses native system interfaces through `golang.org/x/sys/unix` and `github.com/ebitengine/purego`, so it builds with `CGO_ENABLED=0`.

| Source | Used for |
| --- | --- |
| `kern.proc.all` | Enumerating processes: PID, parent PID, user, start time |
| `kern.procargs2` | Original executable path and arguments |
| `proc_pidpath` | Real executable path |
| `proc_pidinfo` | CPU times, threads, resident and virtual memory |
| `proc_pid_rusage` | Physical footprint, disk I/O, wakeups; energy from V6, falling back to V2 |
| `mach_timebase_info` | Converting Mach time to seconds |
| `getpwuid_r` | User name lookup, cached |
| `host_statistics` / `host_statistics64` | Host CPU and memory |
| `ps` | Fallback for basic counters and current process state |
| `nettop -L 1 -x -J bytes_in,bytes_out -n` | Per-process network counters from active connections, with a 2-second timeout |

CPU percentage is the difference in cumulative CPU time between adjacent samples divided by the real interval. Disk, wakeup and network rates are also counter differences. Power is energy in nJ/s divided by 1e6, giving mW. The first sample, and any change of identity, needs a later sample to establish a baseline.

Network totals are kept per PID and start time, with baselines for each active connection. Totals survive connection close and failed network samples, and rates resume after consecutive successful samples. The first observation of a connection counts its existing total, so these numbers are **observed traffic**: short connections that open and close between samples can be missed, and reused connection identifiers can affect accuracy.

OS permissions can block libproc for some processes. Basic CPU and memory then fall back to `ps`, and the response and footer report how many processes are estimated; advanced metrics depend on permissions. Thread totals also report the number of restricted processes.

When `kern.procargs2` is restricted, including for root processes, the command falls back to the `command` column of `ps -axww`, which keeps embedded spaces and avoids terminal-width truncation.

### Linux and Windows

On Windows each sample takes one Toolhelp process snapshot (PID, parent PID, thread count, exe name), then opens limited query handles to read the path, start time, CPU time, memory, disk bytes and token user. Account names are cached by SID and command lines by PID and start time. Protected processes keep the snapshot name and are classified as system. Executables outside `%SystemRoot%` are treated as apps, named from the file description or product name in their version info, with the icon extracted from the exe resources as PNG. A non-forced quit sends a close request to the process's windows; without a window, the console reports that only force quit is possible.

Linux uses gopsutil to read `/proc`. System processes are those whose UID is below `UID_MIN`, plus `nobody`. Reading other users' paths and disk counters needs root or `CAP_SYS_PTRACE` / `CAP_DAC_READ_SEARCH`, which the service installed by `install-service.sh` has. Linux has no app identification; use parent or flat mode.

On both platforms power, wakeups and per-process network are `null`. A parent that started later than its child is treated as PID reuse and the link is dropped.

## Source files

| File | Responsibility |
| --- | --- |
| `process_monitor.go` | Types, shared snapshots, diffing, main API and signals |
| `process_monitor_darwin.go` | Darwin process and network collection, `ps` fallback |
| `process_monitor_native_darwin.go` | Darwin arguments, identity, paths, signals and host statistics |
| `process_monitor_windows.go` | Windows snapshot collection, window close |
| `process_monitor_other.go` / `process_monitor_gopsutil.go` | gopsutil collection for Linux and others; host counters and identity lookup |
| `process_monitor_network.go` | Network total retention and thread totals |
| `process_monitor_actions.go` | Reveal and icon APIs, icon cache |
| `process_monitor_icon_darwin.go` / `process_monitor_icon_windows.go` / `process_monitor_icon_other.go` | Platform icon handling and Windows app identification |

## Testing

```sh
# From the repository root
CGO_ENABLED=0 go test ./app
make -C app/mygo test
git diff --check
```

The Go tests cover counter diffing, PID reuse, network retention and failure recovery, thread totals, Darwin network and argument parsing, icon conversion and signed-copy identification.
