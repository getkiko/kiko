package routes

import (
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/mitmpolicy"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// prefTopology saves whether Traffic Rules shows as the topology.
const prefTopology = "kiko-routes-topology"

// topoTest is the topology's test: a sample connection from the device
// shown, its inbound, domain and IP, and the facts added to narrow it, in
// the order they were added.
type topoTest struct {
	inbound, domain, ip string
	facts               map[string]string
	added               []string
}

// input is the sample of the test, from owner.
func (t *topoTest) input(doc *model.Doc, owner string) policyTestInput {
	in := policyTestInput{deviceID: owner, domain: model.NormalizeDomainName(t.domain), ip: strings.TrimSpace(t.ip), facts: t.facts}
	if in.domain == "" {
		in.domain = strings.TrimSpace(t.domain)
	}
	for _, ib := range doc.Inbounds {
		if ib.Tag == t.inbound {
			in.inboundID = ib.ID
		}
	}
	for _, ep := range doc.Endpoints {
		if ep.Tag == t.inbound {
			in.inboundID = ep.ID
		}
	}
	return in
}

// ownerIcon is the symbol of a policy owner, as the device sidebar shows it.
func ownerIcon(doc *model.Doc, id string) kit.Symbol {
	switch id {
	case model.PreDeviceID:
		return kit.SymRoute
	case model.OtherDeviceID:
		return kit.SymGlobe2
	}
	if model.IsLocalScope(id) {
		return kit.SymRouter
	}
	if d := findDevice(doc, id); d != nil {
		return deviceIcon(d.Icon)
	}
	return kit.SymGlobe2
}

// toggleTopologyTest opens or closes the topology's test.
func (w *Workspace) toggleTopologyTest() {
	if w.topoTest != nil {
		w.topoTest = nil
		return
	}
	w.topoTest = &topoTest{facts: map[string]string{}}
	w.refreshTrust()
}

// topologyView is Traffic Rules as a topology of the device shown, with the
// test beside it while open.
func (w *Workspace) topologyView(c *ui.Context, doc *model.Doc) {
	k := kit.T(c)
	w.syncChoices()
	g := buildTopology(doc, w.owner, func(id string) string { return w.chain(doc, id) })
	var paths *topoPaths
	if t := w.topoTest; t != nil {
		p := explore(doc, t.input(doc, w.owner))
		paths = &p
	}
	query := strings.ToLower(strings.TrimSpace(w.search))
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).BorderWidth(1, 0, 0, 0).BorderColor(k.HairlineSoft).Children(func() {
		ui.Box(c).Grow(1).MinWidth(0).AlignItems(ui.Stretch).Children(func() {
			cam := &w.topoCam
			view := ui.Box(c.Key("topology")).Grow(1).MinWidth(0).Clip().Background(k.Canvas).Label("路由拓扑").
				HandleInput(w.topologyInput)
			bounds := view.Bounds()
			vw, vh := bounds.W, bounds.H
			if vw <= 0 || vh <= 0 {
				vw, vh = 960, 640
			}
			// The layout fits the view's width at 100%.
			l := layoutTopology(g, vw)
			cam.frame(w.owner, bounds.W, bounds.H, l.w, l.h)
			if bounds.W > 0 && bounds.H > 0 && cam.owner == w.owner {
				w.revealFlash(l, bounds)
			}
			if dx, dy, ok := view.Dragged(); ok {
				cam.pan(dx, dy)
				view.Cursor(ui.CursorGrabbing)
			}
			w.topologyShortcuts(c)
			view.Draw(func(p *ui.Painter, r ui.Rect) { drawTopologyDots(p, r, k, cam) })
			zoom := cam.level()
			view.Children(func() {
				canvas := ui.Box(c).Absolute().Left(cam.x).Top(cam.y).Size(l.w, l.h).TransformOrigin(0, 0).Scale(zoom, zoom)
				still := c.Preferences().ReduceMotion
				canvas.Draw(func(p *ui.Painter, r ui.Rect) { w.drawTopology(p, r, k, g, l, paths, still, zoom) })
				canvas.Children(func() {
					w.entryNode(c, doc, l.entry)
					for _, t := range l.titles {
						ui.Text(c, t.text).Absolute().Left(t.x + 2).Top(t.y + 6).Width(t.w).FontSize(kit.FontCaption).
							FontWeight(700).TextColor(k.Label3).SingleLine().Ellipsis("…")
					}
					for _, r := range g.rules() {
						state := pathOff
						if paths != nil {
							state = paths.state[r.id]
						}
						w.ruleNode(c, doc, r, l.rules[r.id], state, query)
					}
					var taken []string
					if paths != nil {
						taken = paths.exits(g)
					}
					var lock *mitmpolicy.Outcome
					if t := w.topoTest; t != nil {
						if o, ok := mitmOutcome(doc, model.NormalizeDomainName(t.domain), w.untrusted, mitmpolicy.Source{Device: w.owner}, "", false); ok {
							lock = &o
						}
					}
					for _, e := range g.exits {
						box, ok := l.exits[e.key]
						if !ok {
							continue
						}
						w.exitNode(c, e, box, paths != nil, slices.Contains(taken, e.key), len(taken) == 1, lock)
					}
				})
			})
			w.topologyZoomControls(c)
		})
		if w.editor != nil || w.fallback != nil {
			w.topoPreview = ""
		}
		switch {
		case w.topoPreview != "":
			w.topologyPreview(c, doc, g)
		case paths != nil:
			w.topologyTestPanel(c, doc, g, *paths)
		}
	})
}

