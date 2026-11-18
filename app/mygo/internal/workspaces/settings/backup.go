package settings

import (
	"context"
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// Views of the backup dialog besides the one its state decides.
const (
	backupAuto     = "auto"
	backupBackend  = "backend"
	backupPassword = "password"
)

// backupDialog configures, unlocks and runs the encrypted backup and sync,
// with a form per storage backend.
type backupDialog struct {
	w *Workspace

	shown      bool
	mode       string
	typ        string
	draft      api.SyncBackendConfig
	showErrors bool
	busy       string
	password   string
	confirm    string
	unlockErr  string
	reset      kit.ConfirmState

	worker        *api.SyncWorkerInfo
	workerLoading bool
	workerFailed  bool
	readmeOpen    bool
}

func (b *backupDialog) open() {
	b.shown, b.mode = true, backupAuto
	b.password, b.confirm, b.unlockErr, b.showErrors = "", "", "", false
	b.reset = kit.ConfirmState{}
	b.prepareDraft(b.currentType())
}

func (b *backupDialog) status() *api.SyncStatus { return b.w.sync }

func (b *backupDialog) state() string {
	if s := b.status(); s != nil {
		return s.State
	}
	return api.SyncUnconfigured
}

func (b *backupDialog) currentType() string {
	if s := b.status(); s != nil && s.Backend != nil {
		return s.Backend.Type
	}
	return api.SyncWebDAV
}

func (b *backupDialog) prepareDraft(typ string) {
	var current *api.SyncBackendConfig
	if s := b.status(); s != nil {
		current = s.Backend
	}
	b.typ = typ
	b.draft = withDefaults(current, typ)
	b.showErrors = false
}

func (b *backupDialog) openBackendEditor() {
	b.prepareDraft(b.currentType())
	b.mode = backupBackend
}

// run performs a sync request that answers the new status, toasting its
// failure, unless fail handles it.
func (b *backupDialog) run(key string, work func(ctx context.Context) (api.SyncStatus, error), ok func(), fail func(error)) {
	if b.busy != "" && key != "" {
		return
	}
	// A request without a key (auto sync, lock) leaves the busy state of
	// another one running alone.
	if key != "" {
		b.busy = key
	}
	env.Go(b.w.env, work, func(status api.SyncStatus, err error) {
		if key != "" && b.busy == key {
			b.busy = ""
		}
		if err != nil {
			if fail != nil {
				fail(err)
			} else {
				b.w.env.Toast(syncErrorMessage(err))
			}
			return
		}
		b.w.sync = &status
		if ok != nil {
			ok()
		}
	})
}

func (b *backupDialog) testConnection() {
	b.showErrors = true
	if len(validateBackend(b.draft).Errors) > 0 || b.busy != "" {
		return
	}
	backend := toWireBackend(b.draft)
	b.busy = "test"
	env.Go(b.w.env, func(ctx context.Context) (bool, error) {
		return b.w.env.API.SyncTest(ctx, backend)
	}, func(exists bool, err error) {
		b.busy = ""
		switch {
		case err != nil:
			b.w.env.Toast(syncErrorMessage(err))
		case exists:
			b.w.env.Toast("连接成功，云端已有加密备份")
		default:
			b.w.env.Toast("连接成功，云端还没有备份")
		}
	})
}

func (b *backupDialog) saveBackend() {
	b.showErrors = true
	if len(validateBackend(b.draft).Errors) > 0 {
		return
	}
	backend := toWireBackend(b.draft)
	var autoSync *bool
	if s := b.status(); s != nil {
		auto := s.AutoSync
		autoSync = &auto
	}
	b.run("save", func(ctx context.Context) (api.SyncStatus, error) {
		return b.w.env.API.SyncSaveConfig(ctx, backend, autoSync)
	}, func() {
		b.mode = backupAuto
		b.w.env.Toast("已保存备份位置")
	}, nil)
}

func (b *backupDialog) unlock(create bool) {
	if create {
		if problem := validateNewPassword(b.password, b.confirm); problem != "" {
			b.unlockErr = problem
			return
		}
	} else if b.password == "" {
		b.unlockErr = "请输入密码"
		return
	}
	b.unlockErr = ""
	password, confirm := b.password, b.confirm
	b.run("unlock", func(ctx context.Context) (api.SyncStatus, error) {
		return b.w.env.API.SyncUnlock(ctx, password, confirm, create)
	}, func() {
		b.password, b.confirm = "", ""
		if create {
			b.w.env.Toast("已创建加密备份")
		} else {
			b.w.env.Toast("已解锁，本机保存了解密密钥")
		}
	}, func(err error) { b.unlockErr = syncErrorMessage(err) })
}

func (b *backupDialog) syncNow() {
	if b.busy != "" {
		return
	}
	b.busy = "sync"
	type outcome struct {
		status api.SyncStatus
		result api.SyncResult
	}
	env.Go(b.w.env, func(ctx context.Context) (outcome, error) {
		status, result, err := b.w.env.API.SyncNow(ctx)
		return outcome{status, result}, err
	}, func(o outcome, err error) {
		b.busy = ""
		if err != nil {
			b.w.env.Toast(syncErrorMessage(err))
			return
		}
		b.w.sync = &o.status
		b.w.env.Toast("同步完成：" + formatSyncResult(&o.result))
	})
}

func (b *backupDialog) changePassword() {
	if problem := validateNewPassword(b.password, b.confirm); problem != "" {
		b.unlockErr = problem
		return
	}
	b.unlockErr = ""
	password, confirm := b.password, b.confirm
	b.run("password", func(ctx context.Context) (api.SyncStatus, error) {
		return b.w.env.API.SyncChangePassword(ctx, password, confirm)
	}, func() {
		b.password, b.confirm, b.mode = "", "", backupAuto
		b.w.env.Toast("密码已修改。其他设备需要用新密码重新解锁")
	}, nil)
}

func (b *backupDialog) title() string {
	switch {
	case b.mode == backupBackend || b.state() == api.SyncUnconfigured:
		return "备份位置"
	case b.mode == backupPassword:
		return "修改备份密码"
	}
	return "配置备份"
}

func (b *backupDialog) view(c *ui.Context) {
	if !b.shown {
		return
	}
	kit.Dialog(c, &b.shown, b.title(), 520, func() {
		state := b.state()
		switch {
		case b.mode == backupBackend || state == api.SyncUnconfigured:
			b.backendView(c)
		case state == api.SyncLocked:
			b.lockedView(c)
		case state == api.SyncNeedsSetup:
			b.setupView(c)
		case b.mode == backupPassword:
			b.passwordView(c)
		default:
			b.unlockedView(c)
		}
	}, nil)
}

func note(c *ui.Context, tone kit.Tone, text string) {
	kit.Notice(c, tone, "", text, nil)
}

// box is a bordered card of the dialog with an icon and a message.
func box(c *ui.Context, icon kit.Symbol, iconColor ui.Color, text string) {
	k := kit.T(c)
	ui.Row(c).Gap(10).Padding(12).Radius(14).Border(1, k.HairlineSoft).Background(k.Surface).AlignItems(ui.Start).Shrink(0).Children(func() {
		kit.Icon(c, icon, 18).TextColor(iconColor).Margin(1, 0, 0)
		ui.Text(c, text).FontSize(kit.FontBody).TextColor(k.Label2).Wrap().Grow(1).MinWidth(0)
	})
}

func (b *backupDialog) busyLabel(key, idle, busy string) string {
	if b.busy == key {
		return busy
	}
	return idle
}

func (b *backupDialog) backendView(c *ui.Context) {
	note(c, kit.AccentTone, "把所有配置档案加密后备份到你自己的存储里，多台设备用同一个密码即可同步。存储方只能看到加密后的数据，看不到订阅链接、节点密码等内容。")
	b.picker(c)
	validation := validateBackend(b.draft)
	errs := validation.Errors
	if !b.showErrors {
		errs = map[string]string{}
	}
	b.backendForm(c, errs, validation.Warnings)
	ui.Row(c).Gap(8).Justify(ui.End).Shrink(0).Children(func() {
		if b.state() != api.SyncUnconfigured {
			kit.Button(c, "返回", kit.ButtonOptions{}).OnClick(func() { b.mode = backupAuto })
		}
		kit.Button(c, b.busyLabel("test", "测试连接", "正在测试…"), kit.ButtonOptions{Disabled: b.busy != ""}).OnClick(b.testConnection)
		kit.Button(c, b.busyLabel("save", "保存", "正在保存…"), kit.ButtonOptions{Variant: kit.Primary, Disabled: b.busy != ""}).OnClick(b.saveBackend)
	})
}

var backendInfo = map[string]struct {
	icon kit.Symbol
	desc string
}{
	api.SyncWebDAV: {kit.SymHardDrive, "网盘或 NAS，例如 Nextcloud、群晖"},
	api.SyncS3:     {kit.SymDatabase, "AWS S3、Cloudflare R2、MinIO 等"},
	api.SyncKV:     {kit.SymCloud, "自己部署一个 Worker，存进 KV"},
}

// picker chooses the kind of backup location.
func (b *backupDialog) picker(c *ui.Context) {
	k := kit.T(c)
	ui.Row(c).Gap(8).AlignItems(ui.Stretch).Shrink(0).Label("备份位置").Children(func() {
		for _, typ := range backendTypes {
			info := backendInfo[typ]
			selected := b.typ == typ
			card := ui.ButtonBase(c.Key(typ)).Grow(1).Basis(0).MinWidth(0).Column().AlignItems(ui.Start).Gap(4).Padding(10).
				Radius(14).Border(1, k.HairlineSoft).Background(k.Surface).Label(backendLabel[typ]).Transition(kit.HoverMotion)
			switch {
			case selected:
				card.Border(1.5, k.Accent).Background(k.AccentWeak)
			case card.Hovered():
				card.Background(k.Fill)
			}
			card.Children(func() {
				icon := kit.Icon(c, info.icon, 18).TextColor(k.Label3)
				if selected {
					icon.TextColor(k.AccentText)
				}
				ui.Text(c, backendLabel[typ]).FontSize(kit.FontBody).FontWeight(500)
				ui.Text(c, info.desc).FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
			})
			if card.Clicked() && !selected {
				b.prepareDraft(typ)
			}
		}
	})
}

// formField is a labelled control of the backend form with its error or
// hint.
func formField(c *ui.Context, label, err, hint string, control func()) {
	k := kit.T(c)
	ui.Column(c.Key(label)).Gap(4).Shrink(0).Children(func() {
		ui.Text(c, label).FontSize(kit.FontTable).TextColor(k.Label3)
		control()
		if err != "" {
			kit.FormError(c, err)
		} else if hint != "" {
			ui.Text(c, hint).FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
		}
	})
}

func textInput(c *ui.Context, label string, value *string, placeholder string, password bool) {
	in := kit.Input(c, "input", value, placeholder).Label(label)
	if password {
		in.Password()
	}
}

var secretHint = "已保存的密钥显示为 " + secretMask + "，不修改就保留原值。"

func secretOr(value, hint string) string {
	if value == secretMask {
		return secretHint
	}
	return hint
}

// switchRow is a switch with a label and a description.
func switchRow(c *ui.Context, label, description string, on *bool, name string) {
	k := kit.T(c)
	ui.Row(c.Key(name)).MinHeight(44).Gap(12).Shrink(0).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, label).FontSize(kit.FontBody)
			if description != "" {
				ui.Text(c, description).FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
			}
		})
		kit.Switch(c, on, name)
	})
}

