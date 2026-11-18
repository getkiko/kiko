package settings

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// remotePage is Settings › Remote Access of the server this window shows: its TLS
// listener for other devices, pairing a new device, and the devices
// paired, which can be revoked. It works the same on the local server and on a remote
// server reached through it.
type remotePage struct {
	w *Workspace

	status    *api.RemoteStatus
	statusErr string
	listen    string
	saving    bool

	pairing    *api.Pairing
	pairingErr string
	pairBusy   bool

	devices    []api.RemoteDevice
	devicesErr string
	revoke     map[string]*kit.ConfirmState
}

func newRemotePage(w *Workspace) *remotePage {
	return &remotePage{w: w, revoke: map[string]*kit.ConfirmState{}}
}

// load reads the remote access state and the paired devices.
func (p *remotePage) load() {
	env.Go(p.w.env, func(ctx context.Context) (api.RemoteStatus, error) {
		return p.w.env.API.Remote(ctx)
	}, func(status api.RemoteStatus, err error) {
		if err != nil {
			p.statusErr = errorText(err, "读取失败")
			return
		}
		p.statusErr = ""
		p.adopt(status)
	})
	p.loadDevices()
}

func (p *remotePage) adopt(status api.RemoteStatus) {
	p.status = &status
	p.listen = status.Listen
}

func (p *remotePage) loadDevices() {
	env.Go(p.w.env, func(ctx context.Context) ([]api.RemoteDevice, error) {
		return p.w.env.API.RemoteDevices(ctx)
	}, func(devices []api.RemoteDevice, err error) {
		if err != nil {
			p.devicesErr = errorText(err, "读取失败")
			return
		}
		p.devices, p.devicesErr = devices, ""
	})
}

// save writes the remote access settings.
func (p *remotePage) save(enabled bool, listen string) {
	if p.saving {
		return
	}
	p.saving = true
	settings := api.RemoteSettings{Enabled: enabled, Listen: strings.TrimSpace(listen)}
	env.Go(p.w.env, func(ctx context.Context) (api.RemoteStatus, error) {
		return p.w.env.API.SetRemote(ctx, settings)
	}, func(status api.RemoteStatus, err error) {
		p.saving = false
		if err != nil {
			p.w.env.Toast(errorText(err, "保存失败"))
			return
		}
		p.adopt(status)
		if !status.Enabled {
			p.pairing = nil
		}
	})
}

func (p *remotePage) createPairing() {
	if p.pairBusy {
		return
	}
	p.pairBusy, p.pairingErr = true, ""
	env.Go(p.w.env, func(ctx context.Context) (api.Pairing, error) {
		return p.w.env.API.CreatePairing(ctx)
	}, func(pairing api.Pairing, err error) {
		p.pairBusy = false
		if err != nil {
			p.pairingErr = errorText(err, "生成配对码失败")
			return
		}
		p.pairing = &pairing
	})
}

func (p *remotePage) view(c *ui.Context) {
	k := kit.T(c)
	kit.Workspace(c, func() {
		kit.PreferencesForm(c, func() {
			backRow(c, p.w)
			kit.PaneTitle(c, "远程访问", "允许其他设备通过 TLS 加密连接控制「"+targetName(p.w.env.Target)+"」。配对过的设备拥有完全控制权限。", nil)
			if p.w.info != nil && !p.w.info.Capabilities["remoteAccess"] {
				kit.Notice(c, kit.WarningTone, "", "此服务端版本不支持远程访问，请先升级 kiko-server。", nil)
				return
			}
			if p.statusErr != "" && p.status == nil {
				kit.Notice(c, kit.DangerTone, "无法读取远程访问状态", p.statusErr, func() {
					kit.Button(c, "重试", kit.ButtonOptions{Small: true}).OnClick(p.load)
				})
				return
			}
			if p.status == nil {
				ui.Row(c).Gap(8).Padding(12, 14).Children(func() {
					ui.Spinner(c)
					ui.Text(c, "正在读取…").FontSize(kit.FontBody).TextColor(k.Label3)
				})
				return
			}
			p.accessGroup(c)
			if p.status.Enabled {
				p.pairingGroup(c)
			}
			p.devicesGroup(c)
		})
	})
}