// drawTopology paints the canvas: its dots, and the lines from the entry
// to each rule and from each rule to its exit. Dots run along a rule's
// lines while it takes connections, faster and more of them the more
// (topology_flow.go); a test lights the paths its sample may take and
// dims the others.
func (w *Workspace) drawTopology(p *ui.Painter, r ui.Rect, k *kit.Tokens, g *topoGraph, l topoLayout, paths *topoPaths, still bool, zoom float32) {
	// Zoomed out, thin lines keep their width on screen.
	thin := max(1, 1/zoom)
	// Everything here draws opaque, in polylines the GPU draws as they
	// are: a path's mask would take room in the atlas the window's text
	// and icons share, every frame anew for the flowing lines.
	solid := func(c ui.Color) ui.Color { return c.Over(k.Canvas) }

	now := p.Now()
	flowing := false
	curve := func(x1, y1, x2, y2 float32) [][2]float32 {
		return topoCurve(r.X+x1, r.Y+y1, r.X+x2, r.Y+y2)
	}
	w.flows.begin()
	fastest := 0.0
	// edge paints a line of a rule: its base, then, while the rule is busy,
	// a tint along it and the dots of line, from along on. A rule's two
	// lines share its dots, the exit's continuing the entry's, and both
	// take its tint, so the way through it reads as one.
	edge := func(pts [][2]float32, lit bool, decided bool, enabled bool, flow float32, tone ui.Color, line *topoFlowLine, along float32) float32 {
		switch {
		case paths == nil && enabled:
			p.StrokePolyline(pts, 1.25*thin, solid(k.Hairline))
		case paths == nil:
			p.StrokePolyline(pts, thin, solid(k.HairlineSoft))
		case lit && decided:
			p.StrokePolyline(pts, 2.25*thin, solid(k.Accent))
		case lit:
			p.StrokePolyline(pts, 2*thin, solid(k.Accent.Alpha(0.6)))
		default:
			p.StrokePolyline(pts, thin, solid(k.HairlineSoft.Alpha(0.6)))
		}
		path := measureTopoPath(pts)
		if !enabled {
			return path.length()
		}
		if still {
			// With less motion a busy line holds a steady tint.
			if flow > 0 {
				flowing = true
				p.StrokePolyline(pts, 2*thin, solid(tone.Alpha(0.5+0.4*flow)))
			}
			return path.length()
		}
		if line == nil || !line.visible() {
			return path.length()
		}
		flowing = true
		p.StrokePolyline(pts, 1.75*thin, solid(tone.Alpha(line.tint())))
		drawFlowDots(p, path, line.travelled-float64(along/thin), line.speed(), line.level, thin, tone, k.Canvas)
		return path.length()
	}
	ex, ey := l.entry.right()
	for _, rule := range g.rules() {
		box := l.rules[rule.id]
		state := pathOff
		if paths != nil {
			state = paths.state[rule.id]
		}
		var flow float32
		if rule.hitKey != "" {
			flow = w.hits.flow(rule.hitKey, now)
		}
		tone := k.Success
		if rule.action == model.ActionReject {
			tone = k.Danger
		}
		var line *topoFlowLine
		if rule.enabled && !still {
			line = w.flows.step(rule.id, flow, now)
			if line.visible() {
				fastest = max(fastest, line.speed())
			}
		}
		lx, ly := box.left()
		entry := edge(curve(ex, ey, lx, ly), state.reaching(), state == pathDecided, rule.enabled, flow, tone, line, 0)
		if out, ok := l.exits[rule.exit]; ok && rule.exit != "" {
			rx, ry := box.right()
			ox, oy := out.left()
			edge(curve(rx, ry, ox, oy), state.taking(), state == pathDecided, rule.enabled, flow, tone, line, entry)
		}
	}
	w.flows.end()
	switch {
	case flowing && still:
		// To go still as the rules' traffic ends.
		p.After(time.Second)
	case flowing && fastest > 60:
		// Fast dots step too far between frames at 30.
		p.AnimationFrameRate(60)
	case flowing:
		p.AnimationFrameRate(30)
	}
}

