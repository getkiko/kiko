package subscriptions

import (
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// protocolLabel capitalizes a protocol type for chips, as "Vless".
func protocolLabel(t string) string {
	if t == "" {
		return t
	}
	return strings.ToUpper(t[:1]) + t[1:]
}

// typeLabel is the display name of a protocol type.
func typeLabel(t string) string {
	if label := schema.TypeLabel[t]; label != "" {
		return label
	}
	return t
}

// outboundEndpoint is an outbound card's address: "Built-in outbound" for direct and
// block, the signal address of burrow, server:port otherwise.
func outboundEndpoint(o *model.Outbound) string {
	switch o.Type {
	case "direct", "block":
		return "基础出站"
	case "burrow":
		return jsv.StringOr(o.Fields["signal_address"], "")
	}
	var parts []string
	for _, key := range []string{"server", "server_port"} {
		if s := jsv.StringOr(o.Fields[key], ""); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ":")
}

// targetEndpoint is a group member's address (GroupMemberPicker).
func targetEndpoint(t model.Target) string {
	if t.Endpoint != nil {
		if a := schema.EndpointAddress(t.Endpoint.Type, t.Endpoint.Fields); a != "" {
			return a
		}
		return t.Endpoint.Type
	}
	var parts []string
	for _, key := range []string{"server", "server_port"} {
		if s := jsv.StringOr(t.Outbound.Fields[key], ""); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return t.Outbound.Type
	}
	return strings.Join(parts, ":")
}

// nodeEndpoint is a subscription node's address in the nodes sheet.
func nodeEndpoint(o *model.Outbound) string {
	server := jsv.Text(o.Fields["server"])
	port := jsv.Text(o.Fields["server_port"])
	if server != "" {
		if port != "" {
			return server + ":" + port
		}
		return server
	}
	return jsv.Text(o.Fields["signal_address"])
}

// invalidBadge is the "Needs fix" chip of a card needing a fix.
func invalidBadge(c *ui.Context) { kit.Badge(c, "需修正", kit.DangerTone) }

// cardHeight is the least height of the outbound and group cards: a
// title and a line of detail.
const cardHeight = 72

// cardDetail is a line of detail under a card's title, in Label2, Label3
// while the card is muted.
func cardDetail(c *ui.Context, muted bool, text string) ui.Element {
	k := kit.T(c)
	color := k.Label2
	if muted {
		color = k.Label3
	}
	return ui.Text(c, text).FontSize(kit.FontTable).TextColor(color).FontFeatures("tnum").SingleLine().
		Ellipsis("…").Shrink(1).MinWidth(0)
}

// cardMono is cardDetail for an address.
func cardMono(c *ui.Context, muted bool, text string) ui.Element {
	return cardDetail(c, muted, text).Font(kit.MonoFont)
}

// cardTitle is the title of an outbound or group card: lead (its flag or
// initial), the name and "Needs fix" when invalid.
func cardTitle(c *ui.Context, lead func(), name string, invalid bool) {
	lead()
	kit.HeroName(c, name)
	if invalid {
		invalidBadge(c)
	}
}

// subHeading is the small heading of a group of cards inside a section,
// as Selector and URLTest under Outbounds.
func subHeading(c *ui.Context, title, count string) {
	k := kit.T(c)
	ui.Row(c).MinHeight(32).Gap(8).PaddingX(4).Shrink(0).Children(func() {
		ui.Text(c, title).FontSize(kit.FontSection).FontWeight(600).TextColor(k.Label2).Grow(1).MinWidth(0).SingleLine()
		ui.Text(c, count).FontSize(kit.FontTable).TextColor(k.Label3).FontFeatures("tnum").SingleLine()
	})
}

// fieldLabel is the caption above an editor's own fields.
func fieldLabel(c *ui.Context, text string) ui.Element {
	return ui.Text(c, text).FontSize(kit.FontTable).TextColor(kit.T(c).Label3).SingleLine()
}

// latencyValue is a target's median connect time for sorting, +Inf when
// untested or failed.
func latencyValue(results map[string]api.LatencyResult, id string) float64 {
	if r, ok := results[id]; ok && r.MedianConnectMs != nil {
		return *r.MedianConnectMs
	}
	return math.Inf(1)
}

// measureLatency tests the latency of the outbounds ids, keeping the
// other results, and tells when none of them has a server to test.
func (w *Workspace) measureLatency(ids []string) {
	if !w.store.MeasureLatencyOf(ids) {
		w.env.Toast("没有可测速的节点")
	}
}

// measureBatch tests ids as the batch entry key (Store.MeasureBatch),
// busy while Store.BatchTesting(key).
func (w *Workspace) measureBatch(key string, ids []string) {
	if !w.store.MeasureBatch(key, ids) {
		w.env.Toast("没有可测速的节点")
	}
}

// The batch keys of the page's entries: groupBatch tests all the servers
// under group id (its card's latency test, its sheet's "Test this group", a
// member group's "Test all members"); subBatch the nodes of subscription id (its card's menu,
// its sheet's button); the region headers of a group's sheet and a
// subscription's sheet test region rid of theirs.
func groupBatch(id string) string            { return "components/group:" + id }
func subBatch(id string) string              { return "components/sub:" + id }
func groupRegionBatch(id, rid string) string { return "components/group-region:" + id + "\x00" + rid }
func subRegionBatch(id, rid string) string   { return "components/sub-region:" + id + "\x00" + rid }

// cardConfirm is a confirmation started from a card's more menu, shown
// at that card's more button (HeroCard's result) as last built.
type cardConfirm struct {
	state kit.ConfirmState
	// id is the card's, "" before a confirmation starts; action is what
	// it confirms, for a card offering several.
	id, action string
	// at and title are the card's more button and title as last built.
	at    ui.Element
	title string
}

// start opens the confirmation of action for the card id.
func (p *cardConfirm) start(id, action string) {
	p.id, p.action, p.state = id, action, kit.ConfirmState{Open: true}
}

// showing tells that the card id's confirmation is open, which keeps
// its more button shown (HeroCardOptions.KeepMore).
func (p *cardConfirm) showing(id string) bool { return p.id == id && p.state.Open }

// track records the more button and title of the card id as it is
// built.
func (p *cardConfirm) track(id, title string, more ui.Element) {
	if p.id == id {
		p.at, p.title = more, title
	}
}

// anchor is the more button to show the confirmation at in this pass;
// ok is false before one starts or when its card was not built.
func (p *cardConfirm) anchor() (at ui.Element, ok bool) {
	return p.at, p.id != "" && p.at.Valid()
}
