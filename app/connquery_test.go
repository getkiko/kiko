package main

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"testing"
)

var sample = connInfo{
	ID: "one", Inbound: "mixed-in", InboundType: "mixed", IPVersion: 4, Network: "tcp",
	Source: "127.0.0.1:5050", Destination: "1.2.3.4:443", Domain: "example.com", Protocol: "tls",
	CreatedAt: 1700000000000, UplinkTotal: 9007199254740993, DownlinkTotal: 2,
	Rule: "domain", Outbound: "proxy", OutboundType: "selector", ChainList: []string{"proxy", "node"},
}

func TestCompareConnectionsAndSourceHost(t *testing.T) {
	a := &connRow{Conn: sample, UpRate: 200, DownRate: 10, UpTotal: 9007199254740993, DownTotal: 1}
	b := &connRow{Conn: sample, UpRate: 100, DownRate: 20, UpTotal: 9007199254740994, DownTotal: 0}
	if compareConnections(a, b, colUploadRate) <= 0 || compareConnections(a, b, colDownloadRate) >= 0 ||
		compareConnections(a, b, colUploadTotal) != -1 || compareConnections(a, b, colDownloadTotal) != 1 {
		t.Error("compare")
	}
	hosts := map[string]string{
		"[fd7a:115c:a1e0::1]:443": "fd7a:115c:a1e0::1", "192.168.1.1:5050": "192.168.1.1",
		"2001:db8::1": "2001:db8::1", "[fe80::1%en0]:53000": "fe80::1%en0", " 192.168.1.1 ": "192.168.1.1", "": "",
	}
	for in, want := range hosts {
		if got := sourceHost(in); got != want {
			t.Errorf("sourceHost(%q) = %q", in, got)
		}
	}
}

func TestFormatRule(t *testing.T) {
	cases := map[string]string{
		"rule_set=[Gfw Telegram openai geosite:youtube]":             "RuleSet: Gfw, Telegram, openai, geosite:youtube",
		"!(network=tcp port=[80 443]) || domain_suffix=example.com":  "!(Network: tcp Port: 80, 443) || DomainSuffix: example.com",
		"process_path=[/Applications/My App.app/Contents/MacOS/App]": "ProcessPath: [/Applications/My App.app/Contents/MacOS/App]",
		"":               "",
		"final":          "final",
		"wifi_ssid=home": "WifiSsid: home",
	}
	for in, want := range cases {
		if got := formatConnectionRule(in); got != want {
			t.Errorf("formatConnectionRule(%q) = %q, want %q", in, got, want)
		}
	}
}

func proc(pid, ppid int, name string, extra ...func(*connProcess)) connProcess {
	p := connProcess{PID: pid, PPID: ppid, Name: name, Path: "/usr/bin/" + name, StartedAt: int64(pid * 100)}
	for _, fn := range extra {
		fn(&p)
	}
	return p
}

var (
	editorApp = proc(10, 1, "Editor", func(p *connProcess) {
		p.AppName, p.AppPath, p.Path = "Editor", "/Applications/Editor.app", "/Applications/Editor.app/Contents/MacOS/Editor"
	})
	editorHelper = proc(20, 10, "helper", func(p *connProcess) {
		p.AppName, p.AppPath, p.Path = "Editor", "/Applications/Editor.app", "/Applications/Editor.app/Contents/Helpers/helper"
	})
)

func row(id string, p connProcess, extra ...func(*connRow)) *connRow {
	c := sample
	c.ID = id
	c.ProcessInfo = &connProcessInfo{ProcessID: p.PID, UserName: "user", ProcessPath: p.Path}
	r := &connRow{Conn: c, UpRate: 10, DownRate: 20, UpTotal: 9007199254740993, DownTotal: 30}
	for _, fn := range extra {
		fn(r)
	}
	return r
}

func procs(list ...connProcess) map[int]connProcess {
	m := map[int]connProcess{}
	for _, p := range list {
		m[p.PID] = p
	}
	return m
}

func groupByLabel(t *testing.T, tree []*connNode, label string) *connNode {
	t.Helper()
	for _, n := range tree {
		if n.group() && n.Label == label {
			return n
		}
	}
	t.Fatalf("no group %q", label)
	return nil
}

