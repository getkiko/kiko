package settings

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/servers"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// serversPage is Settings › Servers: the servers this app opens a window
// for — the local server and the paired remote servers — with adding,
// renaming, pairing
// again and removing them. The list belongs to the app, so every window
// shows the same.
type serversPage struct {
	w *Workspace

	pair    pairDialog
	rename  renameDialog
	menus   map[string]bool
	confirm map[string]*kit.ConfirmState
}

func newServersPage(w *Workspace) *serversPage {
	p := &serversPage{w: w, menus: map[string]bool{}, confirm: map[string]*kit.ConfirmState{}}
	p.pair.w = w
	p.rename.w = w
	return p
}

// open shows the page; "add" opens the add dialog, "repair:<id>" pairs
// that server again.
func (p *serversPage) open(action string) {
	switch {
	case action == "add":
		p.pair.open("")
	case strings.HasPrefix(action, "repair:"):
		p.pair.open(strings.TrimPrefix(action, "repair:"))
	}
}

func (p *serversPage) view(c *ui.Context) {
	dir, ok := p.w.env.Directory()
	kit.Workspace(c, func() {
		kit.PreferencesForm(c, func() {
			backRow(c, p.w)
			kit.PaneTitle(c, "服务器", "每台服务器在独立窗口中打开，可同时管理本机和远程的 kiko-server。", func() {
				if ok {
					kit.Button(c, "添加服务器", kit.ButtonOptions{Variant: kit.Primary, Icon: kit.SymPlus}).OnClick(func() { p.pair.open("") })
				}
			})
			if !ok {
				kit.Notice(c, kit.AccentTone, "", "此窗口不提供服务器列表。", nil)
				return
			}
			footer := ""
			if dir.InsecureTokens() {
				footer = "系统没有可用的钥匙串，设备令牌保存在仅当前用户可读的文件中。"
			}
			list := dir.Servers()
			kit.SettingsGroup(c, "服务器列表", footer, func() {
				for _, s := range list {
					ui.Column(c.Key(s.ID)).Children(func() { p.row(c, dir, s) })
				}
			})
			kit.SettingsGroup(c, "窗口", "", func() {
				kit.SettingsRow(c, "启动时恢复上次打开的窗口", "退出时打开着的远程服务器窗口会在下次启动时重新打开", func() {
					on := dir.RestoreWindows()
					if kit.Switch(c, &on, "启动时恢复上次打开的窗口").Changed() {
						p.w.env.Run(func(context.Context) error { return dir.SetRestoreWindows(on) }, p.w.env.ToastError)
					}
				})
			})
		})
	})
	if ok {
		p.pair.view(c, dir)
		p.rename.view(c, dir)
	}
}

// row is a server: its color, name, address, short fingerprint and last
// connection, with "Open" and a menu of the rest.
func (p *serversPage) row(c *ui.Context, dir env.ServerDirectory, s servers.Server) {
	k := kit.T(c)
	current := s.ID == p.w.env.Target.ID || (s.ID == servers.LocalID && p.w.env.Target.ID == "")
	ui.Row(c).MinHeight(kit.SettingsRowH).PaddingY(10).Gap(12).Children(func() {
		ui.Box(c).Size(28, 28).Radius(8).Center().Background(serverColor(k, s)).Shrink(0).Children(func() {
			icon := kit.SymServer
			if !s.Remote() {
				icon = kit.SymLaptop
			}
			kit.Icon(c, icon, 15).TextColor(ui.Hex("#FFFFFF"))
		})
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).Gap(6).Children(func() {
				ui.Text(c, s.Name).FontSize(kit.FontBody).FontWeight(500).SingleLine().Ellipsis("…").MinWidth(0)
				if current {
					kit.Badge(c, "当前窗口", kit.AccentTone)
				}
			})
			ui.Text(c, serverDetail(s, time.Now())).FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum").
				SingleLine().Ellipsis("…").Selectable()
		})
		ui.Row(c).Gap(6).Shrink(0).Children(func() {
			kit.Button(c, "打开", kit.ButtonOptions{Small: true}).Label("打开 " + s.Name).OnClick(func() { dir.OpenServer(s.ID) })
			if !s.Remote() {
				return
			}
			open := p.menus[s.ID]
			more := kit.IconButton(c, kit.SymEllipsis, s.Name+" 操作", kit.IconButtonOptions{})
			if more.Clicked() {
				open = !open
			}
			confirm := p.confirm[s.ID]
			if confirm == nil {
				confirm = &kit.ConfirmState{}
				p.confirm[s.ID] = confirm
			}
			kit.ActionMenu(c, more, &open, []kit.ActionGroup{{Label: s.Name, Items: []kit.ActionItem{
				{ID: "rename", Label: "重命名", Icon: kit.SymPencil, Run: func() { p.rename.open(s) }},
				{ID: "repair", Label: "重新配对", Icon: kit.SymKeyRound, Run: func() { p.pair.open(s.ID) }},
				{ID: "remove", Label: "移除", Icon: kit.SymTrash2, Danger: true, Run: func() { confirm.Open = true }},
			}}}, kit.ActionMenuOptions{Label: s.Name + " 操作"})
			p.menus[s.ID] = open
			// The confirmation anchors to the menu's button, which stays.
			kit.ConfirmPopover(c, more, confirm, kit.ConfirmOptions{
				Title:   "移除「" + s.Name + "」？",
				Message: "将关闭它的窗口并吊销此设备的令牌；再次使用需要重新配对。",
				Confirm: "移除", Danger: true,
				Run: func(done func(error)) {
					p.w.env.Run(func(ctx context.Context) error { return dir.RemoveServer(ctx, s.ID) }, func(err error) {
						if err == nil {
							p.w.env.Toast("已移除「" + s.Name + "」")
						}
						done(err)
					})
				},
			})
		})
	})
}

