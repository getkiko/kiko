package connections

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/rewrites"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/scripts"
)

// rwEditors returns the rule editors of the shared Rewrites dialogs, nil
// without a document.
func (m *httpMonitor) rwEditors() *rewrites.Editors {
	if m.rw == nil && m.doc != nil {
		m.rw = rewrites.NewEditors(m.env, m.doc)
	}
	return m.rw
}

// scEditors returns the script editor dialog, nil without a document.
func (m *httpMonitor) scEditors() *scripts.Editors {
	if m.sc == nil && m.doc != nil {
		m.sc = scripts.NewEditors(m.env, m.doc)
	}
	return m.sc
}

// addScript opens the script editor for a new script that matches the
// record's URL and starts its test from the record: a response script when
// the record has a response.
func (m *httpMonitor) addScript(r *httpRow) {
	sc := m.scEditors()
	if sc == nil {
		return
	}
	seed := scripts.Seed{Pattern: rewrites.URLPattern(r.URL), TestURL: r.URL, Type: model.ScriptHTTPRequest,
		Record: &scripts.TestSeed{ID: r.ID, Side: api.HTTPSideRequest}}
	if r.Status > 0 {
		seed.Type = model.ScriptHTTPResponse
		seed.Record.Side = api.HTTPSideResponse
	}
	sc.New(seed)
}

// editScript opens the script a hit names; false when it is gone.
func (m *httpMonitor) editScript(h api.HTTPRewriteHit) bool {
	sc := m.scEditors()
	if sc == nil {
		return false
	}
	s := scripts.Find(m.docOf(), hitTag(h))
	return s != nil && sc.Edit(s.ID)
}

// hitTag is the tag of the script a hit names.
func hitTag(h api.HTTPRewriteHit) string {
	if h.Target != "" {
		return h.Target
	}
	if h.Rule != "" {
		return h.Rule
	}
	if h.Run != nil {
		return h.Run.Tag
	}
	return ""
}

// addRewrite opens the editor for a new rule of kind that matches the
// record's URL.
func (m *httpMonitor) addRewrite(r *httpRow, kind rewrites.Kind) {
	if rw := m.rwEditors(); rw != nil {
		rw.New(kind, rewrites.Seed{Pattern: rewrites.URLPattern(r.URL), TestURL: r.URL})
	}
}

// saveAsLocalMapping opens the local mapping editor with the record's
// response: its status, its headers without framing and encoding fields
// and its decoded body.
func (m *httpMonitor) saveAsLocalMapping(r *httpRow) {
	rw := m.rwEditors()
	if rw == nil {
		return
	}
	headers := make([]model.HeaderField, len(r.ResponseHeaders))
	for i, h := range r.ResponseHeaders {
		headers[i] = model.HeaderField{Name: h.Name, Value: h.Value}
	}
	const incomplete = "正文不完整，请手动填写"
	open := func(body []byte, notice string) {
		rule, err := rewrites.MapLocalFromResponse(r.URL, r.Status, headers, body)
		if err != nil {
			m.env.Toast(err.Error())
			rule.Body, rule.BodyBase64 = "", ""
		}
		rw.New(rewrites.KindMapLocal, rewrites.Seed{Pattern: rule.Pattern, TestURL: r.URL, MapLocal: &rule, Notice: notice})
	}
	if r.ResponseBytes == 0 {
		open(nil, "")
		return
	}
	if !r.ResponseBodySaved || r.BodyDropped {
		open(nil, incomplete)
		return
	}
	m.fetchBody(r.ID, api.HTTPSideResponse, 0, int64(model.MaxMapLocalBody)+1, true, func(body api.HTTPBody, err error) {
		if err != nil {
			m.env.Toast("读取正文失败：" + err.Error())
			open(nil, incomplete)
			return
		}
		if body.Truncated || r.ResponseBodyTrunc || body.DecodeError != "" {
			open(nil, incomplete)
			return
		}
		open(body.Data, "")
	})
}

// rewriteMenu adds the rewrite actions to a record's context menu.
func (m *httpMonitor) rewriteMenu(menu *ui.Menu, r *httpRow) {
	menu.Separator()
	if menu.Item("保存为本地映射").Disabled(m.doc == nil || r.live()).Chosen() {
		m.saveAsLocalMapping(r)
	}
	if menu.Item("为此 URL 添加改写").Disabled(m.doc == nil).Chosen() {
		m.addRewrite(r, rewrites.KindURL)
	}
	if menu.Item("为此请求添加脚本").Disabled(m.doc == nil).Chosen() {
		m.addScript(r)
	}
}