func insecureToggle(c *ui.Context, on *bool) {
	switchRow(c, "允许不安全连接（http）", "仅用于局域网内的自建服务；备份内容本身始终是加密的。", on, "允许不安全连接")
	if *on {
		note(c, kit.WarningTone, "使用 http 时，访问存储的账号、密码或令牌会以明文经过网络，同一网络里的其他设备可能看到。请确认这是可信的内网。")
	}
}

func (b *backupDialog) backendForm(c *ui.Context, errs map[string]string, warnings []string) {
	ui.Column(c.Key(b.draft.Type)).Gap(12).Shrink(0).Children(func() {
		switch b.draft.Type {
		case api.SyncWebDAV:
			d := b.draft.WebDAV
			note(c, kit.AccentTone, "备份由本机应用直接访问 WebDAV，不受浏览器跨域（CORS）限制。")
			formField(c, "WebDAV 地址", errs["webdav.url"], "例如 Nextcloud：https://域名/remote.php/dav/files/用户名/", func() {
				textInput(c, "WebDAV 地址", &d.URL, "https://", false)
			})
			formField(c, "用户名", errs["webdav.username"], "", func() { textInput(c, "用户名", &d.Username, "", false) })
			formField(c, "密码", errs["webdav.password"], secretOr(d.Password, "有些网盘要使用专门生成的“应用密码”。"), func() {
				textInput(c, "密码", &d.Password, "", true)
			})
			formField(c, "保存目录", errs["webdav.path"], "备份文件放在这个目录里，目录不存在时会自动创建。", func() {
				textInput(c, "保存目录", &d.Path, "kiko", false)
			})
			insecureToggle(c, &d.AllowInsecure)
		case api.SyncS3:
			d := b.draft.S3
			note(c, kit.AccentTone, "备份由本机应用直接访问存储，不需要给存储桶配置跨域（CORS）。")
			formField(c, "服务地址（Endpoint）", errs["s3.endpoint"], "AWS：https://s3.区域.amazonaws.com；Cloudflare R2：https://账号ID.r2.cloudflarestorage.com；自建 MinIO 填它的地址。", func() {
				textInput(c, "服务地址（Endpoint）", &d.Endpoint, "https://", false)
			})
			ui.Row(c).Gap(12).AlignItems(ui.Start).Shrink(0).Children(func() {
				ui.Column(c).Grow(1).Basis(0).MinWidth(0).Children(func() {
					formField(c, "区域（Region）", errs["s3.region"], "R2 填 auto；MinIO 通常是 us-east-1。", func() {
						textInput(c, "区域（Region）", &d.Region, "", false)
					})
				})
				ui.Column(c).Grow(1).Basis(0).MinWidth(0).Children(func() {
					formField(c, "存储桶（Bucket）", errs["s3.bucket"], "", func() { textInput(c, "存储桶（Bucket）", &d.Bucket, "", false) })
				})
			})
			formField(c, "路径前缀（可选）", errs["s3.prefix"], "备份对象放在这个前缀下，方便和桶里的其他文件区分。", func() {
				textInput(c, "路径前缀（可选）", &d.Prefix, "kiko", false)
			})
			formField(c, "Access Key", errs["s3.accessKey"], "", func() { textInput(c, "Access Key", &d.AccessKey, "", false) })
			formField(c, "Secret Key", errs["s3.secretKey"], secretOr(d.SecretKey, "建议给它单独建一个只能访问这个桶的密钥。"), func() {
				textInput(c, "Secret Key", &d.SecretKey, "", true)
			})
			switchRow(c, "路径风格（Path-style）", "MinIO、自建或 IP 地址访问的存储通常要开启；AWS S3、R2 等多数云服务两种都可以。", &d.PathStyle, "路径风格")
			insecureToggle(c, &d.AllowInsecure)
		default:
			d := b.draft.KV
			b.workerGuide(c)
			formField(c, "同步地址", errs["kv.url"], "部署后的 Worker 或 Pages 地址，例如 https://kiko-sync.pages.dev", func() {
				textInput(c, "同步地址", &d.URL, "https://", false)
			})
			formField(c, "访问令牌（SYNC_TOKEN）", errs["kv.token"], secretOr(d.Token, "就是你用 wrangler secret put 设置的那个令牌。"), func() {
				textInput(c, "访问令牌（SYNC_TOKEN）", &d.Token, "", true)
			})
			insecureToggle(c, &d.AllowInsecure)
		}
		for _, warning := range warnings {
			note(c, kit.WarningTone, warning)
		}
	})
}

