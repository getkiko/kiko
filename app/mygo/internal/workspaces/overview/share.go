package overview

import (
	"context"
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/profiles"
)

// shareDialog uploads a configuration to the share service set in Settings
// (ConfigShareDialog). The service's Cloudflare Turnstile check runs in a
// web page, so the native console saves the file and opens the service's
// upload page in the browser, which checks and uploads it.
type shareDialog struct {
	open      bool
	content   string
	name      string
	confirmed bool
	busy      bool
	done      bool
	err       string
}

// shareAvailable reports whether Settings holds a share address the share
// service can take.
func (w *Workspace) shareAvailable() bool {
	return profiles.ValidShareOrigin(profiles.ShareOrigin(w.env.AppPrefs)) == nil
}

// show opens the dialog for content, saved as name.
func (s *shareDialog) show(content, name string) {
	*s = shareDialog{open: true, content: content, name: name}
}

func (s *shareDialog) start(w *Workspace) {
	if !s.confirmed || len(s.content) > profiles.ShareMaxBytes || s.busy {
		return
	}
	page, err := profiles.ShareUploadPage(profiles.ShareOrigin(w.env.AppPrefs))
	if err != nil {
		s.err = err.Error()
		return
	}
	s.busy, s.err = true, ""
	content, name := s.content, s.name
	env.Go(w.env, func(context.Context) (bool, error) {
		saved, err := w.env.Shell.SaveFile(name, []byte(content), jsonFiles)
		if err != nil || !saved {
			return saved, err
		}
		return true, w.env.Shell.OpenURL(page)
	}, func(opened bool, err error) {
		s.busy = false
		switch {
		case err != nil:
			s.err = err.Error()
		case opened:
			s.done = true
		}
	})
}

func (s *shareDialog) dialog(c *ui.Context, w *Workspace) {
	k := kit.T(c)
	size := len(s.content)
	kit.Dialog(c, &s.open, "上传 Kiko 配置源文件", 0, func() {
		kit.Notice(c, kit.WarningTone, "配置源文件将以明文公开保存",
			"文件保留 Kiko 配置文档与引用的网络设置。任何持有链接的人都能下载文件；管理员可在分享管理页删除文件。", nil)
		ui.Text(c, fmt.Sprintf("配置大小：%s / %s 字节", kit.Count(size), kit.Count(profiles.ShareMaxBytes))).
			FontSize(kit.FontTable).TextColor(k.Label3)
		if size > profiles.ShareMaxBytes {
			kit.Notice(c, kit.DangerTone, "", "配置文件超过 2 MB 限制，当前无法上传。", nil)
		}
		ui.Text(c, "上传验证在浏览器中完成：将先保存配置文件，再打开上传页面，在页面中选择该文件验证并上传。").
			FontSize(kit.FontTable).TextColor(k.Label2)
		if s.done {
			kit.Notice(c, kit.AccentTone, "已打开上传页面", "在浏览器中选择刚保存的文件，通过验证后即可获得分享链接。", nil)
		}
		if s.err != "" {
			kit.Notice(c, kit.DangerTone, "上传失败", s.err, nil)
		}
		box := kit.Checkbox(c, &s.confirmed, "我已确认配置内容适合公开分享，并接受链接持有者可下载文件。").Disabled(s.busy)
		box.Shrink(1)
	}, func() {
		kit.Button(c, "关闭", kit.ButtonOptions{Disabled: s.busy}).OnClick(func() { s.open = false })
		label := "验证并上传"
		if s.busy {
			label = "正在保存…"
		}
		kit.Button(c, label, kit.ButtonOptions{Variant: kit.Primary, Disabled: !s.confirmed || size > profiles.ShareMaxBytes || s.busy}).
			OnClick(func() { s.start(w) })
	})
}
