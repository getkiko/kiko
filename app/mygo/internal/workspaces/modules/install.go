package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/modules"
)

const installWidth = 600

// Source choices of the add dialog.
const (
	sourceURL = iota
	sourceFile
)

// installer is the add dialog: the source to read, the preview of what the
// module would add and the confirmation it needs. For a module's waiting
// update (pendingID) it shows only what the update adds.
type installer struct {
	open, shown bool
	pendingID   string
	// pendingHash and pendingStamp are the waiting update's Hash and
	// Fingerprint when the dialog opened (or last refreshed): a decision is
	// taken only for the update the user was shown.
	pendingHash, pendingStamp string

	kind    int
	address string
	file    struct {
		name string
		data []byte
	}
	picking bool

	busy        bool
	done, total int
	err         string

	conv      *Converted
	source    model.ModuleSource
	fileName  string
	diff      model.ModuleDiff
	confirmed bool
	deciding  bool

	gen    int
	cancel context.CancelFunc
	view   previewState
}

// previewState is what a preview remembers while it shows: which folds
// are open.
type previewState struct {
	open map[string]bool
}

// openAdd opens the dialog on its first page.
func (w *Workspace) openAdd() {
	w.install = installer{open: true, shown: true}
}

// openPending opens the update of module id that waits for a decision.
func (w *Workspace) openPending(id string) {
	m := find(w.doc.Doc(), id)
	if m == nil || m.Pending == nil {
		return
	}
	w.install = installer{open: true, shown: true, pendingID: id, diff: m.Pending.Added,
		pendingHash: m.Pending.Hash, pendingStamp: m.Pending.Fingerprint()}
}

// closeInstall stops what the dialog runs and resets it.
func (w *Workspace) closeInstall() {
	in := &w.install
	if in.cancel != nil {
		in.cancel()
	}
	gen := in.gen + 1
	*in = installer{gen: gen}
}

// Installer state for tests and hosts.

// InstallOpen reports whether the add dialog is open.
func (w *Workspace) InstallOpen() bool { return w.install.open }

func (w *Workspace) installDialog(c *ui.Context) {
	in := &w.install
	if !in.open {
		if in.shown {
			id := in.pendingID
			w.closeInstall()
			switch {
			case id != "":
				w.focusID = id
			case w.focusID == "":
				w.focusAdd = true
			}
		}
		return
	}
	title := "添加模块"
	if in.pendingID != "" {
		title = "待确认的更新"
	}
	kit.Dialog(c, &in.open, title, installWidth, func() { w.installForm(c) }, func() { w.installFooter(c) })
}

func (w *Workspace) installForm(c *ui.Context) {
	in := &w.install
	switch {
	case in.pendingID != "":
		m := find(w.doc.Doc(), in.pendingID)
		if m == nil || m.Pending == nil {
			ui.Text(c, "更新已不存在").FontSize(kit.FontBody)
			return
		}
		p := m.Pending
		if p.Hash != in.pendingHash {
			// Another update replaced the one the dialog showed.
			in.pendingHash, in.pendingStamp = p.Hash, p.Fingerprint()
			in.confirmed = false
		}
		in.diff = p.Added
		w.preview(c, previewData{
			Name: p.Name, Version: p.Version, Source: SourceLabel(m), Hosts: p.Added.Hostnames,
			Scripts: p.Added.Scripts, Direct: p.Added.Direct, Contents: p.Contents, Unsupported: p.Unsupported, OnlyAdded: true,
		})
		w.confirmRow(c, p.Added)
		if in.err != "" {
			kit.Notice(c, kit.DangerTone, "", in.err, nil)
		}
	case in.conv != nil:
		conv := in.conv
		source := SourceLabel(&model.HTTPModule{Source: in.source})
		if in.source.Kind != model.ModuleSourceURL && in.fileName != "" {
			source = in.fileName
		}
		u := conv.Update
		name := u.Name
		if strings.TrimSpace(name) == "" {
			name = fallbackName(in.source.URL, in.fileName)
		}
		w.preview(c, previewData{
			Name: name, Version: u.Version, Source: source, Hosts: in.diff.Hostnames, Scripts: in.diff.Scripts,
			Direct: in.diff.Direct, Contents: u.Contents, Unsupported: u.Unsupported, Failures: conv.Failures,
			Downloads: conv.Result.Fetches,
		})
		w.confirmRow(c, in.diff)
		if in.err != "" {
			kit.Notice(c, kit.DangerTone, "", in.err, nil)
		}
	default:
		w.sourceForm(c)
	}
}

