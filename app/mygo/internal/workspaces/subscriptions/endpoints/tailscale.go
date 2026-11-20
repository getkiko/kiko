package endpoints

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// tailscaleSheet is a Tailscale endpoint's control sheet: state and health, this device, the exit node,
// the login page, the tailnet's devices with their menus, the Taildrop
// receive directory, sends and inbox, and sign-out.
type tailscaleSheet struct {
	s   *Section
	tag string
	// onClose runs as the sheet is dismissed.
	onClose func()

	selection     string // peer key, "self" for this device
	ssh           sshSheet
	logout        kit.ConfirmState
	menus         map[string]bool
	directory     string
	directoryTag  string
	directoryRead bool
	directoryBusy bool
	readUnread    int

	download       string
	downloadBytes  int64
	downloadCancel context.CancelFunc
	// downloadRun tells the current download from one stopped before.
	downloadRun int

	detail peerDetail
	diag   diagnostics
}

// peerKey identifies a peer: its stable id, else its address.
func peerKey(p api.TailscalePeer) string {
	if p.StableID != "" {
		return p.StableID
	}
	return peerAddress(p)
}

func peerDNSName(p api.TailscalePeer) string {
	return strings.TrimSuffix(strings.TrimSpace(p.DNSName), ".")
}

func peerAddress(p api.TailscalePeer) string {
	if len(p.TailscaleIPs) > 0 && p.TailscaleIPs[0] != "" {
		return p.TailscaleIPs[0]
	}
	if name := peerDNSName(p); name != "" {
		return name
	}
	return p.HostName
}

func peerName(p api.TailscalePeer) string {
	if label, _, _ := strings.Cut(peerDNSName(p), "."); label != "" {
		return label
	}
	if p.HostName != "" {
		return p.HostName
	}
	return peerAddress(p)
}

// peerTime shows a Unix time in seconds; never for none.
func peerTime(value string) string {
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || seconds <= 0 || math.IsInf(seconds, 0) {
		return "永不过期"
	}
	return timefmt.Format(time.Unix(int64(seconds), 0), "2006/1/2 15:04:05")
}

// taildropTime shows when a send changed: minutes within the hour, else
// the date.
func taildropTime(value string, now time.Time) string {
	var stamp time.Time
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		stamp = t
	} else if ms, err := strconv.ParseFloat(value, 64); err == nil && ms > 0 {
		stamp = time.UnixMilli(int64(ms))
	} else {
		return "时间未知"
	}
	elapsed := max(now.Sub(stamp), 0)
	if elapsed < time.Hour {
		return fmt.Sprintf("%d 分钟前", max(1, int(elapsed/time.Minute)))
	}
	local := timefmt.In(stamp)
	return fmt.Sprintf("%d 年 %02d 月 %02d 日 %02d:%02d:%02d", local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), local.Second())
}

func (t *tailscaleSheet) guard(action func(ctx context.Context) error) {
	t.s.env.Run(action, func(err error) {
		if err != nil {
			message := err.Error()
			if message == "" {
				message = "操作失败"
			}
			t.s.env.Toast(message)
		}
	})
}

// opened runs as the sheet opens for tag.
func (t *tailscaleSheet) opened(tag string, status *api.TailscaleStatus) {
	t.closed()
	t.tag = tag
	t.menus = map[string]bool{}
	t.s.rt.refreshSends()
	t.directoryRead = false
	if status != nil && status.EndpointTag != "" {
		t.loadDirectory(tag)
	}
}

func (t *tailscaleSheet) loadDirectory(tag string) {
	t.directoryRead, t.directory, t.directoryTag = true, "", ""
	env.Go(t.s.env, func(ctx context.Context) (string, error) {
		return t.s.env.API.TaildropDirectory(ctx, tag)
	}, func(path string, err error) {
		if t.tag != tag {
			return
		}
		if err != nil {
			path = ""
		}
		t.directory, t.directoryTag = path, tag
	})
}

