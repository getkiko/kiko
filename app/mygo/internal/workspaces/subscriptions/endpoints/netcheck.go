package endpoints

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// diagnosticsStale is how old diagnostics are read again while shown.
const diagnosticsStale = 5 * time.Second

// diagnostics is a Tailscale endpoint's network check and peer paths
// (`tailscale netcheck` and `tailscale status`), read while the network
// sheet or a device's sheet shows them.
type diagnostics struct {
	tag        string
	data       *api.TailscaleDiagnostics
	err        string
	busy       bool
	refreshing bool
	fetchedAt  time.Time
	run        int
	// open shows the network sheet.
	open bool
}

// reset forgets the diagnostics of another endpoint; whether the sheet
// shows stays.
func (d *diagnostics) reset(tag string) {
	*d = diagnostics{tag: tag, run: d.run + 1, open: d.open}
}

// load reads the diagnostics; refresh runs a new network check first.
func (d *diagnostics) load(s *Section, refresh bool) {
	if d.busy {
		return
	}
	d.busy, d.refreshing = true, refresh
	run, tag, e := d.run, d.tag, s.env
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		data, err := e.API.TailscaleDiagnostics(ctx, tag, refresh)
		e.Post(func() {
			if run != d.run {
				return
			}
			d.busy, d.refreshing, d.fetchedAt = false, false, time.Now()
			if err != nil {
				d.err = firstOf(err.Error(), "读取失败")
				return
			}
			d.err, d.data = "", &data
		})
	}()
}

// keep reads the diagnostics of tag when missing or stale.
func (d *diagnostics) keep(s *Section, tag string, now time.Time) {
	if d.tag != tag {
		d.reset(tag)
	}
	if !d.busy && (d.fetchedAt.IsZero() || now.Sub(d.fetchedAt) >= diagnosticsStale) {
		d.load(s, false)
	}
}

// peer is the diagnostics of the peer with stableID.
func (d *diagnostics) peer(stableID string) *api.TailscalePeerDiagnostic {
	if d.data == nil || stableID == "" {
		return nil
	}
	for i := range d.data.Peers {
		if d.data.Peers[i].StableID == stableID {
			return &d.data.Peers[i]
		}
	}
	return nil
}