// workerGuide shows how to deploy the KV worker, with its source and
// commands from the server, which match its version.
func (b *backupDialog) workerGuide(c *ui.Context) {
	k := kit.T(c)
	if b.worker == nil && !b.workerLoading && !b.workerFailed {
		b.workerLoading = true
		env.Go(b.w.env, func(ctx context.Context) (api.SyncWorkerInfo, error) {
			return b.w.env.API.SyncWorker(ctx)
		}, func(info api.SyncWorkerInfo, err error) {
			b.workerLoading = false
			if err != nil {
				b.workerFailed = true
				return
			}
			b.worker = &info
		})
	}
	if b.workerFailed {
		note(c, kit.WarningTone, "暂时取不到 Worker 部署说明，请确认本机服务端版本支持配置备份。")
		return
	}
	copyText := func(text, done string) {
		c.WriteClipboard(text)
		b.w.env.Toast(done)
	}
	ui.Column(c).Gap(10).Padding(12).Radius(14).Border(1, k.HairlineSoft).Background(k.Surface).Shrink(0).Children(func() {
		ui.Text(c, "先部署你自己的 Worker（一次即可）").FontSize(kit.FontBody).FontWeight(600).TextColor(k.Label2)
		step := func(n int, text string, extra func()) {
			ui.Row(c.Key(n)).Gap(8).AlignItems(ui.Start).Children(func() {
				ui.Text(c, fmt.Sprintf("%d.", n)).FontSize(kit.FontBody).TextColor(k.Label2).Width(14)
				ui.Column(c).Grow(1).MinWidth(0).Gap(6).Children(func() {
					ui.Text(c, text).FontSize(kit.FontBody).TextColor(k.Label2).Wrap()
					if extra != nil {
						extra()
					}
				})
			})
		}
		step(1, "复制 Worker 代码，在 Cloudflare 后台新建一个 Worker 并粘贴，或保存为 worker.js 用 wrangler 部署。", func() {
			ui.Row(c).Gap(8).Wrap().Children(func() {
				kit.Button(c, "复制 Worker 代码", kit.ButtonOptions{Small: true, Disabled: b.worker == nil}).OnClick(func() {
					copyText(b.worker.Source, "已复制 Worker 代码")
				})
				kit.Button(c, "复制 wrangler.jsonc", kit.ButtonOptions{Small: true, Disabled: b.worker == nil || b.worker.Wrangler == ""}).OnClick(func() {
					copyText(b.worker.Wrangler, "已复制 wrangler.jsonc")
				})
			})
		})
		step(2, "创建 KV 命名空间、设置管理密钥并部署（管理密钥请生成一串足够长的随机字符）：", func() {
			if b.worker == nil {
				ui.Text(c, "正在读取…").FontSize(kit.FontCaption).TextColor(k.Label3)
				return
			}
			for _, command := range b.worker.Commands {
				ui.Row(c.Key(command)).Gap(6).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Children(func() { kit.CodeBlock(c, command) })
					kit.IconButton(c, kit.SymCopy, "复制命令", kit.IconButtonOptions{Size: 26, IconSize: 14}).OnClick(func() {
						copyText(command, "已复制命令")
					})
				})
			}
		})
		step(3, "部署后打开同步地址，用管理密钥进入网页并创建访问令牌。把同步地址和该访问令牌填到下面；支持 Worker 和 Pages 地址。", nil)
		if b.worker != nil && b.worker.Readme != "" {
			kit.Disclosure(c, "查看详细说明", &b.readmeOpen, func() {
				ui.Text(c, b.worker.Readme).FontSize(kit.FontTable).TextColor(k.Label2).Wrap().Selectable()
			})
		}
		ui.Text(c, "Worker 里只会保存加密后的数据；KV 是最终一致的，跨地区写入后可能要几十秒才能在另一处读到。").
			FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
	})
}