// closed forgets the open sheet, canceling a download.
func (t *tailscaleSheet) closed() {
	t.stop()
	t.tag, t.selection = "", ""
	t.ssh.close()
	t.logout = kit.ConfirmState{}
	t.readUnread = 0
	t.detail.stop()
	t.diag.reset("")
	t.diag.open = false
}

// stop cancels what runs in the background.
func (t *tailscaleSheet) stop() {
	if t.downloadCancel != nil {
		t.downloadCancel()
		t.downloadCancel = nil
	}
	t.download, t.downloadBytes = "", 0
	t.downloadRun++
	t.detail.stop()
}

func (t *tailscaleSheet) downloadFile(name string, size int64) {
	if t.downloadCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.downloadCancel, t.download, t.downloadBytes = cancel, name, 0
	t.downloadRun++
	run := t.downloadRun
	tag, e := t.tag, t.s.env
	var mu sync.Mutex
	last := time.Time{}
	progress := func(n int64) {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(last) < 100*time.Millisecond {
			return
		}
		last = time.Now()
		e.Post(func() {
			if t.downloadRun == run {
				t.downloadBytes = n
			}
		})
	}
	e.Run(func(context.Context) error {
		data, err := downloadReceived(ctx, e.API, tag, name, size, downloadLimit, progress)
		if err != nil {
			return err
		}
		_, err = e.Shell.SaveFile(name, data)
		return err
	}, func(err error) {
		cancel()
		if t.downloadRun != run {
			// Stopped: a later download may run now.
			return
		}
		t.downloadCancel, t.download = nil, ""
		if err != nil && ctx.Err() == nil {
			message := err.Error()
			if message == "" {
				message = "下载失败，请重试"
			}
			e.Toast(message)
		}
	})
}

// send uploads files to a peer; the server sends them on.
func (t *tailscaleSheet) send(peer api.TailscalePeer, files []api.TaildropUpload) {
	if len(files) == 0 || peer.StableID == "" {
		return
	}
	tag, id, e := t.tag, peer.StableID, t.s.env
	env.Go(e, func(ctx context.Context) (api.TaildropSendTask, error) {
		return e.API.TaildropSend(ctx, tag, id, files)
	}, func(task api.TaildropSendTask, err error) {
		if err != nil {
			e.Toast(firstOf(err.Error(), "操作失败"))
			return
		}
		t.s.rt.sends.merge(task, false)
	})
}