// rewriteButtons are the inspector's rewrite actions.
func (m *httpMonitor) rewriteButtons(c *ui.Context, r *httpRow) {
	ui.Column(c).Gap(8).AlignItems(ui.Start).Shrink(0).Children(func() {
		kit.Button(c, "保存为本地映射", kit.ButtonOptions{Small: true, Icon: kit.SymFileDown, Disabled: m.doc == nil || r.live()}).
			OnClick(func() { m.saveAsLocalMapping(r) })
		kit.Button(c, "为此 URL 添加改写", kit.ButtonOptions{Small: true, Icon: kit.SymWandSparkles, Disabled: m.doc == nil}).
			OnClick(func() { m.addRewrite(r, rewrites.KindURL) })
		kit.Button(c, "为此请求添加脚本", kit.ButtonOptions{Small: true, Icon: kit.SymFileCode, Disabled: m.doc == nil}).
			OnClick(func() { m.addScript(r) })
	})
}

// changeMark says that a rewrite changed a header: the value it had and
// the rule.
type changeMark struct {
	rule, before string
	had          bool
}

// tip is the hover text of a changed header.
func (c changeMark) tip() string {
	if !c.had {
		return "新增 · " + c.rule
	}
	return "原值：" + c.before + " · " + c.rule
}

// headerMarks are the headers a record's rewrites changed on one side
// (api.HTTPSideRequest or HTTPSideResponse), by lower-case name.
func headerMarks(doc *model.Doc, r *httpRow, side string) map[string]changeMark {
	var marks map[string]changeMark
	for _, hit := range r.Rewrites {
		if hit.Kind != "header" || hit.Side != side {
			continue
		}
		for _, ch := range hit.Changes {
			if !ch.HasAfter {
				continue
			}
			if marks == nil {
				marks = map[string]changeMark{}
			}
			marks[strings.ToLower(ch.Name)] = changeMark{hitTitle(doc, hit), ch.Before, ch.HadBefore}
		}
	}
	return marks
}

// rewriteKindLabel names what a rewrite hit did.
func rewriteKindLabel(h api.HTTPRewriteHit) string {
	switch h.Kind {
	case "url":
		return "URL 重写"
	case "map_local":
		return "本地映射"
	case "connect":
		return "连接拒绝"
	case "body":
		if h.Side == api.HTTPSideResponse {
			return "响应正文"
		}
		return "请求正文"
	case "header":
		if h.Side == api.HTTPSideResponse {
			return "响应头"
		}
		return "请求头"
	case "script":
		switch h.Side {
		case api.HTTPSideResponse:
			return "响应脚本"
		case api.HTTPSideRequest:
			return "请求脚本"
		}
		return "脚本"
	}
	return h.Kind
}

// rewriteDetail is the action line of a hit.
func rewriteDetail(h api.HTTPRewriteHit) string {
	var parts []string
	switch h.Kind {
	case "url":
		action := rewrites.URLActionLabel(h.Action)
		if h.Target != "" {
			action += " → " + h.Target
		}
		parts = append(parts, action)
	case "body":
		if h.Action == "skipped" {
			parts = append(parts, "未改写 · "+bodySkipLabel(h.Target))
		} else {
			parts = append(parts, "已改写")
		}
	case "connect":
		parts = append(parts, "拒绝")
	case "script":
		label := scriptActionLabel(h.Action)
		if reason := scriptSkipReason(h); reason != "" {
			label += " · " + bodySkipLabel(reason)
		}
		parts = append(parts, label)
		if h.Run != nil && h.Run.DurationUS > 0 {
			parts = append(parts, scriptDuration(h.Run.DurationUS))
		}
	}
	if h.Status > 0 {
		parts = append(parts, "状态 "+strconv.Itoa(h.Status))
	}
	return strings.Join(parts, " · ")
}

// scriptActionLabel names what a script hit did.
func scriptActionLabel(action string) string { return scripts.ActionLabel(action) }

// scriptDuration formats a run time given in microseconds.
func scriptDuration(us int64) string { return scripts.Duration(us) }

// scriptSkipReason is why a script did not run: the run's reason, or the
// change {Op: "skipped", Name: reason} of older cores.
func scriptSkipReason(h api.HTTPRewriteHit) string {
	if h.Run != nil && h.Run.Reason != "" {
		return h.Run.Reason
	}
	for _, ch := range h.Changes {
		if ch.Op == "skipped" {
			return ch.Name
		}
	}
	return ""
}

// hitTitle names the rule or script behind a hit. A script hit carries the
// script's tag as target.
func hitTitle(doc *model.Doc, h api.HTTPRewriteHit) string {
	if o, ok := moduleOfHit(doc, h); ok {
		return o.title()
	}
	if h.Kind != "script" {
		return rewrites.RuleName(doc, h.Rule)
	}
	tag := h.Target
	if tag == "" {
		tag = h.Rule
	}
	if doc != nil && doc.HTTPTools != nil {
		for _, s := range doc.HTTPTools.Scripts {
			if s != nil && s.EffectiveTag() == tag && s.Name != "" {
				return s.Name
			}
		}
	}
	return tag
}