// confirmRow is the check box that confirms what diff lists, as it is now.
func (w *Workspace) confirmRow(c *ui.Context, diff model.ModuleDiff) {
	if diff.Empty() {
		return
	}
	kit.Checkbox(c, &w.install.confirmed, ConfirmLabel(diff))
}

// ConfirmLabel names what the check box confirms.
func ConfirmLabel(diff model.ModuleDiff) string {
	var parts []string
	if len(diff.Hostnames) > 0 {
		parts = append(parts, "解密以上主机名")
	}
	if len(diff.Scripts) > 0 {
		parts = append(parts, "运行以上脚本")
	}
	if len(diff.Direct) > 0 {
		parts = append(parts, "直连以上规则")
	}
	if len(parts) == 2 {
		return "确认" + parts[0] + "并" + parts[1]
	}
	return "确认" + strings.Join(parts, "、")
}

// sourceForm is the first page: where the module comes from.
func (w *Workspace) sourceForm(c *ui.Context) {
	in := &w.install
	kit.FormRow(c, "来源", "", func() {
		sel := in.kind
		kit.Segmented(c, "mod-source", &sel, kit.Segment{Label: "链接"}, kit.Segment{Label: "本地文件"}).Label("模块来源")
		if sel != in.kind && !in.busy {
			in.kind, in.err = sel, ""
		}
	})
	if in.kind == sourceURL {
		kit.FormRow(c, "链接", "", func() {
			kit.Input(c, "mod-url", &in.address, "https://example.com/module.sgmodule").Label("模块链接").AutoFocus()
		})
	} else {
		kit.FormRow(c, "文件", "", func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).MinWidth(0).Children(func() {
				pick := kit.Button(c, "选择文件", kit.ButtonOptions{Icon: kit.SymFileUp, Disabled: in.picking || in.busy})
				if pick.Clicked() {
					w.chooseFile()
				}
				if in.file.name != "" {
					ui.Text(c, in.file.name).FontSize(kit.FontBody).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
				}
			})
		})
	}
	if in.busy {
		text := "正在获取"
		if in.total > 0 {
			text = fmt.Sprintf("正在下载脚本 %d / %d", in.done, in.total)
		}
		kit.Notice(c, kit.AccentTone, "", text, nil)
	}
	if in.err != "" {
		kit.Notice(c, kit.DangerTone, "", in.err, nil)
	}
}

func (w *Workspace) installFooter(c *ui.Context) {
	in := &w.install
	if in.pendingID != "" {
		if kit.Button(c, "放弃更新", kit.ButtonOptions{Variant: kit.Danger}).Clicked() {
			w.decidePending(false)
		}
		approve := kit.Button(c, "确认更新", kit.ButtonOptions{Variant: kit.Primary, Disabled: !in.confirmed || in.deciding})
		if approve.Clicked() {
			w.decidePending(true)
		}
		return
	}
	if in.conv != nil {
		if kit.Button(c, "返回", kit.ButtonOptions{}).Clicked() {
			in.conv, in.err, in.confirmed = nil, "", false
		}
		install := kit.Button(c, "安装", kit.ButtonOptions{Variant: kit.Primary, Disabled: !in.diff.Empty() && !in.confirmed})
		if install.Clicked() {
			w.installModule()
		}
		return
	}
	if kit.Button(c, "取消", kit.ButtonOptions{}).Clicked() {
		in.open = false
	}
	label := "获取"
	switch {
	case in.busy:
		label = "获取中…"
	case in.err != "":
		label = "重试"
	}
	ready := (in.kind == sourceURL && strings.TrimSpace(in.address) != "") || (in.kind == sourceFile && in.file.data != nil)
	get := kit.Button(c, label, kit.ButtonOptions{Variant: kit.Primary, Disabled: in.busy || !ready})
	if get.Clicked() {
		w.fetchSource()
	}
}

