package modules

import (
	"context"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/modules"
)

const paramsWidth = 560

// intervals are the update intervals offered, in hours; 0 updates by hand.
var intervals = []int{0, 1, 6, 12, 24, 72, 168}

func intervalLabel(h int) string {
	switch {
	case h == 0:
		return "手动"
	case h%24 == 0:
		return strconv.Itoa(h/24) + " 天"
	}
	return strconv.Itoa(h) + " 小时"
}

// paramsEditor is the Parameters dialog: the module's arguments (key, default,
// current value, description) and its update interval. Saving converts the
// module again with the new values.
type paramsEditor struct {
	open, shown bool
	id          string
	args        []model.ModuleArgument
	values      []string
	interval    string
	hasURL      bool
	busy        bool
	err         string
	gen         int
}

func (p *paramsEditor) close() {
	gen := p.gen + 1
	*p = paramsEditor{gen: gen}
}

// openParams opens the dialog on module id.
func (w *Workspace) openParams(id string) {
	m := find(w.doc.Doc(), id)
	if m == nil {
		return
	}
	p := &w.params
	*p = paramsEditor{open: true, shown: true, id: id, gen: p.gen + 1, hasURL: m.Source.Kind == model.ModuleSourceURL}
	p.args = append([]model.ModuleArgument(nil), m.Arguments...)
	for _, a := range p.args {
		p.values = append(p.values, a.Value)
	}
	p.interval = strconv.Itoa(m.UpdateInterval)
}

// ParamsOpen reports whether the Parameters dialog is open.
func (w *Workspace) ParamsOpen() bool { return w.params.open }

func (w *Workspace) paramsDialog(c *ui.Context) {
	p := &w.params
	if !p.open {
		if p.shown {
			id := p.id
			p.close()
			w.focusID = id
		}
		return
	}
	m := find(w.doc.Doc(), p.id)
	if m == nil {
		p.open = false
		return
	}
	kit.Dialog(c, &p.open, "参数 · "+m.Name, paramsWidth, func() { w.paramsForm(c, m) }, func() {
		if kit.Button(c, "取消", kit.ButtonOptions{}).Clicked() {
			p.open = false
		}
		label := "保存"
		if p.busy {
			label = "保存中…"
		}
		if kit.Button(c, label, kit.ButtonOptions{Variant: kit.Primary, Disabled: p.busy}).Clicked() {
			w.saveParams(m)
		}
	})
}

func (w *Workspace) paramsForm(c *ui.Context, m *model.HTTPModule) {
	k := kit.T(c)
	p := &w.params
	for i := range p.args {
		a := p.args[i]
		kit.FormRow(c, a.Name, "", func() {
			kit.Input(c, "mod-arg-"+strconv.Itoa(i), &p.values[i], a.Default).Label(a.Name + " 的值")
			if a.Default != "" {
				ui.Text(c, "默认 "+a.Default).Font(kit.MonoFont).FontSize(kit.FontCaption).TextColor(k.Label3).Selectable().Wrap()
			}
			if a.Description != "" {
				ui.Text(c, a.Description).FontSize(kit.FontCaption).TextColor(k.Label3).Selectable().Wrap()
			}
		})
	}
	if p.hasURL {
		kit.FormRow(c, "自动更新", "", func() {
			options := make([]kit.Option, 0, len(intervals)+1)
			known := false
			for _, h := range intervals {
				known = known || strconv.Itoa(h) == p.interval
				options = append(options, kit.Option{Value: strconv.Itoa(h), Label: intervalLabel(h)})
			}
			if !known {
				n, _ := strconv.Atoi(p.interval)
				options = append(options, kit.Option{Value: p.interval, Label: intervalLabel(n)})
			}
			kit.Select(c, "mod-interval", &p.interval, options, kit.SelectOptions{Width: 140, Label: "自动更新"})
		})
	}
	if p.busy {
		kit.Notice(c, kit.AccentTone, "", "正在获取", nil)
	}
	if p.err != "" {
		kit.Notice(c, kit.DangerTone, "", p.err, nil)
	}
}

