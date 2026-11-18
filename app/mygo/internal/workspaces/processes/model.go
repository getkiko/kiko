package processes

import (
	"fmt"
	"math"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// Process is a monitored process.
type Process = api.MonitoredProcess

// Sort keys of the table's columns.
const (
	SortName    = "name"
	SortCPU     = "cpu"
	SortMemory  = "memory"
	SortPower   = "power"
	SortThreads = "threads"
	SortCPUTime = "cpuTime"
	SortPID     = "pid"
	SortUser    = "user"
)

// Filters of the toolbar's segmented control.
const (
	FilterAll    = "all"
	FilterApps   = "apps"
	FilterMine   = "mine"
	FilterSystem = "system"
)

// Group modes.
const (
	ModeApp    = "app"
	ModeParent = "parent"
	ModeName   = "name"
)

// FilterChoice is a filter of the segmented control.
type FilterChoice struct{ Key, Label string }

// Filters lists the filters the host's OS supports: App on macOS and
// Windows, whose servers name Apps.
func Filters(os string) []FilterChoice {
	out := []FilterChoice{{FilterAll, "全部"}}
	if os == "darwin" || os == "windows" {
		out = append(out, FilterChoice{FilterApps, "App"})
	}
	return append(out, FilterChoice{FilterMine, "我的进程"}, FilterChoice{FilterSystem, "系统"})
}

// FormatThreads is the toolbar's thread count.
func FormatThreads(threads int64, unavailable int) string {
	count := groupDigits(strconv.FormatInt(threads, 10))
	if unavailable > 0 {
		return fmt.Sprintf("%s 个可读线程（%d 个进程受限）", count, unavailable)
	}
	return count + " 个线程"
}

// Row is a row of the table: a process, or a group of processes shown
// as their aggregate, at a depth of the tree.
type Row struct {
	Process Process
	Depth   int
	Group   bool
	Count   int
	Members []Process
}

// Identity identifies a process across samples: its PID and start.
func Identity(p Process) string { return fmt.Sprintf("%d:%d", p.PID, p.StartedAt) }

// GroupIdentity identifies the group a process heads in mode.
func GroupIdentity(p Process, mode string) string {
	switch mode {
	case ModeApp:
		if p.AppPath != "" {
			return p.AppPath
		}
		return Identity(p)
	case ModeName:
		return "name:" + p.Name
	}
	return "parent:" + Identity(p)
}

// RowIdentity identifies a table row: a process, or a group within a
// filter and mode.
func RowIdentity(p Process, group bool, mode, filter string) string {
	if !group {
		return Identity(p)
	}
	prefix := ""
	if filter != FilterAll {
		prefix = filter + ":"
	}
	if mode == ModeApp {
		return prefix + "app:" + GroupIdentity(p, mode)
	}
	return prefix + GroupIdentity(p, mode)
}

// metric returns a pointer to a summed metric of p.
func metrics(p *Process) []**float64 {
	return []**float64{&p.CPU, &p.CPUTime, &p.Memory, &p.ResidentMemory, &p.VirtualMemory, &p.Threads, &p.Power,
		&p.Wakeups, &p.ReadBytes, &p.WriteBytes, &p.ReceivedBytes, &p.SentBytes, &p.ReadRate, &p.WriteRate,
		&p.ReceiveRate, &p.SendRate}
}

// Aggregate sums the members' metrics under root, named by its App.
func Aggregate(root Process, members []Process) Process {
	result := root
	if root.AppName != "" {
		result.Name = root.AppName
	}
	fields := metrics(&result)
	for i := range fields {
		var sum float64
		any := false
		for _, m := range members {
			m := m
			if v := *metrics(&m)[i]; v != nil {
				sum += *v
				any = true
			}
		}
		if any {
			s := sum
			*fields[i] = &s
		} else {
			*fields[i] = nil
		}
	}
	return result
}

func sortValue(p Process, key string) (num *float64, str string, isString bool) {
	switch key {
	case SortName:
		return nil, p.Name, true
	case SortUser:
		return nil, p.User, true
	case SortPID:
		v := float64(p.PID)
		return &v, "", false
	case SortCPU:
		return p.CPU, "", false
	case SortMemory:
		return p.Memory, "", false
	case SortPower:
		return p.Power, "", false
	case SortThreads:
		return p.Threads, "", false
	case SortCPUTime:
		return p.CPUTime, "", false
	}
	return nil, "", false
}

// localeCompare orders text case-insensitively, lower case first among
// equals, as ICU collation does.
func localeCompare(a, b string) int {
	if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
		return c
	}
	return -strings.Compare(a, b)
}

