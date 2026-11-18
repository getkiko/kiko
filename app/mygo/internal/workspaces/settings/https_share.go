package settings

import (
	"context"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// shareState is the LAN download page as the settings show it.
type shareState struct {
	info *api.MITMShare
	err  string
	// writes counts the starts and stops, started and answered: a read
	// that one overtook does not show its older state.
	writes int
	busy   bool
	// addr is the candidate picked to start on.
	addr string
}

// loadShare reads the state of the LAN download page.
func (p *httpsPage) loadShare() {
	s := &p.share
	writes := s.writes
	env.Go(p.w.env, func(ctx context.Context) (api.MITMShare, error) {
		return p.w.env.API.MITMShare(ctx)
	}, func(info api.MITMShare, err error) {
		if writes != s.writes {
			return
		}
		if err != nil {
			s.err = errorText(err, "读取失败")
			return
		}
		s.info, s.err = &info, ""
	})
}

// shareAct starts or stops the page; the state shown follows its answer.
func (p *httpsPage) shareAct(work func(ctx context.Context, c *api.Client) (api.MITMShare, error)) {
	s := &p.share
	if s.busy {
		return
	}
	s.busy, s.err = true, ""
	s.writes++
	env.Go(p.w.env, func(ctx context.Context) (api.MITMShare, error) {
		return work(ctx, p.w.env.API)
	}, func(info api.MITMShare, err error) {
		s.busy = false
		s.writes++
		if err != nil {
			s.err = errorText(err, "操作失败")
			p.loadShare()
			return
		}
		s.info = &info
	})
}

// shareGroup is LAN Distribution: start and stop, and while it runs the link,
// the fingerprint, the time left, the downloads and the QR code.
func (p *httpsPage) shareGroup(c *ui.Context) {
	k := kit.T(c)
	s := &p.share
	info := s.info
	reason := shareReason(p.status)
	active := info != nil && info.Active && reason == ""
	var addresses []string
	if info != nil {
		addresses = info.Addresses
	}
	s.addr = shareAddress(addresses, s.addr)
	kit.SettingsGroup(c, "局域网分发", "", func() {
		kit.SettingsRow(c, "分发", reason, func() {
			if active {
				kit.Button(c, "停止", kit.ButtonOptions{Small: true, Disabled: s.busy}).Label("停止局域网分发").OnClick(func() {
					p.shareAct(func(ctx context.Context, cl *api.Client) (api.MITMShare, error) { return cl.MITMShareStop(ctx) })
				})
				return
			}
			if len(addresses) > 1 {
				options := make([]kit.Option, len(addresses))
				for i, a := range addresses {
					options[i] = kit.Option{Value: a, Label: a}
				}
				kit.Select(c, "mitm-share-address", &s.addr, options, kit.SelectOptions{Width: 190, Label: "分发地址", Disabled: s.busy || reason != ""})
			} else if len(addresses) == 1 && reason == "" {
				ui.Text(c, addresses[0]).FontSize(kit.FontBody).TextColor(k.Label2).FontFeatures("tnum").Selectable()
			}
			kit.Button(c, "开始", kit.ButtonOptions{Small: true, Variant: kit.Primary,
				Disabled: s.busy || reason != "" || len(addresses) == 0}).Label("开始局域网分发").OnClick(func() {
				address := s.addr
				p.shareAct(func(ctx context.Context, cl *api.Client) (api.MITMShare, error) {
					return cl.MITMShareStart(ctx, address)
				})
			})
		})
		if active {
			p.shareDetails(c, info)
		}
		if s.err != "" {
			ui.Box(c).PaddingY(8).Children(func() { kit.FormError(c, s.err) })
		}
	})
}

// shareDetails are the rows of a running page.
func (p *httpsPage) shareDetails(c *ui.Context, info *api.MITMShare) {
	k := kit.T(c)
	kit.SettingsRow(c, "链接", "", func() {
		ui.Text(c, info.URL).FontSize(kit.FontBody).Selectable().Wrap().MaxWidth(330)
		kit.IconButton(c, kit.SymCopy, "复制链接", kit.IconButtonOptions{}).OnClick(func() {
			c.WriteClipboard(info.URL)
			p.w.env.Toast("已复制链接")
		})
	})
	kit.SettingsRow(c, "指纹", "", func() {
		ui.Text(c, shareFingerprintText(info.Fingerprint)).Font(kit.MonoFont).FontSize(11).TextColor(k.Label2).
			FontFeatures("tnum").Selectable().Wrap().MaxWidth(330)
		kit.IconButton(c, kit.SymCopy, "复制分发指纹", kit.IconButtonOptions{}).OnClick(func() {
			c.WriteClipboard(info.Fingerprint)
			p.w.env.Toast("已复制指纹")
		})
	})
	kit.SettingsRow(c, "剩余", "", func() {
		text, expired := shareRemaining(info.ExpiresAt, time.Now())
		color := k.Label
		if expired {
			color = k.Label3
		} else {
			c.After(time.Second)
		}
		ui.Text(c, text).FontSize(kit.FontBody).TextColor(color).FontFeatures("tnum")
	})
	kit.SettingsRow(c, "下载", "", func() {
		ui.Text(c, kit.Count(info.Downloads)).FontSize(kit.FontBody).FontFeatures("tnum")
	})
	if m, ok := kit.ParseQR(info.QR); ok {
		kit.SettingsRow(c, "二维码", "", func() { kit.QRCode(c, m, "局域网分发二维码") })
	}
}
