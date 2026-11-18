package processes

import (
	"maps"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"
)

func f(v float64) *float64 { return &v }

func proc(pid, ppid int, name string, edit ...func(*Process)) Process {
	p := Process{PID: pid, PPID: ppid, Name: name, User: "user", Mine: true, StartedAt: int64(pid) * 100, Status: "sleep",
		CPU: f(1), CPUTime: f(2), Memory: f(100), ResidentMemory: f(100), VirtualMemory: f(200), Threads: f(1)}
	for _, e := range edit {
		e(&p)
	}
	return p
}

func app(path, name string) func(*Process) {
	return func(p *Process) { p.AppPath, p.AppName = path, name }
}
func cpu(v float64) func(*Process) { return func(p *Process) { p.CPU = f(v) } }
func system(p *Process)            { p.System = true }

var (
	initP  = proc(1, 0, "launchd")
	appP   = proc(10, 1, "goland", app("/GoLand.app", "GoLand"), cpu(5))
	nodeP  = proc(11, 10, "node", cpu(20))
	worker = proc(12, 11, "node worker", cpu(10))
	other  = proc(20, 1, "other")
)

func set(keys ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m
}

func pids(rows []Row) []int {
	var out []int
	for _, r := range rows {
		out = append(out, r.Process.PID)
	}
	return out
}

func memberPIDs(rows []Row) []int {
	var out []int
	for _, r := range rows {
		for _, m := range r.Members {
			out = append(out, m.PID)
		}
	}
	return out
}

func with(p Process, edit ...func(*Process)) Process {
	for _, e := range edit {
		e(&p)
	}
	return p
}

func TestAppGroupingJoinsHelpers(t *testing.T) {
	helper := proc(3, 1, "helper", app("/GoLand.app", "GoLand"), cpu(2))
	unrelated := proc(13, 10, "other app", app("/Other.app", "Other"))
	appNode := with(nodeP, app("/GoLand.app", "GoLand"))
	rows := Rows([]Process{helper, initP, appP, appNode, worker, other, unrelated}, true, nil, FilterAll, "", SortPID, false, ModeApp, -1)
	var group Row
	for _, r := range rows {
		if r.Group && r.Process.AppPath == "/GoLand.app" {
			group = r
		}
	}
	if group.Process.PID != 10 {
		t.Errorf("representative %d", group.Process.PID)
	}
	members := memberPIDs([]Row{group})
	sort.Ints(members)
	if !reflect.DeepEqual(members, []int{3, 10, 11}) || *group.Process.CPU != 27 {
		t.Errorf("members %v cpu %v", members, *group.Process.CPU)
	}
	if !slices.Contains(pids(rows), 12) || !slices.Contains(pids(rows), 13) {
		t.Errorf("rows %v", pids(rows))
	}
	all := map[int]bool{}
	for _, p := range memberPIDs(rows) {
		all[p] = true
	}
	if len(all) != 7 {
		t.Errorf("distinct members %d", len(all))
	}
}

func TestExpandedAppGroups(t *testing.T) {
	members := []Process{initP, appP, with(nodeP, app("/GoLand.app", "GoLand")), with(worker, app("/GoLand.app", "GoLand")), other}
	rows := Rows(members, true, set("/GoLand.app"), FilterAll, "", SortPID, false, ModeApp, -1)
	type shape struct {
		pid   int
		group bool
		depth int
	}
	var got []shape
	for _, r := range rows {
		got = append(got, shape{r.Process.PID, r.Group, r.Depth})
	}
	want := []shape{{1, false, 0}, {10, true, 0}, {11, false, 1}, {12, false, 1}, {20, false, 0}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows %v", got)
	}
	if got := pids(Rows(members, false, nil, FilterAll, "", SortPID, false, ModeApp, -1)); !reflect.DeepEqual(got, []int{1, 10, 11, 12, 20}) {
		t.Errorf("flat %v", got)
	}
}

func TestSearchFindsAppByHelper(t *testing.T) {
	members := []Process{appP, with(nodeP, app("/GoLand.app", "")), with(worker, app("/GoLand.app", ""))}
	collapsed := Rows(members, true, nil, FilterAll, "worker", SortCPU, true, ModeApp, -1)
	if len(collapsed) != 1 || collapsed[0].Process.PID != 10 {
		t.Errorf("collapsed %v", pids(collapsed))
	}
	expanded := Rows(members, true, set("/GoLand.app"), FilterAll, "worker", SortCPU, true, ModeApp, -1)
	if !reflect.DeepEqual(pids(expanded), []int{10, 11, 12}) {
		t.Errorf("expanded %v", pids(expanded))
	}
}

