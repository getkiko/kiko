// Package nodeview is what the views show of outbound nodes: their
// regions as sections, their protocol, subscription and short name, and
// their latency as a tag or a row's speed test. The Overview chooser and the Components page share it.
package nodeview

import (
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Section is the members of a group in one region.
type Section struct {
	ID, Flag, Label string
	Names           []string
}

// preferredRegions lead the regions, in this order, when the group has
// nodes there.
var preferredRegions = []string{"hk", "sg", "us", "jp", "tw", "kr"}

// Sections groups names by the region of each (model.Regions, flag emoji
// first), keeping the group's order within a region: the unrecognized
// first, then Hong Kong, Singapore, the United States, Japan, Taiwan and South Korea, then the table's
// other regions, then the countries of other flags as they appear. table
// is model.Regions of the document, m its matcher.
func Sections(table *model.RegionSettings, m *model.RegionMatcher, names []string) []Section {
	byRegion := map[string][]string{}
	regions := map[string]model.Region{}
	var seen []string
	var other []string
	for _, name := range names {
		r, ok := m.RegionOf(name)
		if !ok {
			other = append(other, name)
			continue
		}
		if _, known := regions[r.ID]; !known {
			regions[r.ID] = r
			seen = append(seen, r.ID)
		}
		byRegion[r.ID] = append(byRegion[r.ID], name)
	}
	var out []Section
	if len(other) > 0 {
		out = append(out, Section{"", "", "未识别", other})
	}
	add := func(id string) {
		if list := byRegion[id]; len(list) > 0 {
			r := regions[id]
			out = append(out, Section{r.ID, r.Flag, model.RegionName(r), list})
			delete(byRegion, id)
		}
	}
	for _, id := range preferredRegions {
		add(id)
	}
	for _, r := range table.Regions {
		add(r.ID)
	}
	for _, id := range seen {
		add(id)
	}
	return out
}

// protocolTags are the short names of outbound types members are tagged
// with.
var protocolTags = map[string]string{
	"vless": "VLESS", "vmess": "VMess", "trojan": "Trojan", "shadowsocks": "SS", "shadowsocksr": "SSR",
	"hysteria": "Hysteria", "hysteria2": "HY2", "tuic": "TUIC", "anytls": "AnyTLS", "naive": "Naive",
	"wireguard": "WireGuard", "ssh": "SSH", "shadowtls": "ShadowTLS", "socks": "SOCKS", "http": "HTTP",
	"snell": "Snell", "tor": "Tor", "direct": "直连", "block": "拒绝", "selector": "手动", "urltest": "最快",
	"fallback": "故障转移", "loadbalance": "负载均衡", "smart": "智能",
}

// ProtocolTag is the short name of outbound type t: its tag, else t in
// upper case.
func ProtocolTag(t string) string {
	if protocol := protocolTags[t]; protocol != "" {
		return protocol
	}
	return strings.ToUpper(t)
}

// Member is what the views show of a member: its name without its
// subscription's prefix (the subscription shows beside it), and its
// protocol and subscription.
type Member struct {
	ID, Name, Protocol, Source string
	Group                      bool
}

// Members describes the configuration's outbounds by tag: their protocol
// and subscription.
func Members(doc *model.Doc) map[string]Member {
	subs := map[string]*model.Subscription{}
	for _, sub := range doc.Subscriptions {
		subs[sub.ID] = sub
	}
	out := map[string]Member{}
	for _, o := range doc.Outbounds {
		if _, taken := out[o.Tag]; taken {
			continue
		}
		m := Member{ID: o.ID, Name: o.Tag, Protocol: ProtocolTag(o.Type), Group: model.IsGroup(o.Type)}
		if sub := subs[o.SubID]; sub != nil {
			m.Source = sub.Tag
			if trimmed := strings.TrimSpace(strings.TrimPrefix(o.Tag, sub.NamePrefix)); trimmed != "" {
				m.Name = trimmed
			}
		}
		out[o.Tag] = m
	}
	return out
}

// GroupInitial is the avatar letter of a group: its first letter or
// digit, past emoji and symbols.
func GroupInitial(name string) string {
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return strings.ToUpper(string(r))
		}
	}
	return "#"
}

// WithoutFlags is s without the flag emoji (pairs of regional indicator
// symbols) it starts with, and the spaces after them; flags further in
// stay.
func WithoutFlags(s string) string {
	runes := []rune(strings.TrimSpace(s))
	i := 0
	for i+1 < len(runes) && isIndicator(runes[i]) && isIndicator(runes[i+1]) {
		i += 2
		for i < len(runes) && unicode.IsSpace(runes[i]) {
			i++
		}
	}
	return string(runes[i:])
}

