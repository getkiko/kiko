package endpoints

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// pingPoints is how many latencies the chart keeps.
const pingPoints = 30

// peerDetail is a tailnet device's sheet:
// addresses, network, a continuous ping with its chart, Taildrop and
// SSH.
type peerDetail struct {
	key     string
	running bool
	history []float64
	latest  *api.TailscalePingResult
	err     string
	cancel  context.CancelFunc
	run     int
}

func (d *peerDetail) stop() {
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	d.running = false
	d.run++
}

func (d *peerDetail) reset(key string) {
	d.stop()
	*d = peerDetail{key: key, run: d.run}
}

// startPing streams pings of address until stopped.
func (d *peerDetail) startPing(s *Section, tag, address string) {
	d.stop()
	d.running, d.err = true, ""
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	run := d.run
	e := s.env
	go func() {
		err := e.API.TailscalePing(ctx, tag, address, func(r api.TailscalePingResult) {
			e.Post(func() {
				if run != d.run {
					return
				}
				if r.Error != "" {
					d.err = r.Error
					return
				}
				d.err = ""
				latest := r
				d.latest = &latest
				d.history = append(d.history, r.LatencyMs)
				if len(d.history) > pingPoints {
					d.history = slices.Clone(d.history[len(d.history)-pingPoints:])
				}
			})
		})
		e.Post(func() {
			if run != d.run {
				return
			}
			if err != nil && ctx.Err() == nil {
				d.err = firstOf(err.Error(), "Ping 失败")
			}
			d.running = false
			d.cancel = nil
		})
	}()
}

func (d *peerDetail) view(c *ui.Context, t *tailscaleSheet, tag string, peer api.TailscalePeer, self bool, status *api.TailscaleStatus) {
	k := kit.T(c)
	s := t.s
	key := tag + ":" + t.selection
	if d.key != key {
		d.reset(key)
	}
	var ipv4, ipv6 string
	for _, ip := range peer.TailscaleIPs {
		if strings.Contains(ip, ":") {
			if ipv6 == "" {
				ipv6 = ip
			}
		} else if ipv4 == "" {
			ipv4 = ip
		}
	}
	dataLine := func(label, value string, copyable bool) {
		if value == "" {
			return
		}
		ui.Row(c.Key(label)).MinHeight(44).Gap(12).PaddingY(6).Children(func() {
			ui.Text(c, label).FontSize(kit.FontBody).TextColor(k.Label3).Shrink(0)
			ui.Text(c, value).FontSize(kit.FontBody).Selectable().Wrap().TextAlign(ui.End).Grow(1).MinWidth(0)
			if copyable {
				kit.IconButton(c, kit.SymCopy, "复制 "+label, kit.IconButtonOptions{Size: 28, IconSize: 14}).OnClick(func() {
					s.copy(c, value, label+" 已复制")
				})
			}
		})
	}
	open := true
	kit.Dialog(c, &open, peerName(peer), 420, func() {
		ui.Row(c).Shrink(0).Children(func() {
			ui.Row(c).Gap(8).Radius(kit.RadiusCapsule).Background(k.Fill).Padding(4, 10).FontSize(kit.FontTable).TextColor(k.Label2).Children(func() {
				tone, label := toneIdle, "控制面离线"
				if peer.Online {
					tone, label = toneOK, "控制面在线"
				}
				statusDot(c, tone)
				ui.Text(c, label)
			})
		})
		detailSection(c, "地址", nil, func() {
			card(c, func() {
				dataLine("MagicDNS", peerDNSName(peer), true)
				dataLine("主机名", peer.HostName, true)
				dataLine("IPv4", ipv4, true)
				dataLine("IPv6", ipv6, true)
			})
		})
		if !self && (peer.Online || peer.CurAddr != "") {
			t.diag.keep(s, tag, time.Now())
			t.diag.connectionView(c, peer, dataLine, time.Now())
		}
		if self {
			detailSection(c, "网络", nil, func() {
				card(c, func() {
					dataLine("名称", status.NetworkName, false)
					dataLine("MagicDNS 后缀", status.MagicDNSSuffix, true)
				})
			})
		}
		if !self {
			d.pingView(c, s, tag, peerAddress(peer))
		}
		if !self && peer.CanReceiveFiles && status.CanShareFiles {
			detailSection(c, "Taildrop", nil, func() {
				drop := ui.ButtonBase(c).MinHeight(112).FillWidth().Column().Center().Gap(8).Padding(20).Radius(16).
					Border(1, k.Hairline).Background(k.Surface).TextColor(k.Label3).FontSize(kit.FontBody).Label("拖放文件到这里发送，或点击选择").Transition(kit.HoverMotion)
				dragging := drop.FileDragOver()
				switch {
				case dragging:
					drop.Border(1, k.AccentText).Background(k.AccentWeak).TextColor(k.AccentText)
				case drop.Hovered():
					drop.Background(k.Fill)
				}
				drop.Children(func() {
					kit.Icon(c, kit.SymFileUp, 22)
					ui.Text(c, "拖放文件到这里发送，或点击选择")
				})
				if paths := drop.DroppedFiles(); len(paths) > 0 {
					var uploads []api.TaildropUpload
					for _, path := range paths {
						u, err := api.UploadFile(path)
						if err != nil {
							s.env.Toast(err.Error())
							continue
						}
						uploads = append(uploads, u)
					}
					t.send(peer, uploads)
				}
				if drop.Clicked() {
					e := s.env
					go func() {
						data, name, ok, err := e.Shell.OpenFile()
						e.Post(func() {
							switch {
							case err != nil:
								e.Toast(err.Error())
							case ok:
								t.send(peer, []api.TaildropUpload{api.UploadBytes(name, data)})
							}
						})
					}()
				}
			})
		}
		detailSection(c, "详情", nil, func() {
			card(c, func() {
				dataLine("操作系统", peer.OS, false)
				dataLine("密钥到期", peerTime(peer.KeyExpiry), false)
				if !peer.Online && peer.LastSeen != "" {
					dataLine("最后在线", peerTime(peer.LastSeen), false)
				}
			})
		})
		if !self && len(peer.SSHHostKeys) > 0 {
			kit.Button(c, "通过 SSH 连接", kit.ButtonOptions{Variant: kit.Primary, Icon: kit.SymTerminal}).FillWidth().
				OnClick(func() { t.ssh.open(tag, peer) })
		}
	}, nil)
	if !open {
		d.stop()
		t.selection = ""
	}
}