// chooseFile asks for a module file.
func (w *Workspace) chooseFile() {
	in := &w.install
	if in.picking {
		return
	}
	if w.env == nil || w.env.Shell == nil {
		in.err = "无法选择文件"
		return
	}
	in.picking, in.err = true, ""
	gen := in.gen
	shell := w.env.Shell
	type picked struct {
		data []byte
		name string
		ok   bool
	}
	env.Go(w.env, func(context.Context) (picked, error) {
		data, name, ok, err := shell.OpenFile(env.FileFilter{Name: "模块", Extensions: []string{"sgmodule", "kikomodule", "poppymodule", "txt", "conf"}})
		return picked{data, name, ok}, err
	}, func(p picked, err error) {
		if gen != w.install.gen {
			return
		}
		in := &w.install
		in.picking = false
		switch {
		case err != nil:
			in.err = "读取失败：" + err.Error()
		case p.ok && len(p.data) > modules.MaxKikoSize:
			in.err = "文件过大"
		case p.ok:
			in.file.name, in.file.data = p.name, p.data
		}
	})
}

// fetchSource reads the source and converts it, downloading the scripts it
// lists.
func (w *Workspace) fetchSource() {
	in := &w.install
	if in.busy {
		return
	}
	address := strings.TrimSpace(in.address)
	var source model.ModuleSource
	if in.kind == sourceURL {
		if !strings.HasPrefix(address, "http://") && !strings.HasPrefix(address, "https://") {
			in.err = "请输入 http 或 https 链接"
			return
		}
		for _, m := range modulesOf(w.doc.Doc()) {
			if m.Source.Kind == model.ModuleSourceURL && m.Source.URL == address {
				in.err = "已安装此链接的模块"
				return
			}
		}
		source = model.ModuleSource{Kind: model.ModuleSourceURL, URL: address}
	} else {
		source = model.ModuleSource{Kind: model.ModuleSourceFile}
	}
	if w.fetch == nil {
		in.err = "无法下载"
		return
	}
	if len(modulesOf(w.doc.Doc())) >= model.MaxModules {
		in.err = fmt.Sprintf("最多安装 %d 个模块", model.MaxModules)
		return
	}
	in.busy, in.err, in.done, in.total = true, "", 0, 0
	in.gen++
	gen := in.gen
	ctx, cancel := context.WithCancel(context.Background())
	in.cancel = cancel
	fileData, fileName, kind := in.file.data, in.file.name, in.kind
	fetch, fetchMain := w.fetch, w.fetchMain
	env.Go(w.env, func(context.Context) (*Converted, error) {
		defer cancel()
		input := Input{BaseURL: address}
		if kind == sourceFile {
			input.Data, input.BaseURL = fileData, ""
		} else {
			body, err := Download(ctx, fetchMain, address)
			if err != nil {
				return nil, err
			}
			input.Data = body
		}
		return Convert(ctx, fetch, input, func(done, total int) {
			w.env.Post(func() {
				if gen == w.install.gen {
					w.install.done, w.install.total = done, total
				}
			})
		})
	}, func(conv *Converted, err error) {
		if gen != w.install.gen {
			return
		}
		in := &w.install
		in.busy, in.cancel = false, nil
		if err != nil {
			in.err = "获取失败：" + shorten(err.Error(), 160)
			return
		}
		in.conv, in.source, in.fileName = conv, source, fileName
		fresh := model.NewHTTPModule()
		in.diff = fresh.NeedsApproval(conv.Contents())
		in.confirmed = false
	})
}

// installModule adds the previewed module to the document.
func (w *Workspace) installModule() {
	in := &w.install
	if in.conv == nil || (!in.diff.Empty() && !in.confirmed) {
		return
	}
	m := NewModule(in.conv, in.source, in.fileName, w.now())
	if issue := m.Issue(); issue != "" {
		in.err = "无法安装：" + issue
		return
	}
	var full bool
	err := w.doc.Update(func(d *model.Doc) {
		t := ensure(d)
		if len(t.Modules) >= model.MaxModules {
			full = true
			return
		}
		t.Modules = append(t.Modules, m)
	})
	switch {
	case full:
		in.err = fmt.Sprintf("最多安装 %d 个模块", model.MaxModules)
	case err != nil:
		in.err = "安装失败：" + err.Error()
	default:
		in.open = false
		w.focusID = m.ID
		w.env.Toast("已安装「" + m.Name + "」")
	}
}