// BesideFlag is name as it shows after flag: WithoutFlags when flag is
// set, unless nothing else is left.
func BesideFlag(flag, name string) string {
	if flag != "" {
		if plain := WithoutFlags(name); plain != "" {
			return plain
		}
	}
	return name
}

func isIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

// LatencyState is where a member's speed test stands.
type LatencyState int

const (
	// LatencyUntested is no result yet.
	LatencyUntested LatencyState = iota
	// LatencyTesting is a test waiting or running.
	LatencyTesting
	// LatencyMeasured is a result with a time (or without a median).
	LatencyMeasured
	// LatencyTimeout is a test that ran out of time.
	LatencyTimeout
	// LatencyFailed is a test that failed for another reason.
	LatencyFailed
)

// LatencyStateOf is the state of a member with last result r, testing
// telling that a test is under way (which wins over the result).
func LatencyStateOf(r api.LatencyResult, testing bool) LatencyState {
	switch {
	case testing:
		return LatencyTesting
	case r.Error != "":
		if isTimeout(r.Error) {
			return LatencyTimeout
		}
		return LatencyFailed
	case r.SuccessSamples == 0:
		return LatencyUntested
	}
	return LatencyMeasured
}

func isTimeout(message string) bool {
	m := strings.ToLower(message)
	return strings.Contains(m, "timeout") || strings.Contains(m, "deadline") || strings.Contains(m, "超时")
}

// LatencyTag is a member's last speed test as a tag's words and colors
// (LatencyTone): "Timeout" or "Unavailable" in red when it failed; ok is false before
// a test. A time measured without the core (direct, a TCP handshake to
// the server) shows in neutral colors: it is not a request through the
// outbound.
func LatencyTag(k *kit.Tokens, r api.LatencyResult) (text string, fg, bg ui.Color, ok bool) {
	if r.Error == "" && r.SuccessSamples == 0 {
		return "", ui.Color{}, ui.Color{}, false
	}
	if r.Error != "" {
		return failureText(r.Error), k.Danger, k.DangerWeak, true
	}
	if r.MedianConnectMs == nil {
		return "已测量", k.Success, k.SuccessWeak, true
	}
	v := *r.MedianConnectMs
	fg, bg = LatencyTone(k, v)
	if r.Mode == api.LatencyModeDirect {
		fg, bg = k.Label2, k.Fill
	}
	return fmt.Sprintf("%d ms", int(math.Round(v))), fg, bg, true
}

// failureText is the words of a failed test: "Timeout" when it ran out of
// time, else "Unavailable".
func failureText(message string) string {
	if isTimeout(message) {
		return "超时"
	}
	return "不可用"
}

// The running core's states of a group's member (api.SelectorMember)
// that show: down and suspect. up and the others show nothing.
const (
	CoreDown    = "down"
	CoreSuspect = "suspect"
)

// MemberTag is a group member's availability as a tag's words, colors
// and hover text: the app's last speed test r (LatencyTag, its hint
// LatencyHint) when there is one, it being the test the user just ran,
// else the running core's state of the member, coreState: "Unavailable" in red
// while down, "Unconfirmed" in orange while suspect. ok is false for nothing to
// show: untested, and up or unknown in the core.
func MemberTag(k *kit.Tokens, r api.LatencyResult, coreState string) (text string, fg, bg ui.Color, hint string, ok bool) {
	if text, fg, bg, ok := LatencyTag(k, r); ok {
		return text, fg, bg, LatencyHint(r), true
	}
	switch coreState {
	case CoreDown:
		return "不可用", k.Danger, k.DangerWeak, "核心状态：不可用", true
	case CoreSuspect:
		return "待确认", k.Warning, k.WarningWeak, "核心状态：待确认", true
	}
	return "", ui.Color{}, ui.Color{}, "", false
}

// LatencyHint is the hover text of a result's tag: what the time
// measures, or why the test failed. Empty before a test.
func LatencyHint(r api.LatencyResult) string {
	switch {
	case r.Error != "":
		return r.Error
	case r.SuccessSamples == 0:
		return ""
	case r.Mode == api.LatencyModeDirect:
		return "直连探测：到服务器的 TCP 连接耗时，不经代理"
	case r.Mode == api.LatencyModeUnified:
		return "统一延迟：同一连接上第二次 HTTPS 请求耗时，不含握手"
	}
	return "HTTPS 请求耗时：经出站，含握手"
}

