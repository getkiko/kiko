package settings

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// httpsPollInterval is how often the certificate's state is read while
// the page shows.
const httpsPollInterval = 4 * time.Second

// httpsErrorShown is how long an invalid host stays beside its field.
const httpsErrorShown = 4 * time.Second

// httpsPage is Settings › HTTPS Decryption: the master switch, the root
// certificate, the hosts to decrypt, and the stored QUIC and body options.
type httpsPage struct {
	w *Workspace

	status    *api.MITMStatus
	statusErr string
	// writes counts the certificate actions, started and answered: a read
	// that an action overtook does not show its older state.
	writes int
	busy   bool
	// actionErr is the failure of the last certificate action.
	actionErr string

	enableConfirm kit.ConfirmState
	regenConfirm  kit.ConfirmState
	deleteConfirm kit.ConfirmState

	include, exclude, skipVerify hostField

	// skipBusy is set while a skip record is being cleared; skipErr is the
	// failure of the last clear or exclude.
	skipBusy     bool
	skipErr      string
	clearConfirm kit.ConfirmState

	share shareState

	persist persistState

	cancelPoll context.CancelFunc
}

// hostField is the add field of one host list.
type hostField struct {
	text  string
	err   string
	errAt time.Time
}

func newHTTPSPage(w *Workspace) *httpsPage { return &httpsPage{w: w} }