func TestStandaloneAcrossMissingParentsAndCycles(t *testing.T) {
	orphan := proc(30, 999, "orphan")
	a, b := proc(40, 41, "a"), proc(41, 40, "b")
	if n := len(Rows([]Process{orphan, a, b}, true, nil, FilterAll, "", SortCPU, true, ModeApp, -1)); n != 3 {
		t.Errorf("rows %d", n)
	}
	if agg := Aggregate(appP, []Process{appP, nodeP}); agg.Power != nil || *agg.Memory != 200 {
		t.Errorf("aggregate power %v memory %v", agg.Power, *agg.Memory)
	}
}

func TestAppIdentitySurvivesRepresentativeExit(t *testing.T) {
	helper := with(nodeP, app("/GoLand.app", "GoLand"))
	expanded := RevealGroup([]Process{appP, helper}, helper.PID, ModeApp, FilterAll)
	before := Rows([]Process{appP, helper}, true, expanded, FilterAll, "", SortPID, false, ModeApp, -1)
	after := Rows([]Process{helper}, true, expanded, FilterAll, "", SortPID, false, ModeApp, -1)
	if !reflect.DeepEqual(slices.Collect(maps.Keys(expanded)), []string{"/GoLand.app"}) {
		t.Errorf("expanded %v", expanded)
	}
	if RowIdentity(before[0].Process, true, ModeApp, FilterAll) != RowIdentity(after[0].Process, true, ModeApp, FilterAll) {
		t.Error("identity changed")
	}
	if len(RevealGroup([]Process{other}, other.PID, ModeApp, FilterAll)) != 0 {
		t.Error("standalone revealed a group")
	}
}

func TestParentGrouping(t *testing.T) {
	helper := proc(3, 1, "helper", app("/GoLand.app", "GoLand"))
	members := []Process{initP, appP, nodeP, worker, helper, other}
	rows := Rows(members, true, nil, FilterAll, "", SortPID, false, ModeParent, -1)
	var got [][2]int
	for _, r := range rows {
		got = append(got, [2]int{r.Process.PID, r.Count})
	}
	if !reflect.DeepEqual(got, [][2]int{{1, 1}, {3, 1}, {10, 3}, {20, 1}}) {
		t.Errorf("parent rows %v", got)
	}
	expanded := RevealGroup(members, worker.PID, ModeParent, FilterAll)
	got = nil
	for _, r := range Rows(members, true, expanded, FilterAll, "worker", SortPID, false, ModeParent, -1) {
		got = append(got, [2]int{r.Process.PID, r.Depth})
	}
	if !reflect.DeepEqual(got, [][2]int{{10, 0}, {11, 1}, {12, 2}}) {
		t.Errorf("expanded %v", got)
	}
	if n := len(Rows(members, true, nil, FilterAll, "worker", SortPID, false, ModeParent, -1)); n != 1 {
		t.Errorf("collapsed %d", n)
	}
	if RowIdentity(appP, true, ModeApp, FilterAll) == RowIdentity(appP, true, ModeParent, FilterAll) {
		t.Error("identities equal across modes")
	}
	a, b, child := proc(40, 41, "a"), proc(41, 40, "b"), proc(42, 40, "child")
	cycle := []Process{a, b, child}
	if got := pids(Rows(cycle, true, RevealGroup(cycle, child.PID, ModeParent, FilterAll), FilterAll, "", SortPID, false, ModeParent, -1)); !reflect.DeepEqual(got, []int{40, 41, 42}) {
		t.Errorf("cycle %v", got)
	}
}