// relativeTime is how long before now a Unix millisecond time was.
func relativeTime(ms int64, now time.Time) string {
	if ms <= 0 {
		return ""
	}
	d := now.Sub(time.UnixMilli(ms))
	switch {
	case d < 5*time.Second:
		return "刚刚"
	case d < time.Minute:
		return fmt.Sprintf("%d 秒前", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	}
	return timefmt.FormatMilli(ms, "2006-01-02 15:04")
}

// triState reads a "true" / "false" / "" field.
func triState(value string) (set, yes bool) {
	return value != "", value == "true"
}

// natLabel describes how the NAT maps this node's UDP.
func natLabel(n *api.TailscaleNetcheck) (string, stateTone) {
	set, varies := triState(n.MappingVariesByDestIP)
	switch {
	case !set:
		return "", toneIdle
	case varies:
		return "随目的地变化", toneWarn
	}
	return "固定映射", toneOK
}

// portMappings lists the port mapping protocols found on the LAN: "None"
// (in Chinese) when none, "" when none was checked.
func portMappings(n *api.TailscaleNetcheck) string {
	var found []string
	checked := false
	for _, p := range []struct{ name, value string }{{"UPnP", n.UPnP}, {"NAT-PMP", n.PMP}, {"PCP", n.PCP}} {
		set, yes := triState(p.value)
		checked = checked || set
		if yes {
			found = append(found, p.name)
		}
	}
	switch {
	case len(found) > 0:
		return strings.Join(found, "、")
	case checked:
		return "无"
	}
	return ""
}

// peerRoute is how a peer is reached now: its path label and where.
func peerRoute(peer api.TailscalePeer, diag *api.TailscalePeerDiagnostic) (label, where string) {
	switch {
	case diag != nil && diag.PeerRelay != "":
		return "对端中继", diag.PeerRelay
	case peer.CurAddr != "":
		label = "直连"
		if peer.UDPPath != "" {
			label = udpPathLabel(peer.UDPPath)
		}
		return label, peer.CurAddr
	case peer.Relay != "":
		return "DERP", derpLabel(peer.Relay)
	}
	return "", ""
}

// changeAddress is the address of a path change value: "ip:port" of
// magicsock's "ip:port@latency+mtu".
func changeAddress(value string) (address, latency string) {
	address, rest, ok := strings.Cut(value, "@")
	if !ok {
		return value, ""
	}
	text, _, _ := strings.Cut(rest, "+")
	if d, err := time.ParseDuration(text); err == nil && d > 0 {
		latency = fmt.Sprintf("%.0f ms", float64(d)/float64(time.Millisecond))
	}
	return address, latency
}

// describeChange names a path change and its detail.
func describeChange(change api.TailscalePathChange) (string, string) {
	what := change.What
	switch {
	case what == "handlePongConnLocked-bestAddr-update":
		address, latency := changeAddress(change.To)
		return "切换路径", strings.TrimSpace(address + " " + latency)
	case strings.HasPrefix(what, "deleteEndpointLocked-bestAddr-"):
		address, _ := changeAddress(change.From)
		return "当前路径失效", address
	case strings.HasPrefix(what, "deleteEndpointLocked-"):
		return "移除候选地址", change.From
	case what == "updateFromNode-new-Endpoints":
		return "更新候选地址", strings.Trim(change.To, "[]")
	case what == "handleCallMeMaybe-new-endpoints":
		return "对端请求打洞", strings.Trim(change.To, "[]")
	case what == "updateFromNode-DERP":
		return "更换 DERP 区域", change.To
	case what == "updateFromNode-remove-DERP":
		return "移除 DERP 区域", change.From
	case strings.HasSuffix(what, "resetLocked"):
		return "重置连接", ""
	}
	return what, strings.TrimSpace(change.From + " " + change.To)
}

// view is the network sheet: this node's network check, the DERP regions
// and how each online device is reached.
func (d *diagnostics) view(c *ui.Context, t *tailscaleSheet, tag string, status *api.TailscaleStatus) {
	k := kit.T(c)
	now := time.Now()
	d.keep(t.s, tag, now)
	open := true
	kit.Dialog(c, &open, "网络诊断", 520, func() {
		ui.Row(c).Gap(8).Shrink(0).Children(func() {
			checked := ""
			if d.data != nil && d.data.Netcheck != nil && d.data.Netcheck.CheckedAt > 0 {
				checked = "检测于 " + timefmt.FormatMilli(int64(d.data.Netcheck.CheckedAt), "15:04:05")
			}
			ui.Text(c, checked).FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum").Grow(1)
			label := "重新检测"
			if d.refreshing {
				label = "检测中…"
			}
			kit.Button(c, label, kit.ButtonOptions{Small: true, Icon: kit.SymRotateCcw, Disabled: d.busy}).OnClick(func() {
				d.load(t.s, true)
			})
		})
		if d.err != "" {
			note(c, kit.DangerTone, d.err)
		}
		if d.data == nil {
			if d.err == "" {
				ui.Text(c, "读取中…").FontSize(kit.FontBody).TextColor(k.Label3)
			}
			return
		}
		if n := d.data.Netcheck; n != nil {
			d.networkView(c, n)
			d.regionsView(c, n)
		}
		d.peersView(c, t, status, now)
	}, nil)
	if !open {
		d.open = false
	}
}

func (d *diagnostics) networkView(c *ui.Context, n *api.TailscaleNetcheck) {
	k := kit.T(c)
	detailSection(c, "本机网络", nil, func() {
		card(c, func() {
			infoRow(c, "UDP", func() {
				tone, label := toneOK, "可用"
				if !n.UDP {
					tone, label = toneError, "不可用"
				}
				if n.CheckedAt == 0 {
					tone, label = toneIdle, "尚未检测"
				}
				statusDot(c, tone)
				ui.Text(c, label)
			})
			if n.STUNPath != "" {
				infoText(c, "检测路径", udpPathLabel(n.STUNPath))
			}
			ipv4 := n.GlobalV4
			if ipv4 == "" && n.CheckedAt > 0 {
				ipv4 = "不可用"
			}
			if ipv4 != "" {
				infoText(c, "公网 IPv4", ipv4)
			}
			ipv6 := n.GlobalV6
			switch {
			case ipv6 != "":
			case n.CheckedAt > 0 && !n.OSHasIPv6:
				ipv6 = "本机没有 IPv6"
			case n.CheckedAt > 0:
				ipv6 = "不可用"
			}
			if ipv6 != "" {
				infoText(c, "公网 IPv6", ipv6)
			}
			if label, tone := natLabel(n); label != "" {
				infoRow(c, "NAT 映射", func() {
					statusDot(c, tone)
					ui.Text(c, label)
				})
			}
			if mappings := portMappings(n); mappings != "" {
				infoText(c, "端口映射", mappings)
			}
			if _, captive := triState(n.CaptivePortal); captive {
				infoRow(c, "强制门户", func() {
					statusDot(c, toneWarn)
					ui.Text(c, "检测到")
				})
			}
			if len(n.SelfEndpoints) > 0 {
				infoRow(c, "通告地址", func() {
					ui.Column(c).Gap(2).AlignItems(ui.End).MinWidth(0).Children(func() {
						for _, address := range n.SelfEndpoints {
							ui.Text(c.Key(address), address).Font(kit.MonoFont).FontSize(kit.FontTable).Selectable().TextColor(k.Label2)
						}
					})
				})
			}
		})
	})
}

func (d *diagnostics) regionsView(c *ui.Context, n *api.TailscaleNetcheck) {
	if len(n.Regions) == 0 {
		return
	}
	k := kit.T(c)
	detailSection(c, "DERP 中继", nil, func() {
		listCard(c, func() {
			for _, region := range n.Regions {
				preferred := region.ID == n.PreferredDERP
				ui.Row(c.Key(region.ID)).MinHeight(40).PaddingX(14).Gap(8).Shrink(0).Children(func() {
					name := ui.Text(c, firstOf(region.Name, region.Code)).FontSize(kit.FontBody).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
					if preferred {
						name.FontWeight(600)
					}
					ui.Text(c, region.Code).FontSize(kit.FontCaption).TextColor(k.Label3).Shrink(0)
					ui.Spacer(c)
					latency := "—"
					if region.LatencyMs > 0 {
						latency = fmt.Sprintf("%.0f ms", region.LatencyMs)
					}
					ui.Text(c, latency).FontSize(kit.FontBody).FontFeatures("tnum").TextColor(k.Label2).Shrink(0)
					if preferred {
						kit.Icon(c, kit.SymCheck, 14).TextColor(k.AccentText).Label("首选 DERP")
					} else {
						ui.Box(c).Size(14, 14).Shrink(0)
					}
				})
			}
		})
	})
}

func (d *diagnostics) peersView(c *ui.Context, t *tailscaleSheet, status *api.TailscaleStatus, now time.Time) {
	if status == nil {
		return
	}
	k := kit.T(c)
	var online []api.TailscalePeer
	for _, group := range status.UserGroups {
		for _, peer := range group.Peers {
			if peer.Online {
				online = append(online, peer)
			}
		}
	}
	if len(online) == 0 {
		return
	}
	detailSection(c, "设备连接", nil, func() {
		listCard(c, func() {
			for _, peer := range online {
				key := peerKey(peer)
				diag := d.peer(peer.StableID)
				label, where := peerRoute(peer, diag)
				row := ui.ButtonBase(c.Key(key)).MinHeight(52).PaddingX(14).Gap(10).FillWidth().Justify(ui.Start).Label(peerName(peer))
				row.Children(func() {
					tone := toneOK
					if label == "DERP" || label == "" {
						tone = toneWarn
					}
					statusDot(c, tone)
					ui.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, peerName(peer)).FontSize(kit.FontBody).SingleLine().Ellipsis("…")
						if where != "" {
							ui.Text(c, where).Font(kit.MonoFont).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine().Ellipsis("…")
						}
					})
					if diag != nil && diag.LastHandshake > 0 {
						ui.Text(c, relativeTime(int64(diag.LastHandshake), now)).FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum").Shrink(0)
					}
					if label != "" {
						kit.Badge(c, label, kit.Neutral)
					}
					kit.Icon(c, kit.SymChevronRight, 14).TextColor(k.Label3)
				})
				if row.Clicked() {
					t.selection = key
				}
			}
		})
	})
}

