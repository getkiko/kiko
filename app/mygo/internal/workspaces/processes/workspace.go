// Package processes is the console's process monitor:
// the host's processes sampled while the page shows, grouped by App,
// parent or name, with search, sorting, a details inspector with CPU and
// memory history, reveal in the file manager, and quit or force quit.
package processes

import (
	"context"
	"strconv"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/procicon"
	"github.com/getkiko/kiko/app/mygo/internal/store"
)

// historyLength is how many samples a process's charts keep.
const historyLength = 60

// selection is the chosen row: its identity, whether it is a group, and
// the process (or aggregate) it showed last.
type selection struct {
	identity string
	group    bool
	original Process
}

// sample is a point of a process's history.
type sample struct{ cpu, memory *float64 }

// confirmation is the quit the confirm popover asks about, and which
// button opened it.
type confirmation struct {
	process Process
	force   bool
	anchor  string
}

// Workspace is the processes page. Its fields are UI-thread state.
type Workspace struct {
	env   *env.Env
	store *store.Store

	active     bool
	running    bool
	interval   time.Duration
	pollCancel context.CancelFunc

	snapshot    *api.ProcessSnapshot
	err         string
	history     map[string][]sample
	lastCapture int64

	grouped   bool
	groupMode string
	filter    string
	query     string
	exactPID  int
	focus     bool
	sortKey   string
	desc      bool
	expanded  map[string]bool
	selection *selection
	details   bool
	scrollTo  string

	confirm       kit.ConfirmState
	confirmTarget *confirmation

	icons *procicon.Store

	table        kit.TableState
	codeExpanded bool
	copied       string
	copiedAt     time.Time
}

// New returns the workspace.
func New(e *env.Env, s *store.Store) *Workspace {
	return &Workspace{
		env: e, store: s, running: true, interval: 2 * time.Second,
		history: map[string][]sample{}, grouped: true, groupMode: ModeApp, filter: FilterAll,
		exactPID: -1, sortKey: SortCPU, desc: true, expanded: map[string]bool{},
		icons: procicon.For(e),
	}
}

// SetActive samples processes while the page shows.
func (w *Workspace) SetActive(active bool) {
	w.active = active
	if active {
		w.startPoll()
	} else {
		w.stopPoll()
	}
}

// Open takes a PID from another page (the connection monitor) and shows
// that process alone, as ProcessMonitor's searchRequest; a name instead
// searches for it.
func (w *Workspace) Open(arg any) {
	var pid int
	switch v := arg.(type) {
	case int:
		pid = v
	case int32:
		pid = int(v)
	case int64:
		pid = int(v)
	case float64:
		pid = int(v)
	case string:
		n, err := strconv.Atoi(v)
		if err != nil {
			w.openSearch(v)
			return
		}
		pid = n
	default:
		return
	}
	w.exactPID = pid
	w.query = strconv.Itoa(pid)
	w.filter = FilterAll
	w.grouped = false
	w.details = false
	w.selection = nil
	w.running = true
	w.focus = true
	w.startPoll()
}

// openSearch shows the processes matching a name, as Usage opens a
// process it recorded.
func (w *Workspace) openSearch(name string) {
	if name == "" {
		return
	}
	w.exactPID = -1
	w.query = name
	w.filter = FilterAll
	w.details = false
	w.selection = nil
	w.running = true
	w.focus = true
	w.startPoll()
}

