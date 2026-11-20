package subscriptions

import (
	"fmt"
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/subscription"
)

// subUsage is what a subscription's card and nodes sheet show of its
// traffic and expiry.
type subUsage struct {
	// fraction is the share of the total used, in [0, 1]; bar whether
	// there is a total to draw it against.
	fraction float64
	bar      bool
	// used and total are the traffic used and the total, as "182 GB",
	// total "" without one; traffic is "182 GB / 300 GB", or "Used 3.00
	// GB" without a total; expiry "Expires 2026-12-01 · 32 days" or
	// "Expired".
	used, total     string
	traffic, expiry string
	expired         bool
}

// usageOf is what sub's traffic tells at now: nothing without traffic.
func usageOf(sub *model.Subscription, now time.Time) subUsage {
	var u subUsage
	t := sub.Traffic
	if t == nil {
		return u
	}
	used := t.Upload + t.Download
	if t.Total > 0 {
		u.bar = true
		u.fraction = math.Min(math.Max(used/t.Total, 0), 1)
		u.used, u.total = subscription.FormatTraffic(used), subscription.FormatTraffic(t.Total)
		u.traffic = u.used + " / " + u.total
	} else if used > 0 {
		u.used = subscription.FormatTraffic(used)
		u.traffic = "已用 " + u.used
	}
	if date := subscription.FormatExpireDate(t.Expire); date != "" {
		days := int(math.Ceil((t.Expire - float64(now.Unix())) / 86400))
		if days <= 0 {
			u.expired, u.expiry = true, "已过期"
		} else {
			u.expiry = fmt.Sprintf("到期 %s · %d 天", date, days)
		}
	}
	return u
}

// usageBar is the 4 DIP bar of a subscription's traffic used: accent,
// orange from 80 %, red from 95 % or once expired.
func usageBar(c *ui.Context, u subUsage) ui.Element {
	k := kit.T(c)
	color := k.Accent
	switch {
	case u.fraction >= 0.95 || u.expired:
		color = k.Danger
	case u.fraction >= 0.8:
		color = k.Warning
	}
	f := float32(u.fraction)
	return ui.Row(c).Height(4).Radius(2).Background(k.Fill).Grow(1).MinWidth(0).Clip().Children(func() {
		ui.Box(c).Height(4).Radius(2).Background(color).Grow(f).MinWidth(0)
		ui.Box(c).Height(4).Grow(1 - f).MinWidth(0)
	})
}