func TestAppSummaries(t *testing.T) {
	var rows []*connRow
	for i := range 120 {
		p := editorHelper
		if i%2 == 1 {
			p = editorApp
		}
		rows = append(rows, row(strconv.Itoa(i), p))
	}
	meta := captureConnectionProcesses(rows, procs(editorApp, editorHelper), nil)
	tree := buildConnectionTree(rows, meta, groupApp, colCreated, true)
	if len(tree) != 1 {
		t.Fatal(len(tree))
	}
	s := tree[0]
	if s.Label != "Editor" || s.Process.PID != 10 || len(s.Rows) != 120 || s.ProcessCount != 2 {
		t.Fatalf("%s %d %d %d", s.Label, s.Process.PID, len(s.Rows), s.ProcessCount)
	}
	if s.Totals.UpRate != 1200 || s.Totals.DownRate != 2400 || s.Totals.DownTotal != 3600 {
		t.Fatalf("%+v", s.Totals)
	}
	if n := len(flattenConnectionTree(tree, nil, nil)); n != 1 {
		t.Fatal(n)
	}
	expanded := flattenConnectionTree(tree, map[string]bool{s.Key: true}, nil)
	if len(expanded) != 52 || expanded[1].Depth != 1 || expanded[1].Node == nil || expanded[1].Node.group() || expanded[51].Node != nil {
		t.Fatal(len(expanded))
	}
	if n := len(flattenConnectionTree(tree, map[string]bool{s.Key: true}, map[string]int{s.Key: 150})); n != 121 {
		t.Fatal(n)
	}
}

func TestCryptexPathsShareGroup(t *testing.T) {
	suffix := "/System/Library/Frameworks/WebKit.framework/Versions/A/XPCServices/com.apple.WebKit.Networking.xpc/Contents/MacOS/com.apple.WebKit.Networking"
	installed := proc(101, 1, "WebKit Networking", func(p *connProcess) { p.Path = "/System/Volumes/Preboot/Cryptexes/OS" + suffix })
	incoming := proc(102, 1, "WebKit Networking", func(p *connProcess) { p.Path = "/System/Volumes/Preboot/Cryptexes/Incoming/OS" + suffix })
	other := proc(103, 1, "Other")
	rows := []*connRow{row("a", installed), row("b", incoming), row("c", other)}
	tree := buildConnectionTree(rows, captureConnectionProcesses(rows, procs(installed, incoming, other), nil), groupApp, colCreated, true)
	if len(tree) != 2 {
		t.Fatal(len(tree))
	}
	for _, g := range tree {
		if len(g.Rows) == 2 && g.ProcessCount != 2 {
			t.Fatal(g.ProcessCount)
		}
	}
}

func TestProcessTreeDepths(t *testing.T) {
	worker := proc(30, 20, "worker")
	rows := []*connRow{row("root", editorApp), row("worker", worker)}
	tree := buildConnectionTree(rows, captureConnectionProcesses(rows, procs(editorApp, editorHelper, worker), nil), groupParent, colHost, false)
	if len(tree) != 1 || len(tree[0].Rows) != 2 {
		t.Fatal(len(tree))
	}
	root := tree[0]
	var child *connNode
	for _, n := range root.Children {
		if n.group() {
			child = n
		}
	}
	grandchild := child.Children[0]
	if child.Process.PID != 20 || grandchild.Process.PID != 30 {
		t.Fatal(child.Process.PID, grandchild.Process.PID)
	}
	visible := flattenConnectionTree(tree, map[string]bool{root.Key: true, child.Key: true, grandchild.Key: true}, nil)
	var depths []int
	for _, v := range visible {
		if v.Node.group() {
			depths = append(depths, v.Depth)
		} else if v.Node.Row.Conn.ID == "worker" && v.Depth != 3 {
			t.Fatal(v.Depth)
		}
	}
	if !slices.Equal(depths, []int{0, 1, 2}) {
		t.Fatal(depths)
	}
}

