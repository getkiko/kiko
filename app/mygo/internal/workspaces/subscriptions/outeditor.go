package subscriptions

import (
	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/forms"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
	"github.com/getkiko/kiko/app/mygo/internal/subscription"
)

// outEditor is the dialog adding or editing an outbound: the name, the member picker of groups, and the protocol form.
// It edits a copy, saved by Save.
type outEditor struct {
	w     *Workspace
	open  bool
	isNew bool
	draft *model.Outbound
	tried bool
	group groupUI
	// advOpen unfolds a group's "Health check" or "Options".
	advOpen bool
	remove  kit.ConfirmState
	// form is the form's last report.
	form forms.Result
	// generation keys the form, so each opening starts fresh.
	generation int
}

func (e *outEditor) start(o *model.Outbound, isNew bool) {
	e.close()
	e.open, e.isNew, e.draft, e.tried = true, isNew, o, false
	e.remove = kit.ConfirmState{}
	e.form = forms.Result{Valid: true}
	e.group.reset(o.ID, o.Members)
	e.advOpen = false
	if model.IsGroup(o.Type) {
		// Patterns that are only literal alternatives become keywords;
		// the rest stay as "Advanced: Custom match".
		model.MigrateFilterKeywords(editableFilter(o))
	}
	e.generation++
	e.w.store.BeginEdit()
}

// openAdd starts a new outbound of typ; groups get their default names.
func (e *outEditor) openAdd(typ string) {
	e.start(model.MakeOutbound(typ, model.GroupModeDefaultName(typ)), true)
}

// openDraft edits an outbound not in the document yet, as an import.
func (e *outEditor) openDraft(o *model.Outbound) { e.start(o, true) }

func (e *outEditor) openEdit(doc *model.Doc, id string) {
	for _, o := range doc.Outbounds {
		if o.ID == id {
			e.start(model.Clone(o), false)
			return
		}
	}
}

func (e *outEditor) close() {
	if !e.open {
		return
	}
	e.open, e.draft = false, nil
	e.w.store.EndEdit()
}

// onChange keeps REALITY's uTLS on: enabling REALITY enables uTLS with
// the chrome fingerprint, and uTLS cannot be turned off under it.
func (e *outEditor) onChange(key string, value any) {
	f := e.draft.Fields
	if key == "tls.reality.enabled" && value == true {
		f["tls.utls.enabled"] = true
		if s, _ := f["tls.utls.fingerprint"].(string); s == "" {
			f["tls.utls.fingerprint"] = "chrome"
		}
	}
	if key == "tls.utls.enabled" && value != true && f["tls.reality.enabled"] == true {
		f["tls.utls.enabled"] = true
		e.w.env.Toast("REALITY 客户端必须启用 uTLS")
	}
}

// importAddress fills an HTTP or SOCKS outbound from a pasted proxy
// address in the server field.
func (e *outEditor) importAddress(key, text string) bool {
	if key != "server" || e.draft == nil {
		return false
	}
	patch, ok := model.ProxyAddressPatch(e.draft, text)
	if !ok {
		return false
	}
	e.draft.Fields = patch
	return true
}