// LatencyTone is the colors of a latency of ms, the text's and the
// background's: green under 120 ms, orange under 300, red otherwise.
func LatencyTone(k *kit.Tokens, ms float64) (fg, bg ui.Color) {
	switch {
	case ms < 120:
		return k.Success, k.SuccessWeak
	case ms < 300:
		return k.Warning, k.WarningWeak
	}
	return k.Danger, k.DangerWeak
}

// LatencyLabel is r as a list row's colored text: "86 ms" in its tone
// (LatencyTone), "Measured" without a median, and "Timeout" or "Unavailable" in red.
func LatencyLabel(c *ui.Context, r api.LatencyResult) ui.Element {
	k := kit.T(c)
	text, color := failureText(r.Error), k.Danger
	if r.Error == "" && r.SuccessSamples > 0 {
		text = "已测量"
		if r.MedianConnectMs != nil {
			v := *r.MedianConnectMs
			text = fmt.Sprintf("%d ms", int(math.Round(v)))
			color, _ = LatencyTone(k, v)
			if r.Mode == api.LatencyModeDirect {
				color = k.Label2
			}
		}
	}
	return ui.Text(c, text).FontSize(kit.FontTable).FontWeight(600).FontFeatures("tnum").TextColor(color).
		SingleLine().Shrink(0)
}

// LatencyCapsule shows r as a capsule tag (LatencyTag), nothing before a
// test, and tells whether it showed.
func LatencyCapsule(c *ui.Context, r api.LatencyResult) bool {
	text, fg, bg, ok := LatencyTag(kit.T(c), r)
	if ok {
		kit.CapsuleTag(c, text, fg, bg, ui.Color{})
	}
	return ok
}

// TestingMark is a speed test under way where its result will show: the
// turning spinner (kit.SpinnerIn), 13 DIPs in Label2, as the buttons'
// symbols, its hover text "Testing"; with less motion asked for, "Testing" in
// words in Label2 instead.
func TestingMark(c *ui.Context) ui.Element { return testingMark(c, "") }

// testingMark is TestingMark, its hover text naming the server via
// tested ("Japan · Testing").
func testingMark(c *ui.Context, via string) ui.Element {
	k := kit.T(c)
	hint := viaHint(via, "测速中")
	if c.Preferences().ReduceMotion {
		return kit.Hint(c, ui.Text(c, "测速中").FontSize(kit.FontCaption).TextColor(k.Label2).SingleLine().Shrink(0), hint)
	}
	return kit.Hint(c, kit.SpinnerIn(c, 13, k.Label2).Label("测速中"), hint)
}

// viaHint is hint naming the server via it tells of first: "Japan · hint".
func viaHint(via, hint string) string {
	switch {
	case via == "":
		return hint
	case hint == "":
		return via
	}
	return via + " · " + hint
}

// LatencyBadge is a member's latency tag in its state: TestingMark while
// testing, nothing before a test, else r's tag (LatencyCapsule), with
// the hover text of what the time measures. It is MemberBadge without a
// state in the core.
func LatencyBadge(c *ui.Context, r api.LatencyResult, testing bool) bool {
	return MemberBadge(c, r, testing, "")
}

// MemberBadge is a group member's availability as a tag: TestingMark
// while testing, else MemberTag of the app's last test r and the running
// core's state of the member as a capsule with its hover text, nothing
// when MemberTag has nothing. It tells whether something showed.
func MemberBadge(c *ui.Context, r api.LatencyResult, testing bool, coreState string) bool {
	return ViaBadge(c, r, testing, coreState, "")
}

// ViaBadge is MemberBadge of a member that is itself a group, r and
// testing those of via, the server its choice comes down to
// (Groups.Final): the hover text of the test names via first ("Japan ·
// HTTPS request time...", "Japan · Testing"); that of the core's state, the
// member's own, does not. via "" is MemberBadge.
func ViaBadge(c *ui.Context, r api.LatencyResult, testing bool, coreState, via string) bool {
	if testing {
		testingMark(c, via)
		return true
	}
	k := kit.T(c)
	text, fg, bg, hint, ok := MemberTag(k, r, coreState)
	if !ok {
		return false
	}
	if _, _, _, tested := LatencyTag(k, r); tested {
		hint = viaHint(via, hint)
	}
	box := ui.Row(c).AlignItems(ui.Center).Shrink(0)
	box.Children(func() { kit.CapsuleTag(c, text, fg, bg, ui.Color{}) })
	kit.Hint(c, box, hint)
	return true
}

