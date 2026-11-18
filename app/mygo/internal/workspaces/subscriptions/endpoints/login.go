package endpoints

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Login ways of the Tailscale login dialog.
const (
	loginBrowser = "browser"
	loginKey     = "key"
)

// tailscaleLogin is the Tailscale endpoint's login dialog, opened from
// the button beside its auth key: authorize in the browser, which keeps
// the node key in the state directory, or enter an auth key.
type tailscaleLogin struct {
	open bool
	mode string
	key  string
}

// show opens the dialog on the way fields use.
func (l *tailscaleLogin) show(fields model.Fields) {
	l.open, l.key, l.mode = true, strings.TrimSpace(text(fields["auth_key"])), loginBrowser
	if l.key != "" {
		l.mode = loginKey
	}
}

func (l *tailscaleLogin) view(c *ui.Context, e *editor, endpoint *model.Endpoint) {
	if !l.open {
		return
	}
	k := kit.T(c)
	open := true
	kit.Dialog(c, &open, "登录 Tailscale", 460, func() {
		ui.Column(c).Gap(8).Shrink(0).Children(func() {
			l.option(c, loginBrowser, kit.SymGlobe, "浏览器授权")
			l.option(c, loginKey, kit.SymKeyRound, "输入 Auth Key")
		})
		ui.Column(c).Gap(10).Padding(4, 2, 0).Shrink(0).Children(func() {
			if l.mode == loginKey {
				ui.Row(c).Gap(8).Children(func() {
					kit.Input(c, "auth-key", &l.key, "tskey-auth-…").Label("Auth Key").Grow(1).MinWidth(0).AutoFocus()
					kit.Button(c, "获取密钥", kit.ButtonOptions{Variant: kit.TextButton, Icon: kit.SymExternalLink}).
						OnClick(func() { e.s.openURL(tailscaleKeysURL) })
				})
				return
			}
			l.authorization(c, k, e, endpoint)
		})
	}, func() {
		if kit.Button(c, "取消", kit.ButtonOptions{}).Clicked() {
			l.open = false
		}
		done := kit.Button(c, "完成", kit.ButtonOptions{Variant: kit.Primary, Disabled: l.mode == loginKey && strings.TrimSpace(l.key) == ""})
		if done.Clicked() {
			key := ""
			if l.mode == loginKey {
				key = strings.TrimSpace(l.key)
			}
			if key != strings.TrimSpace(text(endpoint.Fields["auth_key"])) {
				endpoint.Fields = cloneFields(endpoint.Fields)
				endpoint.Fields["auth_key"] = key
				e.generation++
			}
			l.open = false
		}
	})
	if !open {
		l.open = false
	}
}

// option is a login way: selected, a neutral fill, bold text and a blue
// check.
func (l *tailscaleLogin) option(c *ui.Context, mode string, icon kit.Symbol, title string) {
	k := kit.T(c)
	selected := l.mode == mode
	row := ui.ButtonBase(c.Key(mode)).Height(48).PaddingX(14).Gap(10).Radius(12).Justify(ui.Start).
		Border(1, k.Hairline).FontSize(kit.FontBody).TextColor(k.Label).Label(title).Selected(selected).Transition(kit.HoverMotion)
	switch {
	case selected:
		row.Background(k.Fill).Border(1, k.Fill).FontWeight(600)
	case row.Hovered():
		row.Background(k.Fill.Alpha(0.5))
	}
	row.Children(func() {
		kit.Icon(c, icon, 17).TextColor(k.Label2)
		ui.Text(c, title).Grow(1).MinWidth(0).SingleLine()
		if selected {
			kit.Icon(c, kit.SymCheck, 16).TextColor(k.AccentText)
		}
	})
	if row.Clicked() {
		l.mode = mode
	}
}

// authorization is the browser way's state: what the core reports of the
// saved endpoint, with its authorization page while it needs a login.
func (l *tailscaleLogin) authorization(c *ui.Context, k *kit.Tokens, e *editor, endpoint *model.Endpoint) {
	var status = e.s.rt.tailscaleStatus(endpoint.Tag)
	if e.isNew {
		status = nil
	}
	// line is a state's words after its symbol, the turning spinner
	// (kit.Spinner) while it waits.
	line := func(icon kit.Symbol, color ui.Color, words string) {
		ui.Row(c).Gap(8).MinHeight(30).Children(func() {
			if icon == "" {
				kit.SpinnerIn(c, 16, color)
			} else {
				kit.Icon(c, icon, 16).TextColor(color)
			}
			ui.Text(c, words).FontSize(kit.FontBody).TextColor(k.Label2).Wrap().Grow(1).MinWidth(0)
		})
	}
	switch {
	case status == nil:
		line(kit.SymGlobe, k.Label3, "保存并应用后在此授权")
	case status.BackendState == "Running":
		name := "已授权"
		if status.Self != nil {
			name += " · " + peerName(*status.Self)
		}
		line(kit.SymCircleCheck, k.Success, name)
	case status.AuthURL != "":
		line("", k.Label3, "等待授权")
		kit.Button(c, "打开授权页面", kit.ButtonOptions{Variant: kit.Primary, Icon: kit.SymExternalLink}).FillWidth().
			OnClick(func() { e.s.openURL(status.AuthURL) })
	default:
		line("", k.Label3, "正在获取授权页面")
	}
}

// systemHostname is the name Tailscale gives the server when the
// endpoint's hostname is empty, "" when the server's name is unknown.
func (s *Section) systemHostname() string {
	if info := s.env.Server.Info; info != nil {
		return tailnetHostname(info.ServerName)
	}
	return ""
}