// decidePending approves or drops the update that waits.
func (w *Workspace) decidePending(approve bool) {
	in := &w.install
	id := in.pendingID
	in.deciding, in.err = true, ""
	now := w.now()
	var did, stale bool
	err := w.doc.Update(func(d *model.Doc) {
		m := find(d, id)
		if m == nil || m.Pending == nil {
			return
		}
		if approve && m.Pending.Fingerprint() != in.pendingStamp {
			// The update changed after the dialog showed it: nothing is
			// approved that the user has not seen.
			stale = true
			return
		}
		did = true
		if approve {
			m.ApprovePending(now.UnixMilli())
		} else {
			m.DiscardPending()
		}
	})
	in.deciding = false
	switch {
	case err != nil:
		w.env.ToastError(err)
	case stale:
		in.confirmed, in.err = false, "更新内容已变化，请重新查看"
		if m := find(w.doc.Doc(), id); m != nil && m.Pending != nil {
			in.pendingHash, in.pendingStamp = m.Pending.Hash, m.Pending.Fingerprint()
		}
	case !did:
		in.open = false
	default:
		in.open = false
		if approve {
			w.env.Toast("已确认更新")
		}
	}
}

// ---- preview

// previewData is what a preview shows.
type previewData struct {
	Name, Version, Source string
	// Hosts and Scripts are what needs the user's confirmation.
	Hosts   []string
	Scripts []model.ModuleScriptChange
	// Direct are the DIRECT routing rules ("type,value"): they send traffic
	// past the proxy.
	Direct []string
	// Downloads are the files fetched for the module.
	Downloads []modules.Fetch
	// Contents are the whole converted contents; the other categories
	// list under folds. OnlyAdded shows the confirmation items alone.
	Contents    model.ModuleContents
	Unsupported []model.UnsupportedItem
	Failures    []Failure
	OnlyAdded   bool
}

// maxShown caps the lines a list draws; copying takes them all.
const maxShown = 300

