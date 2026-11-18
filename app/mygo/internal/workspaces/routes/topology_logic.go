package routes

import (
	"slices"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// The traffic policy as a topology, left to right: the device the
// connections come from, the rules they are matched against in order,
// and the exits the rules send them to. A test marks the paths a sample
// connection may take (explore).

// topoRule is a rule node: a traffic rule, or the device's default.
type topoRule struct {
	id, name, summary string
	// number is the rule's position as the table numbers it, "P" before
	// it for Pre-rules' rules in a device's topology.
	number           string
	pre, final       bool
	enabled, invalid bool
	action           string
	// exit is the key of the exit the rule sends connections to, "" for
	// Sniff and Resolve, after which matching goes on.
	exit string
	// hitKey names the rule's hit light ("" for none).
	hitKey string
}

// topoBand is a rule group: its title and rules, in order.
type topoBand struct {
	title string
	rules []topoRule
}

// exitKind is what an exit does with connections.
type exitKind int

const (
	exitOutbound exitKind = iota
	exitReject
	exitHijackDNS
	exitUnset
	exitCoreDefault
	exitNext
)

// topoExit is an exit node: an outbound, with the members of the groups
// it goes through, or what a rule without one does.
type topoExit struct {
	key, label, detail string
	kind               exitKind
}

// topoGraph is a device's topology.
type topoGraph struct {
	owner string
	bands []topoBand
	final topoRule
	exits []topoExit
}

// rules lists the rule nodes in matching order, the default last.
func (g *topoGraph) rules() []topoRule {
	var out []topoRule
	for _, b := range g.bands {
		out = append(out, b.rules...)
	}
	return append(out, g.final)
}

// exit is the exit of key, nil for none.
func (g *topoGraph) exit(key string) *topoExit {
	for i := range g.exits {
		if g.exits[i].key == key {
			return &g.exits[i]
		}
	}
	return nil
}

// buildTopology is the topology of owner's traffic policy: Pre-rules'
// rules, then the owner's, then its default. chain is outboundChain of an
// outbound, for the groups an exit goes through.
func buildTopology(doc *model.Doc, owner string, chain func(id string) string) *topoGraph {
	g := &topoGraph{owner: owner}
	addExit := func(e topoExit) string {
		if g.exit(e.key) == nil {
			g.exits = append(g.exits, e)
		}
		return e.key
	}
	outboundExit := func(id string) string {
		tag := refTag(doc, id)
		if id == "" || tag == "" {
			return addExit(topoExit{key: "unset", label: "请选择出口", kind: exitUnset})
		}
		detail := ""
		if parts := strings.Split(chain(id), " → "); len(parts) > 1 {
			detail = strings.Join(parts[1:], " → ")
		}
		return addExit(topoExit{key: "out:" + id, label: tag, detail: detail, kind: exitOutbound})
	}
	ruleExit := func(r *model.RouteRule) string {
		switch r.Action {
		case model.ActionRoute:
			return outboundExit(r.Outbound.ID())
		case model.ActionReject:
			return addExit(topoExit{key: "reject", label: "拒绝连接", kind: exitReject})
		case model.ActionHijackDNS:
			return addExit(topoExit{key: "hijack-dns", label: "DNS 接管", kind: exitHijackDNS})
		}
		return ""
	}
	owners := []string{owner}
	if owner != model.PreDeviceID {
		owners = []string{model.PreDeviceID, owner}
	}
	for _, o := range owners {
		pre := o == model.PreDeviceID && owner != model.PreDeviceID
		groups := model.RouteGroupsOf(doc, o)
		ordinal := model.RouteRuleOrdinals(doc, o)
		for i, group := range groups {
			band := topoBand{title: group.Name}
			if pre {
				band.title = "前置规则 · " + group.Name
			}
			for _, r := range doc.Route.Rules {
				if r.PolicyOwner() != o || !(r.GroupID == group.ID || r.GroupID == "" && i == 0) {
					continue
				}
				number := strconv.Itoa(ordinal[r.ID])
				if pre {
					number = "P" + number
				}
				band.rules = append(band.rules, topoRule{
					id: r.ID, name: displayName(r.Name), summary: model.RuleSummary(r.Match, r.ConditionTree, r.Logic),
					number: number, pre: pre, enabled: r.Enabled, invalid: model.HasInvalidRuleReferences(doc, r),
					action: r.Action, exit: ruleExit(r), hitKey: r.ID,
				})
			}
			if len(band.rules) > 0 {
				g.bands = append(g.bands, band)
			}
		}
		if o == model.PreDeviceID {
			g.bands = append(g.bands, moduleBands(doc, pre, ruleExit)...)
		}
	}
	g.final = topoRule{id: "final", final: true, enabled: true, action: model.ActionRoute}
	if owner == model.PreDeviceID {
		g.final.name, g.final.summary = "其余连接", "继续匹配设备规则"
		g.final.exit = addExit(topoExit{key: "next", label: "设备规则", kind: exitNext})
		return g
	}
	g.final.name, g.final.summary, g.final.hitKey = "默认出口", "此设备的其余连接", finalHitKey(doc, owner)
	if owner == model.OtherDeviceID {
		g.final.summary = "其他设备的其余连接"
	}
	g.final.number = strconv.Itoa(len(model.RouteRuleOrdinals(doc, owner)) + 1)
	switch id, core := defaultRouteOutbound(doc, owner); {
	case id == "" && owner != model.LocalDeviceID && owner != model.OtherDeviceID:
		// A device's default is its own to choose; the core has none for it.
		g.final.exit = addExit(topoExit{key: "unset", label: "请选择出口", kind: exitUnset})
	case id == "":
		g.final.exit = addExit(topoExit{key: "core", label: "核心默认", kind: exitCoreDefault})
	case core:
		g.final.exit = outboundExit(id)
		if e := g.exit(g.final.exit); e != nil && e.kind == exitOutbound && e.detail == "" {
			e.detail = "核心默认"
		}
	default:
		g.final.exit = outboundExit(id)
	}
	return g
}

// pathState is how a test's sample meets a rule node.
type pathState int

const (
	// pathOff: no sample is being tested.
	pathOff pathState = iota
	// pathSkipped: the rule is off, its conditions miss, or an earlier
	// rule takes every connection the sample may be.
	pathSkipped
	// pathPassed: the sample matches a rule that matches on (Sniff, Resolve),
	// or may.
	pathPassed
	// pathPossible: the rule takes the sample if its unknown conditions
	// match, or if those of earlier rules miss.
	pathPossible
	// pathDecided: the rule takes the sample whatever is unknown.
	pathDecided
)

// taking reports whether the state may send the sample to the rule's exit.
func (s pathState) taking() bool { return s == pathPossible || s == pathDecided }

// reaching reports whether the sample may come to the rule.
func (s pathState) reaching() bool { return s.taking() || s == pathPassed }

// topoPaths are the paths of a sample through a topology: each rule
// node's state by ID ("final" the default's), and the unknown conditions
// of the rules that may take it, by rule ID.
type topoPaths struct {
	state   map[string]pathState
	unknown map[string][]string
}

// exits are the keys of the exits the sample may leave by, in the
// topology's order.
func (p topoPaths) exits(g *topoGraph) []string {
	taken := map[string]bool{}
	for _, r := range g.rules() {
		if p.state[r.id].taking() && r.exit != "" {
			taken[r.exit] = true
		}
	}
	var out []string
	for _, e := range g.exits {
		if taken[e.key] {
			out = append(out, e.key)
		}
	}
	return out
}

// unknownConditions are the conditions the rules that may take the sample
// cannot decide, each once, in rule order.
func (p topoPaths) unknownConditions(g *topoGraph) []string {
	var out []string
	for _, r := range g.rules() {
		for _, u := range p.unknown[r.id] {
			if !slices.Contains(out, u) {
				out = append(out, u)
			}
		}
	}
	return out
}

// explore follows a sample through the device's traffic policy. A rule
// whose conditions the sample may or may not meet forks the paths: the
// sample may leave by it, or go on. The first rule it certainly meets
// takes every path still going; the default takes those no rule takes.
func explore(doc *model.Doc, in policyTestInput) topoPaths {
	p := topoPaths{state: map[string]pathState{}, unknown: map[string][]string{}}
	rules, _ := orderedRoutePolicy(doc, in.deviceID)
	going, certain := true, true
	for _, r := range rules {
		if !going || !r.enabled {
			p.state[r.id] = pathSkipped
			continue
		}
		m := evaluateRule(r.tree, r.catchAll, doc, in, false)
		onward := r.action == model.ActionSniff || r.action == model.ActionResolve
		switch {
		case m.state == stateMiss:
			p.state[r.id] = pathSkipped
		case onward:
			p.state[r.id] = pathPassed
		case m.state == stateUnknown:
			p.state[r.id], p.unknown[r.id], certain = pathPossible, m.unknown, false
		case certain:
			p.state[r.id], going = pathDecided, false
		default:
			p.state[r.id], going = pathPossible, false
		}
	}
	switch {
	case !going:
		p.state["final"] = pathSkipped
	case certain:
		p.state["final"] = pathDecided
	default:
		p.state["final"] = pathPossible
	}
	return p
}

// Topology layout, in DIPs from the canvas's top left.
const (
	topoPad       = 36
	topoEntryW    = 172
	topoEntryH    = 64
	topoRuleW     = 268
	topoRuleH     = 46
	topoRuleGap   = 8
	topoBandTitle = 26
	topoBandGap   = 14
	topoExitW     = 216
	topoExitH     = 50
	topoExitGap   = 16
	topoMaxGap    = 240
	// topoTightGap and topoTightScale are how far the gaps and the nodes'
	// widths shrink in a narrow canvas before it scrolls sideways.
	topoTightGap   = 56
	topoTightScale = 0.75
	topoDot        = 20
)

// topoBox is a node's box.
type topoBox struct{ x, y, w, h float32 }

func (b topoBox) left() (float32, float32)  { return b.x, b.y + b.h/2 }
func (b topoBox) right() (float32, float32) { return b.x + b.w, b.y + b.h/2 }

// topoLayout places a topology's nodes.
type topoLayout struct {
	w, h   float32
	entry  topoBox
	rules  map[string]topoBox
	titles []topoTitle
	exits  map[string]topoBox
}

// topoTitle is a band's title, above its rules.
type topoTitle struct {
	text    string
	x, y, w float32
}

// layoutTopology places g's nodes to fit a view width wide when it can: the
// entry in the middle of the rules' height, the rules in a column with
// their bands' titles, and each exit level with the rules that lead to it
// as far as the exits above it let.
func layoutTopology(g *topoGraph, width float32) topoLayout {
	l := topoLayout{rules: map[string]topoBox{}, exits: map[string]topoBox{}}
	// A narrow canvas narrows the gaps, then the nodes, then scrolls.
	entryW, ruleW, exitW := float32(topoEntryW), float32(topoRuleW), float32(topoExitW)
	nodes := entryW + ruleW + exitW
	gap := (width - 2*topoPad - nodes) / 2
	if gap < topoTightGap {
		scale := min(max((width-2*topoPad-2*topoTightGap)/nodes, topoTightScale), 1)
		entryW, ruleW, exitW = entryW*scale, ruleW*scale, exitW*scale
	}
	gap = min(max((width-2*topoPad-entryW-ruleW-exitW)/2, topoTightGap), topoMaxGap)
	ruleX := topoPad + entryW + gap
	exitX := ruleX + ruleW + gap
	y := float32(topoPad)
	for i, b := range g.bands {
		if i > 0 {
			y += topoBandGap
		}
		l.titles = append(l.titles, topoTitle{text: b.title, x: ruleX, y: y, w: ruleW})
		y += topoBandTitle
		for _, r := range b.rules {
			l.rules[r.id] = topoBox{ruleX, y, ruleW, topoRuleH}
			y += topoRuleH + topoRuleGap
		}
	}
	if len(g.bands) > 0 {
		y += topoBandGap
	}
	l.rules[g.final.id] = topoBox{ruleX, y, ruleW, topoRuleH}
	bottom := y + topoRuleH
	l.entry = topoBox{topoPad, topoPad + (bottom-topoPad-topoEntryH)/2, entryW, topoEntryH}

	// Each exit wants the middle of its rules; in that order, each goes
	// below the one above it.
	type want struct {
		key string
		y   float32
	}
	var wants []want
	for _, e := range g.exits {
		var sum, n float32
		for _, r := range g.rules() {
			if r.exit == e.key {
				sum += l.rules[r.id].y + topoRuleH/2
				n++
			}
		}
		if n > 0 {
			wants = append(wants, want{e.key, sum/n - topoExitH/2})
		}
	}
	slices.SortStableFunc(wants, func(a, b want) int {
		switch {
		case a.y < b.y:
			return -1
		case a.y > b.y:
			return 1
		}
		return 0
	})
	next := float32(topoPad)
	for _, w := range wants {
		at := max(w.y, next)
		l.exits[w.key] = topoBox{exitX, at, exitW, topoExitH}
		next = at + topoExitH + topoExitGap
	}
	// Exits pushed past the rules move up as far as they can, together.
	if over := next - topoExitGap - bottom; over > 0 && len(wants) > 0 {
		first := l.exits[wants[0].key].y
		shift := min(over, first-topoPad)
		for _, w := range wants {
			b := l.exits[w.key]
			b.y -= shift
			l.exits[w.key] = b
		}
		next -= shift
	}
	l.w = exitX + exitW + topoPad
	l.h = max(bottom, next-topoExitGap) + topoPad
	return l
}