func targetName(t env.Target) string {
	if t.Name == "" {
		return "本机"
	}
	return t.Name
}

func (p *remotePage) accessGroup(c *ui.Context) {
	k := kit.T(c)
	status := p.status
	footer := ""
	if !p.w.env.Target.Remote && p.w.env.Shell != nil && p.w.env.Shell.Platform() == "darwin" {
		footer = "开启后，macOS 可能询问是否允许 kiko-server 接受传入的网络连接，请选择允许。"
	}
	kit.SettingsGroup(c, "远程访问", footer, func() {
		kit.SettingsRow(c, "允许远程访问", remoteState(status), func() {
			on := status.Enabled
			sw := kit.Switch(c, &on, "允许远程访问")
			if p.saving {
				sw.Disabled(true)
			} else if sw.Changed() {
				p.save(on, p.listen)
			}
		})
		kit.SettingsRow(c, "监听地址", "端口默认 9443；远程设备需能访问此端口", func() {
			ui.Column(c).Width(150).Children(func() {
				in := kit.Input(c, "remote-listen", &p.listen, ":9443").Label("监听地址").FontFeatures("tnum")
				if in.Submitted() && p.listen != status.Listen {
					p.save(status.Enabled, p.listen)
				}
			})
			if strings.TrimSpace(p.listen) != status.Listen {
				kit.Button(c, "保存", kit.ButtonOptions{Small: true, Variant: kit.Primary, Disabled: p.saving}).
					Label("保存监听地址").OnClick(func() { p.save(status.Enabled, p.listen) })
			}
		})
		if fp, err := api.ParseFingerprint(status.Fingerprint); err == nil {
			kit.SettingsRow(c, "证书指纹", "", func() {
				ui.Text(c, api.GroupFingerprint(fp)).Font(kit.MonoFont).FontSize(11).TextColor(k.Label2).
					Selectable().Wrap().MaxWidth(330)
				kit.IconButton(c, kit.SymCopy, "复制证书指纹", kit.IconButtonOptions{}).OnClick(func() {
					c.WriteClipboard(status.Fingerprint)
					p.w.env.Toast("已复制证书指纹")
				})
			})
		}
		if status.Running && len(status.Addresses) > 0 {
			kit.SettingsRow(c, "访问地址", strings.Join(status.Addresses, "\n"), nil)
		}
	})
}

// remoteState is the listener's state in words.
func remoteState(s *api.RemoteStatus) string {
	switch {
	case s.Error != "":
		return "启动失败：" + s.Error
	case s.Running:
		return "运行中 · " + s.Listen
	case s.Enabled:
		return "正在启动…"
	}
	return "已关闭 · 仅本机可以访问"
}