func TestAppOwnershipJoinsReparentedHelpers(t *testing.T) {
	orphan := editorHelper
	orphan.PPID = 1
	standalone, standalone2 := proc(40, 10, "node"), proc(41, 1, "node")
	rows := []*connRow{row("a", editorApp), row("h", orphan), row("s", standalone), row("s2", standalone2)}
	meta := captureConnectionProcesses(rows, procs(editorApp, orphan, standalone, standalone2), nil)
	tree := buildConnectionTree(rows, meta, groupApp, colHost, false)
	if len(tree) != 2 || len(groupByLabel(t, tree, "Editor").Rows) != 2 {
		t.Fatal(len(tree))
	}
	if g := groupByLabel(t, tree, "node"); len(g.Rows) != 2 || g.ProcessCount != 2 {
		t.Fatal(len(g.Rows), g.ProcessCount)
	}
	if n := len(buildConnectionTree(rows, meta, groupParent, colHost, false)); n != 3 {
		t.Fatal(n)
	}
}

func TestStandaloneGroupsMergeAcrossRestart(t *testing.T) {
	first, second := proc(40, 1, "codex"), proc(41, 1, "codex")
	other := proc(42, 1, "codex", func(p *connProcess) { p.Path = "/opt/codex" })
	rows := []*connRow{row("first", first), row("second", second), row("other", other)}
	meta := captureConnectionProcesses(rows, procs(first, second, other), nil)
	tree := buildConnectionTree(rows, meta, groupApp, colHost, false)
	if len(tree) != 2 {
		t.Fatal(len(tree))
	}
	var merged *connNode
	for _, n := range tree {
		if n.Process.Path == first.Path {
			merged = n
		}
	}
	if len(merged.Rows) != 2 || merged.ProcessCount != 2 || merged.Totals.UpRate != 20 || merged.Totals.DownTotal != 60 {
		t.Fatalf("%+v", merged.Totals)
	}
	leaves := 0
	for _, v := range flattenConnectionTree(tree, map[string]bool{merged.Key: true}, nil) {
		if v.Node != nil && !v.Node.group() {
			leaves++
		}
	}
	if leaves != 2 {
		t.Fatal(leaves)
	}
	exited := captureConnectionProcesses(rows, nil, meta)
	if buildConnectionTree(rows[:2], exited, groupApp, colHost, false)[0].Key != merged.Key {
		t.Error("exited key")
	}
	restarted := first
	restarted.StartedAt += 1000
	restartedRows := []*connRow{row("restart", restarted)}
	if buildConnectionTree(restartedRows, captureConnectionProcesses(restartedRows, procs(restarted), nil), groupApp, colHost, false)[0].Key != merged.Key {
		t.Error("restart key")
	}
	if n := len(buildConnectionTree(rows, meta, groupParent, colHost, false)); n != 3 {
		t.Fatal(n)
	}
}

func TestCapturedOwnershipSurvivesExit(t *testing.T) {
	rows := []*connRow{row("a", editorApp), row("h", editorHelper)}
	before := captureConnectionProcesses(rows, procs(editorApp, editorHelper), nil)
	after := captureConnectionProcesses(rows, nil, before)
	if fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatal("after exit")
	}
	recycled := editorHelper
	recycled.StartedAt = sample.CreatedAt + 1000
	if got := captureConnectionProcesses(rows, procs(recycled), before)["h"].Process.StartedAt; got != editorHelper.StartedAt {
		t.Fatal(got)
	}
	if got := captureConnectionProcesses(rows, procs(recycled), nil)["h"].Process.StartedAt; got != 0 {
		t.Fatal(got)
	}
	mismatched := editorHelper
	mismatched.Path = "/other"
	if got := captureConnectionProcesses(rows, procs(mismatched), nil)["h"].Process.StartedAt; got != 0 {
		t.Fatal(got)
	}
	if n := len(buildConnectionTree(rows, after, groupParent, colHost, false)[0].Rows); n != 2 {
		t.Fatal(n)
	}
	helperOnly := rows[1:]
	tree := buildConnectionTree(helperOnly, captureConnectionProcesses(helperOnly, nil, before), groupApp, colHost, false)
	if tree[0].Key != buildConnectionTree(rows, before, groupApp, colHost, false)[0].Key {
		t.Error("helper key")
	}
}