// saveParams converts the module again with the edited values when they
// changed, and sets the interval.
func (w *Workspace) saveParams(m *model.HTTPModule) {
	p := &w.params
	if p.busy {
		return
	}
	hours, _ := strconv.Atoi(p.interval)
	changed := false
	values := map[string]string{}
	for i, a := range p.args {
		v := strings.TrimSpace(p.values[i])
		values[a.Name] = v
		if v != a.Value {
			changed = true
		}
	}
	id, gen := m.ID, p.gen
	if !changed || m.Raw == "" {
		w.update(func(d *model.Doc) {
			if x := find(d, id); x != nil && p.hasURL {
				x.UpdateInterval = hours
			}
		})
		p.open = false
		return
	}
	p.busy, p.err = true, ""
	if w.updates.busy(id) {
		p.busy, p.err = false, "模块正在更新"
		return
	}
	w.enqueue(job{id: id, raw: true, arguments: values, interval: &hours, done: func(o Outcome, err error) {
		if gen != w.params.gen {
			return
		}
		p := &w.params
		p.busy = false
		switch {
		case err != nil:
			p.err = shorten(err.Error(), 160)
		default:
			p.open = false
			if o == Awaiting {
				w.env.Toast("有新内容待确认")
			}
		}
	}})
}

// ---- content

const contentWidth = 640

// contentView is the read-only view of a module's converted content.
type contentView struct {
	open, shown bool
	id          string
	folds       map[string]bool
}

// openContent shows the module's content and marks its update as seen.
func (w *Workspace) openContent(id string) {
	m := find(w.doc.Doc(), id)
	if m == nil {
		return
	}
	w.view = contentView{open: true, shown: true, id: id, folds: map[string]bool{}}
	if m.Updated {
		w.update(func(d *model.Doc) {
			if x := find(d, id); x != nil {
				x.Updated = false
			}
		})
	}
}

// ContentOpen reports whether the content view is open.
func (w *Workspace) ContentOpen() bool { return w.view.open }

func (w *Workspace) contentDialog(c *ui.Context) {
	v := &w.view
	if !v.open {
		if v.shown {
			w.focusID = v.id
			*v = contentView{}
		}
		return
	}
	m := find(w.doc.Doc(), v.id)
	if m == nil {
		v.open = false
		return
	}
	kit.Dialog(c, &v.open, m.Name, contentWidth, func() { w.contentForm(c, m) }, func() {
		if kit.Button(c, "复制全部", kit.ButtonOptions{Icon: kit.SymCopy}).Clicked() {
			c.WriteClipboard(ContentText(m))
			w.env.Toast("已复制")
		}
		if kit.Button(c, "关闭", kit.ButtonOptions{Variant: kit.Primary}).Clicked() {
			v.open = false
		}
	})
}

func (w *Workspace) contentForm(c *ui.Context, m *model.HTTPModule) {
	k := kit.T(c)
	v := &w.view
	meta := []string{SourceLabel(m)}
	if m.Version != "" {
		meta = append(meta, m.Version)
	}
	ui.Text(c, strings.Join(meta, " · ")).FontSize(kit.FontCaption).TextColor(k.Label3).Selectable().Wrap()
	if m.Description != "" {
		ui.Text(c, m.Description).FontSize(kit.FontTable).TextColor(k.Label2).Selectable().Wrap()
	}
	groups := Groups(m.Contents)
	for _, g := range groups {
		open := v.folds[g.Title]
		if len(groups) == 1 {
			open = true
		}
		kit.Disclosure(c, g.Title+" "+kit.Count(len(g.Items)), &open, func() { itemLines(c, g.Items, k.Label, k.Label3) })
		v.folds[g.Title] = open
	}
	if len(m.Unsupported) > 0 {
		open := v.folds["未支持"]
		kit.Disclosure(c, "未支持 "+kit.Count(len(m.Unsupported)), &open, func() { unsupportedLines(c, m.Unsupported) })
		v.folds["未支持"] = open
	}
	if len(groups) == 0 && len(m.Unsupported) == 0 {
		ui.Text(c, "没有内容").FontSize(kit.FontBody).TextColor(k.Label3)
	}
}

// ---- export

// export saves the module as a .kikomodule file.
func (w *Workspace) export(id string) {
	m := find(w.doc.Doc(), id)
	if m == nil || w.env.Shell == nil {
		return
	}
	data := modules.ExportKiko(m)
	name := fileName(m.Name) + ".kikomodule"
	shell := w.env.Shell
	env.Go(w.env, func(context.Context) (bool, error) {
		return shell.SaveFile(name, data, env.FileFilter{Name: "模块", Extensions: []string{"kikomodule"}})
	}, func(ok bool, err error) {
		switch {
		case err != nil:
			w.env.Toast("导出失败：" + err.Error())
		case ok:
			w.env.Toast("已导出")
		}
	})
}

// fileName is a module name made fit for a file name.
func fileName(name string) string {
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 0x20 {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	if name == "" {
		return "模块"
	}
	return name
}