// bodySkipLabel names why a body rule left the message alone.
func bodySkipLabel(reason string) string { return scripts.SkipLabel(reason) }

// rewritesSection lists what the rewrite rules did to the exchange.
func (m *httpMonitor) rewritesSection(c *ui.Context, r *httpRow) {
	if len(r.Rewrites) == 0 {
		return
	}
	k := kit.T(c)
	doc := m.docOf()
	kit.Disclosure(c, "改写 · "+strconv.Itoa(len(r.Rewrites)), m.insp.disclosure("rewrites", true), func() {
		for i, h := range r.Rewrites {
			ui.Column(c.Key(i)).Gap(3).PaddingY(4).Shrink(0).Children(func() {
				ui.Row(c).Gap(8).Children(func() {
					kit.Badge(c, rewriteKindLabel(h), kit.AccentTone)
					kit.Clipped(c, hitTitle(doc, h), kit.FontTable, 600).Grow(1)
					if o, ok := moduleOfHit(doc, h); ok {
						moduleButton(c, m.env, o)
					} else if h.Kind == "script" {
						kit.Button(c, "编辑脚本", kit.ButtonOptions{Small: true, Variant: kit.TextButton,
							Disabled: m.doc == nil || scripts.Find(doc, hitTag(h)) == nil}).
							OnClick(func() { m.editScript(h) })
					}
				})
				if detail := rewriteDetail(h); detail != "" {
					ui.Text(c, detail).FontSize(kit.FontTable).TextColor(k.Label2).Wrap().Selectable()
				}
				if h.Run != nil {
					scriptRunLines(c, h.Run)
				}
				for _, ch := range h.Changes {
					if h.Kind == "script" && ch.Op == "skipped" {
						continue
					}
					if h.Kind == "body" && isBodyEdit(ch.Op) {
						bodyChangeLine(c, ch)
					} else {
						changeLine(c, ch)
					}
				}
			})
		}
	})
}

// scriptRunLines shows the error and console lines of a script run.
func scriptRunLines(c *ui.Context, run *api.HTTPScriptRun) {
	k := kit.T(c)
	if run.Error != "" {
		ui.Text(c, run.Error).FontSize(kit.FontTable).Font(kit.MonoFont).Wrap().Selectable().TextColor(k.Danger)
	}
	for _, l := range run.Logs {
		color := k.Label2
		if l.Level == "error" {
			color = k.Danger
		}
		ui.Text(c, l.Message).FontSize(kit.FontTable).Font(kit.MonoFont).Wrap().Selectable().TextColor(color)
	}
}

// changeLine is one header change: the operation, the name, and the value
// before and after; a deleted header shows its value struck through.
func changeLine(c *ui.Context, ch api.HTTPRewriteChange) {
	k := kit.T(c)
	ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
		ui.Text(c, changeOpLabel(ch.Op)).FontSize(kit.FontCaption).TextColor(k.Label3).Width(56).Shrink(0)
		ui.Text(c, ch.Name).FontSize(kit.FontTable).FontWeight(600).TextColor(k.Label2).Shrink(0)
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			if ch.HadBefore {
				before := ui.Text(c, ch.Before).FontSize(kit.FontTable).Font(kit.MonoFont).Wrap().Selectable().TextColor(k.Label3)
				if !ch.HasAfter {
					before.Strikethrough()
				}
			}
			if ch.HasAfter {
				prefix := ""
				if ch.HadBefore {
					prefix = "→ "
				}
				ui.Text(c, prefix+ch.After).FontSize(kit.FontTable).Font(kit.MonoFont).Wrap().Selectable().TextColor(k.AccentText)
			}
		})
	})
}

// bodyChangeLine is one body edit: the operation, the path or expression,
// and how many places it changed.
func bodyChangeLine(c *ui.Context, ch api.HTTPRewriteChange) {
	k := kit.T(c)
	ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
		ui.Text(c, rewrites.BodyActionLabel(ch.Op)).FontSize(kit.FontCaption).TextColor(k.Label3).Width(56).Shrink(0)
		ui.Text(c, ch.Name).FontSize(kit.FontTable).Font(kit.MonoFont).Wrap().Selectable().TextColor(k.Label2).Grow(1).MinWidth(0)
		if ch.HasAfter {
			ui.Text(c, ch.After+" 处").FontSize(kit.FontTable).FontFeatures("tnum").TextColor(k.AccentText).Shrink(0)
		}
	})
}

// isBodyEdit reports whether op is a body edit; other changes of a body hit
// are header fields the core fixed up (a dropped digest or trailer).
func isBodyEdit(op string) bool { return model.ValidBodyAction(op) }

// changeOpLabel names a header change's operation.
func changeOpLabel(op string) string {
	if op == "trailer-dropped" {
		return "丢弃"
	}
	return rewrites.HeaderActionLabel(op)
}
