package endpoints

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// tunnelSheet shows an OpenConnect or OpenVPN endpoint's state and
// answers its authentication challenges.
type tunnelSheet struct {
	s *Section

	// challenge is the endpoint and challenge the form answers; values
	// reset with it and as the sheet closes.
	challenge string
	values    map[string]string
	username  string
	password  string
	secret    string
	busy      bool
}

var reSecretField = regexp.MustCompile(`(?i)pass|secret|token|otp`)

func (t *tunnelSheet) run(action func(ctx context.Context) error) {
	t.busy = true
	t.s.env.Run(action, func(err error) {
		t.busy = false
		if err != nil {
			message := err.Error()
			if message == "" {
				message = "操作失败"
			}
			t.s.env.Toast(message)
		}
	})
}

func (t *tunnelSheet) view(c *ui.Context, tag, kind string) {
	open := true
	var state, stateText, errText string
	var info *api.TunnelInfo
	var known bool
	challengeID := ""
	if kind == "openconnect" {
		if st := t.s.rt.openconnect[tag]; st != nil {
			known, state, stateText, errText, info = true, st.State, st.StateText, st.Error, st.TunnelInfo
			if st.AuthChallenge != nil {
				challengeID = st.AuthChallenge.ID
			}
		}
	} else if st := t.s.rt.openvpn[tag]; st != nil {
		known, state, stateText, errText, info = true, st.State, st.StateText, st.Error, st.TunnelInfo
		if st.Challenge != nil {
			challengeID = st.Challenge.ID
		}
	}
	if key := kind + "\x00" + tag + "\x00" + challengeID; key != t.challenge {
		t.challenge = key
		t.values = map[string]string{}
		t.password, t.secret = "", ""
		t.username = ""
		if kind == "openvpn" && challengeID != "" {
			t.username = t.s.rt.openvpn[tag].Challenge.Username
		}
	}
	kit.Dialog(c, &open, tag, 0, func() {
		if !known {
			note(c, kit.Neutral, "暂无运行状态：需要核心正在运行且已应用包含该端点的配置。")
			return
		}
		tone := toneOf(state, errText)
		ui.Row(c).Gap(8).Children(func() {
			statusDot(c, tone)
			ui.Text(c, firstOf(stateText, state, "未知")).FontSize(15).FontWeight(500)
		})
		if errText != "" {
			note(c, kit.DangerTone, errText)
		}
		if info != nil {
			tunnelInfoRows(c, info)
		}
		if challengeID != "" {
			if kind == "openconnect" {
				t.openConnectForm(c, tag, t.s.rt.openconnect[tag].AuthChallenge)
			} else {
				t.openVPNForm(c, tag, t.s.rt.openvpn[tag].Challenge)
			}
		}
	}, nil)
	if !open {
		t.s.control = control{}
		t.forget()
	}
}

// forget drops what the form holds, typed secrets included.
func (t *tunnelSheet) forget() {
	t.challenge, t.values = "", nil
	t.username, t.password, t.secret = "", "", ""
}

func tunnelInfoRows(c *ui.Context, info *api.TunnelInfo) {
	list := func(values []string) string {
		if len(values) == 0 {
			return "—"
		}
		return strings.Join(values, "，")
	}
	card(c, func() {
		if info.Server != "" {
			infoText(c, "服务器", info.Server)
		}
		if transport := firstOf(info.Transport, info.Network); transport != "" {
			infoText(c, "传输", transport)
		}
		infoText(c, "IPv4", list(info.IPv4))
		if len(info.IPv6) > 0 {
			infoText(c, "IPv6", list(info.IPv6))
		}
		infoText(c, "DNS", list(info.DNS))
		if info.MTU != 0 {
			infoText(c, "MTU", strconv.Itoa(info.MTU))
		}
		if info.Cipher != "" {
			infoText(c, "加密", info.Cipher)
		}
	})
}