// serverDetail is a server's second line: address, fingerprint and last
// connection.
func serverDetail(s servers.Server, now time.Time) string {
	parts := []string{s.Address()}
	if !s.Remote() {
		parts = append(parts, "此电脑上的服务端")
		return strings.Join(parts, " · ")
	}
	if fp := s.Pin(); fp != nil {
		parts = append(parts, "指纹 "+api.ShortFingerprint(fp))
	}
	if s.LastConnected.IsZero() {
		parts = append(parts, "尚未连接")
	} else {
		parts = append(parts, "最近连接 "+relativeTime(s.LastConnected, now))
	}
	return strings.Join(parts, " · ")
}

// relativeTime is a past time in words: "just now", "N minutes ago",
// "N hours ago", or the date.
func relativeTime(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " 分钟前"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + " 小时前"
	}
	return timefmt.Format(t, "2006-01-02")
}

// serverColor is a server's accent: its own color, the accent for the local
// server.
func serverColor(k *kit.Tokens, s servers.Server) ui.Color {
	if s.Color != "" {
		return ui.Hex(s.Color)
	}
	return k.Accent
}

// backRow returns to the main settings page.
func backRow(c *ui.Context, w *Workspace) {
	ui.Row(c).Margin(0, 0, 14).Shrink(0).Children(func() {
		back := kit.Button(c, "设置", kit.ButtonOptions{Variant: kit.TextButton, Small: true, Icon: kit.SymArrowLeft}).
			Label("返回设置").OnClick(w.back)
		if w.focusBack {
			back.Focus()
			w.focusBack = false
		}
	})
}

// pairDialog adds a server, from a pairing string or by hand, or pairs a
// listed one again. A certificate seen for the first time (or changed)
// pairs only after the user compared its fingerprint.
type pairDialog struct {
	w *Workspace

	shown  bool
	id     string // the server paired again; "" adds one
	mode   int    // 0 pairing string, 1 by hand
	uri    string
	addr   string
	code   string
	busy   bool
	err    string
	seen   []byte // the certificate to confirm
	trust  []byte // the certificate confirmed
	result string
}

func (d *pairDialog) open(id string) {
	*d = pairDialog{w: d.w, shown: true, id: id}
}

func (d *pairDialog) title(dir env.ServerDirectory) string {
	if d.id == "" {
		return "添加服务器"
	}
	for _, s := range dir.Servers() {
		if s.ID == d.id {
			return "重新配对「" + s.Name + "」"
		}
	}
	return "重新配对"
}

func (d *pairDialog) submit(dir env.ServerDirectory) {
	if d.busy {
		return
	}
	d.busy, d.err = true, ""
	id, mode, uri, addr, code, trust := d.id, d.mode, strings.TrimSpace(d.uri), d.addr, strings.TrimSpace(d.code), d.trust
	var seen []byte
	confirm := func(fp []byte) bool {
		if trust != nil && bytes.Equal(fp, trust) {
			return true
		}
		seen = bytes.Clone(fp)
		return false
	}
	env.Go(d.w.env, func(ctx context.Context) (servers.Server, error) {
		switch {
		case id != "" && mode == 0:
			return servers.Server{ID: id}, dir.RepairServer(ctx, id, uri, confirm)
		case id != "":
			return servers.Server{ID: id}, dir.RepairServer(ctx, id, code, confirm)
		case mode == 0:
			return dir.AddServer(ctx, uri)
		default:
			return dir.AddServerManual(ctx, addr, code, confirm)
		}
	}, func(s servers.Server, err error) {
		d.busy = false
		if err != nil {
			if seen != nil {
				// A certificate to compare first: shown, then trusted.
				d.seen, d.err = seen, ""
				return
			}
			d.err = err.Error()
			return
		}
		d.shown = false
		if id == "" {
			d.w.env.Toast("已添加「" + s.Name + "」")
			dir.OpenServer(s.ID)
		} else {
			d.w.env.Toast("已重新配对")
		}
	})
}