func TestSystemGroups(t *testing.T) {
	members := []Process{
		appP,
		proc(3, 1, "helper", app("/GoLand.app", "GoLand"), system, cpu(2)),
		proc(4, 1, "helper", app("/Other.app", "Other"), system, cpu(3)),
		proc(5, 3, "helper", app("/GoLand.app", ""), cpu(80)),
		proc(6, 3, "daemon", app("/GoLand.app", ""), system, cpu(7)),
	}
	collapsed := Rows(members, true, nil, FilterSystem, "", SortPID, false, ModeApp, -1)
	type shape struct {
		name  string
		count int
		cpu   float64
	}
	var got []shape
	for _, r := range collapsed {
		got = append(got, shape{r.Process.Name, r.Count, *r.Process.CPU})
	}
	if !reflect.DeepEqual(got, []shape{{"helper", 2, 5}, {"daemon", 1, 7}}) {
		t.Errorf("collapsed %v", got)
	}
	if !reflect.DeepEqual(memberPIDs(collapsed), []int{3, 4, 6}) {
		t.Errorf("members %v", memberPIDs(collapsed))
	}
	expanded := Rows(members, true, RevealGroup(members, 4, ModeName, FilterAll), FilterSystem, "helper", SortPID, false, ModeApp, -1)
	if !reflect.DeepEqual(pids(expanded), []int{3, 4}) {
		t.Errorf("expanded %v", pids(expanded))
	}
	if len(Rows(members, true, nil, FilterSystem, "80", SortPID, false, ModeApp, -1)) != 0 {
		t.Error("non-system member matched")
	}
	if Rows(members, false, nil, FilterSystem, "", SortPID, false, ModeParent, -1)[0].Process.Name != "helper" {
		t.Error("system filter not grouped by name")
	}
	var rest []Process
	for _, p := range members {
		if p.PID != 3 {
			rest = append(rest, p)
		}
	}
	after := Rows(rest, true, nil, FilterSystem, "", SortPID, false, ModeApp, -1)[0]
	if RowIdentity(collapsed[0].Process, true, ModeName, FilterSystem) != RowIdentity(after.Process, true, ModeName, FilterSystem) {
		t.Error("system identity changed")
	}
}

func TestMyProcesses(t *testing.T) {
	members := []Process{
		appP,
		proc(11, 10, "owned helper", app("/GoLand.app", ""), cpu(20)),
		proc(12, 10, "root helper", app("/GoLand.app", ""), system, cpu(90)),
		proc(13, 10, "other user", app("/GoLand.app", ""), cpu(70), func(p *Process) { p.Mine = false }),
	}
	for _, mode := range []string{ModeApp, ModeParent} {
		rows := Rows(members, true, nil, FilterMine, "", SortPID, false, mode, -1)
		if !reflect.DeepEqual(memberPIDs(rows), []int{10, 11}) || *rows[0].Process.CPU != 25 {
			t.Errorf("%s: members %v", mode, memberPIDs(rows))
		}
		if len(Rows(members, true, nil, FilterMine, "root helper", SortPID, false, mode, -1)) != 0 {
			t.Errorf("%s: system member matched", mode)
		}
		expanded := Rows(members, true, RevealGroup(members, 11, mode, FilterMine), FilterMine, "", SortPID, false, mode, -1)
		if !reflect.DeepEqual(pids(expanded), []int{10, 11}) {
			t.Errorf("%s: expanded %v", mode, pids(expanded))
		}
	}
	if got := pids(Rows(members, false, nil, FilterMine, "", SortPID, false, ModeApp, -1)); !reflect.DeepEqual(got, []int{10, 11}) {
		t.Errorf("flat %v", got)
	}
	if RowIdentity(appP, true, ModeApp, FilterMine) == RowIdentity(appP, true, ModeApp, FilterAll) {
		t.Error("identity ignores filter")
	}
}

func TestAppTab(t *testing.T) {
	members := []Process{appP, nodeP, with(worker, func(p *Process) { p.Name, p.AppPath, p.System = "worker", "/GoLand.app", true })}
	rows := Rows(members, false, nil, FilterApps, "", SortPID, false, ModeParent, -1)
	if len(rows) != 1 || !rows[0].Group || rows[0].Process.Name != "GoLand" || !reflect.DeepEqual(memberPIDs(rows), []int{10, 12}) {
		t.Errorf("apps %+v", rows)
	}
	if len(Rows(members, true, nil, FilterApps, "node", SortPID, false, ModeParent, -1)) != 0 {
		t.Error("non-App member matched")
	}
}

func TestFilters(t *testing.T) {
	keys := func(os string) []string {
		var out []string
		for _, f := range Filters(os) {
			out = append(out, f.Key)
		}
		return out
	}
	for _, os := range []string{"darwin", "windows"} {
		if !reflect.DeepEqual(keys(os), []string{"all", "apps", "mine", "system"}) {
			t.Error(os, keys(os))
		}
	}
	for _, os := range []string{"linux", ""} {
		if !reflect.DeepEqual(keys(os), []string{"all", "mine", "system"}) {
			t.Error(os, keys(os))
		}
	}
}