// strengthBar rates a new password in four steps.
func strengthBar(c *ui.Context, password string) {
	if password == "" {
		return
	}
	k := kit.T(c)
	score, label, tone := passwordStrength(password)
	color := k.Danger
	switch tone {
	case toneFair:
		color = k.Warning
	case toneGood, toneStrong:
		color = k.Success
	}
	ui.Row(c).Gap(8).Shrink(0).Children(func() {
		ui.Row(c).Grow(1).Gap(4).Children(func() {
			for step := 1; step <= 4; step++ {
				bar := ui.Box(c).Height(4).Grow(1).Basis(0).Radius(2).Background(k.Fill2)
				if score >= step {
					bar.Background(color)
				}
			}
		})
		ui.Text(c, label).FontSize(kit.FontCaption).TextColor(k.Label3).Width(36).TextAlign(ui.End)
	})
}

// newPasswordFields asks for a new password twice, rating it and warning
// that it cannot be recovered.
func (b *backupDialog) newPasswordFields(c *ui.Context, submit func()) {
	k := kit.T(c)
	ui.Column(c).Gap(4).Shrink(0).Children(func() {
		ui.Text(c, "备份密码").FontSize(kit.FontTable).TextColor(k.Label3)
		in := kit.Input(c, "password", &b.password, "").Label("备份密码").Password().AutoFocus()
		if in.Changed() {
			b.unlockErr = ""
		}
		strengthBar(c, b.password)
	})
	ui.Column(c).Gap(4).Shrink(0).Children(func() {
		ui.Text(c, "再输入一次").FontSize(kit.FontTable).TextColor(k.Label3)
		in := kit.Input(c, "confirm", &b.confirm, "").Label("再输入一次").Password()
		if in.Changed() {
			b.unlockErr = ""
		}
		if in.Submitted() {
			submit()
		}
	})
	kit.FormError(c, b.unlockErr)
	note(c, kit.WarningTone, "密码只用来在本机生成加密密钥，不会上传，也不会保存到任何地方。忘记密码就无法解密云端备份，也没有找回方式，只能重置云端备份后重新开始。请务必记牢。")
}