// preview is what a module would add: its name and counts, the hostnames
// to decrypt and the scripts to run first and set apart, the other
// categories folded, the downloads that failed and the lines that could
// not be converted.
func (w *Workspace) preview(c *ui.Context, p previewData) {
	k := kit.T(c)
	ui.Row(c).Gap(8).AlignItems(ui.Center).MinWidth(0).Shrink(0).Children(func() {
		ui.Text(c, p.Name).FontSize(15).FontWeight(600).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
		if p.Version != "" {
			ui.Text(c, p.Version).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine().Shrink(0)
		}
		ui.Spacer(c)
		ui.Text(c, p.Source).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
	})
	if !p.OnlyAdded {
		n := p.Contents.Counts()
		ui.Row(c).Gap(14).Shrink(0).Children(func() {
			for _, s := range []struct {
				n     int
				label string
			}{{n.Hostnames, "主机名"}, {n.Rewrites, "改写"}, {n.Scripts, "脚本"}, {n.Rules, "规则"}} {
				ui.Row(c).Gap(4).AlignItems(ui.Baseline).Children(func() {
					ui.Text(c, kit.Count(s.n)).FontSize(18).FontWeight(600).FontFeatures("tnum")
					ui.Text(c, s.label).FontSize(kit.FontCaption).TextColor(k.Label3)
				})
			}
		})
	}
	if len(p.Hosts) > 0 {
		title := fmt.Sprintf("将解密 %s 个主机名", kit.Count(len(p.Hosts)))
		w.sensitiveBox(c, title, func() {
			for i, h := range p.Hosts {
				if i >= maxShown {
					ui.Text(c, fmt.Sprintf("另有 %s 个", kit.Count(len(p.Hosts)-maxShown))).FontSize(kit.FontCaption).TextColor(k.Label3)
					break
				}
				ui.Text(c, h).Font(kit.MonoFont).FontSize(kit.FontTable).Selectable().Wrap()
			}
		})
	}
	if len(p.Scripts) > 0 {
		sizes := map[string]int{}
		for _, s := range p.Contents.Scripts {
			sizes[model.ScriptApprovalKey(s)] = len(s.Source)
		}
		title := fmt.Sprintf("将运行 %s 个脚本", kit.Count(len(p.Scripts)))
		w.sensitiveBox(c, title, func() {
			for _, s := range p.Scripts {
				name := s.Name
				if name == "" {
					name = s.Tag
				}
				note := fmt.Sprintf("%s · %s", ShortSHA(s.SHA256), size(sizes[s.Key]))
				if s.Changed {
					note += " · 已变更"
				}
				ui.Column(c).Gap(1).MinWidth(0).Shrink(0).Children(func() {
					ui.Row(c).Gap(10).AlignItems(ui.Baseline).MinWidth(0).Children(func() {
						ui.Text(c, name).FontSize(kit.FontTable).FontWeight(500).Selectable().Wrap().Grow(1).MinWidth(0)
						ui.Text(c, s.Type).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine().Shrink(0)
						ui.Text(c, note).Font(kit.MonoFont).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine().Shrink(0)
					})
					if trigger := ScriptTrigger(s); trigger != "" {
						ui.Text(c, trigger).Font(kit.MonoFont).FontSize(kit.FontCaption).TextColor(k.Label2).Selectable().Wrap()
					}
				})
			}
		})
	}
	if len(p.Direct) > 0 {
		title := fmt.Sprintf("将直连 %s 条规则", kit.Count(len(p.Direct)))
		w.sensitiveBox(c, title, func() {
			for i, r := range p.Direct {
				if i >= maxShown {
					ui.Text(c, fmt.Sprintf("另有 %s 条", kit.Count(len(p.Direct)-maxShown))).FontSize(kit.FontCaption).TextColor(k.Label3)
					break
				}
				ui.Text(c, r).Font(kit.MonoFont).FontSize(kit.FontTable).Selectable().Wrap()
			}
		})
	}
	if len(p.Downloads) > 0 {
		w.downloadList(c, p.Downloads)
	}
	if len(p.Failures) > 0 {
		kit.Notice(c, kit.DangerTone, fmt.Sprintf("%s 个下载失败", kit.Count(len(p.Failures))), "", nil)
		ui.Column(c).Gap(2).Shrink(0).Children(func() {
			for _, f := range p.Failures {
				ui.Text(c, f.String()).FontSize(kit.FontCaption).TextColor(k.Label2).Selectable().Wrap()
			}
		})
	}
	if !p.OnlyAdded {
		for _, g := range Groups(p.Contents) {
			if g.Title == "主机名" || g.Title == "脚本" {
				continue
			}
			w.group(c, g)
		}
	}
	if len(p.Unsupported) > 0 {
		w.unsupportedList(c, p.Unsupported)
	}
}

// ScriptTrigger is when a script runs: its pattern, or its cron schedule.
func ScriptTrigger(s model.ModuleScriptChange) string {
	switch {
	case s.Pattern != "":
		return s.Pattern
	case s.Cron != "":
		return s.Cron
	}
	return ""
}

func downloadKind(kind string) string {
	if kind == modules.FetchScript {
		return "脚本"
	}
	return "数据"
}

// downloadList folds the addresses fetched for the module.
func (w *Workspace) downloadList(c *ui.Context, list []modules.Fetch) {
	k := kit.T(c)
	st := &w.install.view
	if st.open == nil {
		st.open = map[string]bool{}
	}
	open := st.open["下载"]
	kit.Disclosure(c, fmt.Sprintf("下载 %s", kit.Count(len(list))), &open, func() {
		for i, f := range list {
			if i >= maxShown {
				ui.Text(c, fmt.Sprintf("另有 %s 个", kit.Count(len(list)-maxShown))).FontSize(kit.FontCaption).TextColor(k.Label3)
				return
			}
			ui.Row(c).Gap(10).AlignItems(ui.Baseline).MinWidth(0).Children(func() {
				ui.Text(c, f.URL).Font(kit.MonoFont).FontSize(kit.FontCaption).TextColor(k.Label2).Selectable().Wrap().Grow(1).MinWidth(0)
				ui.Text(c, downloadKind(f.Kind)).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine().Shrink(0)
			})
		}
	})
	st.open["下载"] = open
}

