package routes

import (
	"math"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// topologyDoc has a Pre-rules rule and local rules on a process, a domain
// and a port.
func topologyDoc() *model.Doc {
	doc := baseTestDoc()
	doc.Route.Rules = []*model.RouteRule{
		routeRule("pre-block", func(r *model.RouteRule) {
			r.DeviceID, r.GroupID, r.Action, r.Outbound = model.PreDeviceID, "pre-group", model.ActionReject, model.Ref{}
			r.Match = map[string]any{"domain_suffix": strs("ads.test")}
		}),
		routeRule("browser", func(r *model.RouteRule) {
			r.Outbound = model.Some("proxy")
			r.Match = map[string]any{"process_path": strs("/Applications/Browser.app/Contents/MacOS/Browser")}
		}),
		routeRule("example", func(r *model.RouteRule) {
			r.Match = map[string]any{"domain_suffix": strs("example.com")}
		}),
		routeRule("https", func(r *model.RouteRule) {
			r.GroupID, r.Outbound = "local-later", model.Some("proxy")
			r.Match = map[string]any{"port": []any{float64(443)}}
		}),
	}
	return doc
}

func TestTopologyGraph(t *testing.T) {
	doc := topologyDoc()
	g := buildTopology(doc, model.LocalDeviceID, func(id string) string { return outboundChain(doc, id, nil, tagIndex(doc)) })
	var ids, numbers []string
	for _, r := range g.rules() {
		ids, numbers = append(ids, r.id), append(numbers, r.number)
	}
	if !slices.Equal(ids, []string{"pre-block", "browser", "example", "https", "final"}) || !slices.Equal(numbers, []string{"P1", "1", "2", "3", "4"}) {
		t.Fatalf("rules %v numbered %v", ids, numbers)
	}
	if len(g.bands) != 3 || g.bands[0].title != "前置规则 · 前置" {
		t.Errorf("bands %+v", g.bands)
	}
	var exits []string
	for _, e := range g.exits {
		exits = append(exits, e.key+"="+e.label)
	}
	// Two rules share Proxy; the default goes Direct as the sample does.
	if !slices.Equal(exits, []string{"reject=拒绝连接", "out:proxy=代理", "out:direct=直连"}) {
		t.Errorf("exits %v", exits)
	}
	if g.final.exit != "out:direct" || g.final.hitKey != "final:local" {
		t.Errorf("final %+v", g.final)
	}

	pre := buildTopology(doc, model.PreDeviceID, func(string) string { return "" })
	if len(pre.rules()) != 2 || pre.final.exit != "next" || pre.final.hitKey != "" {
		t.Errorf("前置规则 topology %+v", pre)
	}
}

func TestTopologyTestNarrows(t *testing.T) {
	doc := topologyDoc()
	g := buildTopology(doc, model.LocalDeviceID, func(string) string { return "" })
	in := policyTestInput{deviceID: model.LocalDeviceID, domain: "news.test", facts: map[string]string{}}

	// Neither the process nor the port is known: either rule may take
	// the connection, or neither, leaving it to the default.
	p := explore(doc, in)
	want := map[string]pathState{"pre-block": pathSkipped, "browser": pathPossible, "example": pathSkipped, "https": pathPossible, "final": pathPossible}
	for id, s := range want {
		if p.state[id] != s {
			t.Errorf("unknown facts: %s is %d, want %d", id, p.state[id], s)
		}
	}
	if exits := p.exits(g); !slices.Equal(exits, []string{"out:proxy", "out:direct"}) {
		t.Errorf("exits %v", exits)
	}
	if u := p.unknownConditions(g); !slices.Equal(u, []string{"进程路径", "目标端口"}) {
		t.Errorf("unknown %v", u)
	}

	// Another process on 443: only the port rule takes it.
	in.facts["process_path"] = "/usr/bin/curl"
	in.facts["port"] = "443"
	p = explore(doc, in)
	if p.state["browser"] != pathSkipped || p.state["https"] != pathDecided || p.state["final"] != pathSkipped {
		t.Errorf("narrowed: %v", p.state)
	}
	if exits := p.exits(g); !slices.Equal(exits, []string{"out:proxy"}) {
		t.Errorf("narrowed exits %v", exits)
	}

	// The browser itself is taken first, whatever the port.
	in.facts = map[string]string{"process_path": "/Applications/Browser.app/Contents/MacOS/Browser"}
	if p = explore(doc, in); p.state["browser"] != pathDecided || p.state["https"] != pathSkipped {
		t.Errorf("browser: %v", p.state)
	}
	if got := testVia(g, p, "out:proxy"); got != "1 · 未命名规则" {
		t.Errorf("via %q", got)
	}
}

func TestSampleFacts(t *testing.T) {
	in := policyTestInput{ip: "192.168.1.8", facts: map[string]string{
		"port": "8443", "process_path": `C:\Apps\Tool.exe`, "network": "TCP", "source_ip": "10.0.0.5",
	}}
	cases := []struct {
		field  string
		values []string
		want   matchState
	}{
		{"port_range", []string{"8000:9000"}, stateMatch},
		{"port", []string{"443"}, stateMiss},
		{"process_name", []string{"Tool.exe"}, stateMatch},
		{"process_path_regex", []string{`(?i)\\apps\\`}, stateMatch},
		{"network", []string{"tcp"}, stateMatch},
		{"source_ip_cidr", []string{"10.0.0.0/8"}, stateMatch},
		{"ip_is_private", []string{"true"}, stateMatch},
		{"ip_version", []string{"6"}, stateMiss},
	}
	for _, tc := range cases {
		r, ok := evaluateFact(tc.field, tc.values, in)
		if !ok || r.state != tc.want {
			t.Errorf("%s %v: %v %v, want %v", tc.field, tc.values, r.state, ok, tc.want)
		}
	}
	if _, ok := evaluateFact("user", []string{"root"}, in); ok {
		t.Error("an untold fact decided")
	}
	for ip, private := range map[string]bool{"224.0.0.251": true, "0.0.0.0": true, "::ffff:10.1.2.3": true, "8.8.8.8": false} {
		r, ok := evaluateFact("ip_is_private", []string{"true"}, policyTestInput{ip: ip})
		if !ok || (r.state == stateMatch) != private {
			t.Errorf("ip_is_private %s: %v", ip, r.state)
		}
	}
	if r, ok := evaluateFact("source_ip_is_private", []string{"true"}, in); !ok || r.state != stateMatch {
		t.Errorf("source_ip_is_private: %v %v", r.state, ok)
	}
}

// A leaf meets the core's groups of fields when any of a group's fields
// matches: the ports, and the destination address with ip_is_private.
func TestLeafGroups(t *testing.T) {
	in := policyTestInput{domain: "example.com", ip: "8.8.8.8", facts: map[string]string{"port": "8443"}}
	cases := []struct {
		match map[string]any
		want  matchState
	}{
		{map[string]any{"port": strs("80"), "port_range": strs("8000:9000")}, stateMatch},
		{map[string]any{"port": strs("80"), "port_range": strs("1:100")}, stateMiss},
		{map[string]any{"domain_suffix": strs("example.com"), "ip_is_private": true}, stateMatch},
		{map[string]any{"domain_suffix": strs("other.test"), "ip_is_private": true}, stateMiss},
		{map[string]any{"port": strs("8443"), "domain_suffix": strs("other.test")}, stateMiss},
	}
	for _, tc := range cases {
		node := &model.Condition{Kind: model.ConditionMatch, Match: tc.match}
		if r := evaluateLeaf(node, baseTestDoc(), in, false); r.state != tc.want {
			t.Errorf("%v: %v, want %v", tc.match, r.state, tc.want)
		}
	}
	if f := factFor(model.ConditionLabels["process_name"]); f == nil || f.key != "process_name" {
		t.Errorf("进程名 adds %+v", f)
	}
}

func TestTopologyLayout(t *testing.T) {
	doc := topologyDoc()
	g := buildTopology(doc, model.LocalDeviceID, func(string) string { return "" })
	l := layoutTopology(g, 1100)
	if l.w > 1100 || l.h <= 0 {
		t.Fatalf("size %v×%v", l.w, l.h)
	}
	var boxes []topoBox
	for _, e := range g.exits {
		boxes = append(boxes, l.exits[e.key])
	}
	for i, a := range boxes {
		if final := l.rules["final"]; a.x <= final.x+final.w {
			t.Errorf("exit %d left of the rules: %+v", i, a)
		}
		for _, b := range boxes[i+1:] {
			if a.y < b.y+b.h && b.y < a.y+a.h {
				t.Errorf("exits overlap: %+v %+v", a, b)
			}
		}
	}
	if narrow := layoutTopology(g, 400); narrow.w <= 400 {
		t.Errorf("narrow canvas %v keeps the columns apart", narrow.w)
	}
}

func TestTopologyView(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	f.click("拓扑")
	testkit.UntilText(t, tt, "入口")
	if !f.w.topology || !tt.HasText("美国节点") || !tt.HasText("默认出口") {
		t.Fatalf("topology: %q", tt.Texts())
	}
	// Connections on Dev & Collaboration make its lines flow.
	f.w.hits.add(map[string]int{"r-dev": 12, "final:local": 2}, tt.Now())
	tt.Advance(400 * time.Millisecond)
	f.snapshot("topology.png", false)
	tt.SetDark(true)
	tt.Advance(400 * time.Millisecond)
	f.snapshot("topology-dark.png", true)
	tt.SetDark(false)
	tt.Advance(400 * time.Millisecond)

	f.click("测试路径")
	testkit.UntilText(t, tt, "样本连接")
	if !tt.HasText("可能出口") || !tt.HasText("待确定条件") {
		t.Fatalf("test: %q", tt.Texts())
	}
	tt.Advance(400 * time.Millisecond)
	f.snapshot("topology-test-open.png", false)
	f.w.topoTest.domain = "api.github.com"
	testkit.UntilText(t, tt, "唯一出口")
	if !tt.HasText("开发与协作") {
		t.Errorf("way out: %q", tt.Texts())
	}
	// The menu of "Add condition" adds a fact's field (the tester does not show
	// popover menus).
	f.w.topoTest.addFact("process_path")
	testkit.Until(t, tt, func() bool { _, ok := tt.Find("移除进程路径"); return ok })
	tt.Advance(400 * time.Millisecond)
	f.snapshot("topology-test.png", false)

	f.click("测试路径")
	tt.Advance(400 * time.Millisecond)

	// A rule node opens its editor.
	f.click("查看 Apple 服务")
	testkit.UntilText(t, tt, "匹配条件")
	if !tt.HasText("apple.com") || !tt.HasText("美国节点") || tt.HasText("编辑路由规则") {
		t.Errorf("preview: %q", tt.Texts())
	}
	tt.Advance(400 * time.Millisecond)
	f.snapshot("topology-preview.png", false)
	f.click("编辑")
	testkit.UntilText(t, tt, "编辑路由规则")

	f.click("列表")
	testkit.Until(t, tt, func() bool { return !f.w.topology })
}

// The camera zooms about a point, keeps the topology in view and the
// middle of the view as it resizes.
func TestTopologyCameraMath(t *testing.T) {
	cam := &topoCamera{}
	cam.frame("local", 1000, 600, 800, 400)
	if cam.level() != 1 || cam.x != 100 || cam.y != 100 {
		t.Fatalf("centered %+v", cam)
	}
	// macOS pinches in many small steps.
	at := func(px float32) float32 { return (px - cam.x) / cam.level() }
	before := at(300)
	for range 40 {
		cam.zoomAt(cam.level()*1.01, 300, 200)
	}
	if cam.level() < 1.4 || abs32(at(300)-before) > 0.01 {
		t.Errorf("pinch: zoom %v, point %v→%v", cam.level(), before, at(300))
	}
	cam.reset()
	cam.pan(5000, 5000)
	if cam.x != 1000-topoKeep || cam.y != 600-topoKeep {
		t.Errorf("panned away: %+v", cam)
	}
	cam.reset()
	cam.pan(1, 0)
	cam.frame("local", 700, 600, 800, 400)
	if cam.x != -49 {
		t.Errorf("resize keeps the middle: %v", cam.x)
	}
	cam.frame("phone", 700, 600, 300, 200)
	if cam.x != 200 || cam.y != 200 {
		t.Errorf("another owner centers: %+v", cam)
	}

	// A topology larger than the view fits whole, in its middle, and
	// fits again as the view resizes until it is moved.
	cam = &topoCamera{}
	cam.frame("local", 600, 400, 1136, 1536)
	fit := float32(400-2*topoFitMargin) / 1536
	if abs32(cam.level()-fit) > 1e-4 {
		t.Fatalf("fit zoom %v, want %v", cam.level(), fit)
	}
	if abs32(cam.x+1136*fit/2-300) > 0.01 || abs32(cam.y+1536*fit/2-200) > 0.01 {
		t.Errorf("fit off the middle: %+v", cam)
	}
	if cam.y < topoFitMargin-0.01 || cam.x < 0 {
		t.Errorf("fit cut off: %+v", cam)
	}
	// Zooming out stops at the fit, below the least step.
	cam.step(true)
	for range 20 {
		cam.step(false)
	}
	if abs32(cam.level()-fit) > 1e-4 {
		t.Errorf("zoomed out to %v, fit %v", cam.level(), fit)
	}
	cam.reset()
	cam.frame("local", 600, 800, 1136, 1536)
	if fit := float32(800-2*topoFitMargin) / 1536; abs32(cam.level()-fit) > 1e-4 {
		t.Errorf("refit zoom %v, want %v", cam.level(), fit)
	}
	cam.step(true)
	cam.pan(30, 0)
	cam.frame("local", 600, 700, 1136, 1536)
	if cam.fitted || cam.level() != 0.67 {
		t.Errorf("a moved camera refit: %+v", cam)
	}
	cam.reset()
	if !cam.fitted || abs32(cam.x+1136*cam.level()/2-300) > 0.01 {
		t.Errorf("reset off the middle: %+v", cam)
	}
}

// Pinching, scrolling, dragging and the buttons move the topology.
func TestTopologyCamera(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	f.click("拓扑")
	testkit.UntilText(t, tt, "入口")
	tt.Advance(400 * time.Millisecond)
	view, ok := tt.Find("路由拓扑")
	if !ok {
		t.Fatalf("no view: %q", tt.Texts())
	}
	cam := &f.w.topoCam
	cx, cy := view.X+view.W/2, view.Y+view.H/2
	home := cam.x
	homeLabel := zoomLabel(cam.fitZoom())

	// A pinch of many small steps zooms about the pointer.
	px, py := view.X+view.W/3, view.Y+view.H/3
	under := (px - view.X - cam.x) / cam.level()
	for range 30 {
		tt.Pinch(px, py, 1.01)
	}
	testkit.Until(t, tt, func() bool { return cam.level() > 1.3 })
	if got := (px - view.X - cam.x) / cam.level(); abs32(got-under) > 0.5 {
		t.Errorf("pinch moved the point under the pointer: %v→%v", under, got)
	}
	f.click("恢复原始大小")
	testkit.UntilText(t, tt, homeLabel)
	if cam.x != home {
		t.Errorf("reset at %v, centered at %v", cam.x, home)
	}

	// The buttons zoom about the middle of the view.
	middle := (cx - view.X - cam.x) / cam.level()
	f.click("放大")
	testkit.UntilText(t, tt, "缩放比例 125%")
	f.click("放大")
	testkit.UntilText(t, tt, "缩放比例 150%")
	if got := (cx - view.X - cam.x) / cam.level(); abs32(got-middle) > 0.5 {
		t.Errorf("zoom moved the middle: %v→%v", middle, got)
	}
	tt.Advance(400 * time.Millisecond)
	f.snapshot("topology-zoom.png", false)
	f.click("恢复原始大小")
	for range 6 {
		f.click("缩小")
	}
	testkit.UntilText(t, tt, zoomLabel(cam.minZoom()))
	tt.Advance(400 * time.Millisecond)
	f.snapshot("topology-zoom-out.png", false)
	f.click("恢复原始大小")

	// Scrolling pans, past where the old edge stopped it.
	x0 := cam.x
	tt.Wheel(cx, cy, -200, 0)
	testkit.Until(t, tt, func() bool { return cam.x > x0+150 })

	// Dragging the empty canvas pans.
	x0, y0 := cam.x, cam.y
	bx, by := view.X+view.W-40, view.Y+view.H-120
	tt.Press(bx, by)
	for i := 1; i <= 5; i++ {
		tt.Move(bx-float32(i)*20, by-float32(i)*10)
		tt.Advance(16 * time.Millisecond)
	}
	tt.Release(bx-100, by-50)
	tt.Advance(100 * time.Millisecond)
	if abs32(cam.x-(x0-100)) > 2 || abs32(cam.y-(y0-50)) > 2 {
		t.Errorf("drag: %v,%v → %v,%v", x0, y0, cam.x, cam.y)
	}
	tt.Advance(400 * time.Millisecond)
	f.snapshot("topology-panned.png", false)

	// Dragging from a node pans it too, and opens nothing.
	node, ok := tt.Find("查看 Apple 服务")
	if !ok {
		t.Fatalf("no node")
	}
	x0 = cam.x
	nx, ny := node.X+node.W/2, node.Y+node.H/2
	tt.Press(nx, ny)
	for i := 1; i <= 5; i++ {
		tt.Move(nx+float32(i)*12, ny)
		tt.Advance(16 * time.Millisecond)
	}
	tt.Release(nx+60, ny)
	tt.Advance(100 * time.Millisecond)
	if cam.x <= x0+30 || tt.HasText("匹配条件") {
		t.Errorf("node drag: %v→%v, preview %v", x0, cam.x, tt.HasText("匹配条件"))
	}
	// A click on it still shows its rule.
	f.click("查看 Apple 服务")
	testkit.UntilText(t, tt, "匹配条件")
}

// Open connections' traffic keeps a rule's lines flowing without new
// connections, more for more, until the rates go stale.
func TestFlowRates(t *testing.T) {
	var h ruleHits
	now := time.Now()
	h.setRates(map[string]float64{"slow": 2 << 10, "fast": 16 << 20, "trickle": 10}, now)
	slow, fast := h.flow("slow", now), h.flow("fast", now)
	if slow < rateFlowMin || slow >= fast || fast != 1 || h.flow("trickle", now) != 0 {
		t.Errorf("flows slow %v fast %v trickle %v", slow, fast, h.flow("trickle", now))
	}
	if h.flow("fast", now.Add(flowSpan+time.Second)) != 0 {
		t.Error("stale rates flow")
	}
	h.add(map[string]int{"slow": 40}, now)
	if h.flow("slow", now) <= slow {
		t.Error("new connections no longer add")
	}
}

func zoomLabel(z float32) string {
	return "缩放比例 " + strconv.Itoa(int(math.Round(float64(z*100)))) + "%"
}