func TestReparentedAncestryRetained(t *testing.T) {
	worker := proc(30, 20, "worker")
	rows := []*connRow{row("h", editorHelper), row("w", worker)}
	before := captureConnectionProcesses(rows, procs(editorApp, editorHelper, worker), nil)
	original := buildConnectionTree(rows, before, groupParent, colHost, false)
	expanded := map[string]bool{}
	for _, v := range flattenConnectionTree(original, nil, nil) {
		expanded[v.Node.Key] = true
	}
	orphan := editorHelper
	orphan.PPID = 1
	orphanWorker := worker
	orphanWorker.PPID = 1
	snapshots := []map[int]connProcess{procs(orphan, worker), procs(orphan, worker), procs(orphanWorker)}
	meta := before
	pids := func(list []connProcess) []int {
		var out []int
		for _, p := range list {
			out = append(out, p.PID)
		}
		return out
	}
	for _, snapshot := range snapshots {
		meta = captureConnectionProcesses(rows, snapshot, meta)
		if !slices.Equal(pids(meta["h"].Ancestors), []int{10}) || !slices.Equal(pids(meta["w"].Ancestors), []int{10, 20}) {
			t.Fatal(pids(meta["h"].Ancestors), pids(meta["w"].Ancestors))
		}
		tree := buildConnectionTree(rows, meta, groupParent, colHost, false)
		if tree[0].Key != original[0].Key || len(tree[0].Rows) != 2 || len(flattenConnectionTree(tree, expanded, nil)) != 2 {
			t.Fatal("tree changed")
		}
	}
	recycled := worker
	recycled.StartedAt += 1000
	recycled.PPID = 1
	newRows := []*connRow{row("new-worker", recycled)}
	if a := captureConnectionProcesses(newRows, procs(recycled), meta)["new-worker"].Ancestors; len(a) != 0 {
		t.Fatal(a)
	}
}

func TestMissingSnapshotsInferApp(t *testing.T) {
	nested := editorHelper
	nested.PID, nested.Path = 25, "/Applications/Editor.app/Contents/Helpers/Helper.app/Contents/MacOS/Helper"
	unknown := proc(0, 0, "")
	unknown.Path = ""
	a, b := row("unknownA", unknown), row("unknownB", unknown)
	b.Conn.Source = "192.168.1.2:8080"
	rows := []*connRow{row("h", editorHelper), row("n", nested), a, b}
	tree := buildConnectionTree(rows, captureConnectionProcesses(rows, nil, nil), groupApp, colHost, false)
	if len(tree) != 3 || len(groupByLabel(t, tree, "Editor").Rows) != 2 {
		t.Fatal(len(tree))
	}
}

func TestAncestryCycles(t *testing.T) {
	a, b, c, orphan := proc(40, 41, "a"), proc(41, 40, "b"), proc(42, 41, "c"), proc(50, 999, "orphan")
	var rows []*connRow
	for _, p := range []connProcess{a, b, c, orphan} {
		rows = append(rows, row(strconv.Itoa(p.PID), p))
	}
	tree := buildConnectionTree(rows, captureConnectionProcesses(rows, procs(a, b, c, orphan), nil), groupParent, colHost, false)
	if len(tree) != 2 {
		t.Fatal(len(tree))
	}
	all := map[string]bool{}
	for _, n := range tree {
		if n.Process.PID == 40 && len(n.Rows) != 3 {
			t.Fatal(len(n.Rows))
		}
	}
	for key, n := range indexNodes(tree) {
		if n.group() {
			all[key] = true
		}
	}
	leaves := 0
	for _, v := range flattenConnectionTree(tree, all, nil) {
		if v.Node != nil && !v.Node.group() {
			leaves++
		}
	}
	if leaves != 4 {
		t.Fatal(leaves)
	}
}

func TestGroupMetricsSort(t *testing.T) {
	other := proc(30, 1, "Other")
	rows := []*connRow{
		row("a", editorApp, func(r *connRow) { r.UpRate, r.DownRate = 100, 5 }),
		row("h", editorHelper, func(r *connRow) { r.UpRate, r.DownRate = 100, 5 }),
		row("o", other, func(r *connRow) { r.UpRate, r.DownRate = 150, 40 }),
	}
	meta := captureConnectionProcesses(rows, procs(editorApp, editorHelper, other), nil)
	if l := buildConnectionTree(rows, meta, groupApp, colUploadRate, true)[0].Label; l != "Editor" {
		t.Fatal(l)
	}
	if l := buildConnectionTree(rows, meta, groupApp, colDownloadRate, true)[0].Label; l != "Other" {
		t.Fatal(l)
	}
	filtered := buildConnectionTree(rows[1:2], meta, groupApp, colHost, false)[0]
	if len(filtered.Rows) != 1 || filtered.Totals.UpRate != 100 || fmt.Sprint(connRuleCounts(filtered.Rows)) != "1 1" {
		t.Fatal(connRuleCounts(filtered.Rows))
	}
	if n := len(buildConnectionTree(rows, meta, groupNone, colUploadRate, true)); n != 3 {
		t.Fatal(n)
	}
}

