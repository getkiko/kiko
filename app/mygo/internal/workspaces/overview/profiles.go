package overview

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/profiles"
)

// jsonFiles filters open and save dialogs to JSON.
var jsonFiles = env.FileFilter{Name: "JSON", Extensions: []string{"json"}}

// profilePanel is the current profile card's state (ProfilePanel).
type profilePanel struct {
	// msg reports the last operation under the card.
	msg string

	adding   bool
	more     bool
	confirm  kit.ConfirmState
	deleting string

	creating bool
	newName  string

	renaming   bool
	renameID   string
	renameText string

	urlOpen   bool
	url       string
	headers   string
	urlErr    string
	urlBusy   bool
	urlSaving bool
	urlCancel context.CancelFunc

	preview configViewer
	share   shareDialog
}

// profileCard is Current Profile: the profile chooser, the add menu (new profile,
// URL import) and the management menu, with the document's summary.
func (w *Workspace) profileCard(c *ui.Context) {
	k := kit.T(c)
	p := &w.profiles
	list := w.store.Profiles()
	activeID := w.store.ActiveProfileID()
	active := profiles.Find(list, activeID)
	dashboardCard(c, "当前配置", 1).Gap(12).Grow(1).Children(func() {
		cardHeader(c, kit.SymFileText, k.Label2, "配置", func() {
			ui.Text(c, profiles.Summary(w.store.Doc())).FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum").
				SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
		})
		ui.Row(c).Gap(6).Children(func() {
			value := ""
			if active != nil {
				value = active.ID
			}
			var options []kit.Option
			for _, profile := range profiles.Sorted(list) {
				options = append(options, kit.Option{Value: profile.ID, Label: profile.Name, Detail: profiles.Summary(profile.Doc)})
			}
			chooser := kit.Select(c, "profile", &value, options, kit.SelectOptions{Placeholder: "选择配置", Label: "选择配置"}).
				Grow(1).Height(40).FontSize(15).FontWeight(600)
			if chooser.Changed() {
				if next := profiles.Find(list, value); next != nil && next.ID != activeID {
					w.loadProfile(next)
				}
			}
			add := kit.IconButton(c, kit.SymPlus, "添加配置", kit.IconButtonOptions{Size: 40, IconSize: 20, Active: p.adding}).TextColor(k.AccentText)
			if add.Clicked() {
				p.adding = !p.adding
			}
			kit.ActionMenu(c, add, &p.adding, []kit.ActionGroup{{Items: []kit.ActionItem{
				{ID: "new", Label: "新建配置", Icon: kit.SymFilePlus2, Run: func() { p.creating = true }},
				{ID: "url", Label: "从 URL 导入", Icon: kit.SymLink2, Run: func() { p.urlErr, p.urlOpen = "", true }},
			}}}, kit.ActionMenuOptions{Width: 200, Label: "添加配置"})
			more := kit.IconButton(c, kit.SymEllipsis, "管理配置", kit.IconButtonOptions{Size: 40, IconSize: 21, Active: p.more})
			if more.Clicked() {
				p.more = !p.more
			}
			w.profileActions(c, more, active)
			name := ""
			if deleting := profiles.Find(list, p.deleting); deleting != nil {
				name = deleting.Name
			}
			kit.ConfirmPopover(c, more, &p.confirm, kit.ConfirmOptions{
				Title: fmt.Sprintf("删除「%s」？", name), Message: "此操作不可撤销。", Confirm: "删除", Danger: true,
				Run: func(done func(error)) { done(w.deleteProfile(p.deleting)) },
			})
		})
		if p.msg != "" {
			ui.Text(c, p.msg).FontSize(kit.FontTable).TextColor(k.Label3).Role(ui.RoleStatus)
		}
	})
}