func TestFormatting(t *testing.T) {
	if got := FormatThreads(7206, 0); got != "7,206 个线程" {
		t.Error(got)
	}
	if got := FormatThreads(6000, 256); got != "6,000 个可读线程（256 个进程受限）" {
		t.Error(got)
	}
	for _, c := range []struct {
		in   *float64
		want string
	}{{nil, "—"}, {f(0), "0"}, {f(5.6), "5.6 mW"}, {f(66), "66 mW"}, {f(999.9), "999.9 mW"}, {f(1000), "1 W"}, {f(1550), "1.55 W"}, {f(12345), "12.35 W"}} {
		if got := FormatPower(c.in); got != c.want {
			t.Errorf("power %v: %q", c.in, got)
		}
	}
	for _, c := range []struct {
		in   *float64
		want string
	}{{nil, "—"}, {f(0), "0:00:00"}, {f(59.99), "0:00:59"}, {f(1789.79), "0:29:49"}, {f(3600), "1:00:00"}, {f(90061.99), "25:01:01"}} {
		if got := FormatCPUTime(c.in); got != c.want {
			t.Errorf("cpu time %v: %q", c.in, got)
		}
	}
	ref := time.Date(2026, 1, 1, 0, 0, 1, 0, time.Local).UnixMilli()
	if got := FormatStartedAt(time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local).UnixMilli(), ref); got != "1/1 00:00:00" {
		t.Error(got)
	}
	if got := FormatStartedAt(time.Date(2025, 12, 31, 23, 59, 59, 0, time.Local).UnixMilli(), ref); got != "2025/12/31 23:59:59" {
		t.Error(got)
	}
	if got := FormatStartedAt(0, ref); got != "—" {
		t.Error(got)
	}
	for _, c := range []struct {
		in   *float64
		want string
	}{{nil, "—"}, {f(59), "59秒"}, {f(3599), "59分钟"}, {f(3600), "1小时0分钟"}, {f(8999), "2小时29分钟"}, {f(86400), "1天0小时0分钟"}, {f(90061), "1天1小时1分钟"}} {
		if got := FormatUptime(c.in); got != c.want {
			t.Errorf("uptime %v: %q", c.in, got)
		}
	}
	for in, want := range map[float64]string{0: "0 B", 512: "512 B", 1536: "2 KB", 5.5 * 1024 * 1024: "5.5 MB", 12.94 * (1 << 30): "12.94 GB"} {
		if got := FormatBytes(f(in)); got != want {
			t.Errorf("bytes %v: %q, want %q", in, got, want)
		}
	}
}

func TestCodeParts(t *testing.T) {
	commands := []string{"'/Applications/My App.app/Contents/MacOS/My App' --path 'folder with spaces'",
		"/Library/PrivilegedHelperTools/io.github.getkiko.kiko.core run -c /Users/user/config.json", "node --version"}
	for i, command := range commands {
		d, e, a := CodeParts(command, false)
		want := command
		if i == 0 {
			want = "/Applications/My App.app/Contents/MacOS/My App --path 'folder with spaces'"
		}
		if d+e+a != want {
			t.Errorf("parts of %q: %q", command, d+e+a)
		}
	}
	if _, e, _ := CodeParts(commands[0], false); e != "My App" {
		t.Error(e)
	}
	d, e, a := CodeParts("'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' --profile 'My Profile'", false)
	if d != "/Applications/Google Chrome.app/Contents/MacOS/" || e != "Google Chrome" || a != " --profile 'My Profile'" {
		t.Errorf("%q %q %q", d, e, a)
	}
	if d, _, _ := CodeParts(`'/Applications/User'\''s App.app/bin/tool' --flag`, false); d != "/Applications/User's App.app/bin/" {
		t.Error(d)
	}
	if _, _, a := CodeParts(commands[1], false); a != " run -c /Users/user/config.json" {
		t.Error(a)
	}
	d, e, a = CodeParts("/Applications/My App.app/Contents/MacOS/My App", true)
	if d != "/Applications/My App.app/Contents/MacOS/" || e != "My App" || a != "" {
		t.Errorf("plain %q %q %q", d, e, a)
	}
}

func TestExactPID(t *testing.T) {
	target, similarPID, similarName := proc(42, 1, "target"), proc(142, 1, "other"), proc(99, 1, "worker42")
	if got := pids(Rows([]Process{target, similarPID, similarName}, false, nil, FilterAll, "42", SortPID, false, ModeApp, 42)); !reflect.DeepEqual(got, []int{42}) {
		t.Error(got)
	}
	if n := len(Rows([]Process{similarPID, similarName}, false, nil, FilterAll, "42", SortPID, false, ModeApp, 42)); n != 0 {
		t.Error(n)
	}
}