func (b *backupDialog) lockedView(c *ui.Context) {
	k := kit.T(c)
	host := backendHost(b.status().Backend)
	if host == "" {
		host = "已配置"
	}
	box(c, kit.SymLock, k.Label3, "云端（"+backendName(b.status().Backend)+" · "+host+"）已经有加密备份。输入创建备份时设置的密码，本机才能读取和同步。")
	ui.Column(c).Gap(4).Shrink(0).Children(func() {
		ui.Text(c, "备份密码").FontSize(kit.FontTable).TextColor(k.Label3)
		in := kit.Input(c, "unlock", &b.password, "").Label("备份密码").Password().AutoFocus()
		if in.Changed() {
			b.unlockErr = ""
		}
		if in.Submitted() {
			b.unlock(false)
		}
		kit.FormError(c, b.unlockErr)
	})
	note(c, kit.AccentTone, "解锁后，本机只保存由密码生成的解密密钥（存放在本应用的数据目录），不保存密码本身。之后启动无需再输入。")
	kit.Button(c, b.busyLabel("unlock", "解锁", "正在解锁…"), kit.ButtonOptions{Variant: kit.Primary, Disabled: b.busy != ""}).
		FillWidth().OnClick(func() { b.unlock(false) })
	kit.Button(c, "更换备份位置", kit.ButtonOptions{}).FillWidth().OnClick(b.openBackendEditor)
}