// profileActions is the management menu anchored to the "Manage profile"
// button: Profile, File and Delete groups. "Share current profile" needs
// the share address of Settings: without one it shows unavailable and
// leads there.
func (w *Workspace) profileActions(c *ui.Context, anchor ui.Element, active *model.Profile) {
	p := &w.profiles
	all := w.store.Profiles()
	var groups []kit.ActionGroup
	if active != nil {
		groups = append(groups, kit.ActionGroup{Label: "配置", Items: []kit.ActionItem{
			{ID: "copy", Label: "复制", Icon: kit.SymCopy, Run: func() { w.duplicateProfile(active) }},
			{ID: "rename", Label: "改名", Icon: kit.SymPencil, Run: func() {
				p.renameID, p.renameText, p.renaming = active.ID, active.Name, true
			}},
			{ID: "preview", Label: "预览", Icon: kit.SymEye, Run: func() { w.previewProfile(active) }},
		}})
	}
	files := kit.ActionGroup{Label: "文件", Items: []kit.ActionItem{
		{ID: "import", Label: "导入", Icon: kit.SymFolderInput, Run: w.importFile},
		{ID: "export-all", Label: "导出全部", Icon: kit.SymDownload, Disabled: len(all) == 0, Run: func() {
			w.exportProfiles(all, "kiko-all-profiles.json", true)
		}},
	}}
	if active != nil {
		share := kit.ActionItem{ID: "share", Label: "分享当前配置", Icon: kit.SymCloudUpload, Run: func() { w.shareProfile(active) }}
		if !w.shareAvailable() {
			share.Unavailable, share.Hint = true, "在设置中配置分享地址"
			share.Run = func() { w.env.Navigate(env.PageSettings, profiles.ShareOriginSetting) }
		}
		files.Items = append(files.Items,
			kit.ActionItem{ID: "export", Label: "导出当前", Icon: kit.SymDownload, Run: func() {
				w.exportProfiles([]*model.Profile{active}, active.Name+".kiko.json", false)
			}},
			share,
		)
	}
	groups = append(groups, files)
	if active != nil {
		groups = append(groups, kit.ActionGroup{Label: "删除", Danger: true, Items: []kit.ActionItem{
			{ID: "delete", Label: "删除", Name: "删除当前配置", Icon: kit.SymTrash2, Danger: true, Run: func() {
				p.deleting, p.confirm.Open = active.ID, true
			}},
		}})
	}
	kit.ActionMenu(c, anchor, &p.more, groups, kit.ActionMenuOptions{Width: 220, Label: "管理配置"})
}

// loadProfile makes p the document, unless its dependencies are broken.
func (w *Workspace) loadProfile(p *model.Profile) {
	if err := profiles.LoadError(p, w.store.NetworkConfigs()); err != nil {
		w.env.ToastError(err)
		return
	}
	w.store.SetProfiles(w.store.Profiles(), p.ID, model.FillDefaults(model.Clone(p.Doc)))
	w.profiles.msg = fmt.Sprintf("已加载「%s」", p.Name)
}

// createProfile adds an empty profile and makes it the document.
func (w *Workspace) createProfile() bool {
	p := &w.profiles
	profile, err := profiles.NewProfile(w.store.Profiles(), p.newName, w.store.Doc().Platform)
	if err != nil {
		w.env.ToastError(err)
		return false
	}
	w.store.SetProfiles(append(append([]*model.Profile(nil), w.store.Profiles()...), profile), profile.ID, profile.Doc)
	p.newName, p.creating = "", false
	p.msg = fmt.Sprintf("已新建「%s」", profile.Name)
	return true
}

func (w *Workspace) duplicateProfile(profile *model.Profile) {
	all := w.store.Profiles()
	copied := profiles.Duplicate(all, profile)
	w.store.SetProfiles(append(append([]*model.Profile(nil), all...), copied), w.store.ActiveProfileID(), nil)
	w.profiles.msg = fmt.Sprintf("已复制为「%s」", copied.Name)
}

// renameProfile saves the rename dialog's name.
func (w *Workspace) renameProfile() {
	p := &w.profiles
	name, err := profiles.CheckName(w.store.Profiles(), p.renameText, p.renameID)
	if err != nil {
		w.env.ToastError(err)
		return
	}
	w.store.SetProfiles(profiles.Renamed(w.store.Profiles(), p.renameID, name), w.store.ActiveProfileID(), nil)
	p.renaming = false
	p.msg = fmt.Sprintf("已改名为「%s」", name)
}

// deleteProfile removes a profile; deleting the active one loads another.
func (w *Workspace) deleteProfile(id string) error {
	profile := profiles.Find(w.store.Profiles(), id)
	if profile == nil {
		return nil
	}
	result, err := profiles.Delete(w.store.Profiles(), id, w.store.ActiveProfileID(), w.store.Doc().Platform, w.store.NetworkConfigs())
	if err != nil {
		return err
	}
	w.store.SetProfiles(result.Profiles, result.ActiveID, result.Doc)
	w.profiles.msg = fmt.Sprintf("已删除「%s」", profile.Name)
	return nil
}