// subCard is a subscription's hero card, opening its editor: its state,
// name and node count, which opens the nodes sheet ("N matches" while
// searching); the traffic used over the total, with a bar of their
// share; the expiry, or when it was updated, or "Fetch failed" with the
// whole failure in a hint, and "Update now". Its menu turns it on or off, edits it,
// copies its link and deletes it after a confirmation at the more
// button, which the section's overlays anchor to; it tests its nodes as
// well. The context menu lists the same.
func (s *subsSection) subCard(c *ui.Context, doc *model.Doc, item model.TopLevelItem, hits int) {
	k := kit.T(c)
	sub := item.Subscription
	id, title := sub.ID, sub.Tag
	invalid := model.HasInvalidSubscriptionReferences(doc, sub)
	now := c.Now()
	usage := usageOf(sub, now)
	updated := ""
	if sub.UpdatedAt > 0 {
		updated = "更新于 " + kit.Ago(time.UnixMilli(sub.UpdatedAt), now)
	}
	nodeIDs := make([]string, len(item.Nodes))
	for i, o := range item.Nodes {
		nodeIDs[i] = o.ID
	}
	menu := []kit.EntityMenuItem{{Label: "编辑订阅", Icon: kit.SymPencil, Run: func(ui.Element) {
		s.editor.openEdit(s.w.store.Doc(), id)
	}}}
	if len(nodeIDs) > 0 {
		batch := subBatch(id)
		menu = append(menu, kit.EntityMenuItem{Label: "测速", Icon: kit.SymGauge, Disabled: s.w.store.BatchTesting(batch),
			Run: func(ui.Element) { s.w.measureBatch(batch, nodeIDs) }})
	}
	if url := sub.URL; url != "" {
		menu = append(menu, kit.EntityMenuItem{Label: "复制订阅链接", Icon: kit.SymCopy, Run: func(ui.Element) {
			c.WriteClipboard(url)
			s.w.env.Toast("订阅链接已复制")
		}})
	}
	menu = append(menu, kit.EntityMenuItem{Label: "删除订阅", Icon: kit.SymTrash2, Danger: true, Run: func(ui.Element) {
		s.remove.start(id, "")
	}})
	more := kit.HeroCard(c, id, kit.HeroCardOptions{
		Label: "编辑 " + title,
		Muted: !sub.Enabled,
		Header: func() {
			kit.HeroDot(c, sub.Enabled)
			kit.HeroName(c, title)
			if invalid {
				invalidBadge(c)
			}
		},
		// A chain every node dials through shows as a tag; the more
		// button needs no room kept for it.
		HeaderEnd: func() {
			if len(sub.Chain) > 0 {
				kit.HeroTags(c, true, "")
			}
		},
		Main: func() {
			if usage.used == "" {
				kit.HeroValue(c, "—")
				return
			}
			ui.Column(c).Grow(1).MinWidth(0).Gap(6).AlignItems(ui.Stretch).Children(func() {
				ui.Row(c).Gap(6).MinWidth(0).AlignItems(ui.Baseline).Children(func() {
					kit.HeroValue(c, usage.used)
					if usage.total != "" {
						ui.Text(c, "/ "+usage.total).FontSize(kit.FontBody).FontFeatures("tnum").TextColor(k.Label3).
							SingleLine().Shrink(0)
					}
				})
				if usage.bar {
					usageBar(c, usage)
				}
			})
		},
		Footer: func() {
			switch {
			case sub.LastError != "":
				failure := ui.Row(c).Gap(4).AlignItems(ui.Center).Shrink(1).MinWidth(0).Label(sub.LastError)
				kit.Hint(c, failure, sub.LastError)
				failure.Children(func() {
					kit.Icon(c, kit.SymTriangleAlert, 12).TextColor(k.Danger).Shrink(0)
					ui.Text(c, "拉取失败").SingleLine().Shrink(0)
				})
			case usage.expiry != "":
				t := ui.Text(c, usage.expiry).FontFeatures("tnum").SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
				if usage.expired {
					t.TextColor(k.Danger)
				}
				kit.Hint(c, t, updated)
			default:
				ui.Text(c, updated).FontFeatures("tnum").SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
			}
			ui.Spacer(c)
			count := fmt.Sprintf("%d 节点 ›", len(item.Nodes))
			if hits > 0 {
				count = fmt.Sprintf("命中 %d ›", hits)
			}
			nodes := heroLink(c, count, "查看 "+title+" 的节点", false)
			if s.nodes.sheet.Refocus && s.nodes.id == id {
				s.nodes.sheet.Refocus = false
				nodes.Focus()
			}
			if nodes.Clicked() {
				s.nodes.show(id, s.w.query)
			}
			s.refreshButton(c, sub, 22, 13)
		},
		Enabled: sub.Enabled,
		OnEnabledChange: func(enabled bool) {
			s.w.update(func(d *model.Doc) {
				if x := findSubscription(d, id); x != nil {
					x.Enabled = enabled
				}
			})
		},
		Menu:        menu,
		KeepMore:    s.remove.showing(id),
		ContextMenu: true,
		OnOpen:      func() { s.editor.openEdit(s.w.store.Doc(), id) },
	})
	s.remove.track(id, title, more)
}

// refreshButton is "Update now" of subscription sub (refreshSubscription), a
// spinner while it runs, size DIPs wide with an icon of iconSize.
func (s *subsSection) refreshButton(c *ui.Context, sub *model.Subscription, size, iconSize float32) {
	busy := s.refreshing[sub.ID]
	id := sub.ID
	kit.IconButton(c, kit.SymRefreshCw, "更新 "+sub.Tag, kit.IconButtonOptions{Size: size, IconSize: iconSize, Busy: busy, BusyHint: "更新中"}).
		OnClick(func() { refreshSubscription(s.w, id) })
}
