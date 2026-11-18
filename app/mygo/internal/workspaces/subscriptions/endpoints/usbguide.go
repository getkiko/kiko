package endpoints

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// guideView is the connection guide in a service's editor: the path, address help, the checklist and usage
// notes.
type guideView struct {
	pathOpen   bool
	pathShown  string
	moreOpen   bool
	usageOpen  bool
	peerPicker bool
}

func (g *guideView) view(c *ui.Context, e *editor, doc *model.Doc, service *model.Service) {
	k := kit.T(c)
	s := e.s
	isServer := service.Type == "usbip-server"
	path := detectPath(doc, service)
	info := pathInfo(path)
	if g.pathShown != path {
		g.pathShown, g.pathOpen = path, true
	}
	ctx := s.rt.guideContext()
	items := sortItems(analyzeService(doc, service, ctx))
	var important, others []guideItem
	for _, item := range items {
		if item.Level == levelError || item.Level == levelWarn {
			important = append(important, item)
		} else {
			others = append(others, item)
		}
	}
	enabledTS := tailscaleEndpoints(doc, true)
	portKey := "server_port"
	if isServer {
		portKey = "listen_port"
	}
	port := text(service.Fields[portKey])
	if port == "" {
		port = "3240"
	}
	detourID := text(service.Fields["detour"])
	var detourEndpoint *model.Endpoint
	for _, ep := range doc.Endpoints {
		if ep.ID == detourID && ep.Type == "tailscale" {
			detourEndpoint = ep
		}
	}
	var addresses []addressOption
	if isServer {
		addresses = serverAddressOptions(doc, service, ctx)
	}
	patch := func(p map[string]any) {
		for key, v := range p {
			service.Fields[key] = v
		}
	}
	choosePath := func(next string) {
		if next == path && service.Fields["guide_path"] != nil && service.Fields["guide_path"] != "" {
			return
		}
		endpointID := ""
		if next == pathTailscale && len(enabledTS) == 0 {
			if existing := model.TailscaleEndpoint(doc); existing != nil {
				endpointID = existing.ID
			} else {
				endpointID = e.addPendingTailscale(doc, isServer)
				doc = e.editDoc(s.store.Doc())
			}
		}
		patch(applyPath(doc, service.Type, next, endpointID))
	}
	copied := func() { s.env.Toast("已复制命令") }

	ui.Column(c.Key("usbip-guide")).Gap(12).Margin(0, 0, 8).Shrink(0).Children(func() {
		side := "这是使用设备一侧（Client）：在需要挂载远程 USB 设备的那台机器上使用这份配置。"
		if isServer {
			side = "这是共享设备一侧（Server）：在插着 USB 设备的那台机器上使用这份配置。"
		}
		note(c, kit.Neutral, side+"Server 和 Client 是两台不同的机器，各自配置。")

		ui.Column(c).Gap(8).Shrink(0).Children(func() {
			subHeading(c, "连接途径", "", nil)
			selected := 0
			for i, p := range usbIPPaths {
				if p.ID == path {
					selected = i
				}
			}
			segments := make([]kit.Segment, len(usbIPPaths))
			for i, p := range usbIPPaths {
				segments[i] = kit.Segment{Label: p.Short}
			}
			if kit.Segmented(c, "path", &selected, segments...).Label("连接途径").Changed() {
				choosePath(usbIPPaths[selected].ID)
			}
			sideName := "Client"
			if isServer {
				sideName = "Server"
			}
			collapsible(c, info.Title+"："+sideName+" 这一侧要做什么", nil, &g.pathOpen, func() {
				ui.Text(c, info.Summary).Wrap().TextColor(k.Label2)
				steps := info.Client
				if isServer {
					steps = info.Server
				}
				bulletList(c, steps, kit.Neutral)
				ui.Grid(c).Columns(2).Gap(12).Margin(6, 0, 0).Children(func() {
					ui.Column(c).Gap(4).MinWidth(0).Children(func() {
						ui.Text(c, "优点").FontSize(kit.FontTable).TextColor(k.Label3)
						bulletList(c, info.Pros, kit.SuccessTone)
					})
					ui.Column(c).Gap(4).MinWidth(0).Children(func() {
						ui.Text(c, "要注意").FontSize(kit.FontTable).TextColor(k.Label3)
						bulletList(c, info.Cons, kit.WarningTone)
					})
				})
			})
		})

		if path == pathTailscale && len(enabledTS) == 0 {
			if existing := model.TailscaleEndpoint(doc); existing != nil {
				note(c, kit.WarningTone, "Tailscale 途径需要启用 Tailscale 端点。")
			} else {
				kit.Notice(c, kit.WarningTone, "", "Tailscale 途径需要一个已启用的 Tailscale 端点，当前还没有。", func() {
					kit.Button(c, "添加 Tailscale 端点", kit.ButtonOptions{Small: true, Icon: kit.SymPlus}).OnClick(func() { choosePath(pathTailscale) })
				})
			}
		}

		if !isServer && path == pathTailscale && len(enabledTS) > 0 {
			ui.Column(c).Gap(8).Shrink(0).Children(func() {
				subHeading(c, "通过哪个 Tailscale 端点连接", "", nil)
				options := make([]kit.Option, len(enabledTS))
				for i, ep := range enabledTS {
					options[i] = kit.Option{Value: ep.ID, Label: ep.Tag}
				}
				value := detourID
				if kit.Select(c, "detour", &value, options, kit.SelectOptions{Placeholder: "选择 Tailscale 端点", Label: "选择 Tailscale 端点"}).FillWidth().Changed() {
					patch(map[string]any{"detour": value})
				}
				ui.Row(c).Gap(8).Wrap().Children(func() {
					kit.Button(c, "从 Tailscale 对端选择服务器", kit.ButtonOptions{Small: true, Icon: kit.SymUsers, Disabled: detourEndpoint == nil}).
						OnClick(func() { g.peerPicker = true })
					ui.Text(c, "也可以直接在下面手填 100.x.x.x 或 MagicDNS 名称。").FontSize(kit.FontTable).TextColor(k.Label3).Wrap()
				})
			})
		}

		if isServer {
			ui.Column(c).Gap(8).Shrink(0).Children(func() {
				subHeading(c, "告诉 Client 连接到哪里", "", nil)
				ui.Column(c).Radius(14).Border(1, k.HairlineSoft).Background(k.Surface).Padding(12).Gap(6).Shrink(0).Children(func() {
					if len(addresses) > 0 {
						for _, option := range addresses {
							ui.Row(c.Key(option.Address)).Gap(8).Children(func() {
								ui.Column(c).Grow(1).MinWidth(0).Children(func() {
									ui.Text(c, option.Label).FontSize(kit.FontTable).TextColor(k.Label3)
									ui.Text(c, option.Address+":"+port).FontSize(15).Selectable().Wrap().FontFeatures("tnum")
								})
								kit.IconButton(c, kit.SymCopy, "复制 "+option.Address, kit.IconButtonOptions{Size: 32, IconSize: 15}).
									OnClick(func() { s.copy(c, option.Address, "已复制地址") })
							})
						}
					} else {
						var message string
						switch path {
						case pathTailscale:
							message = "启动核心并登录 Tailscale 后，这里会显示本机的 Tailscale 地址（100.x.x.x）和 MagicDNS 名称，把它填到 Client 的「服务器地址」。"
						case pathLAN:
							message = "在 Client 的「服务器地址」里填这台机器的局域网 IP（例如 192.168.1.20），端口 " + port + "。"
						default:
							message = "在 Client 的「服务器地址」里填这台机器的VPN 地址或公网地址，端口 " + port + "。"
						}
						ui.Text(c, message).FontSize(kit.FontBody).TextColor(k.Label2).Wrap()
					}
					ui.Row(c).Gap(8).Wrap().Margin(6, 0, 0).Children(func() {
						first := ""
						if len(addresses) > 0 {
							first = addresses[0].Address
						}
						dnsName := ""
						for _, o := range addresses {
							if strings.HasSuffix(o.Address, ".ts.net") && o.Address != first {
								dnsName = o.Address
								break
							}
						}
						kit.Button(c, "复制连接信息", kit.ButtonOptions{Small: true}).OnClick(func() {
							s.copy(c, clientConnectionText(path, first, port, dnsName), "已复制连接信息")
						})
						kit.Button(c, "复制 Client 配置片段", kit.ButtonOptions{Small: true}).OnClick(func() {
							s.copy(c, clientConfigSnippet(path, first, port), "已复制 Client 配置片段")
						})
					})
				})
			})
		}

		ui.Column(c).Gap(8).Shrink(0).Children(func() {
			status := worstLevel(items)
			count := ""
			if status == "" && len(items) > 0 {
				count = "全部通过"
			}
			subHeading(c, "检查清单", count, func() {
				if status != "" {
					tone := kit.WarningTone
					if status == levelError {
						tone = kit.DangerTone
					}
					kit.Badge(c, fmt.Sprintf("%d 项待处理", len(important)), tone)
				}
				kit.IconButton(c, kit.SymRefreshCw, "重新检查环境", kit.IconButtonOptions{Size: 28, IconSize: 15, Disabled: s.rt.preflightLoading}).
					OnClick(s.rt.refreshPreflight)
			})
			ui.Column(c).Radius(14).Border(1, k.HairlineSoft).Background(k.Surface).PaddingX(12).Label("检查清单").Shrink(0).Children(func() {
				if len(important) == 0 {
					ui.Text(c, "没有需要处理的问题。").FontSize(kit.FontBody).TextColor(k.Label2).PaddingY(12)
				}
				for _, item := range important {
					checkRow(c, item, copied)
				}
			})
			switch {
			case s.rt.preflight == nil && !s.rt.preflightLoading:
				ui.Text(c, "没有取到环境检查结果（Kiko 服务端版本较旧或接口不可用），上面只显示静态提示。").FontSize(kit.FontTable).TextColor(k.Label3).Wrap().PaddingX(4)
			case s.rt.preflight != nil:
				ui.Row(c).Gap(6).PaddingX(4).Children(func() {
					statusDot(c, toneOK)
					ui.Text(c, "环境检查针对运行 Kiko 服务端的这台机器（"+s.rt.preflight.OS+"/"+s.rt.preflight.Arch+"）。").FontSize(kit.FontTable).TextColor(k.Label3).Wrap()
				})
			}
			if len(others) > 0 {
				collapsible(c, "更多检查与说明", func() { kit.Badge(c, fmt.Sprint(len(others)), kit.Neutral) }, &g.moreOpen, func() {
					for _, item := range others {
						checkRow(c, item, copied)
					}
				})
			}
		})

		usage := []string{
			"连接成功后，远程设备会出现在本机的 USB 设备列表里：Linux 用 lsusb 查看，macOS 看「系统信息 → USB」，Windows 看设备管理器。",
			"Kiko 目前不显示 Client 的挂载状态，可以在日志里查看核心的输出。",
			"停止核心会断开已挂载的设备。",
		}
		if isServer {
			usage = []string{
				"Server 启动后在 " + port + " 端口等待 Client 连接。",
				"被导出的设备会从本机系统里「让出」，Client 挂载期间本机不能再使用它。",
				"停止核心会断开所有 Client 的连接。",
			}
		}
		collapsible(c, "使用说明", nil, &g.usageOpen, func() { bulletList(c, usage, kit.Neutral) })
	})

	g.peerPickerView(c, s, service, detourEndpoint)
}