func (b *backupDialog) setupView(c *ui.Context) {
	k := kit.T(c)
	host := backendHost(b.status().Backend)
	if host == "" {
		host = "已配置"
	}
	box(c, kit.SymShieldCheck, k.Success, "已连接到 "+backendName(b.status().Backend)+"（"+host+"），云端还没有备份。设置一个密码来创建加密备份；之后在其他设备上填同样的存储位置并输入这个密码，就能同步。")
	b.newPasswordFields(c, func() { b.unlock(true) })
	kit.Button(c, b.busyLabel("unlock", "创建加密备份", "正在创建…"), kit.ButtonOptions{Variant: kit.Primary, Disabled: b.busy != ""}).
		FillWidth().OnClick(func() { b.unlock(true) })
	kit.Button(c, "更换备份位置", kit.ButtonOptions{}).FillWidth().OnClick(b.openBackendEditor)
}

func (b *backupDialog) passwordView(c *ui.Context) {
	note(c, kit.WarningTone, "修改后，云端备份会用新密码重新加密。其他设备会被锁定，需要用新密码重新解锁一次。")
	b.newPasswordFields(c, b.changePassword)
	ui.Row(c).Gap(8).Justify(ui.End).Shrink(0).Children(func() {
		kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(func() { b.mode = backupAuto })
		kit.Button(c, b.busyLabel("password", "修改密码", "正在修改…"), kit.ButtonOptions{Variant: kit.Primary, Disabled: b.busy != ""}).
			OnClick(b.changePassword)
	})
}

