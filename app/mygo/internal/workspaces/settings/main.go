package settings

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

var logLevels = []string{"trace", "debug", "info", "warn", "error", "fatal", "panic"}

// update changes the document, showing a refusal as a toast.
func (w *Workspace) update(fn func(d *model.Doc)) {
	w.env.ToastError(w.store.Update(fn))
}

// navRow is a settings row that opens something: an icon, a label with a
// description, and a chevron. Read Clicked on the element.
func navRow(c *ui.Context, icon kit.Symbol, label, description string, danger bool) ui.Element {
	k := kit.T(c)
	b := ui.ButtonBase(c).MinHeight(kit.SettingsRowH).PaddingY(10).Gap(12).Label(label)
	if b.Hovered() {
		b.Opacity(0.85)
	}
	return b.Children(func() {
		kit.Icon(c, icon, 17).TextColor(k.AccentText)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, label).FontSize(kit.FontBody)
			if description != "" {
				d := ui.Text(c, description).FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
				if danger {
					d.TextColor(k.Danger)
				}
			}
		})
		kit.Icon(c, kit.SymChevronRight, 14).TextColor(k.Label3)
	})
}

// pageRow opens a subpage as its row is clicked; the row takes the focus
// as the subpage returns.
func (w *Workspace) pageRow(row ui.Element, page string) {
	if w.returned == page {
		row.Focus()
		w.returned = ""
	}
	if row.Clicked() {
		w.Open(page)
	}
}

func (w *Workspace) mainView(c *ui.Context) {
	doc := w.store.Doc()
	kit.Workspace(c, func() {
		kit.Preferences(c, func() {
			kit.PaneTitle(c, "设置", "", func() {
				if w.info != nil {
					ui.Text(c, "v"+w.info.Version).FontSize(10).TextColor(kit.T(c).Label3).FontFeatures("tnum")
				}
			})
			kit.SettingsColumns(c, func() {
				w.networkGroup(c, doc)
				w.generalGroup(c)
				w.serversGroup(c)
			}, func() {
				w.logsGroup(c)
				w.connectionsGroup(c)
				w.proxyGroup(c, doc)
				w.backupGroup(c)
				w.aboutGroup(c)
			})
		})
	})
	w.returned = ""
	w.proxyDialog.view(c, doc)
	w.backup.view(c)
}

func (w *Workspace) networkGroup(c *ui.Context, doc *model.Doc) {
	kit.SettingsGroup(c, "网络", "", func() {
		w.pageRow(navRow(c, kit.SymSlidersHorizontal, "通用网络配置", "", false), pageNetwork)
		w.pageRow(navRow(c, kit.SymSlidersHorizontal, "高级 TLS 配置", "", false), pageTLS)
		if w.RuleSets != nil {
			w.pageRow(navRow(c, kit.SymFileSearch, "规则集", ruleSetsSummary(doc), false), pageRuleSets)
		}
		kit.SettingsRow(c, "启用 IPv6", "", func() {
			on := doc.IPv6Enabled
			if kit.Switch(c, &on, "启用 IPv6").Changed() {
				w.update(func(d *model.Doc) { setGlobalIPv6(d, on) })
			}
		})
		w.pageRow(navRow(c, kit.SymGlobe2, "DNS 服务器", dnsSummary(doc), dnsNeedsFix(doc, w.store.NetworkConfigs())), pageDNS)
		w.pageRow(navRow(c, kit.SymShieldCheck, "HTTPS 解密", httpsSummary(doc), false), pageHTTPS)
		w.pageRow(navRow(c, kit.SymGlobe, "地区识别", regionsSummary(doc), false), pageRegions)
		kit.SettingsRow(c, "测速 204 地址", "通过代理服务器 CONNECT 请求此地址", func() {
			value := doc.LatencyTestURL
			ui.Column(c).Width(300).Children(func() {
				if kit.Input(c, "latency-url", &value, "https://www.gstatic.com/generate_204").Label("测速 204 地址").Changed() {
					w.update(func(d *model.Doc) { d.LatencyTestURL = value })
				}
			})
		})
		kit.SettingsRow(c, "浏览器默认页面", "", func() {
			value := doc.BrowserHomeURL
			ui.Column(c).Width(300).Gap(4).Children(func() {
				if kit.Input(c, "browser-home", &value, model.DefaultBrowserHomeURL).Label("浏览器默认页面").Changed() {
					w.update(func(d *model.Doc) { d.BrowserHomeURL = strings.TrimSpace(value) })
				}
				if !model.ValidBrowserHomeURL(value) {
					kit.FormError(c, "请输入 HTTP 或 HTTPS 网页地址")
				}
			})
		})
		kit.SettingsRow(c, "浏览器允许 WebRTC", "", func() {
			on := doc.BrowserWebRTC
			if kit.Switch(c, &on, "浏览器允许 WebRTC").Changed() {
				w.update(func(d *model.Doc) { d.BrowserWebRTC = on })
			}
		})
		w.faviconRows(c, doc)
		kit.SettingsRow(c, "统一延迟", "", func() {
			on := !doc.LatencyPlain
			if kit.Switch(c, &on, "统一延迟").Changed() {
				err := w.store.Update(func(d *model.Doc) { d.LatencyPlain = !on })
				w.env.ToastError(err)
				if err == nil {
					// Old values were measured the other way.
					w.store.ResetLatency()
				}
			}
		})
	})
}