func (t *tailscaleSheet) view(c *ui.Context, tag string) {
	k := kit.T(c)
	// The core's requests go by the tag it runs the endpoint with.
	tag = t.s.rt.liveTailscaleTag(tag)
	status := t.s.rt.tailscaleStatus(tag)
	if t.tag != tag {
		t.opened(tag, status)
	}
	if status != nil && status.EndpointTag != "" && !t.directoryRead {
		t.loadDirectory(tag)
	}
	inbox := t.s.rt.taildrop[tag]
	if status != nil && status.UnreadFileCount > 0 && t.readUnread != status.UnreadFileCount {
		t.readUnread = status.UnreadFileCount
		t.guard(func(ctx context.Context) error { return t.s.env.API.TaildropRead(ctx, tag) })
	}
	var groups []api.TailscaleUserGroup
	connection := stateLine{"状态未知", toneIdle}
	if status != nil {
		groups = status.UserGroups
		connection = tailscaleConnectionState(status, time.Now())
	}
	open := true
	kit.Dialog(c, &open, "Tailscale", 600, func() {
		if status == nil {
			note(c, kit.Neutral, "暂无运行状态：需要核心正在运行且已应用包含该端点的配置。")
			t.configView(c, tag)
			return
		}
		detailSection(c, "状态", nil, func() {
			card(c, func() {
				infoRow(c, "状态", func() {
					stateMark(c, connection.tone)
					ui.Text(c, connection.text)
					if reconnectable(true, connection) {
						busy := t.s.reconnects.busy(tag, time.Now())
						t.s.tailnet.ReconnectButton(c, TailscaleSummary{Reconnect: true, Reconnecting: busy, tag: tag})
					}
				})
				for i, message := range status.Health {
					d := tailscaleHealthDiagnostic(message)
					infoRow(c.Key(i), d.label, func() { ui.Text(c, d.text).Selectable().Wrap().TextAlign(ui.End).MinWidth(0) })
				}
				if status.Self != nil {
					self := *status.Self
					row := ui.ButtonBase(c).MinHeight(44).Gap(12).FillWidth().Label("本机 " + peerName(self))
					row.Children(func() {
						ui.Text(c, "本机").FontSize(kit.FontBody).TextColor(k.Label3)
						ui.Text(c, peerName(self)).FontSize(kit.FontBody).SingleLine().Ellipsis("…").Grow(1).MinWidth(0).TextAlign(ui.End)
						kit.Icon(c, kit.SymChevronRight, 14).TextColor(k.Label3)
					})
					if row.Clicked() {
						t.selection = "self"
					}
				}
				diagRow := ui.ButtonBase(c).MinHeight(44).Gap(12).FillWidth().Label("网络诊断")
				diagRow.Children(func() {
					ui.Text(c, "网络诊断").FontSize(kit.FontBody).TextColor(k.Label3).Grow(1)
					kit.Icon(c, kit.SymChevronRight, 14).TextColor(k.Label3)
				})
				if diagRow.Clicked() {
					t.diag.open = true
				}
				if status.ExitNode != nil {
					infoRow(c, "出口节点", func() {
						ui.Text(c, peerName(*status.ExitNode))
						kit.Button(c, "清除", kit.ButtonOptions{Variant: kit.TextButton, Small: true}).OnClick(func() {
							t.guard(func(ctx context.Context) error { return t.s.env.API.TailscaleExitNode(ctx, tag, "") })
						})
					})
				}
			})
			if status.AuthURL != "" {
				url := status.AuthURL
				kit.Button(c, "打开登录页面", kit.ButtonOptions{Variant: kit.Primary, Icon: kit.SymExternalLink}).FillWidth().
					OnClick(func() { t.s.openURL(url) })
			}
		})
		for gi, group := range groups {
			title := firstOf(group.DisplayName, group.LoginName, "设备")
			ui.Column(c.Key(gi)).Shrink(0).Children(func() {
				detailSection(c, title, nil, func() {
					listCard(c, func() {
						for _, peer := range group.Peers {
							t.peerRow(c, tag, peer, connection.tone)
						}
					})
				})
			})
		}
		detailSection(c, "Taildrop 接收目录", nil, func() {
			ui.Row(c).Radius(16).Border(1, k.HairlineSoft).Background(k.Surface).Padding(12, 14).Gap(12).Shrink(0).Children(func() {
				path := "读取中…"
				if t.directoryTag == tag {
					path = t.directory
					if path == "" {
						path = "读取失败"
					}
				}
				ui.Text(c, path).Font(kit.MonoFont).FontSize(kit.FontTable).TextColor(k.Label3).Selectable().Wrap().Grow(1).MinWidth(0)
				disabled := t.directoryTag != tag || t.directory == "" || t.directoryBusy
				kit.Button(c, "打开", kit.ButtonOptions{Icon: kit.SymFolderOpen, Disabled: disabled}).OnClick(func() {
					t.directoryBusy = true
					t.s.env.Run(func(ctx context.Context) error { return t.s.env.API.TaildropOpenDirectory(ctx, tag) }, func(err error) {
						t.directoryBusy = false
						if err != nil {
							t.s.env.Toast(firstOf(err.Error(), "操作失败"))
						}
					})
				})
			})
		})
		t.sendsView(c, tag, groups)
		t.inboxView(c, tag, inbox)
		logout := kit.Button(c, "退出登录", kit.ButtonOptions{Variant: kit.Danger, Icon: kit.SymLogOut}).FillWidth()
		if logout.Clicked() {
			t.logout.Open = true
		}
		kit.ConfirmPopover(c, logout, &t.logout, kit.ConfirmOptions{Title: "退出登录？", Confirm: "退出", Danger: true,
			Message: "将退出 Tailscale 账号，需要重新登录才能使用。",
			Run: func(done func(error)) {
				t.s.env.Run(func(ctx context.Context) error { return t.s.env.API.TailscaleLogout(ctx, tag) }, done)
			}})
	}, nil)
	if status != nil && t.diag.open {
		t.diag.view(c, t, tag, status)
	}
	shown := false
	if status != nil && t.selection != "" {
		if peer, self, ok := t.selected(status); ok {
			t.detail.view(c, t, tag, peer, self, status)
			shown = t.selection != ""
		} else {
			t.selection = ""
		}
	}
	if !shown && t.detail.running {
		// The device left the tailnet, or the state is gone: its sheet
		// closed, and so does its ping.
		t.detail.stop()
	}
	t.ssh.view(c, t.s)
	if !open {
		if t.onClose != nil {
			t.onClose()
		}
		t.closed()
	}
}