func (b *backupDialog) unlockedView(c *ui.Context) {
	k := kit.T(c)
	s := b.status()
	ui.Column(c).Padding(6, 12).Radius(14).Border(1, k.HairlineSoft).Background(k.Surface).Shrink(0).Children(func() {
		kit.PropertyWith(c, "状态", func() {
			tone := kit.SuccessTone
			if s.LastError != "" {
				tone = kit.DangerTone
			}
			text := "已解锁"
			if s.Syncing {
				text = "正在同步…"
			}
			kit.Status(c, text, tone)
		})
		location := backendName(s.Backend)
		if host := backendHost(s.Backend); host != "" {
			location += " · " + host
		}
		kit.Property(c, "备份位置", location)
		last := "尚未同步"
		if s.LastSyncedAt != 0 {
			last = formatAgo(s.LastSyncedAt, time.Now())
		}
		kit.Property(c, "上次同步", last)
		if s.RemoteRevision != nil {
			kit.Property(c, "云端版本", fmt.Sprintf("#%d", *s.RemoteRevision))
		}
		device := s.DeviceID
		if len(device) > 12 {
			device = device[:12]
		}
		kit.Property(c, "本机设备号", device)
	})
	if s.LastError != "" {
		note(c, kit.DangerTone, "上次同步失败："+s.LastError+"。本机配置没有受影响，可以稍后重试。")
	}
	if s.LastResult != nil && s.LastResult.Conflicts > 0 {
		note(c, kit.WarningTone, fmt.Sprintf("上次同步时，有 %d 个配置档案在两台设备上都被修改过。较新的一份已作为正式档案，另一份保存为名称带“冲突副本”的档案，没有丢失；请在配置档案里对照后保留需要的那份。", s.LastResult.Conflicts))
	}
	syncing := b.busy == "sync" || s.Syncing
	label := "立即同步"
	if syncing {
		label = "正在同步…"
	}
	kit.Button(c, label, kit.ButtonOptions{Variant: kit.Primary, Icon: kit.SymRefreshCw, Disabled: b.busy != "" || s.Syncing}).
		FillWidth().OnClick(b.syncNow)
	ui.Column(c).Padding(4, 12).Radius(14).Border(1, k.HairlineSoft).Background(k.Surface).Shrink(0).Children(func() {
		auto := s.AutoSync
		ui.Row(c).MinHeight(52).Gap(12).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, "自动同步").FontSize(kit.FontBody)
				ui.Text(c, "启动时拉取云端，修改配置 30 秒后自动推送，并每 10 分钟检查一次云端。").FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
			})
			if kit.Switch(c, &auto, "自动同步").Changed() {
				b.run("", func(ctx context.Context) (api.SyncStatus, error) { return b.w.env.API.SyncAuto(ctx, auto) }, nil, nil)
			}
		})
	})
	note(c, kit.AccentTone, "同步按“配置档案”为单位合并，两边都改过同一份时较新的胜出，另一份保留为冲突副本。各设备用自己的系统时间判断先后，请保持时间准确。")
	ui.Column(c).Gap(8).Shrink(0).Children(func() {
		kit.Button(c, "修改备份密码", kit.ButtonOptions{Icon: kit.SymKeyRound}).FillWidth().OnClick(func() {
			b.password, b.confirm, b.unlockErr, b.mode = "", "", "", backupPassword
		})
		kit.Button(c, "更换备份位置", kit.ButtonOptions{}).FillWidth().OnClick(b.openBackendEditor)
		kit.Button(c, "锁定（忘记本机密钥）", kit.ButtonOptions{Icon: kit.SymLock}).FillWidth().OnClick(func() {
			b.run("", func(ctx context.Context) (api.SyncStatus, error) { return b.w.env.API.SyncLock(ctx) }, func() {
				b.w.env.Toast("已锁定：本机密钥已删除，同步已暂停")
			}, nil)
		})
		reset := kit.Button(c, "重置云端备份…", kit.ButtonOptions{Variant: kit.Danger, Disabled: b.busy != ""}).FillWidth()
		if reset.Clicked() {
			b.reset.Open = true
		}
		kit.ConfirmPopover(c, reset, &b.reset, kit.ConfirmOptions{
			Title:   "重置云端备份？",
			Message: "将删除云端的加密备份，并清除本机保存的解密密钥。本机的配置档案不会被删除；之后可以重新设置密码并创建新的备份。其他设备需要重新连接。",
			Confirm: "删除云端备份", Danger: true,
			Run: func(done func(error)) {
				b.busy = "reset"
				env.Go(b.w.env, func(ctx context.Context) (api.SyncStatus, error) {
					return b.w.env.API.SyncReset(ctx)
				}, func(status api.SyncStatus, err error) {
					b.busy = ""
					if err != nil {
						done(fmt.Errorf("%s", syncErrorMessage(err)))
						return
					}
					b.w.sync = &status
					b.w.env.Toast("云端备份已重置")
					done(nil)
				})
			},
		})
	})
	ui.Text(c, "“锁定”只删除本机保存的解密密钥，云端备份不受影响，再次输入密码即可解锁。").FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
}