// dnsSummary describes Settings › DNS on its row: the servers and how many
// are enabled.
func dnsSummary(doc *model.Doc) string {
	enabled := 0
	for _, s := range doc.DNS.Servers {
		if s.Enabled {
			enabled++
		}
	}
	return fmt.Sprintf("查询策略、默认解析器与缓存 · %d 个服务器，%d 个已启用", len(doc.DNS.Servers), enabled)
}

// httpsSummary is Settings › HTTPS Decryption on its row: the switch and the
// hosts it covers.
func httpsSummary(doc *model.Doc) string {
	m := mitmSettingsOf(doc)
	if !m.Enabled {
		return "已关闭"
	}
	return fmt.Sprintf("已开启 · %d 个主机", len(hostList(m, false)))
}

// dnsNeedsFix reports a DNS server or the default domain resolver holding
// a broken reference, as the sidebar's warning dot on Settings.
func dnsNeedsFix(doc *model.Doc, library []*model.NetworkConfig) bool {
	return model.TabWarnings(doc, library)["settings"]
}

func (w *Workspace) generalGroup(c *ui.Context) {
	doc := w.store.Doc()
	kit.SettingsGroup(c, "通用", "", func() {
		kit.SettingsRow(c, "日志等级", "", func() {
			options := make([]kit.Option, len(logLevels))
			for i, level := range logLevels {
				options[i] = kit.Option{Value: level, Label: strings.ToUpper(level)}
			}
			value := doc.Log.Level
			if kit.Select(c, "log-level", &value, options, kit.SelectOptions{Width: 170, Label: "日志等级"}).Changed() {
				w.update(func(d *model.Doc) { d.Log.Level = value })
			}
		})
		kit.SettingsRow(c, "时区", "", func() {
			value := timefmt.ZoneName()
			if kit.SearchSelect(c, "display-time-zone", &value, w.zones.options(), kit.SearchSelectOptions{
				Width: 220, PopoverWidth: 300, Label: "时区", Placeholder: "搜索城市或时区",
			}).Changed() {
				w.env.AppPrefs.Save(timefmt.PrefZone, value)
				timefmt.SetZone(value)
				if w.env.AppPrefsChanged != nil {
					w.env.AppPrefsChanged()
				}
			}
		})
		if w.info != nil && w.info.OS == "windows" {
			status := "普通用户 · 使用 TUN 和 USB/IP 时，请以管理员身份启动 Kiko 或 kiko-server"
			if w.info.Elevated {
				status = "管理员"
			}
			kit.SettingsRow(c, "运行权限", status, nil)
		}
		// The helper belongs to this computer: a remote server has its own.
		if w.env.Shell != nil && w.env.Shell.Platform() == "darwin" && !w.env.Target.Remote {
			kit.SettingsRow(c, "TUN 特权助手", w.helperText()+"\n用于创建 TUN 网络接口；安装后重新应用配置", func() {
				kit.Button(c, w.helperAction(), kit.ButtonOptions{Variant: kit.TextButton, Disabled: w.helperBusy}).
					Label("安装特权助手").OnClick(w.installHelper)
			})
		}
	})
}

// zoneOptions are the Time Zone choices, rebuilt only as timefmt.Zones or the
// system's offset change (each hour).
type zoneOptions struct {
	zones  []timefmt.Zone
	system string
	list   []kit.Option
}

// options are "Follow system" and the IANA zones, each with its UTC offset.
func (z *zoneOptions) options() []kit.Option {
	zones, system := timefmt.Zones(), timefmt.SystemOffset()
	if z.list != nil && system == z.system && len(zones) == len(z.zones) && (len(zones) == 0 || &zones[0] == &z.zones[0]) {
		return z.list
	}
	list := make([]kit.Option, 0, len(zones)+1)
	list = append(list, kit.Option{Value: "", Label: "跟随系统", Detail: system})
	for _, zone := range zones {
		list = append(list, kit.Option{Value: zone.Name, Label: zone.City, Detail: zone.Offset})
	}
	z.zones, z.system, z.list = zones, system, list
	return list
}