func TestClosedFallbackEnriched(t *testing.T) {
	rows := []*connRow{row("h", editorHelper, func(r *connRow) { r.ClosedAt = 1700000001000 })}
	fallback := captureConnectionProcesses(rows, nil, nil)
	known := captureConnectionProcesses(rows, procs(editorHelper), fallback)
	if known["h"].Process.StartedAt != editorHelper.StartedAt {
		t.Fatal(known["h"].Process.StartedAt)
	}
	reused := editorHelper
	reused.StartedAt = 3000
	if got := captureConnectionProcesses(rows, procs(reused), known)["h"].Process.StartedAt; got != editorHelper.StartedAt {
		t.Fatal(got)
	}
}

func TestParentsWithoutStartTimes(t *testing.T) {
	zero := func(p *connProcess) { p.StartedAt = 0 }
	parent, first, second := proc(60, 1, "parent", zero), proc(61, 60, "first", zero), proc(62, 60, "second", zero)
	rows := []*connRow{row("first", first), row("second", second)}
	tree := buildConnectionTree(rows, captureConnectionProcesses(rows, procs(parent, first, second), nil), groupParent, colHost, false)
	if len(tree) != 1 {
		t.Fatal(len(tree))
	}
	root := tree[0]
	if root.Process.PID != 60 || len(root.Children) != 2 || root.Children[0].Key == root.Children[1].Key || len(root.Rows) != 2 {
		t.Fatalf("%+v", root)
	}
}

// testLine is a line of a page as the client lists it: a node, or a
// "more" line (Node nil) of a group with children left.
type testLine struct {
	Node  *connNode
	Depth int
}

// flattenConnectionTree lists the lines of the page's output of roots with
// the expanded groups open, as the client shows them.
func flattenConnectionTree(roots []*connNode, expanded map[string]bool, limits map[string]int) []testLine {
	expansion := connExpansion{keys: expanded, limits: limits}
	var lines []testLine
	var add func(item connItemJSON, depth int)
	add = func(item connItemJSON, depth int) {
		if item.Row != nil {
			lines = append(lines, testLine{Node: &connNode{Key: "connection:" + item.Row.ID, Row: &connRow{Conn: item.Row.connInfo}}, Depth: depth})
			return
		}
		lines = append(lines, testLine{Node: &connNode{Key: item.Group.Key}, Depth: depth})
		for _, child := range item.Group.Children {
			add(child, depth+1)
		}
		if item.Group.Remaining > 0 {
			lines = append(lines, testLine{Depth: depth + 1})
		}
	}
	for _, root := range roots {
		if root.group() {
			add(connItemJSON{Group: connGroupOutput(root, expansion)}, 0)
		} else {
			row := connRowOutput(root.Row)
			add(connItemJSON{Row: &row}, 0)
		}
	}
	return lines
}

func indexNodes(roots []*connNode) map[string]*connNode {
	result := map[string]*connNode{}
	var visit func(n *connNode)
	visit = func(n *connNode) {
		result[n.Key] = n
		for _, child := range n.Children {
			visit(child)
		}
	}
	for _, root := range roots {
		visit(root)
	}
	return result
}

func TestProtocolFacet(t *testing.T) {
	for protocol, want := range map[string]string{"tls": "TLS", "QUIC": "QUIC", "bittorrent": "BitTorrent", "": "未识别"} {
		row := &connRow{Conn: connInfo{Protocol: protocol}}
		if got := facetValue(row, facetProtocol); got != want {
			t.Errorf("facetValue(%q) = %q, want %q", protocol, got, want)
		}
	}
}