func (t *tunnelSheet) openConnectForm(c *ui.Context, tag string, ch *api.OpenConnectChallenge) {
	k := kit.T(c)
	client := t.s.env.API
	if ch.Browser != nil {
		note(c, kit.WarningTone, "该认证需要在浏览器中完成（SSO），Kiko 暂不支持，请使用 kiko-core 命令行完成登录。")
		ui.Row(c).Children(func() {
			kit.Button(c, "取消认证", kit.ButtonOptions{Disabled: t.busy}).OnClick(func() {
				t.run(func(ctx context.Context) error { return client.OpenConnectCancel(ctx, tag, ch.ID) })
			})
		})
		return
	}
	var fields []api.OpenConnectField
	if ch.Form != nil {
		fields = ch.Form.Fields
	}
	valueOf := func(f api.OpenConnectField) string {
		if v, ok := t.values[f.SubmissionKey]; ok {
			return v
		}
		return f.Value
	}
	submit := func() {
		submitted := map[string]string{}
		for _, f := range fields {
			submitted[f.SubmissionKey] = valueOf(f)
		}
		t.run(func(ctx context.Context) error { return client.OpenConnectAuth(ctx, tag, ch.ID, submitted) })
	}
	ui.Column(c.Key(ch.ID)).Gap(12).Shrink(0).Children(func() {
		if ch.Banner != "" {
			note(c, kit.Neutral, ch.Banner)
		}
		if ch.Message != "" {
			ui.Text(c, ch.Message).FontSize(14).TextColor(k.Label2).Wrap()
		}
		if ch.Error != "" {
			note(c, kit.DangerTone, ch.Error)
		}
		for _, f := range fields {
			if strings.ToLower(f.Kind) == "hidden" {
				continue
			}
			label := firstOf(f.Label, f.Name, f.SubmissionKey)
			ui.Column(c.Key(f.SubmissionKey)).Gap(4).Children(func() {
				ui.Text(c, label).FontSize(kit.FontBody).TextColor(k.Label3)
				value := valueOf(f)
				if len(f.Options) > 0 {
					options := make([]kit.Option, len(f.Options))
					for i, o := range f.Options {
						options[i] = kit.Option{Value: o.Value, Label: firstOf(o.Label, o.Value)}
					}
					if kit.Select(c, "value", &value, options, kit.SelectOptions{Label: label}).FillWidth().Changed() {
						t.values[f.SubmissionKey] = value
					}
					return
				}
				input := kit.Input(c, "value", &value, "").Label(label)
				if reSecretField.MatchString(f.Kind + f.Name) {
					input.Password()
				}
				if input.Changed() {
					t.values[f.SubmissionKey] = value
				}
				if input.Shortcut(0, ui.KeyEnter) {
					submit()
				}
			})
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			kit.Button(c, "取消", kit.ButtonOptions{Disabled: t.busy}).OnClick(func() {
				t.run(func(ctx context.Context) error { return client.OpenConnectCancel(ctx, tag, ch.ID) })
			})
			kit.Button(c, "提交", kit.ButtonOptions{Variant: kit.Primary, Disabled: t.busy}).OnClick(submit)
		})
	})
}

func (t *tunnelSheet) openVPNForm(c *ui.Context, tag string, ch *api.OpenVPNChallenge) {
	k := kit.T(c)
	client := t.s.env.API
	submit := func() {
		answer := api.OpenVPNAnswer{Username: t.username, Password: t.password, Secret: t.secret}
		t.run(func(ctx context.Context) error { return client.OpenVPNChallengeAnswer(ctx, tag, ch.ID, answer) })
	}
	field := func(label string, value *string, secret bool) {
		ui.Column(c.Key(label)).Gap(4).Children(func() {
			ui.Text(c, label).FontSize(kit.FontBody).TextColor(k.Label3)
			input := kit.Input(c, "value", value, "").Label(label)
			if secret {
				input.Password()
			}
			if input.Shortcut(0, ui.KeyEnter) {
				submit()
			}
		})
	}
	ui.Column(c.Key(ch.ID)).Gap(12).Shrink(0).Children(func() {
		if ch.Message != "" {
			note(c, kit.Neutral, ch.Message)
		}
		if ch.PreviousError != "" {
			note(c, kit.DangerTone, ch.PreviousError)
		}
		field("用户名", &t.username, false)
		field("密码", &t.password, true)
		if ch.SecretMessage != "" {
			field(ch.SecretMessage, &t.secret, !ch.Echo)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			kit.Button(c, "取消", kit.ButtonOptions{Disabled: t.busy}).OnClick(func() {
				t.run(func(ctx context.Context) error { return client.OpenVPNCancel(ctx, tag, ch.ID) })
			})
			kit.Button(c, "提交", kit.ButtonOptions{Variant: kit.Primary, Disabled: t.busy}).OnClick(submit)
		})
	})
}