// defaultClosedLimit is the server's closed connection limit until set.
const defaultClosedLimit = 1000

var closedLimits = []int{500, 1000, 2000, 5000, 10000, 50000}

// connectionsGroup keeps the connection monitor's closed records: the most
// recent ones, or all of them with the switch off.
func (w *Workspace) connectionsGroup(c *ui.Context) {
	settings := w.connections
	if settings == nil {
		return
	}
	limit := settings.ClosedLimit
	if limit > 0 {
		w.connectionsLast = limit
	}
	kit.SettingsGroup(c, "连接记录", "", func() {
		description := "关闭后保留全部已关闭连接，记录会持续占用服务端内存"
		if limit > 0 {
			description = fmt.Sprintf("只保留最近关闭的 %s 条连接", kit.Count(limit))
		}
		kit.SettingsRow(c, "自动清理已关闭连接", description, func() {
			on := limit > 0
			if kit.Switch(c, &on, "自动清理已关闭连接").Disabled(w.connectionsBusy).Changed() {
				next := 0
				if on {
					next = w.connectionsLast
				}
				w.saveConnections(next)
			}
		})
		if limit <= 0 {
			return
		}
		kit.SettingsRow(c, "保留数量", "", func() {
			values := closedLimits
			if !slices.Contains(values, limit) {
				values = append(slices.Clone(values), limit)
				slices.Sort(values)
			}
			options := make([]kit.Option, len(values))
			for i, n := range values {
				label := kit.Count(n) + " 条"
				if n == defaultClosedLimit {
					label += "（默认）"
				}
				options[i] = kit.Option{Value: strconv.Itoa(n), Label: label}
			}
			value := strconv.Itoa(limit)
			if kit.Select(c, "closed-limit", &value, options, kit.SelectOptions{Width: 170, Label: "保留数量"}).Changed() {
				if n, err := strconv.Atoi(value); err == nil {
					w.saveConnections(n)
				}
			}
		})
	})
}

// saveConnections sets the closed connection limit, 0 for none; the row
// shows the new value at once and the saved one after.
func (w *Workspace) saveConnections(limit int) {
	previous := *w.connections
	w.connections = &api.ConnectionSettings{ClosedLimit: limit}
	w.connectionsBusy = true
	env.Go(w.env, func(ctx context.Context) (api.ConnectionSettings, error) {
		return w.env.API.SetConnectionSettings(ctx, api.ConnectionSettings{ClosedLimit: limit})
	}, func(settings api.ConnectionSettings, err error) {
		w.connectionsBusy = false
		if err != nil {
			w.connections = &previous
			w.env.ToastError(err)
			return
		}
		w.connections = &settings
	})
}

// serversGroup opens the server list and this server's remote access.
func (w *Workspace) serversGroup(c *ui.Context) {
	kit.SettingsGroup(c, "服务器", "", func() {
		if dir, ok := w.env.Directory(); ok {
			description := fmt.Sprintf("本机和 %d 台远程服务器，每台在独立窗口中打开", len(dir.Servers())-1)
			w.pageRow(navRow(c, kit.SymServer, "服务器", description, false), pageServers)
		}
		description := "允许其他设备连接「" + targetName(w.env.Target) + "」"
		if status := w.remote.status; status != nil {
			description = remoteState(status)
		}
		w.pageRow(navRow(c, kit.SymGlobe, "远程访问", description, false), pageRemote)
	})
}

// formatLogSize is a size in B, KB, MB or GB with one decimal.
func formatLogSize(bytes int64) string {
	switch {
	case bytes < 1024:
		return fmt.Sprintf("%d B", bytes)
	case bytes < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	case bytes < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(bytes)/(1024*1024*1024))
}

// loadLogSize refreshes the total shown on the cleanup row.
func (w *Workspace) loadLogSize() {
	env.Go(w.env, func(ctx context.Context) (api.LogSize, error) {
		return w.env.API.LogSize(ctx)
	}, func(size api.LogSize, err error) {
		if err == nil {
			w.logSize = &size
		}
	})
}