// peerPickerView chooses the server among the tailnet's peers.
func (g *guideView) peerPickerView(c *ui.Context, s *Section, service *model.Service, detour *model.Endpoint) {
	if !g.peerPicker {
		return
	}
	k := kit.T(c)
	var peers []tailnetPeer
	if detour != nil {
		peers = tailnetPeers(s.rt.tailscaleStatus(detour.Tag))
	}
	kit.Dialog(c, &g.peerPicker, "选择 Server 设备", 0, func() {
		if len(peers) == 0 {
			note(c, kit.WarningTone, "还没有可选的设备。请先启动核心并登录 Tailscale，且确保 Server 那台机器也在同一个 tailnet 里；也可以直接手填 100.x.x.x 或 MagicDNS 名称。")
			return
		}
		listCard(c, func() {
			for _, peer := range peers {
				row := ui.ButtonBase(c.Key(peer.Address)).MinHeight(56).PaddingX(12).Gap(12).FillWidth().Justify(ui.Start).Label(peer.Name).Transition(kit.HoverMotion)
				if row.Hovered() {
					row.Background(k.Fill)
				}
				row.Children(func() {
					tone := toneIdle
					if peer.Online {
						tone = toneOK
					}
					statusDot(c, tone)
					ui.Column(c).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, peer.Name).FontSize(15).SingleLine().Ellipsis("…")
						detail := peer.Address
						if peer.OS != "" {
							detail += " · " + peer.OS
						}
						if !peer.Online {
							detail += " · 离线"
						}
						ui.Text(c, detail).FontSize(kit.FontTable).TextColor(k.Label3).SingleLine().FontFeatures("tnum")
					})
				})
				if row.Clicked() {
					service.Fields["server"] = peer.Address
					g.peerPicker = false
				}
			}
		})
	}, nil)
}

// addPendingTailscale creates a Tailscale endpoint saved with the edit,
// for the guide's Tailscale path, and returns its id.
func (e *editor) addPendingTailscale(doc *model.Doc, server bool) string {
	created := model.MakeEndpoint("tailscale", model.TailscaleTagFor(e.editDoc(doc), ""), doc.IPv6Enabled)
	e.pending = append(e.pending, created)
	return created.ID
}