// start reads the state and polls it until stop.
func (p *httpsPage) start() {
	p.stop()
	ctx, cancel := context.WithCancel(context.Background())
	p.cancelPoll = cancel
	go func() {
		ticker := time.NewTicker(httpsPollInterval)
		defer ticker.Stop()
		for {
			p.w.env.Post(func() {
				if p.w.active && p.w.page == pageHTTPS {
					p.load()
				}
			})
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (p *httpsPage) stop() {
	if p.cancelPoll != nil {
		p.cancelPoll()
		p.cancelPoll = nil
	}
}

// load reads the state.
func (p *httpsPage) load() {
	writes := p.writes
	env.Go(p.w.env, func(ctx context.Context) (api.MITMStatus, error) {
		return p.w.env.API.MITMStatus(ctx)
	}, func(status api.MITMStatus, err error) {
		if err != nil {
			p.statusErr = errorText(err, "读取失败")
			return
		}
		if writes == p.writes {
			p.status, p.statusErr = &status, ""
		}
	})
	p.loadShare()
	p.loadPersist()
}

// act runs one certificate action; the state shown follows its answer.
func (p *httpsPage) act(work func(ctx context.Context, c *api.Client) (api.MITMStatus, error), done func(error)) {
	if p.busy {
		return
	}
	p.busy, p.actionErr = true, ""
	p.writes++
	env.Go(p.w.env, func(ctx context.Context) (api.MITMStatus, error) {
		return work(ctx, p.w.env.API)
	}, func(status api.MITMStatus, err error) {
		p.busy = false
		p.writes++
		if err == nil {
			// Once the root certificate is deleted or regenerated, the old
			// download link and QR code are withdrawn at once.
			changed := p.status != nil && p.status.CA.Fingerprint != status.CA.Fingerprint
			p.status = &status
			if changed && p.share.info != nil && p.share.info.Active {
				p.share.info = nil
				p.share.writes++
				p.loadShare()
			}
		} else {
			p.load()
		}
		if done != nil {
			done(err)
		}
	})
}

// simple runs an action whose failure shows under the certificate.
func (p *httpsPage) simple(work func(ctx context.Context, c *api.Client) (api.MITMStatus, error)) {
	p.act(work, func(err error) {
		if err != nil {
			p.actionErr = errorText(err, "操作失败")
		}
	})
}

// confirmed runs an action of a ConfirmPopover.
func (p *httpsPage) confirmed(work func(ctx context.Context, c *api.Client) (api.MITMStatus, error)) func(func(error)) {
	return func(done func(error)) {
		if p.busy {
			done(errors.New("正在处理"))
			return
		}
		p.act(work, func(err error) {
			if err != nil {
				done(errors.New(errorText(err, "操作失败")))
				return
			}
			p.turnOff()
			done(nil)
		})
	}
}

// export saves the certificate to a file the user picks.
func (p *httpsPage) export(format string) {
	if p.status == nil || p.w.env.Shell == nil {
		return
	}
	p.actionErr = ""
	name := mitmExportName(p.status.CA, format)
	filter := env.FileFilter{Name: format, Extensions: []string{format}}
	if i := slices.IndexFunc(exportFormats, func(e exportFormat) bool { return e.Format == format }); i >= 0 {
		filter.Name = exportFormats[i].Filter
	}
	p.w.env.Run(func(ctx context.Context) error {
		data, err := p.w.env.API.MITMExport(ctx, format)
		if err != nil {
			return err
		}
		_, err = p.w.env.Shell.SaveFile(name, data, filter)
		return err
	}, func(err error) {
		if err != nil {
			p.actionErr = errorText(err, "导出失败")
		}
	})
}

func (p *httpsPage) view(c *ui.Context) {
	k := kit.T(c)
	kit.Workspace(c, func() {
		kit.PreferencesForm(c, func() {
			backRow(c, p.w)
			kit.PaneTitle(c, "HTTPS 解密", "", nil)
			if p.status == nil {
				if p.statusErr != "" {
					kit.Notice(c, kit.DangerTone, "无法读取证书状态", p.statusErr, func() {
						kit.Button(c, "重试", kit.ButtonOptions{Small: true}).OnClick(p.load)
					})
					return
				}
				ui.Row(c).Gap(8).Padding(12, 14).Children(func() {
					ui.Spinner(c)
					ui.Text(c, "正在读取…").FontSize(kit.FontBody).TextColor(k.Label3)
				})
				return
			}
			p.switchGroup(c)
			p.certificateGroup(c)
			p.shareGroup(c)
			p.hostsGroup(c, "包含", false)
			p.hostsGroup(c, "排除", true)
			p.builtinGroup(c)
			p.failuresGroup(c)
			p.devicesGroup(c)
			p.upstreamGroup(c)
			p.optionsGroup(c)
			p.persistGroup(c)
		})
	})
}

func (p *httpsPage) switchGroup(c *ui.Context) {
	m := mitmSettingsOf(p.w.store.Doc())
	blocker := mitmBlocker(p.status)
	kit.SettingsGroup(c, "", "", func() {
		description := blocker
		kit.SettingsRow(c, "解密 HTTPS", description, func() {
			on := m.Enabled
			sw := kit.Switch(c, &on, "解密 HTTPS")
			// An enabled switch can always turn off.
			if !on && blocker != "" {
				sw.Disabled(true)
			}
			if sw.Changed() {
				switch {
				case !on:
					p.w.update(func(d *model.Doc) { editMITM(d, func(m *model.MITMSettings) { m.Enabled = false }) })
				case blocker != "":
				case !m.Acknowledged:
					p.enableConfirm.Open = true
				default:
					p.w.update(func(d *model.Doc) { editMITM(d, func(m *model.MITMSettings) { m.Enabled = true }) })
				}
			}
			kit.ConfirmPopover(c, sw, &p.enableConfirm, kit.ConfirmOptions{
				Title: "解密 HTTPS？", Message: "Kiko 将用自己的根证书代签所选主机的证书，并读取其中的明文流量。",
				Confirm: "开启", Run: func(done func(error)) {
					if mitmBlocker(p.status) != "" {
						done(errors.New(mitmBlocker(p.status)))
						return
					}
					done(p.w.store.Update(func(d *model.Doc) {
						editMITM(d, func(m *model.MITMSettings) { m.Acknowledged, m.Enabled = true, true })
					}))
				}})
		})
	})
}

func (p *httpsPage) certificateGroup(c *ui.Context) {
	k := kit.T(c)
	s := p.status
	ca := s.CA
	kit.SettingsGroup(c, "根证书", "", func() {
		if ca.State == caAbsent || ca.State == "" {
			kit.SettingsRow(c, "根证书", "未生成", func() {
				kit.Button(c, "生成", kit.ButtonOptions{Small: true, Variant: kit.Primary, Disabled: p.busy || !s.Supported}).
					Label("生成根证书").OnClick(func() {
					p.simple(func(ctx context.Context, cl *api.Client) (api.MITMStatus, error) { return cl.MITMGenerate(ctx) })
				})
			})
			p.actionError(c)
			return
		}
		kit.SettingsRow(c, "名称", "", func() {
			ui.Text(c, ca.CommonName).FontSize(kit.FontBody).Selectable()
		})
		kit.SettingsRow(c, "指纹", "", func() {
			text := ca.Fingerprint
			if fp, err := api.ParseFingerprint(text); err == nil {
				text = api.GroupFingerprint(fp)
			}
			ui.Text(c, text).Font(kit.MonoFont).FontSize(11).TextColor(k.Label2).Selectable().Wrap().MaxWidth(330)
			kit.IconButton(c, kit.SymCopy, "复制指纹", kit.IconButtonOptions{}).OnClick(func() {
				c.WriteClipboard(ca.Fingerprint)
				p.w.env.Toast("已复制指纹")
			})
		})
		kit.SettingsRow(c, "到期", "", func() {
			ui.Text(c, mitmDate(ca.NotAfter)).FontSize(kit.FontBody).FontFeatures("tnum")
		})
		kit.SettingsRow(c, "信任", "", func() {
			ui.Text(c, mitmTrustText(ca)).FontSize(kit.FontBody)
			if ca.TrustSupported {
				if ca.Trusted {
					kit.Button(c, "移除信任", kit.ButtonOptions{Small: true, Variant: kit.TextButton, Disabled: p.busy}).OnClick(func() {
						p.act(func(ctx context.Context, cl *api.Client) (api.MITMStatus, error) { return cl.MITMRemoveTrust(ctx) }, func(err error) {
							if err != nil {
								p.actionErr = errorText(err, "操作失败")
								return
							}
							p.turnOff()
						})
					})
				} else if ca.State == caReady {
					kit.Button(c, "安装信任", kit.ButtonOptions{Small: true, Variant: kit.Primary, Disabled: p.busy}).OnClick(func() {
						p.simple(func(ctx context.Context, cl *api.Client) (api.MITMStatus, error) { return cl.MITMInstallTrust(ctx) })
					})
				}
			}
		})
		kit.SettingsRow(c, "核心", "", func() {
			ui.Text(c, mitmCoreText(s)).FontSize(kit.FontBody)
		})
		kit.SettingsRow(c, "操作", "", func() {
			if ca.State == caReady {
				export := kit.Button(c, "导出", kit.ButtonOptions{Small: true, Disabled: p.busy, Icon: kit.SymDownload}).Label("导出根证书")
				export.PopoverMenuButton(func(menu *ui.Menu) {
					for _, e := range exportFormats {
						if menu.Item(e.Label).Chosen() {
							p.export(e.Format)
						}
					}
				})
			}
			regen := kit.Button(c, "重新生成", kit.ButtonOptions{Small: true, Disabled: p.busy && !p.regenConfirm.Busy}).Label("重新生成根证书")
			if regen.Clicked() {
				p.regenConfirm.Open = true
			}
			kit.ConfirmPopover(c, regen, &p.regenConfirm, kit.ConfirmOptions{Title: "重新生成根证书？",
				Message: "旧证书的信任将被移除，需要重新安装信任。", Confirm: "重新生成", Danger: true,
				Run: p.confirmed(func(ctx context.Context, cl *api.Client) (api.MITMStatus, error) { return cl.MITMRegenerate(ctx) })})
			del := kit.Button(c, "删除", kit.ButtonOptions{Small: true, Variant: kit.Danger, Disabled: p.busy && !p.deleteConfirm.Busy}).Label("删除根证书")
			if del.Clicked() {
				p.deleteConfirm.Open = true
			}
			kit.ConfirmPopover(c, del, &p.deleteConfirm, kit.ConfirmOptions{Title: "删除根证书？",
				Message: "证书与私钥将被删除，信任一并移除。", Confirm: "删除", Danger: true,
				Run: p.confirmed(func(ctx context.Context, cl *api.Client) (api.MITMStatus, error) { return cl.MITMDelete(ctx) })})
		})
		p.actionError(c)
	})
}

func (p *httpsPage) actionError(c *ui.Context) {
	if p.actionErr != "" {
		ui.Box(c).PaddingY(8).Children(func() { kit.FormError(c, p.actionErr) })
	}
}

// hostSpec describes one host list of the settings.
type hostSpec struct {
	title, key string
	field      *hostField
	// modules lists the hosts the modules add to this list; false for a
	// list they do not touch. exclude tells which.
	modules, exclude bool
	get              func(m model.MITMSettings) []string
	set              func(m *model.MITMSettings, list []string)
	// other is the list a host may not be in at once; nil for none.
	other func(m model.MITMSettings) []string
}

func (p *httpsPage) listSpec(exclude bool) hostSpec {
	spec := hostSpec{title: "包含", key: "mitm-include", field: &p.include,
		modules: true,
		get:     func(m model.MITMSettings) []string { return hostList(m, false) },
		set:     func(m *model.MITMSettings, l []string) { setHostList(m, false, l) },
		other:   func(m model.MITMSettings) []string { return hostList(m, true) }}
	if exclude {
		spec = hostSpec{title: "排除", key: "mitm-exclude", field: &p.exclude, modules: true, exclude: true,
			get:   func(m model.MITMSettings) []string { return hostList(m, true) },
			set:   func(m *model.MITMSettings, l []string) { setHostList(m, true, l) },
			other: func(m model.MITMSettings) []string { return hostList(m, false) }}
	}
	return spec
}

func (p *httpsPage) skipVerifySpec() hostSpec {
	return hostSpec{title: "跳过证书校验", key: "mitm-skipverify", field: &p.skipVerify,
		get: func(m model.MITMSettings) []string { return upstreamOf(m).SkipVerify },
		set: func(m *model.MITMSettings, l []string) {
			editUpstream(m, func(u *model.MITMUpstream) { u.SkipVerify = l })
		}}
}

// hostsGroup is the include or exclude list.
func (p *httpsPage) hostsGroup(c *ui.Context, title string, exclude bool) {
	p.hostListGroup(c, title, p.listSpec(exclude))
}

// hostListGroup is a host list: a row per pattern with its remove button,
// then the field that adds one.
func (p *httpsPage) hostListGroup(c *ui.Context, groupTitle string, spec hostSpec) {
	k := kit.T(c)
	doc := p.w.store.Doc()
	m := mitmSettingsOf(doc)
	list := spec.get(m)
	var fromModules []moduleHostRow
	if spec.modules {
		fromModules = moduleHostRows(doc, spec.exclude)
	}
	var other []string
	if spec.other != nil {
		other = spec.other(m)
	}
	field, key, title := spec.field, spec.key, spec.title
	kit.SettingsGroup(c, groupTitle, "", func() {
		var remove string
		for _, host := range list {
			ui.Column(c.Key(host)).Children(func() {
				kit.SettingsRow(c, host, "", func() {
					if kit.IconButton(c, kit.SymX, "移除 "+host, kit.IconButtonOptions{Size: kit.SmallControl, IconSize: 14}).Clicked() {
						remove = host
					}
				})
			})
		}
		for _, row := range fromModules {
			p.moduleHostLine(c, row)
		}
		if len(list) == 0 && len(fromModules) == 0 {
			ui.Text(c, "无").FontSize(kit.FontBody).TextColor(k.Label3).PaddingY(16)
		}
		kit.SettingsRow(c, "添加", "", func() {
			ui.Column(c).Width(240).Children(func() {
				in := kit.Input(c, key, &field.text, "example.com").Label(title + "主机名").FontFeatures("tnum")
				if in.Submitted() {
					p.addHost(field, spec, list, other)
				}
			})
			kit.Button(c, "添加", kit.ButtonOptions{Small: true, Disabled: field.text == ""}).Label("添加" + title + "主机名").
				OnClick(func() { p.addHost(field, spec, list, other) })
		})
		if field.err != "" {
			if left := httpsErrorShown - time.Since(field.errAt); left > 0 {
				c.After(left)
				ui.Box(c).PaddingY(8).Children(func() { kit.FormError(c, field.err) })
			} else {
				field.err = ""
			}
		}
		if remove != "" {
			p.w.update(func(d *model.Doc) {
				editMITM(d, func(m *model.MITMSettings) { spec.set(m, removeHost(spec.get(*m), remove)) })
			})
		}
	})
}

func (p *httpsPage) addHost(field *hostField, spec hostSpec, list, other []string) {
	next, err := addHost(list, other, field.text)
	if err != nil {
		field.err, field.errAt = err.Error(), time.Now()
		return
	}
	field.text, field.err = "", ""
	p.w.update(func(d *model.Doc) { editMITM(d, func(m *model.MITMSettings) { spec.set(m, next) }) })
}

// builtinGroup is the switch of the core's built-in exclusion table, with
// its size; it is absent when the core reports no table.
func (p *httpsPage) builtinGroup(c *ui.Context) {
	k := kit.T(c)
	if p.status.BuiltinCount <= 0 {
		return
	}
	m := mitmSettingsOf(p.w.store.Doc())
	kit.SettingsGroup(c, "", "", func() {
		kit.SettingsRow(c, "内置排除", "", func() {
			on := m.BuiltinExcludeOn()
			if count := builtinShown(p.status.BuiltinCount, m, on); count > 0 {
				ui.Text(c, kit.Count(count)).FontSize(kit.FontBody).TextColor(k.Label2).FontFeatures("tnum")
			}
			if kit.Switch(c, &on, "内置排除").Changed() {
				p.w.update(func(d *model.Doc) {
					editMITM(d, func(m *model.MITMSettings) {
						if on {
							m.BuiltinExclude = nil
						} else {
							off := false
							m.BuiltinExclude = &off
						}
					})
				})
			}
		})
	})
}

// failuresGroup lists the hosts whose interception failed or that the core
// skips. It is absent while there are none.
func (p *httpsPage) failuresGroup(c *ui.Context) {
	rows := skipRows(p.status, time.Now())
	if len(rows) == 0 {
		return
	}
	kit.SettingsGroup(c, "解密失败的主机", "", func() {
		for _, row := range rows {
			ui.Column(c.Key("skip:" + row.Host)).Children(func() {
				kit.SettingsRow(c, row.Host, row.Detail, func() {
					kit.Button(c, "加入排除", kit.ButtonOptions{Small: true, Disabled: p.skipBusy}).Label("加入排除 " + row.Host).
						OnClick(func() { p.excludeFailed(row.Host) })
					kit.Button(c, "清除", kit.ButtonOptions{Small: true, Variant: kit.TextButton, Disabled: p.skipBusy}).Label("清除 " + row.Host).
						OnClick(func() { p.clearSkip(row.Host, nil) })
				})
			})
		}
		kit.SettingsRow(c, "全部", "", func() {
			clearAll := kit.Button(c, "全部清除", kit.ButtonOptions{Small: true, Disabled: p.skipBusy && !p.clearConfirm.Busy}).Label("清除全部跳过记录")
			if clearAll.Clicked() {
				p.clearConfirm.Open = true
			}
			kit.ConfirmPopover(c, clearAll, &p.clearConfirm, kit.ConfirmOptions{Title: "清除全部记录？", Confirm: "清除", Danger: true,
				Run: func(done func(error)) { p.clearSkip("", done) }})
		})
		if p.skipErr != "" {
			ui.Box(c).PaddingY(8).Children(func() { kit.FormError(c, p.skipErr) })
		}
	})
}

// excludeFailed adds host to the exclude list and clears its record.
func (p *httpsPage) excludeFailed(host string) {
	var err error
	p.skipErr = ""
	probe := mitmSettingsOf(p.w.store.Doc())
	if err = excludeHost(&probe, host); err != nil {
		p.skipErr = err.Error()
		return
	}
	p.w.update(func(d *model.Doc) {
		editMITM(d, func(m *model.MITMSettings) { _ = excludeHost(m, host) })
	})
	p.clearSkip(host, nil)
}

// clearSkip forgets the core's record of host, of all hosts when empty.
func (p *httpsPage) clearSkip(host string, done func(error)) {
	if p.skipBusy {
		if done != nil {
			done(errors.New("正在处理"))
		}
		return
	}
	p.skipBusy, p.skipErr = true, ""
	p.w.env.Run(func(ctx context.Context) error { return p.w.env.API.ClearMITMSkip(ctx, host) }, func(err error) {
		p.skipBusy = false
		if err != nil {
			err = errors.New(errorText(err, "清除失败"))
			if done == nil {
				p.skipErr = err.Error()
			}
		}
		p.load()
		if done != nil {
			done(err)
		}
	})
}

// devicesGroup picks the gateway devices whose traffic is decrypted
// besides this machine's. It is absent while no device exists.
func (p *httpsPage) devicesGroup(c *ui.Context) {
	doc := p.w.store.Doc()
	if doc == nil || len(doc.Route.Devices) == 0 {
		return
	}
	m := mitmSettingsOf(doc)
	kit.SettingsGroup(c, "设备", "", func() {
		kit.SettingsRow(c, "本机", "", func() {
			on := !m.NoLocal
			if kit.Switch(c, &on, "解密本机").Changed() {
				p.w.update(func(d *model.Doc) { editMITM(d, func(m *model.MITMSettings) { m.NoLocal = !on && len(m.Devices) > 0 }) })
			}
		})
		for _, device := range doc.Route.Devices {
			name := firstText(device.Name, device.ID)
			ui.Column(c.Key("device:" + device.ID)).Children(func() {
				kit.SettingsRow(c, name, "", func() {
					on := slices.Contains(m.Devices, device.ID)
					if kit.Switch(c, &on, "解密 "+name).Changed() {
						p.w.update(func(d *model.Doc) { editMITM(d, func(m *model.MITMSettings) { setDeviceScope(m, device.ID, on) }) })
					}
				})
			})
		}
	})
}

// upstreamGroup holds the TLS options toward the real server.
func (p *httpsPage) upstreamGroup(c *ui.Context) {
	m := mitmSettingsOf(p.w.store.Doc())
	fingerprints := []string{model.MITMFingerprintNone, model.MITMFingerprintChrome, model.MITMFingerprintSafari, model.MITMFingerprintFirefox}
	kit.SettingsGroup(c, "上游", "", func() {
		kit.SettingsRow(c, "指纹", "", func() {
			current := cmp.Or(upstreamOf(m).Fingerprint, model.MITMFingerprintChrome)
			selected := max(0, slices.Index(fingerprints, current))
			if kit.Segmented(c, "mitm-fingerprint", &selected, kit.Segment{Label: "无"}, kit.Segment{Label: "Chrome"},
				kit.Segment{Label: "Safari"}, kit.Segment{Label: "Firefox"}).Changed() {
				fp := fingerprints[selected]
				p.w.update(func(d *model.Doc) {
					editMITM(d, func(m *model.MITMSettings) { editUpstream(m, func(u *model.MITMUpstream) { u.Fingerprint = fp }) })
				})
			}
		})
	})
	p.hostListGroup(c, "跳过证书校验", p.skipVerifySpec())
}

func (p *httpsPage) optionsGroup(c *ui.Context) {
	m := mitmSettingsOf(p.w.store.Doc())
	kit.SettingsGroup(c, "选项", "", func() {
		kit.SettingsRow(c, "QUIC", "", func() {
			selected := 0
			if m.QUIC == model.MITMQUICAllow {
				selected = 1
			}
			if kit.Segmented(c, "mitm-quic", &selected, kit.Segment{Label: "阻止"}, kit.Segment{Label: "放行"}).Changed() {
				mode := model.MITMQUICBlock
				if selected == 1 {
					mode = model.MITMQUICAllow
				}
				p.w.update(func(d *model.Doc) { editMITM(d, func(m *model.MITMSettings) { m.QUIC = mode }) })
			}
		})
		kit.SettingsRow(c, "保存正文", "", func() {
			on := m.SaveBody
			if kit.Switch(c, &on, "保存正文").Changed() {
				p.w.update(func(d *model.Doc) { editMITM(d, func(m *model.MITMSettings) { m.SaveBody = on }) })
			}
		})
	})
}

// turnOff switches decryption off after the certificate it relied on is
// gone or no longer trusted.
func (p *httpsPage) turnOff() {
	if mitmSettingsOf(p.w.store.Doc()).Enabled {
		p.w.update(func(d *model.Doc) { editMITM(d, func(m *model.MITMSettings) { m.Enabled = false }) })
	}
}