// configView is what the sheet shows without a state: the endpoint's
// configuration and this device as last seen.
func (t *tailscaleSheet) configView(c *ui.Context, tag string) {
	doc := t.s.store.Doc()
	i := slices.IndexFunc(doc.Endpoints, func(x *model.Endpoint) bool { return x.Tag == tag })
	if i < 0 {
		return
	}
	facts := tailscaleFacts(doc.Endpoints[i].Fields)
	last := t.s.rt.lastTailscale[tag]
	detailSection(c, "配置", nil, func() {
		card(c, func() {
			infoText(c, "主机名", firstOf(facts.hostname, "系统主机名"))
			infoText(c, "控制服务器", firstOf(facts.control, "Tailscale"))
			infoText(c, "登录方式", facts.login)
			if facts.udpPath != "" {
				infoText(c, "UDP 路径", facts.udpPath)
			}
			if facts.systemInterface {
				infoText(c, "系统接口", firstOf(facts.interfaceName, "已开启"))
			}
		})
	})
	if last != nil && last.Self != nil {
		self := *last.Self
		detailSection(c, "上次状态", nil, func() {
			card(c, func() {
				infoText(c, "本机", peerName(self))
				if ip := preferV4(self.TailscaleIPs); ip != "" {
					infoText(c, "Tailscale IP", ip)
				}
				if name := firstOf(last.NetworkName, strings.TrimSuffix(last.MagicDNSSuffix, ".")); name != "" {
					infoText(c, "Tailnet", name)
				}
			})
		})
	}
}

func (t *tailscaleSheet) selected(status *api.TailscaleStatus) (api.TailscalePeer, bool, bool) {
	if t.selection == "self" {
		if status.Self != nil {
			return *status.Self, true, true
		}
		return api.TailscalePeer{}, true, false
	}
	for _, g := range status.UserGroups {
		for _, p := range g.Peers {
			if peerKey(p) == t.selection {
				return p, false, true
			}
		}
	}
	return api.TailscalePeer{}, false, false
}

func (t *tailscaleSheet) toggleExit(tag string, peer api.TailscalePeer) {
	id := peer.StableID
	if peer.ExitNode {
		id = ""
	}
	t.guard(func(ctx context.Context) error { return t.s.env.API.TailscaleExitNode(ctx, tag, id) })
}