// sensitiveBox sets apart what the user must confirm: a warning-tinted
// box with its title and a list that scrolls when long.
func (w *Workspace) sensitiveBox(c *ui.Context, title string, list func()) {
	k := kit.T(c)
	ui.Column(c).Padding(10, 12).Gap(6).Radius(10).Background(k.WarningWeak).Shrink(0).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			kit.Icon(c, kit.SymTriangleAlert, 15).TextColor(k.Warning)
			ui.Text(c, title).FontSize(kit.FontBody).FontWeight(600)
		})
		ui.Scroll(c.Key("sensitive-" + title)).MaxHeight(150).Gap(3).Children(list)
	})
}

// group is a category folded under its title with its count.
func (w *Workspace) group(c *ui.Context, g ContentGroup) {
	k := kit.T(c)
	st := &w.install.view
	if st.open == nil {
		st.open = map[string]bool{}
	}
	open := st.open[g.Title]
	title := fmt.Sprintf("%s %s", g.Title, kit.Count(len(g.Items)))
	kit.Disclosure(c, title, &open, func() { itemLines(c, g.Items, k.Label2, k.Label3) })
	st.open[g.Title] = open
}

// itemLines draws items as monospaced lines with their notes.
func itemLines(c *ui.Context, items []Item, fg, note ui.Color) {
	for i, it := range items {
		if i >= maxShown {
			ui.Text(c, fmt.Sprintf("另有 %s 项", kit.Count(len(items)-maxShown))).FontSize(kit.FontCaption).TextColor(note)
			return
		}
		ui.Row(c).Gap(10).AlignItems(ui.Baseline).MinWidth(0).Children(func() {
			ui.Text(c, it.Text).Font(kit.MonoFont).FontSize(kit.FontCaption).TextColor(fg).Selectable().Wrap().Grow(1).MinWidth(0)
			if it.Note != "" {
				ui.Text(c, it.Note).FontSize(kit.FontCaption).TextColor(note).SingleLine().Shrink(0)
			}
		})
	}
}

// unsupportedList is the lines that could not be converted, each with its
// reason, folded; one button copies them all.
func (w *Workspace) unsupportedList(c *ui.Context, items []model.UnsupportedItem) {
	st := &w.install.view
	if st.open == nil {
		st.open = map[string]bool{}
	}
	open := st.open["未支持"]
	ui.Row(c).AlignItems(ui.Start).Shrink(0).Children(func() {
		kit.Disclosure(c, fmt.Sprintf("未支持 %s", kit.Count(len(items))), &open, func() { unsupportedLines(c, items) }).Grow(1).MinWidth(0)
		if kit.IconButton(c, kit.SymCopy, "复制未支持的行", kit.IconButtonOptions{Size: 26, IconSize: 14}).Clicked() {
			c.WriteClipboard(UnsupportedText(items))
			w.env.Toast("已复制")
		}
	})
	st.open["未支持"] = open
}

// unsupportedLines draws the unsupported lines: where, why and the text.
func unsupportedLines(c *ui.Context, items []model.UnsupportedItem) {
	k := kit.T(c)
	for i, u := range items {
		if i >= maxShown {
			ui.Text(c, fmt.Sprintf("另有 %s 项", kit.Count(len(items)-maxShown))).FontSize(kit.FontCaption).TextColor(k.Label3)
			return
		}
		where := u.Section
		if u.Line > 0 {
			where += fmt.Sprintf(" · 第 %d 行", u.Line)
		}
		ui.Column(c).Gap(1).Shrink(0).Children(func() {
			ui.Text(c, where+" · "+u.Reason).FontSize(kit.FontTable).Selectable().Wrap()
			if u.Text != "" {
				ui.Text(c, u.Text).Font(kit.MonoFont).FontSize(kit.FontCaption).TextColor(k.Label3).Selectable().Wrap()
			}
		})
	}
}