// topoNodeLook is a node's fill, border and, when muted, text color.
type topoNodeLook struct {
	bg, border  ui.Color
	borderWidth float32
	muted       bool
	text        ui.Color
}

// nodeStyle is a node's look for a test's state: the paths it decides
// strong, those it may take in the accent, the others muted.
func nodeStyle(k *kit.Tokens, lit, decided, tested bool) topoNodeLook {
	switch {
	case !tested:
		return topoNodeLook{bg: k.Surface, border: k.Hairline, borderWidth: 1}
	case decided:
		return topoNodeLook{bg: k.AccentWeak, border: k.Accent, borderWidth: 1.5}
	case lit:
		return topoNodeLook{bg: k.Surface, border: k.Accent.Alpha(0.7), borderWidth: 1.5}
	}
	return topoNodeLook{bg: k.Surface2, border: k.HairlineSoft, borderWidth: 1, muted: true, text: k.Label3}
}

// apply styles e with the look.
func (l topoNodeLook) apply(e ui.Element) {
	e.Background(l.bg).Border(l.borderWidth, l.border)
	if l.muted {
		e.TextColor(l.text)
	}
}

// nodeLook styles a node's element for a test's state (nodeStyle).
func nodeLook(e ui.Element, k *kit.Tokens, lit, decided, tested bool) {
	nodeStyle(k, lit, decided, tested).apply(e)
}

// entryNode is the device the connections come from.
func (w *Workspace) entryNode(c *ui.Context, doc *model.Doc, b topoBox) {
	k := kit.T(c)
	name := model.PolicyOwnerName(doc, w.owner)
	e := ui.Row(c.Key("entry")).Absolute().Left(b.x).Top(b.y).Size(b.w, b.h).Padding(0, 14).Gap(10).Radius(14).
		Shadow(0, 1, 3, 0, k.Shadow).Label("入口 " + name)
	nodeLook(e, k, false, false, false)
	e.Children(func() {
		ui.Box(c).Size(34, 34).Radius(10).Center().Background(k.AccentWeak).TextColor(k.AccentText).Shrink(0).
			Children(func() { kit.Icon(c, ownerIcon(doc, w.owner), 18) })
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			kit.Clipped(c, name, kit.FontBody, 600)
			kit.Clipped(c, "入口", kit.FontCaption, 400).TextColor(k.Label3)
		})
	})
}