// sourceDoc is the document a profile emits: the edited document for the
// active profile, the stored one on the host's platform otherwise.
func (w *Workspace) sourceDoc(profile *model.Profile) *model.Doc {
	current := w.store.Doc()
	if profile.ID == w.store.ActiveProfileID() {
		return current
	}
	return model.ProjectRuntimeDoc(profile.Doc, current.Platform)
}

// emitProfile emits a profile's document, toasting its errors.
func (w *Workspace) emitProfile(profile *model.Profile) (emit.Result, bool) {
	result := emit.Emit(w.sourceDoc(profile), emit.Options{NetworkConfigs: w.store.NetworkConfigs(), LocalAddresses: w.store.LocalAddresses})
	if len(result.Errors) > 0 {
		w.env.Toast(strings.Join(result.Errors, "；"))
		return result, false
	}
	return result, true
}

// previewProfile shows the profile's emitted configuration.
func (w *Workspace) previewProfile(profile *model.Profile) {
	if result, ok := w.emitProfile(profile); ok {
		w.profiles.preview.show(profile.Name+" · config.json", string(result.JSON))
	}
}

// shareProfile offers the profile's source file for sharing.
func (w *Workspace) shareProfile(profile *model.Profile) {
	if _, ok := w.emitProfile(profile); !ok {
		return
	}
	shared := *profile
	shared.Doc = w.sourceDoc(profile)
	if err := model.CheckDeviceImagesTotal(shared.Doc); err != nil {
		w.env.ToastError(err)
		return
	}
	content, err := profiles.SerializeBundle([]*model.Profile{&shared}, w.store.NetworkConfigs(), false)
	if err != nil {
		w.env.ToastError(err)
		return
	}
	w.profiles.share.show(string(content), "config.kiko.json")
}

// exportProfiles saves profiles as a backup file.
func (w *Workspace) exportProfiles(list []*model.Profile, name string, all bool) {
	data, err := profiles.SerializeBundle(list, w.store.NetworkConfigs(), all)
	if err != nil {
		w.env.ToastError(err)
		return
	}
	w.saveFile(name, data)
}

// saveFile asks where to save data.
func (w *Workspace) saveFile(name string, data []byte) {
	env.Go(w.env, func(context.Context) (bool, error) {
		return w.env.Shell.SaveFile(name, data, jsonFiles)
	}, func(saved bool, err error) {
		switch {
		case err != nil:
			w.env.Toast("导出失败：" + err.Error())
		case saved:
			w.env.Toast("文件已导出")
		}
	})
}

// importFile asks for a file and imports its profiles.
func (w *Workspace) importFile() {
	type picked struct {
		data []byte
		ok   bool
	}
	env.Go(w.env, func(context.Context) (picked, error) {
		data, _, ok, err := w.env.Shell.OpenFile()
		return picked{data, ok}, err
	}, func(file picked, err error) {
		switch {
		case err != nil:
			w.profiles.msg = "文件读取失败"
		case file.ok:
			w.importProfiles(file.data)
		}
	})
}

// importProfiles adds the profiles of an imported file, making the first
// the document. It reports the outcome under the card.
func (w *Workspace) importProfiles(content []byte) error {
	result, err := profiles.ImportProfiles(content, w.store.Doc().Platform, w.store.Profiles(), w.store.NetworkConfigs(), w.store.LocalAddresses)
	if err != nil {
		w.profiles.msg = err.Error()
		return err
	}
	w.store.SetNetworkConfigs(result.Library)
	w.store.SetProfiles(result.Profiles, result.ActiveID, result.Doc)
	w.profiles.msg = result.Message()
	return nil
}