func (d *peerDetail) pingView(c *ui.Context, s *Section, tag, address string) {
	k := kit.T(c)
	detailSection(c, "Ping", func() {
		icon, label := kit.SymPlay, "开始 Ping"
		if d.running {
			icon, label = kit.SymPause, "暂停 Ping"
		}
		kit.IconButton(c, icon, label, kit.IconButtonOptions{Size: 28, IconSize: 13}).OnClick(func() {
			if d.running {
				d.stop()
			} else {
				d.startPing(s, tag, address)
			}
		})
	}, func() {
		ui.Column(c).Radius(16).Border(1, k.HairlineSoft).Background(k.Surface).PaddingX(14).Shrink(0).Children(func() {
			if d.err != "" {
				ui.Text(c, d.err).FontSize(kit.FontTable).TextColor(k.Danger).PaddingY(12).Role(ui.RoleStatus).Wrap()
			}
			ui.Row(c).PaddingY(12).Gap(12).FontSize(kit.FontBody).Children(func() {
				kind := "等待开始"
				switch {
				case d.latest != nil && d.latest.IsDirect:
					kind = "直连"
				case d.latest != nil && d.latest.PeerRelay != "":
					kind = "设备中继"
				case d.latest != nil:
					kind = "DERP 中继"
				case d.running:
					kind = "正在探测连接…"
				}
				ui.Text(c, kind).TextColor(k.Label3).Grow(1)
				latency := "— ms"
				if d.latest != nil {
					latency = fmt.Sprintf("%.1f ms", d.latest.LatencyMs)
				}
				ui.Text(c, latency).FontFeatures("tnum")
			})
			detail := ""
			if d.latest != nil {
				switch {
				case d.latest.IsDirect:
					detail = d.latest.Endpoint
				case d.latest.PeerRelay != "":
					detail = d.latest.PeerRelay
				default:
					detail = "DERP " + firstOf(d.latest.DERPRegionCode, fmt.Sprint(d.latest.DERPRegionID))
				}
			}
			ui.Text(c, detail).FontSize(kit.FontTable).TextColor(k.Label3).MinHeight(20).PaddingY(0)
			d.chart(c)
		})
	})
}

// chart draws the latest latencies, new samples entering on the right.
func (d *peerDetail) chart(c *ui.Context) {
	k := kit.T(c)
	history := d.history
	color := k.AccentText
	if d.latest != nil && d.latest.IsDirect {
		color = k.Success
	}
	maxValue := 1.0
	for _, v := range history {
		maxValue = max(maxValue, v)
	}
	maxValue *= 1.2
	peak := 0.0
	for _, v := range history {
		peak = max(peak, v)
	}
	ui.Box(c).Height(96).FillWidth().Margin(0, 0, 8).Label(fmt.Sprintf("最近 %d 次 Ping 延迟，单位毫秒", len(history))).
		DrawOver(func(p *ui.Painter, r ui.Rect) {
			// The web chart's 300×80 box, scaled to the width.
			sx, sy := r.W/300, r.H/80
			x := func(v float32) float32 { return r.X + v*sx }
			y := func(v float32) float32 { return r.Y + v*sy }
			for _, gy := range []float32{10, 34, 58} {
				p.Line(x(8), y(gy), x(292), y(gy), 1, k.HairlineSoft)
			}
			slot := float32(284) / (pingPoints - 1)
			points := make([][2]float32, len(history))
			for i, v := range history {
				px := 292 - float32(len(history)-1-i)*slot
				py := 58 - float32(v/maxValue)*48
				points[i] = [2]float32{x(px), y(py)}
			}
			if len(points) > 1 {
				var area ui.Path
				area.MoveTo(points[0][0], y(58))
				for _, pt := range points {
					area.LineTo(pt[0], pt[1])
				}
				area.LineTo(points[len(points)-1][0], y(58)).Close()
				p.FillPath(&area, color.Alpha(0.1))
				p.StrokePolyline(points, 1.8, color)
			}
			for i, pt := range points {
				radius, alpha := float32(1.5), float32(0.55)
				if i == len(points)-1 {
					radius, alpha = 2.4, 1
				}
				var dot ui.Path
				dot.Circle(pt[0], pt[1], radius)
				p.FillPath(&dot, color.Alpha(alpha))
			}
			p.Text(x(8), y(76)-9, "最近 30 次", 9, k.Label3)
			label := "0 ms"
			if len(history) > 0 {
				label = fmt.Sprintf("%.1f ms", peak)
			}
			w, _ := p.MeasureText(0, ui.Span{Text: label, Size: 9})
			p.Text(x(292)-w, y(76)-9, label, 9, k.Label3)
		})
}

// derpLabel names a DERP region code, "" for none.
func derpLabel(region string) string {
	if region == "" {
		return ""
	}
	return "DERP " + region
}