// ruleNode is a rule, or the default: its light, number, name and
// conditions, and its action; a click edits it.
func (w *Workspace) ruleNode(c *ui.Context, doc *model.Doc, r topoRule, b topoBox, state pathState, query string) {
	k := kit.T(c)
	selected := w.topoPreview == r.id || r.final && w.fallback != nil || w.editor != nil && !r.final && w.editor.id() == r.id
	n := ui.ButtonBase(c.Key("rule:"+r.id)).Absolute().Left(b.x).Top(b.y).Size(b.w, b.h).PaddingX(10).Gap(8).
		Radius(10).Justify(ui.Start).Label("查看 " + r.name).Transition(kit.HoverMotion)
	tested := state != pathOff
	look := nodeStyle(k, state.reaching(), state == pathDecided, tested)
	switch {
	case selected:
		look.borderWidth, look.border = 1.5, k.Accent
	case !tested && n.Hovered():
		look.bg = k.RowHover
	}
	look.apply(n)
	w.flashNode(c, n, r.id, look)
	dim := !r.enabled || query != "" && !strings.Contains(strings.ToLower(r.name+" "+r.summary), query)
	if dim && !selected {
		n.TextColor(k.Label4)
	}
	n.Children(func() {
		label, icon, tone := actionLook(c, r.action, false)
		if !r.enabled {
			tone = k.Label4
		}
		if r.hitKey != "" {
			w.hitLight(c, r.hitKey, tone)
		}
		if r.number != "" {
			ui.Text(c, r.number).FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum").Shrink(0)
		}
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			kit.Clipped(c, r.name, kit.FontTable, 600)
			kit.Clipped(c, firstNonEmptyText(r.summary, "任意连接"), kit.FontCaption, 400).TextColor(k.Label3)
		})
		if r.invalid {
			kit.Hint(c, kit.Icon(c, kit.SymTriangleAlert, 13).TextColor(k.Warning).Label("请修正配置"), "请修正配置")
		}
		if !r.final {
			kit.Hint(c, kit.Icon(c, icon, 15).TextColor(tone).Label(label), label)
		}
	})
	if n.Pressed() {
		w.rowHeld = true
	}
	// A drag from the node pans the topology; a click shows its rule.
	cam := &w.topoCam
	if dx, dy, ok := n.Dragged(); ok {
		if cam.dragKey != r.id {
			cam.dragKey, cam.drag, cam.panned = r.id, 0, false
		}
		z := cam.level()
		cam.drag += (abs32(dx) + abs32(dy)) * z
		if cam.drag > dragSlop {
			cam.pan(dx*z, dy*z)
			cam.panned = true
		}
	}
	if n.Clicked() && !(cam.dragKey == r.id && cam.panned) {
		w.previewTopologyNode(r.id)
	}
	if !n.Pressed() && cam.dragKey == r.id {
		cam.dragKey, cam.drag, cam.panned = "", 0, false
	}
}

// firstNonEmptyText is the first of values that is not empty.
func firstNonEmptyText(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// exitNode is an exit: an outbound with the groups' members it goes
// through, or what rules without one do. A test lights the exits its
// sample may leave by, the one it surely does strongest.
func (w *Workspace) exitNode(c *ui.Context, e topoExit, b topoBox, tested, taken, only bool, lock *mitmpolicy.Outcome) {
	k := kit.T(c)
	n := ui.Row(c.Key("exit:"+e.key)).Absolute().Left(b.x).Top(b.y).Size(b.w, b.h).PaddingX(12).Gap(10).Radius(12).
		Label("出口 " + e.label)
	nodeLook(n, k, taken, taken && only, tested)
	// An outbound's node tests its server, or a group's, from its menu
	// as the rules' rows do.
	if id := strings.TrimPrefix(e.key, "out:"); e.kind == exitOutbound && id != e.key && w.hasExitTests(id) {
		n.PopoverMenu(func(m *ui.Menu) { w.exitTests(m, id, false) })
	}
	icon, tone := kit.SymRouter, k.Success
	switch e.kind {
	case exitReject:
		icon, tone = kit.SymBan, k.Danger
	case exitHijackDNS:
		icon, tone = kit.SymGlobe2, k.AccentText
	case exitUnset:
		icon, tone = kit.SymTriangleAlert, k.Warning
	case exitCoreDefault:
		icon, tone = kit.SymRouter, k.Label3
	case exitNext:
		icon, tone = kit.SymCornerDownRight, k.Label3
	}
	if tested && !taken {
		tone = k.Label4
	}
	n.Children(func() {
		kit.Icon(c, icon, 17).TextColor(tone)
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			kit.Clipped(c, e.label, kit.FontTable, 600)
			if e.detail != "" {
				kit.Clipped(c, "→ "+e.detail, kit.FontCaption, 400).TextColor(k.Label3)
			}
		})
		if lock != nil && taken && e.kind != exitReject {
			tone := k.Success
			if lock.Decision != mitmpolicy.Decrypted {
				tone = k.Label4
			}
			kit.Icon(c, kit.SymLock, 13).TextColor(tone).Label("HTTPS 解密 " + lock.Label())
		}
	})
}