func (d *pairDialog) view(c *ui.Context, dir env.ServerDirectory) {
	if !d.shown {
		return
	}
	k := kit.T(c)
	kit.Dialog(c, &d.shown, d.title(dir), 480, func() {
		if d.seen != nil {
			d.fingerprintView(c)
			return
		}
		kit.Segmented(c, "mode", &d.mode, kit.Segment{Label: "配对字符串"}, kit.Segment{Label: "手动填写"}).Label("配对方式")
		if d.mode == 0 {
			ui.Text(c, "在服务器上运行 sudo kiko pair，或在另一台已连接的设备上打开 设置 › 远程访问 › 配对新设备，然后粘贴配对字符串。").
				FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
			kit.TextArea(c, "uri", &d.uri, "kiko-pair://…", true).Label("配对字符串").MinHeight(72)
		} else {
			if d.id == "" {
				kit.FormRow(c, "地址", "主机名或 IP，默认端口 9443", func() {
					kit.Input(c, "addr", &d.addr, "nas.local:9443").Label("服务器地址")
				})
			}
			kit.FormRow(c, "配对码", "8 位，5 分钟内有效", func() {
				kit.Input(c, "code", &d.code, "ABCD-EFGH").Label("配对码").Font(kit.MonoFont)
			})
			ui.Text(c, "手动填写时将读取服务器证书，并请你与服务器上显示的指纹核对。").
				FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
		}
		kit.FormError(c, d.err)
	}, func() {
		kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(func() { d.shown = false })
		if d.seen != nil {
			kit.Button(c, "指纹一致，配对", kit.ButtonOptions{Variant: kit.Primary, Disabled: d.busy}).OnClick(func() {
				d.trust, d.seen = d.seen, nil
				d.submit(dir)
			})
			return
		}
		ready := strings.TrimSpace(d.uri) != ""
		if d.mode == 1 {
			ready = strings.TrimSpace(d.code) != "" && (d.id != "" || strings.TrimSpace(d.addr) != "")
		}
		label := "配对"
		if d.busy {
			label = "正在配对…"
		}
		kit.Button(c, label, kit.ButtonOptions{Variant: kit.Primary, Disabled: d.busy || !ready}).OnClick(func() { d.submit(dir) })
	})
}

// fingerprintView asks to compare the server's certificate.
func (d *pairDialog) fingerprintView(c *ui.Context) {
	k := kit.T(c)
	title := "请核对服务器证书指纹"
	message := "确认与服务器上 kiko pair 或 远程访问 页显示的指纹一致后再配对。"
	if d.id != "" {
		title = "服务器证书已变化"
		message = "此服务器的证书与配对时不同。只有在你确认服务器重新生成了证书时才继续。"
	}
	tone := kit.WarningTone
	if d.id != "" {
		tone = kit.DangerTone
	}
	kit.Notice(c, tone, title, message, nil)
	ui.Column(c).Gap(4).Padding(10, 12).Radius(10).Background(k.Surface2).Shrink(0).Children(func() {
		ui.Text(c, "SHA-256").FontSize(kit.FontCaption).TextColor(k.Label3)
		ui.Text(c, api.GroupFingerprint(d.seen)).Font(kit.MonoFont).FontSize(13).Selectable().Wrap()
	})
}

// renameDialog names a remote server.
type renameDialog struct {
	w     *Workspace
	shown bool
	id    string
	name  string
	err   string
}

func (d *renameDialog) open(s servers.Server) {
	*d = renameDialog{w: d.w, shown: true, id: s.ID, name: s.Name}
}

func (d *renameDialog) view(c *ui.Context, dir env.ServerDirectory) {
	if !d.shown {
		return
	}
	save := func() {
		id, name := d.id, d.name
		d.w.env.Run(func(context.Context) error { return dir.RenameServer(id, name) }, func(err error) {
			if err != nil {
				d.err = err.Error()
				return
			}
			d.shown = false
		})
	}
	kit.Dialog(c, &d.shown, "重命名服务器", 400, func() {
		kit.FormRow(c, "名称", "", func() {
			in := kit.Input(c, "name", &d.name, "").Label("服务器名称").AutoFocus()
			if in.Submitted() {
				save()
			}
		})
		kit.FormError(c, d.err)
	}, func() {
		kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(func() { d.shown = false })
		kit.Button(c, "保存", kit.ButtonOptions{Variant: kit.Primary, Disabled: strings.TrimSpace(d.name) == ""}).OnClick(save)
	})
}