// importURL downloads a configuration through the server and imports it.
func (w *Workspace) importURL() {
	p := &w.profiles
	if p.urlBusy {
		return
	}
	p.urlErr = ""
	headers, err := profiles.ParseCustomHeaders(p.headers)
	if err != nil {
		p.urlErr = err.Error()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.urlCancel, p.urlBusy = cancel, true
	rawURL := p.url
	env.Go(w.env, func(context.Context) (string, error) {
		return profiles.FetchConfigText(ctx, rawURL, headers, func(ctx context.Context, url string, headers map[string]string) (profiles.URLResponse, error) {
			result, err := w.env.API.Fetch(ctx, url, headers)
			return profiles.URLResponse{Status: result.Status, Body: result.Body}, err
		})
	}, func(content string, err error) {
		if ctx.Err() != nil {
			return
		}
		cancel()
		p.urlCancel, p.urlBusy = nil, false
		if err != nil {
			p.urlErr = err.Error()
			return
		}
		p.urlSaving = true
		err = w.importProfiles([]byte(content))
		p.urlSaving = false
		if err != nil {
			p.urlErr = err.Error()
			if p.urlErr == "" {
				p.urlErr = "配置检查失败"
			}
			return
		}
		p.url, p.headers, p.urlOpen = "", "", false
	})
}

// closeURLImport cancels a download in flight and closes the dialog,
// unless the import is being saved.
func (w *Workspace) closeURLImport() {
	p := &w.profiles
	if p.urlSaving {
		p.urlOpen = true
		return
	}
	if p.urlCancel != nil {
		p.urlCancel()
		p.urlCancel = nil
	}
	p.urlBusy, p.urlOpen = false, false
}

// profileDialogs are the new, rename, URL import, preview and share
// dialogs.
func (w *Workspace) profileDialogs(c *ui.Context) {
	k := kit.T(c)
	p := &w.profiles
	kit.Dialog(c, &p.creating, "新建配置", 0, func() {
		ui.Text(c, "配置名称").FontSize(13).TextColor(k.Label3)
		if kit.Input(c, "new-profile", &p.newName, "例如：家里 / 公司 / 打洞").AutoFocus().Submitted() {
			w.createProfile()
		}
	}, func() {
		kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(func() { p.creating = false })
		kit.Button(c, "保存", kit.ButtonOptions{Variant: kit.Primary, Disabled: strings.TrimSpace(p.newName) == ""}).OnClick(func() { w.createProfile() })
	})

	renamed := profiles.Find(w.store.Profiles(), p.renameID)
	if p.renaming && renamed == nil {
		p.renaming = false
	}
	kit.Dialog(c, &p.renaming, "重命名", 0, func() {
		if kit.Input(c, "rename-profile", &p.renameText, "配置名称").AutoFocus().Submitted() {
			w.renameProfile()
		}
		if _, err := profiles.CheckName(w.store.Profiles(), p.renameText, p.renameID); errors.Is(err, profiles.ErrNameTaken) {
			kit.FormError(c, "已存在同名配置")
		}
	}, func() {
		kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(func() { p.renaming = false })
		unchanged := renamed == nil || strings.TrimSpace(p.renameText) == renamed.Name
		kit.Button(c, "保存", kit.ButtonOptions{Variant: kit.Primary, Disabled: unchanged}).OnClick(w.renameProfile)
	})

	wasOpen := p.urlOpen
	kit.Dialog(c, &p.urlOpen, "从 URL 导入配置", 0, func() {
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, "配置 URL").FontSize(13).FontWeight(500)
			if kit.Input(c, "config-url", &p.url, "https://example.com/config.json").AutoFocus().Submitted() && strings.TrimSpace(p.url) != "" {
				w.importURL()
			}
		})
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, "自定义 Header（可选）").FontSize(13).FontWeight(500)
			kit.TextArea(c, "config-headers", &p.headers, "Authorization: Bearer token\nX-Config-Key: value", true).Label("自定义 Header（可选）")
			ui.Text(c, "每行填写一个“名称: 值”。").FontSize(kit.FontTable).TextColor(k.Label3)
		})
		if p.urlErr != "" {
			ui.Text(c, p.urlErr).FontSize(13).TextColor(k.Danger).Role(ui.RoleStatus).Selectable()
		}
	}, func() {
		kit.Button(c, "取消", kit.ButtonOptions{Disabled: p.urlSaving}).OnClick(w.closeURLImport)
		label := "下载并导入"
		switch {
		case p.urlSaving:
			label = "导入中…"
		case p.urlBusy:
			label = "下载中…"
		}
		kit.Button(c, label, kit.ButtonOptions{Variant: kit.Primary, Disabled: p.urlBusy || strings.TrimSpace(p.url) == ""}).OnClick(w.importURL)
	})
	if wasOpen && !p.urlOpen {
		w.closeURLImport()
	}

	p.preview.dialog(c, w.env)
	p.share.dialog(c, w)
}