// peerRow is a device of the tailnet with its menu.
func (t *tailscaleSheet) peerRow(c *ui.Context, tag string, peer api.TailscalePeer, connection stateTone) {
	k := kit.T(c)
	key := peerKey(peer)
	name := peerName(peer)
	ui.Row(c.Key(key)).MinHeight(48).PaddingX(14).Gap(8).Shrink(0).Children(func() {
		row := ui.ButtonBase(c).MinHeight(48).Gap(10).Grow(1).MinWidth(0).Justify(ui.Start).Label(name)
		row.Children(func() {
			tone := toneIdle
			switch {
			case connection == toneWarn:
				tone = toneWarn
			case peer.Online:
				tone = toneOK
			}
			statusDot(c, tone)
			ui.Text(c, name).FontSize(14).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
			if len(peer.TailscaleIPs) > 0 {
				ui.Text(c, peer.TailscaleIPs[0]).Font(kit.MonoFont).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine().Shrink(1).MinWidth(0)
			}
			if peer.ExitNode {
				ui.Text(c, "出口").FontSize(10).TextColor(k.Success)
			}
			if peer.UDPPath != "" {
				udpPathBadge(c, peer.UDPPath)
			}
			if len(peer.SSHHostKeys) > 0 {
				ui.Spacer(c)
				ui.Box(c).Radius(6).Border(1, k.HairlineSoft).Padding(1, 6).Shrink(0).Children(func() {
					ui.Text(c, "SSH").FontSize(10).TextColor(k.Label3)
				})
			}
		})
		if row.Clicked() {
			t.selection = key
		}
		open := t.menus[key]
		more := kit.IconButton(c, kit.SymEllipsis, name+" 操作", kit.IconButtonOptions{Size: 32, IconSize: 17})
		if more.Clicked() {
			open = !open
		}
		items := []kit.ActionItem{{ID: "detail", Label: "查看详情", Icon: kit.SymInfo, Run: func() { t.selection = key }}}
		if len(peer.SSHHostKeys) > 0 {
			items = append(items, kit.ActionItem{ID: "ssh", Label: "通过 SSH 连接", Icon: kit.SymTerminal, Run: func() { t.ssh.open(tag, peer) }})
		}
		if peer.ExitNodeOption {
			label := "设为出口节点"
			if peer.ExitNode {
				label = "清除出口节点"
			}
			items = append(items, kit.ActionItem{ID: "exit", Label: label, Icon: kit.SymGlobe, Run: func() { t.toggleExit(tag, peer) }})
		}
		kit.ActionMenu(c, more, &open, []kit.ActionGroup{{Label: name, Items: items}}, kit.ActionMenuOptions{Label: name + " 操作"})
		t.menus[key] = open
	})
}

func (t *tailscaleSheet) sendsView(c *ui.Context, tag string, groups []api.TailscaleUserGroup) {
	k := kit.T(c)
	var sends []api.TaildropSendTask
	for _, task := range t.s.rt.sends.snapshot() {
		if task.EndpointTag == tag {
			sends = append(sends, task)
		}
	}
	if len(sends) == 0 {
		return
	}
	peers := map[string]api.TailscalePeer{}
	for _, g := range groups {
		for _, p := range g.Peers {
			if p.StableID != "" {
				peers[p.StableID] = p
			}
		}
	}
	now := time.Now()
	ui.Column(c.Key("sends")).Gap(4).Shrink(0).Children(func() {
		subHeading(c, "Taildrop 发送", "", nil)
		listCard(c, func() {
			for _, task := range sends {
				names := make([]string, len(task.Files))
				for i, f := range task.Files {
					names[i] = f.Name
				}
				var state string
				switch task.State {
				case "uploading":
					state = "准备发送…"
				case "sending":
					percent := 0
					if task.TotalBytes > 0 {
						percent = min(100, int(math.Round(float64(task.SentBytes)/float64(task.TotalBytes)*100)))
					}
					state = fmt.Sprintf("发送中 %d%% · %s / %s", percent, formatBytes(task.SentBytes), formatBytes(task.TotalBytes))
				case "completed":
					state = "已发送"
				case "canceled":
					state = "已取消"
				default:
					state = "发送失败：" + firstOf(task.Error, "未知错误")
				}
				who := task.Peer
				if p, ok := peers[task.Peer]; ok {
					who = peerName(p)
				}
				ui.Row(c.Key(task.ID)).Padding(10, 12).Gap(8).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, strings.Join(names, "、")).FontSize(14).SingleLine().Ellipsis("…")
						line := ui.Text(c, who+" · "+state+" · "+taildropTime(task.UpdatedAt, now)).FontSize(kit.FontTable).TextColor(k.Label3).Wrap()
						if task.State == "failed" {
							line.TextColor(k.Danger)
						}
					})
					if taildropActive(task) {
						id := task.ID
						kit.IconButton(c, kit.SymX, "取消发送", kit.IconButtonOptions{Size: 32}).OnClick(func() {
							t.guard(func(ctx context.Context) error { return t.s.env.API.TaildropSendCancel(ctx, id) })
							t.s.rt.refreshSends()
						})
					}
				})
			}
		})
	})
}