// connectionView is a device's connection: how it is reached, its
// handshake and traffic, its candidates with their paths, and its recent
// path changes.
func (d *diagnostics) connectionView(c *ui.Context, peer api.TailscalePeer, line func(label, value string, copyable bool), now time.Time) {
	k := kit.T(c)
	diag := d.peer(peer.StableID)
	label, where := peerRoute(peer, diag)
	detailSection(c, "连接", nil, func() {
		card(c, func() {
			line("路径", label, false)
			if label == "DERP" {
				line("当前中继", where, false)
			} else {
				line("当前地址", where, where != "")
			}
			if peer.CurAddr != "" || (diag != nil && diag.PeerRelay != "") {
				line("备用中继", derpLabel(peer.Relay), false)
			}
			if diag != nil {
				line("最近握手", relativeTime(int64(diag.LastHandshake), now), false)
			}
			if peer.RxBytes > 0 || peer.TxBytes > 0 {
				line("收 / 发", formatBytes(int64(peer.RxBytes))+" / "+formatBytes(int64(peer.TxBytes)), false)
			}
		})
	})
	if diag == nil {
		return
	}
	if len(diag.Endpoints) > 0 {
		detailSection(c, "候选地址", nil, func() {
			listCard(c, func() {
				for _, endpoint := range diag.Endpoints {
					ui.Row(c.Key(endpoint.Address)).MinHeight(40).PaddingX(14).Gap(8).Shrink(0).Children(func() {
						ui.Text(c, endpoint.Address).Font(kit.MonoFont).FontSize(kit.FontTable).Selectable().Grow(1).MinWidth(0).
							SingleLine().Ellipsis("…")
						if endpoint.Address == peer.CurAddr {
							kit.Icon(c, kit.SymCheck, 14).TextColor(k.AccentText).Label("当前地址")
						}
						if endpoint.Path != "" {
							udpPathBadge(c, endpoint.Path)
						}
					})
				}
			})
		})
	}
	if len(diag.Changes) > 0 {
		detailSection(c, "路径变化", nil, func() {
			listCard(c, func() {
				for i, change := range diag.Changes {
					title, detail := describeChange(change)
					ui.Row(c.Key(i)).MinHeight(44).PaddingX(14).PaddingY(6).Gap(10).Shrink(0).Children(func() {
						ui.Text(c, timefmt.FormatMilli(int64(change.At), "15:04:05")).FontSize(kit.FontTable).FontFeatures("tnum").
							TextColor(k.Label3).Shrink(0)
						ui.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
							ui.Text(c, title).FontSize(kit.FontBody)
							if detail != "" {
								ui.Text(c, detail).Font(kit.MonoFont).FontSize(kit.FontCaption).TextColor(k.Label3).Selectable().Wrap()
							}
						})
					})
				}
			})
		})
	}
}