func TestAppAndPIDFacets(t *testing.T) {
	inApp, bare, unknown := sample, sample, sample
	inApp.ProcessInfo = &connProcessInfo{ProcessID: 101, ProcessPath: "/Applications/Google Chrome.app/Contents/Frameworks/Helper"}
	bare.ProcessInfo = &connProcessInfo{ProcessID: 202, ProcessPath: "/usr/bin/curl"}
	pool := []*connRow{{Conn: inApp}, {Conn: bare}, {Conn: unknown}}
	facets := connectionFacets(pool)
	if apps := facets[facetApp]; len(apps) != 1 || apps[0].Value != "Google Chrome" || apps[0].Count != 1 || apps[0].ProcessInfo == nil {
		t.Errorf("app facet = %+v", apps)
	}
	if pids := facets[facetPID]; len(pids) != 2 || pids[0].Value != "101" || pids[1].Value != "202" {
		t.Errorf("pid facet = %+v", pids)
	}
	if rows := filterConnectionRows(pool, map[connFacet][]string{facetApp: {"Google Chrome"}}, ""); len(rows) != 1 || rows[0].Conn.ProcessInfo.ProcessID != 101 {
		t.Errorf("app filter = %d rows", len(rows))
	}
	if rows := filterConnectionRows(pool, map[connFacet][]string{facetPID: {"202"}}, ""); len(rows) != 1 || rows[0].Conn.ProcessInfo.ProcessID != 202 {
		t.Errorf("pid filter = %d rows", len(rows))
	}
}

// The id filter keeps one connection and adds no group to the filter pane.
func TestConnectionIDFilter(t *testing.T) {
	a, b := sample, sample
	a.ID, b.ID = "conn-a", "conn-b"
	pool := []*connRow{{Conn: a}, {Conn: b}}
	if rows := filterConnectionRows(pool, map[connFacet][]string{facetID: {"conn-b"}}, ""); len(rows) != 1 || rows[0].Conn.ID != "conn-b" {
		t.Errorf("id filter = %d rows", len(rows))
	}
	if _, ok := connectionFacets(pool)[facetID]; ok {
		t.Error("id facet listed")
	}
	q, err := parseConnectionQuery(url.Values{"filter.id": {"conn-a"}})
	if err != nil || !slices.Equal(q.Filter[facetID], []string{"conn-a"}) {
		t.Errorf("query %+v %v", q.Filter, err)
	}
}

// A group's options count over every limit but its own; the tab counts
// over every limit but the tab; a chosen value stays listed.
func TestFacetedConnectionCounts(t *testing.T) {
	a, b, c := sample, sample, sample
	a.ID, a.Network, a.Inbound = "a", "tcp", "tun-in"
	b.ID, b.Network, b.Inbound = "b", "udp", "tun-in"
	c.ID, c.Network, c.Inbound = "c", "tcp", "mixed-in"
	all := []*connRow{{Conn: a}, {Conn: b}, {Conn: c, ClosedAt: 1}}
	q := connQuery{State: "active", Filter: map[connFacet][]string{facetNetwork: {"TCP"}, facetInbound: {"socks-in"}}}
	matched, counts, totals, facets := facetConnectionRows(all, q)
	if len(matched) != 0 || totals.All != 3 || counts.All != 0 {
		t.Fatalf("matched %d counts %+v totals %+v", len(matched), counts, totals)
	}
	if inbound := facets[facetInbound]; len(inbound) != 2 || inbound[0].Value != "tun-in" || inbound[0].Count != 1 || inbound[1].Value != "socks-in" || inbound[1].Count != 0 {
		t.Errorf("inbound %+v", inbound)
	}
	q.Filter = map[connFacet][]string{facetNetwork: {"TCP"}}
	matched, counts, _, facets = facetConnectionRows(all, q)
	if len(matched) != 1 || counts.All != 2 || counts.Active != 1 || counts.Closed != 1 {
		t.Errorf("matched %d counts %+v", len(matched), counts)
	}
	if network := facets[facetNetwork]; len(network) != 2 || network[0].Count != 1 || network[1].Count != 1 {
		t.Errorf("network %+v", network)
	}
	if inbound := facets[facetInbound]; len(inbound) != 1 || inbound[0].Value != "tun-in" {
		t.Errorf("inbound %+v", inbound)
	}
}