// Compare orders processes by key, nil metrics last, then by PID.
func Compare(key string, descending bool) func(a, b Process) int {
	return func(a, b Process) int {
		an, as, str := sortValue(a, key)
		bn, bs, _ := sortValue(b, key)
		if !str {
			if an == nil && bn != nil {
				return 1
			}
			if bn == nil && an != nil {
				return -1
			}
		}
		order := 0
		switch {
		case str:
			order = localeCompare(as, bs)
		case an != nil && bn != nil:
			order = cmpFloat(*an, *bn)
		}
		if descending {
			order = -order
		}
		if order != 0 {
			return order
		}
		return a.PID - b.PID
	}
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// ViewOptions forces grouping on the App and system filters.
func ViewOptions(filter string, grouped bool, mode string) (bool, string) {
	switch filter {
	case FilterSystem:
		return true, ModeName
	case FilterApps:
		return true, ModeApp
	}
	return grouped, mode
}

// InFilter reports whether p belongs to the filter.
func InFilter(p Process, filter string) bool {
	switch filter {
	case FilterAll:
		return true
	case FilterApps:
		return p.AppPath != ""
	case FilterMine:
		return p.Mine && !p.System
	case FilterSystem:
		return p.System
	}
	return false
}

// Rows builds the table: the processes in filter matching query (or the
// exact PID when exactPID >= 0), grouped in mode when grouped, sorted, with
// the members of expanded groups below them. App bundles own their groups,
// including helpers reparented to launchd.
func Rows(all []Process, grouped bool, expanded map[string]bool, filter, query, key string, descending bool, mode string, exactPID int) []Row {
	grouped, mode = ViewOptions(filter, grouped, mode)
	var processes []Process
	for _, p := range all {
		if InFilter(p, filter) {
			processes = append(processes, p)
		}
	}
	compare := Compare(key, descending)
	search := strings.ToLower(strings.TrimSpace(query))
	matches := func(p Process) bool {
		if exactPID >= 0 {
			return p.PID == exactPID
		}
		text := fmt.Sprintf("%s %s %s %s %d %s", p.Name, p.AppName, p.Path, p.Command, p.PID, p.User)
		return strings.Contains(strings.ToLower(text), search)
	}
	single := func(p Process, depth int) Row { return Row{Process: p, Depth: depth, Count: 1, Members: []Process{p}} }
	sortRows := func(rows []Row) {
		sort.SliceStable(rows, func(i, j int) bool { return compare(rows[i].Process, rows[j].Process) < 0 })
	}
	sorted := func(list []Process) []Process {
		out := slices.Clone(list)
		sort.SliceStable(out, func(i, j int) bool { return compare(out[i], out[j]) < 0 })
		return out
	}
	if !grouped {
		var rows []Row
		for _, p := range processes {
			if matches(p) {
				rows = append(rows, single(p, 0))
			}
		}
		sortRows(rows)
		return rows
	}
	if mode == ModeParent {
		byPID := map[int]Process{}
		for _, p := range processes {
			byPID[p.PID] = p
		}
		children := map[int][]Process{}
		for _, p := range processes {
			if p.PID != p.PPID {
				children[p.PPID] = append(children[p.PPID], p)
			}
		}
		var roots []Process
		rootSeen := map[int]bool{}
		for _, p := range processes {
			root := p
			seen := map[int]bool{p.PID: true}
			for {
				parent, ok := byPID[root.PPID]
				if root.PPID <= 1 || !ok || seen[root.PPID] {
					break
				}
				root = parent
				seen[root.PID] = true
			}
			// Choose the same root for every member of a cyclic ancestry.
			if root.PPID > 1 && seen[root.PPID] {
				cursor, ok := byPID[root.PPID]
				var cycle []Process
				for ok {
					cycle = append(cycle, cursor)
					cursor, ok = byPID[cursor.PPID]
					if ok && cursor.PID == cycle[0].PID {
						break
					}
				}
				root = slices.MinFunc(cycle, func(a, b Process) int { return a.PID - b.PID })
			}
			if !rootSeen[root.PID] {
				rootSeen[root.PID] = true
				roots = append(roots, root)
			} else {
				for i := range roots {
					if roots[i].PID == root.PID {
						roots[i] = root
					}
				}
			}
		}
		var collect func(p Process, seen map[int]bool) []Process
		collect = func(p Process, seen map[int]bool) []Process {
			if seen[p.PID] {
				return nil
			}
			seen[p.PID] = true
			out := []Process{p}
			if p.PID > 1 {
				for _, child := range children[p.PID] {
					out = append(out, collect(child, seen)...)
				}
			}
			return out
		}
		var groups []Row
		for _, root := range roots {
			members := collect(root, map[int]bool{})
			row := Row{Process: root, Group: len(members) > 1, Count: len(members), Members: members}
			if row.Group {
				row.Process = Aggregate(root, members)
			}
			if slices.ContainsFunc(members, matches) {
				groups = append(groups, row)
			}
		}
		sortRows(groups)
		var rows []Row
		for _, row := range groups {
			rows = append(rows, row)
			if !row.Group || !expanded[GroupIdentity(row.Process, mode)] {
				continue
			}
			seen := map[int]bool{row.Process.PID: true}
			var appendTree func(p Process, depth int)
			appendTree = func(p Process, depth int) {
				if seen[p.PID] {
					return
				}
				seen[p.PID] = true
				rows = append(rows, single(p, depth))
				for _, child := range sorted(children[p.PID]) {
					appendTree(child, depth+1)
				}
			}
			for _, child := range sorted(children[row.Process.PID]) {
				appendTree(child, 1)
			}
		}
		return rows
	}
	var order []string
	apps := map[string][]Process{}
	var groups []Row
	for _, p := range processes {
		if mode == ModeName || p.AppPath != "" {
			key := GroupIdentity(p, mode)
			if _, ok := apps[key]; !ok {
				order = append(order, key)
			}
			apps[key] = append(apps[key], p)
		} else if matches(p) {
			groups = append(groups, single(p, 0))
		}
	}
	for _, key := range order {
		members := apps[key]
		if !slices.ContainsFunc(members, matches) {
			continue
		}
		pids := map[int]bool{}
		for _, p := range members {
			pids[p.PID] = true
		}
		rank := func(p Process) int {
			if mode == ModeName {
				return 0
			}
			name := p.AppName
			if name == "" {
				name = strings.TrimSuffix(path.Base(p.AppPath), ".app")
			}
			if strings.HasPrefix(p.Path, p.AppPath+"/Contents/MacOS/") || strings.EqualFold(p.Name, name) {
				return 2
			}
			if pids[p.PPID] {
				return 0
			}
			return 1
		}
		ranked := slices.Clone(members)
		sort.SliceStable(ranked, func(i, j int) bool {
			a, b := ranked[i], ranked[j]
			if ra, rb := rank(a), rank(b); ra != rb {
				return ra > rb
			}
			if a.StartedAt != b.StartedAt {
				return a.StartedAt < b.StartedAt
			}
			return a.PID < b.PID
		})
		root := ranked[0]
		aggregate := Aggregate(root, members)
		if mode == ModeName {
			aggregate.Name = root.Name
		}
		groups = append(groups, Row{Process: aggregate, Group: true, Count: len(members), Members: members})
	}
	sortRows(groups)
	var rows []Row
	for _, row := range groups {
		rows = append(rows, row)
		if !row.Group || !expanded[GroupIdentity(row.Process, mode)] {
			continue
		}
		var others []Process
		for _, p := range row.Members {
			if p.PID != row.Process.PID {
				others = append(others, p)
			}
		}
		for _, p := range sorted(others) {
			rows = append(rows, single(p, 1))
		}
	}
	return rows
}

// RevealGroup returns the groups to expand so that the process pid shows.
func RevealGroup(all []Process, pid int, mode, filter string) map[string]bool {
	_, mode = ViewOptions(filter, true, mode)
	byPID := map[int]Process{}
	var target *Process
	for _, p := range all {
		if InFilter(p, filter) {
			byPID[p.PID] = p
			if p.PID == pid && target == nil {
				p := p
				target = &p
			}
		}
	}
	expanded := map[string]bool{}
	if mode == ModeParent {
		current, ok := byPID[pid]
		for ok && !expanded[GroupIdentity(current, mode)] {
			expanded[GroupIdentity(current, mode)] = true
			if current.PPID <= 1 {
				break
			}
			current, ok = byPID[current.PPID]
		}
		return expanded
	}
	if target != nil && (mode == ModeName || target.AppPath != "") {
		expanded[GroupIdentity(*target, mode)] = true
	}
	return expanded
}

// ---- formatting ----

func groupDigits(s string) string {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	if frac != "" {
		whole += "." + frac
	}
	if neg {
		return "-" + whole
	}
	return whole
}

// formatNumber formats v with at most digits decimals, en-US style.
func formatNumber(v float64, digits int) string {
	p := math.Pow(10, float64(digits))
	return groupDigits(strconv.FormatFloat(math.Round(v*p)/p, 'f', -1, 64))
}

// FormatBytes formats a byte count: "—" when unknown, two decimals from GB.
func FormatBytes(v *float64) string {
	if v == nil {
		return "—"
	}
	if *v == 0 {
		return "0 B"
	}
	index := min(4, max(0, int(math.Floor(math.Log(*v)/math.Log(1024)))))
	digits := 0
	switch {
	case index >= 3:
		digits = 2
	case index == 2:
		digits = 1
	}
	return formatNumber(*v/math.Pow(1024, float64(index)), digits) + " " + []string{"B", "KB", "MB", "GB", "TB"}[index]
}

// FormatCount formats a count with separators, "—" when unknown.
func FormatCount(v *float64) string {
	if v == nil {
		return "—"
	}
	return formatNumber(*v, 3)
}

// FormatPower formats milliwatts, switching to watts at 1,000 mW.
func FormatPower(v *float64) string {
	switch {
	case v == nil:
		return "—"
	case *v == 0:
		return "0"
	case *v >= 1000:
		return formatNumber(*v/1000, 2) + " W"
	}
	return formatNumber(*v, 1) + " mW"
}

// FormatCPUTime formats seconds as h:mm:ss.
func FormatCPUTime(v *float64) string {
	if v == nil || math.IsInf(*v, 0) || math.IsNaN(*v) {
		return "—"
	}
	total := int64(math.Max(0, math.Floor(*v)))
	return fmt.Sprintf("%d:%02d:%02d", total/3600, total/60%60, total%60)
}

// FormatStartedAt formats a start time (Unix ms) as zh-CN, with the year
// when it differs from the reference's.
func FormatStartedAt(startedAt, reference int64) string {
	if startedAt <= 0 {
		return "—"
	}
	t := timefmt.Milli(startedAt)
	if t.Year() != timefmt.Milli(reference).Year() {
		return t.Format("2006/1/2 15:04:05")
	}
	return t.Format("1/2 15:04:05")
}

// FormatUptime formats seconds as days, hours and minutes in Chinese units,
// or seconds alone under a minute.
func FormatUptime(seconds *float64) string {
	if seconds == nil || math.IsInf(*seconds, 0) || math.IsNaN(*seconds) {
		return "—"
	}
	minutes := int64(math.Max(0, math.Floor(*seconds/60)))
	if minutes < 1 {
		return timefmt.Seconds(*seconds)
	}
	if minutes < 60 {
		return fmt.Sprintf("%d分钟", minutes)
	}
	hours := minutes / 60
	if hours >= 24 {
		return fmt.Sprintf("%d天%d小时%d分钟", hours/24, hours%24, minutes%60)
	}
	return fmt.Sprintf("%d小时%d分钟", hours, minutes%60)
}

// FormatPercent formats a CPU share with digits decimals.
func FormatPercent(v *float64, digits int) string {
	if v == nil {
		return "—"
	}
	return strconv.FormatFloat(*v, 'f', digits, 64)
}

// CodeParts splits a command (or, plain, a path) into the executable's
// directory, its name and the arguments, dropping the executable's quotes.
func CodeParts(value string, plain bool) (directory, executable, args string) {
	end := len(value)
	if !plain {
		quote := byte(0)
	scan:
		for i := 0; i < len(value); i++ {
			ch := value[i]
			switch {
			case ch == '\\' && quote != '\'':
				i++
			case quote != 0:
				if ch == quote {
					quote = 0
				}
			case ch == '"' || ch == '\'':
				quote = ch
			case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '\f' || ch == '\v':
				end = i
				break scan
			}
		}
	}
	executable = value[:end]
	if !plain && len(executable) >= 2 {
		q := executable[0]
		if (q == '\'' || q == '"') && executable[len(executable)-1] == q {
			executable = executable[1 : len(executable)-1]
			if q == '\'' {
				executable = strings.ReplaceAll(executable, `'\''`, "'")
			}
		}
	}
	sep := max(strings.LastIndex(executable, "/"), strings.LastIndex(executable, `\`))
	return executable[:sep+1], executable[sep+1:], value[end:]
}