func (w *Workspace) startPoll() {
	if w.pollCancel != nil || !w.active || !w.running {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.pollCancel = cancel
	interval := w.interval
	go func() {
		for {
			snapshot, err := w.env.API.ProcessMonitor(ctx)
			if ctx.Err() != nil {
				return
			}
			w.env.Post(func() {
				if ctx.Err() == nil {
					w.receive(snapshot, err)
				}
			})
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
		}
	}()
}

func (w *Workspace) stopPoll() {
	if w.pollCancel != nil {
		w.pollCancel()
		w.pollCancel = nil
	}
}

func (w *Workspace) restartPoll() {
	w.stopPoll()
	w.startPoll()
}

// receive takes a sample: it extends the histories of processes and
// groups, and follows the selection.
func (w *Workspace) receive(next api.ProcessSnapshot, err error) {
	if err != nil {
		w.err = err.Error()
		return
	}
	w.err = ""
	if next.CapturedAt <= w.lastCapture {
		return
	}
	w.lastCapture = next.CapturedAt
	samples := map[string]Process{}
	for _, p := range next.Processes {
		samples[RowIdentity(p, false, ModeApp, FilterAll)] = p
	}
	for _, view := range [][2]string{{FilterAll, ModeApp}, {FilterAll, ModeParent}, {FilterMine, ModeApp}, {FilterMine, ModeParent}, {FilterApps, ModeApp}, {FilterSystem, ModeName}} {
		for _, row := range Rows(next.Processes, true, nil, view[0], "", SortPID, false, view[1], -1) {
			if row.Group {
				samples[RowIdentity(row.Process, true, view[1], view[0])] = row.Process
			}
		}
	}
	for key, p := range samples {
		h := append(w.history[key], sample{p.CPU, p.Memory})
		if len(h) > historyLength {
			h = h[len(h)-historyLength:]
		}
		w.history[key] = h
	}
	var keep string
	if w.selection != nil {
		keep = w.selection.identity
		if latest, ok := samples[keep]; ok {
			w.selection.original = latest
		}
	}
	for key := range w.history {
		if _, ok := samples[key]; !ok && key != keep {
			delete(w.history, key)
		}
	}
	w.snapshot = &next
}

// ---- derived view state ----

// view is what a frame shows, derived from the state.
type view struct {
	processes []Process
	filter    string
	grouped   bool
	mode      string
	rows      []Row
	allGroups []Row
	selected  *Process
	selRow    *Row
	members   []Process
	exited    bool
}

func (w *Workspace) rowKey(p Process, group bool, v *view) string {
	return RowIdentity(p, group, v.mode, v.filter)
}

func (w *Workspace) derive() *view {
	v := &view{}
	if w.snapshot != nil {
		v.processes = w.snapshot.Processes
	}
	v.filter = FilterAll
	os := ""
	if w.snapshot != nil {
		os = w.snapshot.OS
	}
	for _, f := range Filters(os) {
		if f.Key == w.filter {
			v.filter = w.filter
		}
	}
	v.grouped, v.mode = ViewOptions(v.filter, w.grouped, w.groupMode)
	v.rows = Rows(v.processes, v.grouped, w.expanded, v.filter, w.query, w.sortKey, w.desc, v.mode, w.exactPID)
	v.allGroups = Rows(v.processes, v.grouped, nil, v.filter, "", SortPID, false, v.mode, -1)
	if w.selection == nil {
		return v
	}
	for _, list := range [][]Row{v.rows, v.allGroups} {
		for i := range list {
			if v.selRow == nil && w.rowKey(list[i].Process, list[i].Group, v) == w.selection.identity {
				v.selRow = &list[i]
			}
		}
	}
	var raw *Process
	for i := range v.processes {
		if Identity(v.processes[i]) == Identity(w.selection.original) {
			raw = &v.processes[i]
			break
		}
	}
	switch {
	case v.selRow != nil:
		v.members = v.selRow.Members
		p := v.selRow.Process
		v.selected = &p
	case raw != nil:
		v.members = []Process{*raw}
		v.selected = raw
	default:
		p := w.selection.original
		v.selected = &p
	}
	v.exited = v.selRow == nil && raw == nil
	return v
}

// ---- actions ----

func (w *Workspace) selectRow(row Row, v *view) {
	w.selection = &selection{identity: w.rowKey(row.Process, row.Group, v), group: row.Group, original: row.Process}
	w.codeExpanded = false
}

// selectProcess shows a related process (parent, child, member), revealing
// it in the full list.
func (w *Workspace) selectProcess(p Process, v *view) {
	w.exactPID, w.query, w.filter, w.details = -1, "", FilterAll, true
	for key := range RevealGroup(v.processes, p.PID, w.groupMode, FilterAll) {
		w.expanded[key] = true
	}
	group := false
	if w.grouped {
		for _, row := range Rows(v.processes, w.grouped, nil, FilterAll, "", SortPID, false, w.groupMode, -1) {
			if row.Group && Identity(row.Process) == Identity(p) {
				group = true
			}
		}
	}
	key := RowIdentity(p, group, w.groupMode, FilterAll)
	w.selection = &selection{identity: key, group: group, original: p}
	w.scrollTo = key
	w.codeExpanded = false
}

// updateViewSelection keeps the selection across a change of filter or
// grouping, moving it to the group that now holds it.
func (w *Workspace) updateViewSelection(processes []Process, scope string, enabled bool, mode string) {
	if w.selection == nil {
		return
	}
	var p *Process
	for i := range processes {
		if Identity(processes[i]) == Identity(w.selection.original) {
			p = &processes[i]
		}
	}
	if p == nil || !InFilter(*p, scope) {
		w.selection = nil
		return
	}
	var representative *Row
	groups := Rows(processes, enabled, nil, scope, "", SortPID, false, mode, -1)
	for i := range groups {
		if groups[i].Group && Identity(groups[i].Process) == Identity(*p) {
			representative = &groups[i]
			break
		}
	}
	group := representative != nil
	if enabled && !group {
		for key := range RevealGroup(processes, p.PID, mode, scope) {
			w.expanded[key] = true
		}
	}
	original := *p
	if representative != nil {
		original = representative.Process
	}
	w.selection = &selection{identity: RowIdentity(original, group, mode, scope), group: group, original: original}
}

func (w *Workspace) changeFilter(scope string, v *view) {
	grouped, mode := ViewOptions(scope, w.grouped, w.groupMode)
	w.filter = scope
	w.updateViewSelection(v.processes, scope, grouped, mode)
}

// changeGrouping sets grouping: ModeApp, ModeParent, or "none" for a flat
// list. The App and system filters keep their own grouping.
func (w *Workspace) changeGrouping(mode string, v *view) {
	if v.filter == FilterApps || v.filter == FilterSystem {
		return
	}
	enabled := mode != "none"
	next := mode
	if mode == "none" {
		next = w.groupMode
	}
	if next == ModeName {
		return
	}
	w.grouped, w.groupMode = enabled, next
	w.updateViewSelection(v.processes, v.filter, enabled, next)
}

func (w *Workspace) toggleExpanded(p Process, mode string) {
	key := GroupIdentity(p, mode)
	if w.expanded[key] {
		delete(w.expanded, key)
	} else {
		w.expanded[key] = true
	}
}

func (w *Workspace) askQuit(p Process, force bool, anchor string) {
	w.confirmTarget = &confirmation{process: p, force: force, anchor: anchor}
	w.confirm.Open, w.confirm.Error = true, ""
}

func (w *Workspace) signal(done func(error)) {
	if w.confirmTarget == nil {
		done(nil)
		return
	}
	target := *w.confirmTarget
	w.env.Run(func(ctx context.Context) error {
		return w.env.API.SignalProcess(ctx, target.process.PID, target.process.StartedAt, target.force)
	}, func(err error) {
		if err == nil {
			w.err = ""
		}
		done(err)
	})
}

func (w *Workspace) reveal(p Process) {
	w.env.Run(func(ctx context.Context) error { return w.env.API.RevealProcess(ctx, p.PID, p.StartedAt) }, func(err error) {
		if err != nil {
			w.err = err.Error()
		}
	})
}