func (p *remotePage) pairingGroup(c *ui.Context) {
	k := kit.T(c)
	kit.SettingsGroup(c, "配对新设备", "配对码只能使用一次。在新设备的 设置 › 服务器 › 添加服务器 中粘贴配对字符串，或填写地址和配对码。", func() {
		pairing := p.pairing
		var left time.Duration
		if pairing != nil {
			if expires, err := time.Parse(time.RFC3339, pairing.ExpiresAt); err == nil {
				left = time.Until(expires)
			}
		}
		if pairing == nil || left <= 0 {
			description := ""
			if pairing != nil {
				description = "配对码已过期"
			}
			kit.SettingsRow(c, "配对码", description, func() {
				kit.Button(c, "生成配对码", kit.ButtonOptions{Small: true, Variant: kit.Primary, Disabled: p.pairBusy}).OnClick(p.createPairing)
			})
			if p.pairingErr != "" {
				ui.Box(c).PaddingY(8).Children(func() { kit.FormError(c, p.pairingErr) })
			}
			return
		}
		// The countdown ticks each second.
		c.After(max(left%time.Second, 50*time.Millisecond))
		kit.SettingsRow(c, "配对码", fmt.Sprintf("剩余 %d:%02d", int(left.Minutes()), int(left.Seconds())%60), func() {
			ui.Text(c, pairing.Code).Font(kit.MonoFont).FontSize(20).FontWeight(600).Selectable()
			kit.Button(c, "重新生成", kit.ButtonOptions{Small: true, Disabled: p.pairBusy}).OnClick(p.createPairing)
		})
		ui.Column(c).PaddingY(10).Gap(6).Children(func() {
			ui.Row(c).Gap(8).Children(func() {
				ui.Text(c, "配对字符串").FontSize(kit.FontBody).Grow(1)
				kit.Button(c, "复制", kit.ButtonOptions{Small: true, Icon: kit.SymCopy}).Label("复制配对字符串").OnClick(func() {
					c.WriteClipboard(pairing.URI)
					p.w.env.Toast("已复制配对字符串")
				})
			})
			ui.Text(c, pairing.URI).Font(kit.MonoFont).FontSize(11).TextColor(k.Label2).Selectable().Wrap()
		})
	})
}

func (p *remotePage) devicesGroup(c *ui.Context) {
	k := kit.T(c)
	kit.SettingsGroup(c, "已配对设备", "吊销后，该设备会立即断开，需要重新配对才能再次连接。", func() {
		if p.devicesErr != "" {
			kit.SettingsRow(c, "无法读取设备列表", p.devicesErr, func() {
				kit.Button(c, "重试", kit.ButtonOptions{Small: true}).OnClick(p.loadDevices)
			})
			return
		}
		if len(p.devices) == 0 {
			ui.Text(c, "尚无已配对设备").FontSize(kit.FontBody).TextColor(k.Label3).PaddingY(16)
			return
		}
		self := ""
		if p.w.info != nil && p.w.info.Client != nil {
			self = p.w.info.Client.ID
		}
		for _, d := range p.devices {
			ui.Column(c.Key(d.ID)).Children(func() {
				description := deviceDetail(d, time.Now())
				name := d.Name
				if d.ID == self {
					name += "（此设备）"
				}
				kit.SettingsRow(c, name, description, func() {
					state := p.revoke[d.ID]
					if state == nil {
						state = &kit.ConfirmState{}
						p.revoke[d.ID] = state
					}
					b := kit.Button(c, "吊销", kit.ButtonOptions{Small: true, Variant: kit.TextButton}).Label("吊销 " + d.Name)
					if b.Clicked() {
						state.Open = true
					}
					message := "该设备将立即断开连接。"
					if d.ID == self {
						message = "这是当前设备：吊销后此窗口会断开，需要重新配对。"
					}
					kit.ConfirmPopover(c, b, state, kit.ConfirmOptions{Title: "吊销「" + d.Name + "」？", Message: message,
						Confirm: "吊销设备", Danger: true, Run: func(done func(error)) {
							id := d.ID
							p.w.env.Run(func(ctx context.Context) error { return p.w.env.API.RevokeDevice(ctx, id) }, func(err error) {
								if err != nil {
									done(fmt.Errorf("%s", errorText(err, "吊销失败")))
									return
								}
								done(nil)
								p.loadDevices()
							})
						}})
				})
			})
		}
	})
}

// deviceDetail is a device's last address and when it was last seen.
func deviceDetail(d api.RemoteDevice, now time.Time) string {
	var parts []string
	if d.LastAddr != "" {
		parts = append(parts, d.LastAddr)
	}
	if t, err := time.Parse(time.RFC3339, d.LastSeenAt); err == nil && !t.IsZero() {
		parts = append(parts, "最近连接 "+relativeTime(t, now))
	} else if t, err := time.Parse(time.RFC3339, d.CreatedAt); err == nil {
		parts = append(parts, "配对于 "+timefmt.Format(t, "2006-01-02"))
	}
	return strings.Join(parts, " · ")
}