// topologyTestPanel is the test: the sample's inbound, domain, IP and
// added facts, the exits it may leave by, and the conditions still
// unknown on the way, which add as facts where the test has them.
func (w *Workspace) topologyTestPanel(c *ui.Context, doc *model.Doc, g *topoGraph, paths topoPaths) {
	k := kit.T(c)
	t := w.topoTest
	kit.Inspector(c, "测试路径", model.PolicyOwnerName(doc, w.owner), func() {
		ui.Box(c).Size(32, 32).Radius(9).Center().Background(k.AccentWeak).TextColor(k.AccentText).Shrink(0).
			Children(func() { kit.Icon(c, kit.SymPlay, 15) })
	}, nil, func() { w.topoTest = nil }, func() {
		kit.Section(c, "样本连接", nil, func() {
			ui.Column(c).Gap(8).Children(func() {
				inbounds := []kit.Option{{Value: "", Label: "任意进站"}}
				for _, tag := range testInbounds(doc) {
					inbounds = append(inbounds, kit.Option{Value: tag, Label: tag})
				}
				testField(c, "进站", func() {
					kit.Select(c, "inbound", &t.inbound, inbounds, kit.SelectOptions{Label: "进站"}).Grow(1)
				})
				testField(c, "域名", func() { testInput(c, "domain", &t.domain, "api.example.com", "域名") })
				testField(c, "目标 IP", func() { testInput(c, "ip", &t.ip, "203.0.113.10", "目标 IP") })
				for _, key := range slices.Clone(t.added) {
					f := factOf(key)
					if f == nil {
						continue
					}
					testField(c, f.label, func() {
						value := t.facts[key]
						if len(f.options) > 0 {
							options := []kit.Option{{Value: "", Label: "任意"}}
							for _, o := range f.options {
								options = append(options, kit.Option{Value: o, Label: strings.ToUpper(o)})
							}
							kit.Select(c, "fact:"+key, &value, options, kit.SelectOptions{Label: f.label}).Grow(1)
						} else {
							testInput(c, "fact:"+key, &value, f.placeholder, f.label)
						}
						t.facts[key] = value
						kit.IconButton(c.Key("remove:"+key), kit.SymX, "移除"+f.label, kit.IconButtonOptions{Size: 26, IconSize: 13}).
							OnClick(func() { t.removeFact(key) })
					})
				}
				var more []sampleFact
				for _, f := range sampleFacts {
					if !slices.Contains(t.added, f.key) {
						more = append(more, f)
					}
				}
				if len(more) > 0 {
					kit.Button(c, "添加条件", kit.ButtonOptions{Variant: kit.TextButton, Small: true, Icon: kit.SymPlus}).
						PopoverMenuButton(func(menu *ui.Menu) {
							for _, f := range more {
								if menu.Item(f.label).Chosen() {
									t.addFact(f.key)
								}
							}
						})
				}
			})
		})
		if o, ok := mitmOutcome(doc, model.NormalizeDomainName(t.domain), w.untrusted, mitmpolicy.Source{Device: w.owner}, "", false); ok {
			kit.Section(c, "HTTPS 解密", nil, func() { mitmProperty(c, o) })
		}
		exits := paths.exits(g)
		title := "可能出口"
		if len(exits) == 1 {
			title = "唯一出口"
		}
		kit.Section(c, title, func() {
			ui.Text(c, kit.Count(len(exits))).FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum")
		}, func() {
			for _, key := range exits {
				e := g.exit(key)
				if e == nil {
					continue
				}
				via := testVia(g, paths, key)
				kit.PropertyWith(c, e.label, func() {
					kit.Clipped(c, via, kit.FontTable, 400).TextColor(k.Label2).Grow(1)
				})
			}
		})
		if unknown := paths.unknownConditions(g); len(unknown) > 0 {
			kit.Section(c, "待确定条件", nil, func() {
				ui.Row(c).Wrap().Gap(6).Children(func() {
					for _, label := range unknown {
						f := factFor(label)
						chip := ui.ButtonBase(c.Key("unknown:" + label)).Height(24).PaddingX(9).Radius(12).Gap(5).
							FontSize(kit.FontCaption).Background(k.Fill).TextColor(k.Label2).Label(label).Transition(kit.HoverMotion)
						if f == nil || slices.Contains(t.added, f.key) {
							chip.Disabled(true)
						} else if chip.Hovered() {
							chip.Background(k.Fill2)
						}
						chip.Children(func() {
							if f != nil && !slices.Contains(t.added, f.key) {
								kit.Icon(c, kit.SymPlus, 11)
							}
							ui.Text(c, label).SingleLine()
						})
						if f != nil && chip.Clicked() {
							t.addFact(f.key)
						}
					}
				})
			})
		}
	})
}