func (w *Workspace) logsGroup(c *ui.Context) {
	kit.SettingsGroup(c, "日志管理", "", func() {
		label := "清理历史日志"
		if w.logBusy {
			label = "正在处理…"
		}
		description := "清理历史轮转文件，保留当前日志"
		if w.logSize != nil {
			description = "日志共 " + formatLogSize(w.logSize.Total)
		}
		row := navRow(c, kit.SymTrash2, label, description, false).Disabled(w.logBusy)
		if row.Clicked() {
			w.prepareLogCleanup()
		}
		message := ""
		if w.logCleanup != nil {
			message = fmt.Sprintf("将删除 %d 个历史日志文件，释放约 %s。当前日志会保留。", w.logCleanup.Files, formatLogSize(w.logCleanup.Bytes))
			if w.logCleanup.SystemBytes > 0 {
				message = fmt.Sprintf("将清理 %d 个日志文件并清空 TUN 核心日志，释放约 %s。", w.logCleanup.Files, formatLogSize(w.logCleanup.Bytes))
			}
		}
		kit.ConfirmPopover(c, row, &w.logConfirm, kit.ConfirmOptions{Title: "清理历史日志", Message: message, Confirm: "清理", Danger: true,
			Run: w.cleanupLogs})
		if !w.logConfirm.Open && !w.logConfirm.Busy {
			w.logCleanup = nil
		}
		w.coreLogRow(c)
		// A remote server's logs are on its host; the logs page exports them.
		if w.env.Shell != nil && !w.env.Target.Remote && navRow(c, kit.SymFolderOpen, "打开日志目录", "", false).Clicked() {
			w.env.Run(func(context.Context) error { return w.env.Shell.OpenLogsDirectory() }, func(err error) {
				w.env.ToastError(err)
			})
		}
	})
}

// coreLogRow is the TUN core log under /var/log: its path and size with
// rotated copies, reveal and Console on this Mac, and clearing through the
// helper. Without that log (other systems, never used TUN) it hides.
func (w *Workspace) coreLogRow(c *ui.Context) {
	if w.logSize == nil {
		return
	}
	path, size, ok := w.logSize.CoreLog()
	if !ok {
		return
	}
	local := w.env.Shell != nil && !w.env.Target.Remote && w.env.Shell.Platform() == "darwin"
	kit.SettingsRow(c, "核心日志", path+" · "+formatLogSize(size), func() {
		if local {
			small := kit.IconButtonOptions{Size: kit.SmallControl, IconSize: 15}
			if kit.IconButton(c, kit.SymFolderOpen, "在 Finder 中显示", small).Clicked() {
				w.env.Run(func(context.Context) error { return w.env.Shell.RevealPath(path) }, w.env.ToastError)
			}
			if kit.IconButton(c, kit.SymFileText, "用控制台打开", small).Clicked() {
				w.env.Run(func(context.Context) error { return w.env.Shell.OpenInConsole(path) }, w.env.ToastError)
			}
		}
		clearButton := kit.Button(c, "清空", kit.ButtonOptions{Small: true, Disabled: size == 0 || w.coreConfirm.Busy})
		if clearButton.Clicked() {
			w.coreConfirm.Open = true
		}
		kit.ConfirmPopover(c, clearButton, &w.coreConfirm, kit.ConfirmOptions{Title: "清空核心日志",
			Message: fmt.Sprintf("将清空 TUN 核心日志与特权助手日志，释放约 %s。", formatLogSize(size)),
			Confirm: "清空", Danger: true, Run: w.clearCoreLog})
	})
}

func (w *Workspace) clearCoreLog(done func(error)) {
	env.Go(w.env, func(ctx context.Context) (api.LogHistory, error) {
		return w.env.API.ClearSystemLogs(ctx)
	}, func(result api.LogHistory, err error) {
		w.loadLogSize()
		if err != nil {
			done(fmt.Errorf("%s", errorText(err, "操作失败")))
			return
		}
		w.env.Toast("已清空核心日志，释放 " + formatLogSize(result.Bytes))
		done(nil)
	})
}

func (w *Workspace) prepareLogCleanup() {
	if w.logBusy {
		return
	}
	w.logBusy = true
	env.Go(w.env, func(ctx context.Context) (api.LogHistory, error) {
		return w.env.API.LogHistory(ctx)
	}, func(history api.LogHistory, err error) {
		w.logBusy = false
		switch {
		case err != nil:
			w.env.Toast(errorText(err, "操作失败"))
		case history.Files == 0:
			w.env.Toast("历史日志已清理完毕")
		default:
			w.logCleanup = &history
			w.logConfirm.Open = true
		}
	})
}