// LatencyTest is a list row's speed test, the button label names: the
// badge of r (LatencyBadge), waiting while testing, the member's own
// test. Read Clicked on it.
func LatencyTest(c *ui.Context, r api.LatencyResult, label string, testing bool) ui.Element {
	return MemberTest(c, r, label, testing, "")
}

// MemberTest is LatencyTest of a group's member, its badge MemberBadge
// with the running core's state of the member. Before a test, with
// nothing to show, it keeps a place as wide as a symbol, where a gauge
// shows as the pointer rests on it. While testing it takes no clicks
// (kit.BusyCover), its spinner unfaded.
func MemberTest(c *ui.Context, r api.LatencyResult, label string, testing bool, coreState string) ui.Element {
	return ViaTest(c, r, label, testing, coreState, "")
}

// ViaTest is MemberTest of a member that is itself a group, its badge
// ViaBadge: r and testing are those of via, the server its choice comes
// down to, which the button tests.
func ViaTest(c *ui.Context, r api.LatencyResult, label string, testing bool, coreState, via string) ui.Element {
	k := kit.T(c)
	if testing {
		var cover ui.Element
		box := ui.Row(c).Height(24).MinWidth(24).PaddingX(4).AlignItems(ui.Center).Justify(ui.Center).Shrink(0)
		box.Children(func() {
			testingMark(c, via)
			cover = kit.BusyCover(c, label)
		})
		kit.Hint(c, box, viaHint(via, "测速中"))
		return cover
	}
	test := ui.ButtonBase(c).Height(24).MinWidth(24).PaddingX(4).Radius(6).AlignItems(ui.Center).
		Justify(ui.Center).Shrink(0).Label(label)
	hovered := test.Hovered()
	test.Children(func() {
		if !ViaBadge(c, r, false, coreState, via) && hovered {
			kit.Icon(c, kit.SymGauge, 13).TextColor(k.Label3)
		}
	})
	return test
}

// SectionHeader is a region's header in a list of members, height DIPs
// high on background (none when zero), keyed key: the region's label,
// 15 DIPs bold, and its count at the bottom, as one button testing the
// region's servers. measure runs as it is clicked, or its context menu's
// "Test this group" is chosen. Hovered it shows the row hover fill, the pointing
// hand and a gauge at its right end; busy, the test it started under way
// (Store.BatchTesting), the spinner (TestingMark) in its place, taking no
// clicks. Its hover text reads "Test this group".
func SectionHeader(c *ui.Context, key any, label string, count int, height float32, background ui.Color, busy bool, measure func()) ui.Element {
	k := kit.T(c)
	outer := ui.Row(c.Key(key)).Height(height).Padding(4, 0, 0).AlignItems(ui.Stretch).Shrink(0)
	if background != (ui.Color{}) {
		outer.Background(background)
	}
	var head ui.Element
	outer.Children(func() {
		head = ui.ButtonBase(c).Grow(1).MinWidth(0).Gap(7).AlignItems(ui.End).Justify(ui.Start).Padding(0, 10, 7).Radius(10).
			Label("测速本分组 " + label).Cursor(ui.CursorPointer).Transition(kit.HoverMotion)
		switch {
		case head.Pressed() && !busy:
			head.Background(k.Fill2)
		case head.Hovered():
			head.Background(k.RowHover)
		}
		hovered := head.Hovered()
		head.Children(func() {
			ui.Text(c, label).FontSize(15).FontWeight(700).TextColor(k.Label).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
			ui.Text(c, fmt.Sprint(count)).FontSize(kit.FontTable).FontWeight(500).TextColor(k.Label3).
				FontFeatures("tnum").Shrink(0)
			ui.Box(c).Grow(1).MinWidth(0)
			ui.Row(c).Height(16).MinWidth(16).AlignItems(ui.Center).Justify(ui.Center).Shrink(0).Children(func() {
				switch {
				case busy:
					TestingMark(c)
				case hovered:
					kit.Icon(c, kit.SymGauge, 13).TextColor(k.Label2)
				}
			})
		})
		kit.Hint(c, head, "测速本分组")
		head.ContextMenu(func(m *ui.Menu) {
			if m.Item("测速本分组").Disabled(busy).Chosen() {
				measure()
			}
		})
		if head.Clicked() && !busy {
			measure()
		}
	})
	return head
}