func (e *outEditor) save() {
	o := e.draft
	doc := e.w.store.Doc()
	e.tried = true
	if !e.form.Valid {
		e.w.env.Toast("请修正 JSON 格式")
		return
	}
	if msg := model.TagValidationError(doc, o.Tag, o.ID); msg != "" {
		e.w.env.Toast(msg)
		return
	}
	// A group's filter is checked as it saves, normalized on a copy: a
	// failed save keeps the draft's filter as it was edited.
	var filter *model.GroupFilter
	if model.IsGroup(o.Type) {
		if o.Filter != nil {
			e.commitTagInputs(o.Filter)
		}
		probe := &model.Outbound{Filter: model.Clone(o.Filter)}
		normalizeFilter(probe)
		filter = probe.Filter
		if problem := filter.Error(); problem != "" {
			e.w.env.Toast(problem)
			return
		}
		if len(o.Members) == 0 && !filter.Active() {
			e.w.env.Toast("请至少选择地区、关键词或手动添加节点")
			return
		}
	}
	table := schema.Fields(schema.KindOutbound, o.Type)
	platform := doc.Platform
	if platform == "" {
		platform = "linux"
	}
	effective := model.EffectiveNetworkFields(table, o.Fields, o.NetworkConfigRefs, e.w.store.NetworkConfigs(), doc.Platform)
	if errs := schema.FormValidationErrors(table, effective, platform); len(errs) > 0 {
		if model.IsGroup(o.Type) && isGroupField(o.Type, errs[0].Field.Key) {
			e.advOpen = true
		}
		e.w.env.Toast(errs[0].Field.Label + "：" + errs[0].Message)
		return
	}
	saved := model.Clone(o)
	if model.IsGroup(o.Type) {
		saved.Filter = filter
	}
	// The fields of other modes stay on the draft until the save goes
	// through, so that a failed save keeps switching back lossless.
	model.PruneGroupFields(saved)
	if !e.w.update(func(d *model.Doc) { putOutbound(d, saved) }) {
		return
	}
	e.close()
}

func (e *outEditor) delete(done func(error)) {
	id := e.draft.ID
	if err := e.w.store.Update(func(d *model.Doc) { removeOutbound(d, id) }); err != nil {
		done(err)
		return
	}
	done(nil)
	e.close()
}

func (e *outEditor) view(c *ui.Context) {
	if !e.open || e.draft == nil {
		return
	}
	k := kit.T(c)
	o := e.draft
	doc := e.w.store.Doc()
	verb := "编辑"
	if e.isNew {
		verb = "添加"
	}
	name := o.Tag
	if name == "" {
		name = typeLabel(o.Type)
	}
	if model.IsGroup(o.Type) {
		e.groupView(c, doc, o)
		return
	}
	link, hasLink := subscription.FormatOutboundLink(o)
	open := true
	kit.Dialog(c, &open, verb+"出站 · "+name, 600, func() {
		fc := forms.Context{
			Doc: doc, NetworkConfigs: e.w.store.NetworkConfigs(), NetworkRefs: o.NetworkConfigRefs,
			OnNetworkRefs: func(r model.NetworkConfigRefs) { o.NetworkConfigRefs = r },
			ExcludeRef:    o.ID, ShowErrors: e.tried, OnChange: e.onChange,
		}
		if r, ok := e.w.store.Latency[o.ID]; ok {
			ui.Row(c).Padding(8, 12).Radius(12).Background(k.Fill).Shrink(0).Children(func() {
				ui.Text(c, "连接延迟").FontSize(kit.FontBody).Grow(1)
				nodeview.LatencyLabel(c, r)
			})
		}
		fc.Leading = func() {
			ui.Column(c).Gap(5).Shrink(0).Children(func() {
				ui.Text(c, "名称").FontSize(kit.FontBody).TextColor(k.Label2)
				kit.Input(c, "name", &o.Tag, "").Label("名称")
			})
		}
		fc.OnTextImport = e.importAddress
		e.form = forms.Form(c, []any{"outbound-form", o.Type, o.ID, e.generation}, schema.Fields(schema.KindOutbound, o.Type), o.Fields, fc)
	}, func() {
		if hasLink {
			kit.IconButton(c, kit.SymCopy, "复制分享链接", kit.IconButtonOptions{}).OnClick(func() {
				c.WriteClipboard(link)
				e.w.env.Toast("分享链接已复制")
			})
		}
		if !e.isNew {
			del := kit.Button(c, "删除", kit.ButtonOptions{Variant: kit.Danger, Small: true})
			if del.Clicked() {
				e.remove.Open = true
			}
			kit.ConfirmPopover(c, del, &e.remove, kit.ConfirmOptions{
				Title: "删除出站？", Message: "「" + name + "」将从出站列表中删除。", Confirm: "删除", Danger: true, Run: e.delete,
			})
		}
		ui.Spacer(c)
		kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(e.close)
		kit.Button(c, "保存", kit.ButtonOptions{Variant: kit.Primary}).OnClick(e.save)
	})
	if !open {
		e.close()
	}
}
