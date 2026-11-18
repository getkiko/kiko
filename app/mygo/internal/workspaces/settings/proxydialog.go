package settings

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// proxyDialog edits the system proxy's settings.
type proxyDialog struct {
	w *Workspace

	shown          bool
	status         api.SystemProxyStatus
	host           string
	pac, guard     bool
	interval       string
	defaultBypass  bool
	validateBypass bool
	custom         string
	saving         bool
}

func (d *proxyDialog) open(status api.SystemProxyStatus) {
	d.shown, d.status = true, status
	d.host, d.pac, d.guard = status.Host, status.PAC, status.Guard
	d.interval = strconv.FormatFloat(status.GuardInterval, 'f', -1, 64)
	d.defaultBypass, d.validateBypass = status.DefaultBypass, status.ValidateBypass
	d.custom = strings.Join(status.Bypass, "\n")
}

// settings checks the form and returns the settings to save.
func (d *proxyDialog) settings() (api.SystemProxySettings, string) {
	interval, err := strconv.Atoi(strings.TrimSpace(d.interval))
	if err != nil || interval < 1 || interval > 3600 {
		return api.SystemProxySettings{}, "代理守卫间隔需为 1–3600 秒"
	}
	return api.SystemProxySettings{
		Enabled: d.status.Enabled, Host: strings.TrimSpace(d.host), PAC: d.pac, Guard: d.guard,
		GuardInterval: float64(interval), DefaultBypass: d.defaultBypass, ValidateBypass: d.validateBypass,
		Bypass: splitBypass(d.custom),
	}, ""
}

func (d *proxyDialog) save() {
	if d.saving {
		return
	}
	settings, problem := d.settings()
	if problem != "" {
		d.w.env.Toast(problem)
		return
	}
	d.saving = true
	d.w.saveProxy(settings, func(err error) {
		d.saving = false
		if err != nil {
			d.w.env.Toast(errorText(err, "保存失败"))
			return
		}
		d.shown = false
	})
}

// proxyRow is a row of the dialog: a label, an optional hint shown as an
// info icon's tooltip, and the control.
func proxyRow(c *ui.Context, label, hint string, control func()) {
	k := kit.T(c)
	ui.Row(c).MinHeight(40).Gap(12).Children(func() {
		ui.Row(c).Grow(1).MinWidth(0).Gap(6).Children(func() {
			ui.Text(c, label).FontSize(kit.FontBody)
			if hint != "" {
				kit.Hint(c, kit.Icon(c, kit.SymInfo, 14).TextColor(k.Label3).Label(hint), hint)
			}
		})
		ui.Row(c).Gap(8).Shrink(0).Children(control)
	})
}

func (d *proxyDialog) view(c *ui.Context, doc *model.Doc) {
	if !d.shown {
		return
	}
	k := kit.T(c)
	endpoint, ok := proxyEndpointOf(doc, d.status.Host)
	address := "—"
	if ok {
		address = formatHostPort(endpoint)
	}
	autoHost := "127.0.0.1"
	if auto, ok := proxyEndpointOf(doc, ""); ok {
		autoHost = auto.Host
	}
	kit.Dialog(c, &d.shown, "系统代理设置", 0, func() {
		ui.Column(c).Padding(4, 12).Radius(12).Border(1, k.HairlineSoft).Shrink(0).Children(func() {
			ui.Text(c, "当前系统代理").FontSize(kit.FontCaption).TextColor(k.Label3).Margin(6, 0, 0)
			state := "未启用"
			if d.status.Enabled {
				state = "已启用"
			}
			proxyRow(c, "开启状态", "", func() { ui.Text(c, state).FontSize(kit.FontBody) })
			proxyRow(c, "服务地址", "", func() { ui.Text(c, address).FontSize(kit.FontBody).FontFeatures("tnum") })
		})
		proxyRow(c, "代理主机", "", func() {
			ui.Column(c).Width(160).Children(func() {
				kit.Input(c, "host", &d.host, "自动 · "+autoHost).Label("代理主机")
			})
		})
		ui.Text(c, "留空即自动：Mixed 入站监听某个具体 IP 时用该 IP，监听全部地址（0.0.0.0 / ::）时用 127.0.0.1。一般不用改。").
			FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
		proxyRow(c, "使用 PAC 模式", "", func() { kit.Switch(c, &d.pac, "使用 PAC 模式") })
		proxyRow(c, "系统代理守卫", "开启后会定时检查，防止其他代理软件修改系统代理", func() { kit.Switch(c, &d.guard, "系统代理守卫") })
		proxyRow(c, "代理守卫间隔", "", func() {
			ui.Column(c).Width(96).Children(func() {
				in := kit.Input(c, "interval", &d.interval, "").Label("代理守卫间隔")
				if !d.guard {
					in.Disabled(true).Opacity(0.5)
				}
			})
			ui.Text(c, "s").FontSize(kit.FontBody).TextColor(k.Label3)
		})
		proxyRow(c, "始终使用默认绕过", "", func() { kit.Switch(c, &d.defaultBypass, "始终使用默认绕过") })
		proxyRow(c, "验证代理绕过格式", "", func() { kit.Switch(c, &d.validateBypass, "验证代理绕过格式") })
		ui.Column(c).Gap(5).Shrink(0).Children(func() {
			ui.Text(c, "自定义绕过（每行一项）").FontSize(kit.FontTable).TextColor(k.Label3)
			kit.TextArea(c, "bypass", &d.custom, "例如：*.example.com", false).Label("自定义绕过").MinHeight(72)
		})
		ui.Column(c).Gap(6).Shrink(0).Children(func() {
			ui.Text(c, "当前绕过：").FontSize(kit.FontTable).TextColor(k.Label3)
			chips := effectiveBypass(d.defaultBypass, splitBypass(d.custom))
			ui.Row(c).Wrap().Gap(6).Children(func() {
				if len(chips) == 0 {
					ui.Text(c, "无").FontSize(kit.FontTable).TextColor(k.Label3)
				}
				for _, entry := range chips {
					ui.Row(c.Key(entry)).Children(func() { kit.Tag(c, entry, nil) })
				}
			})
		})
	}, func() {
		kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(func() { d.shown = false })
		kit.Button(c, "保存", kit.ButtonOptions{Variant: kit.Primary, Disabled: d.saving}).OnClick(d.save)
	})
}