func (w *Workspace) cleanupLogs(done func(error)) {
	if w.logCleanup == nil {
		done(nil)
		return
	}
	token := w.logCleanup.Token
	w.logBusy = true
	env.Go(w.env, func(ctx context.Context) (api.LogHistory, error) {
		return w.env.API.CleanupLogHistory(ctx, token)
	}, func(result api.LogHistory, err error) {
		w.logBusy = false
		if err != nil {
			done(fmt.Errorf("%s", errorText(err, "操作失败")))
			return
		}
		toast := fmt.Sprintf("已清理 %d 个日志文件，释放 %s", result.Files, formatLogSize(result.Bytes))
		if result.Warning != "" {
			toast += "；" + result.Warning
		}
		w.env.Toast(toast)
		w.logCleanup = nil
		w.loadLogSize()
		done(nil)
	})
}

func (w *Workspace) proxyGroup(c *ui.Context, doc *model.Doc) {
	host := ""
	if w.proxy != nil {
		host = w.proxy.Host
	}
	endpoint, hasEndpoint := proxyEndpointOf(doc, host)
	tun := hasEnabledTun(doc)
	available := hasEndpoint && !tun
	address := ""
	if hasEndpoint {
		address = formatHostPort(endpoint)
	}
	hint := "需要启用一个 Mixed 入站"
	switch {
	case tun:
		hint = "已启用 TUN 入站，系统代理不可用"
	case hasEndpoint:
		hint = address
	}
	kit.SettingsGroup(c, "系统代理", "", func() {
		if w.proxy != nil && w.proxy.Supported {
			kit.SettingsRow(c, "系统代理", hint, func() {
				kit.IconButton(c, kit.SymSettings2, "系统代理设置", kit.IconButtonOptions{}).OnClick(func() {
					w.proxyDialog.open(*w.proxy)
				})
				on := w.proxy.Enabled && available
				sw := kit.Switch(c, &on, "系统代理")
				if !available {
					sw.Disabled(true).Opacity(0.5)
				} else if sw.Changed() {
					w.toggleSystemProxy(on)
				}
			})
		}
		row := navRow(c, kit.SymClipboardCopy, "复制系统代理", "", false).Disabled(!hasEndpoint)
		if !hasEndpoint {
			row.Opacity(0.4)
			return
		}
		row.PopoverMenuButton(func(m *ui.Menu) {
			for _, shell := range proxyShells {
				if m.Item("复制为 " + shell.Label).Chosen() {
					c.WriteClipboard(shellCommand(shell.Value, endpoint))
					w.env.Toast("已复制 " + shell.Label + " 命令")
				}
			}
		})
	})
}

func (w *Workspace) toggleSystemProxy(enabled bool) {
	if w.proxy == nil {
		return
	}
	settings := w.proxy.SystemProxySettings
	settings.Enabled = enabled
	if settings.Bypass == nil {
		settings.Bypass = []string{}
	}
	w.saveProxy(settings, func(err error) {
		if err != nil {
			w.env.Toast(errorText(err, "操作失败"))
		}
	})
}

func (w *Workspace) backupGroup(c *ui.Context) {
	text, tone := syncSummary(w.sync, time.Now())
	kit.SettingsGroup(c, "备份与分享", "", func() {
		if navRow(c, kit.SymCloudUpload, "加密备份与同步", text, tone == syncError).Clicked() {
			w.backup.open()
		}
		w.shareOriginRow(c)
	})
}

func (w *Workspace) aboutGroup(c *ui.Context) {
	if w.info == nil {
		return
	}
	kit.SettingsGroup(c, "关于", "", func() {
		kit.SettingsRow(c, "kiko-server", w.info.OS+" / "+w.info.Arch, func() {
			if w.info.Outdated() {
				ui.Text(c, "服务端版本过旧，请升级").FontSize(kit.FontCaption).TextColor(kit.T(c).Warning).Shrink(0)
			}
			ui.Text(c, w.info.Version).FontSize(kit.FontBody).TextColor(kit.T(c).Label2).Selectable()
		})
		core := w.info.SingBox.Version
		if core == "" {
			core = "未安装"
		}
		kit.SettingsRow(c, "kiko-core", w.info.SingBox.Path, func() {
			ui.Text(c, coreVersion(core)).FontSize(kit.FontBody).TextColor(kit.T(c).Label2).Selectable().MaxWidth(320)
		})
	})
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// coreVersion is the first line of the core's version output without the
// program name the core prints ("sing-box version 1.14.4" reads 1.14.4).
func coreVersion(s string) string {
	line := firstLine(s)
	if _, version, ok := strings.Cut(line, " version "); ok && version != "" {
		return version
	}
	return line
}