// testField is a labeled control of the test.
func testField(c *ui.Context, label string, control func()) {
	k := kit.T(c)
	ui.Column(c).Gap(4).Shrink(0).Children(func() {
		ui.Text(c, label).FontSize(kit.FontCaption).TextColor(k.Label3)
		ui.Row(c).Gap(6).FillWidth().Children(control)
	})
}

// testInput is an input of the test as wide as the panel lets it.
func testInput(c *ui.Context, key string, value *string, placeholder, label string) {
	ui.Box(c).Grow(1).MinWidth(0).Children(func() { kit.Input(c, key, value, placeholder).Label(label) })
}

// testVia names the rules by which the sample may leave by exit key, the
// one that surely takes it alone.
func testVia(g *topoGraph, paths topoPaths, key string) string {
	var names []string
	for _, r := range g.rules() {
		if r.exit == key && paths.state[r.id].taking() {
			name := r.name
			if r.number != "" {
				name = r.number + " · " + name
			}
			names = append(names, name)
		}
	}
	return strings.Join(names, "、")
}

// factFor is the fact that decides the condition labeled label, nil for
// none: the fact of the condition's own field first.
func factFor(label string) *sampleFact {
	for i, f := range sampleFacts {
		if model.ConditionLabels[f.key] == label {
			return &sampleFacts[i]
		}
	}
	for i, f := range sampleFacts {
		for _, field := range f.fields {
			if model.ConditionLabels[field] == label {
				return &sampleFacts[i]
			}
		}
	}
	return nil
}

// addFact adds the fact of key to the sample, empty.
func (t *topoTest) addFact(key string) {
	if !slices.Contains(t.added, key) {
		t.added = append(t.added, key)
	}
}

// removeFact takes the fact of key off the sample.
func (t *topoTest) removeFact(key string) {
	t.added = slices.DeleteFunc(t.added, func(k string) bool { return k == key })
	delete(t.facts, key)
}

// topoCurveSteps is how many straight pieces draw a curve of the canvas.
const topoCurveSteps = 32

// topoCurve is the S curve from (x1, y1) to (x2, y2), leaving and coming
// level, as points along it.
func topoCurve(x1, y1, x2, y2 float32) [][2]float32 {
	dx := (x2 - x1) / 2
	pts := make([][2]float32, topoCurveSteps+1)
	for i := range pts {
		t := float32(i) / topoCurveSteps
		u := 1 - t
		a, b, c, d := u*u*u, 3*u*u*t, 3*u*t*t, t*t*t
		pts[i] = [2]float32{a*x1 + b*(x1+dx) + c*(x2-dx) + d*x2, (a+b)*y1 + (c+d)*y2}
	}
	return pts
}