func (t *tailscaleSheet) inboxView(c *ui.Context, tag string, inbox *api.TaildropInbox) {
	k := kit.T(c)
	if inbox == nil || len(inbox.Files)+len(inbox.Receiving) == 0 {
		return
	}
	ui.Column(c.Key("inbox")).Gap(4).Shrink(0).Children(func() {
		subHeading(c, "Taildrop 收件箱", strconv.Itoa(len(inbox.Files)+len(inbox.Receiving)), nil)
		listCard(c, func() {
			for _, f := range inbox.Receiving {
				f := f
				ui.Row(c.Key("r:"+f.SenderID+":"+f.Name)).Padding(10, 12).Gap(8).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, f.Name).FontSize(14).SingleLine().Ellipsis("…")
						progress := formatBytes(int64(f.ReceivedBytes))
						if f.Size > 0 {
							progress = fmt.Sprintf("%.0f%%", math.Round(float64(f.ReceivedBytes)/float64(f.Size)*100))
						}
						line := "接收中 " + progress
						if f.SenderName != "" {
							line += " · " + f.SenderName
						}
						ui.Text(c, line).FontSize(kit.FontTable).TextColor(k.Label3)
					})
					kit.IconButton(c, kit.SymX, "取消接收", kit.IconButtonOptions{Size: 32}).OnClick(func() {
						t.guard(func(ctx context.Context) error { return t.s.env.API.TaildropCancel(ctx, tag, f.SenderID, f.Name) })
					})
				})
			}
			for _, f := range inbox.Files {
				f := f
				ui.Row(c.Key("f:"+f.Name)).Padding(10, 12).Gap(8).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, f.Name).FontSize(14).SingleLine().Ellipsis("…")
						line := formatBytes(int64(f.Size))
						if f.SenderName != "" {
							line += " · " + f.SenderName
						}
						ui.Text(c, line).FontSize(kit.FontTable).TextColor(k.Label3)
					})
					if t.download == f.Name {
						ui.Text(c, formatBytes(t.downloadBytes)).FontSize(kit.FontTable).TextColor(k.Label3).Role(ui.RoleStatus)
						kit.IconButton(c, kit.SymX, "取消下载", kit.IconButtonOptions{Size: 32}).OnClick(func() {
							if t.downloadCancel != nil {
								t.downloadCancel()
							}
						})
					} else {
						kit.IconButton(c, kit.SymDownload, "下载", kit.IconButtonOptions{Size: 32, Disabled: t.download != ""}).
							OnClick(func() { t.downloadFile(f.Name, int64(f.Size)) })
					}
					kit.IconButton(c, kit.SymTrash2, "删除", kit.IconButtonOptions{Size: 32, Danger: true}).OnClick(func() {
						t.guard(func(ctx context.Context) error { return t.s.env.API.TaildropDelete(ctx, tag, f.Name) })
					})
				})
			}
		})
	})
}
