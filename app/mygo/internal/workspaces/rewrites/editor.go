package rewrites

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Doc is the document the editors read and change; store.Store is one.
type Doc interface {
	Doc() *model.Doc
	Update(fn func(doc *model.Doc)) error
}

// editGuard is a Doc that holds the server's changes back while an
// editor is open (store.Store).
type editGuard interface {
	BeginEdit()
	EndEdit()
}

// Seed prefills a new rule. Pattern and TestURL fill the pattern field and
// its test; MapLocal, when set, fills the local mapping's status, headers
// and body.
type Seed struct {
	Pattern  string
	TestURL  string
	MapLocal *model.MapLocal
	// Notice is a short warning shown in the dialog while it is open.
	Notice string
}

// dialogWidth is the editor dialog's width.
const dialogWidth = 660

// opDraft is a header operation being edited.
type opDraft struct {
	key                        int
	action, name, value, regex string
}

// editDraft is a body edit being edited.
type editDraft struct {
	key                        int
	action, regex, path, value string
}

// fieldDraft is a header field of a local mapping being edited.
type fieldDraft struct {
	key         int
	name, value string
}

// editor is the draft of the rule the dialog edits.
type editor struct {
	open    bool
	kind    Kind
	isNew   bool
	id      string
	enabled bool
	name    string
	pattern string
	test    string

	action, target string

	scope int
	ops   []*opDraft

	// contentTypes is the comma separated list of the body rule; edits its
	// body edits.
	contentTypes string
	edits        []*editDraft

	status string
	fields []*fieldDraft
	body   string
	// binary is the base64 body of a file that is not text; binaryName
	// is the file's name.
	binary, binaryName string

	keys      int
	submitted bool
	saveErr   string
	fileErr   string
	notice    string
	importing bool
}

func (e *editor) nextKey() int {
	e.keys++
	return e.keys
}

func (e *editor) addOp(action string) *opDraft {
	op := &opDraft{key: e.nextKey(), action: action}
	e.ops = append(e.ops, op)
	return op
}

func (e *editor) addEdit(action string) *editDraft {
	d := &editDraft{key: e.nextKey(), action: action}
	e.edits = append(e.edits, d)
	return d
}

func (e *editor) addField(name, value string) {
	e.fields = append(e.fields, &fieldDraft{key: e.nextKey(), name: name, value: value})
}

// Editors is the rule editor dialog, shared by the Rewrites workspace and the
// HTTP inspector. Build it each frame with View; open it with New or Edit.
type Editors struct {
	env *env.Env
	doc Doc

	// OnSaved, when set, is told the kind and id of a rule the dialog
	// saved.
	OnSaved func(kind Kind, id string)

	ed      *editor
	guarded bool
	closed  bool
}

// NewEditors returns the editors of the document d.
func NewEditors(e *env.Env, d Doc) *Editors { return &Editors{env: e, doc: d} }

// Open reports whether the dialog is open.
func (x *Editors) Open() bool { return x.ed != nil && x.ed.open }

// Close closes the dialog without saving.
func (x *Editors) Close() {
	if x.ed != nil {
		x.ed.open = false
		x.ed = nil
		x.closed = true
	}
	x.sync()
}

// TakeClosed reports that the dialog closed since the last call, so the
// opener can take the keyboard focus back.
func (x *Editors) TakeClosed() bool {
	closed := x.closed
	x.closed = false
	return closed
}

// New opens the dialog for a new rule of kind.
func (x *Editors) New(kind Kind, seed Seed) {
	e := &editor{open: true, kind: kind, isNew: true, enabled: true, pattern: seed.Pattern, test: seed.TestURL,
		action: model.URLActionReject, status: "200", notice: seed.Notice}
	e.addOp(model.HeaderActionAdd)
	e.addEdit(model.BodyActionReplaceRegex)
	if m := seed.MapLocal; m != nil {
		e.status = strconv.Itoa(m.Status)
		for _, h := range m.Headers {
			e.addField(h.Name, h.Value)
		}
		e.body, e.binary = m.Body, m.BodyBase64
		if e.binary != "" {
			e.binaryName = "二进制内容"
		}
	}
	x.ed, x.closed = e, false
}

// Edit opens the dialog for the rule with id; false when it is gone.
func (x *Editors) Edit(kind Kind, id string) bool {
	if x.doc == nil {
		return false
	}
	r := rulesOf(x.doc.Doc())
	if r == nil {
		return false
	}
	e := &editor{open: true, kind: kind, id: id, status: "200"}
	found := false
	switch kind {
	case KindURL:
		for _, u := range r.URL {
			if u.ID == id {
				found = true
				e.name, e.pattern, e.enabled, e.action, e.target = u.Name, u.Pattern, !u.Disabled, u.Action, u.Target
			}
		}
	case KindHeader:
		for _, h := range r.Headers {
			if h.ID == id {
				found = true
				e.name, e.pattern, e.enabled = h.Name, h.Pattern, !h.Disabled
				if h.Scope == model.HeaderScopeResponse {
					e.scope = 1
				}
				for _, op := range h.Ops {
					if op == nil {
						continue
					}
					d := e.addOp(op.Action)
					d.name, d.value, d.regex = op.Name, op.Value, op.Regex
				}
			}
		}
		if len(e.ops) == 0 {
			e.addOp(model.HeaderActionAdd)
		}
	case KindBody:
		for _, b := range r.Body {
			if b.ID == id {
				found = true
				e.name, e.pattern, e.enabled = b.Name, b.Pattern, !b.Disabled
				if b.Scope == model.HeaderScopeResponse {
					e.scope = 1
				}
				e.contentTypes = strings.Join(b.ContentTypes, ", ")
				for _, ed := range b.Edits {
					if ed == nil {
						continue
					}
					d := e.addEdit(ed.Action)
					d.regex, d.path, d.value = ed.Regex, ed.Path, ed.Value
				}
			}
		}
		if len(e.edits) == 0 {
			e.addEdit(model.BodyActionReplaceRegex)
		}
	case KindMapLocal:
		for _, m := range r.MapLocal {
			if m.ID == id {
				found = true
				e.name, e.pattern, e.enabled = m.Name, m.Pattern, !m.Disabled
				e.status = strconv.Itoa(m.Status)
				for _, h := range m.Headers {
					e.addField(h.Name, h.Value)
				}
				e.body, e.binary = m.Body, m.BodyBase64
				if e.binary != "" {
					e.binaryName = "二进制内容"
				}
			}
		}
	}
	if !found {
		return false
	}
	x.ed, x.closed = e, false
	return true
}

// issue is a problem of a field; required ones show once a save was tried.
type issue struct {
	msg      string
	required bool
}

func (i issue) shown(submitted bool) string {
	if i.required && !submitted {
		return ""
	}
	return i.msg
}

// issues are the problems of the draft by field.
type issues struct {
	pattern, target, status, body, ops, edits, types issue
	opRows, fieldRows, editRows                      []issue
}

func (s issues) any() bool {
	if s.pattern.msg != "" || s.target.msg != "" || s.status.msg != "" || s.body.msg != "" || s.ops.msg != "" || s.edits.msg != "" || s.types.msg != "" {
		return true
	}
	for _, i := range s.opRows {
		if i.msg != "" {
			return true
		}
	}
	for _, i := range s.fieldRows {
		if i.msg != "" {
			return true
		}
	}
	for _, i := range s.editRows {
		if i.msg != "" {
			return true
		}
	}
	return false
}

// compute checks the draft with the model's validation, field by field.
func (e *editor) compute() issues {
	var s issues
	s.pattern = issue{model.PatternIssue(e.pattern), strings.TrimSpace(e.pattern) == ""}
	switch e.kind {
	case KindURL:
		if s.pattern.msg == "" {
			if msg := e.urlRule().Issue(); msg != "" {
				s.target = issue{msg, strings.TrimSpace(e.target) == ""}
			}
		}
	case KindHeader:
		if len(e.ops) == 0 {
			s.ops = issue{"请添加至少一项操作", true}
		}
		for _, op := range e.ops {
			probe := &model.HeaderRewrite{Pattern: ".", Scope: model.HeaderScopeRequest, Ops: []*model.HeaderEdit{e.edit(op)}}
			required := op.name == "" || op.action == model.HeaderActionAdd && op.value == "" ||
				op.action == model.HeaderActionReplaceRegex && op.regex == ""
			s.opRows = append(s.opRows, issue{probe.Issue(), required})
		}
	case KindBody:
		if len(e.edits) == 0 {
			s.edits = issue{"请添加至少一项编辑", true}
		}
		if len(e.edits) > model.MaxBodyEdits {
			s.edits = issue{"编辑项过多", false}
		}
		for _, ed := range e.edits {
			required := ed.regex == "" && ed.action == model.BodyActionReplaceRegex ||
				ed.path == "" && ed.action != model.BodyActionReplaceRegex ||
				strings.TrimSpace(ed.value) == "" && (ed.action == model.BodyActionJSONSet || ed.action == model.BodyActionJSONAppend)
			s.editRows = append(s.editRows, issue{e.bodyEdit(ed).Issue(), required})
		}
		if msg := (&model.BodyRewrite{Pattern: ".", Scope: model.HeaderScopeRequest, ContentTypes: e.typeList(), Edits: []*model.BodyEdit{{Action: model.BodyActionJSONDelete, Path: "$.a"}}}).Issue(); msg != "" {
			s.types = issue{msg, false}
		}
	case KindMapLocal:
		if status, err := strconv.Atoi(strings.TrimSpace(e.status)); err != nil || status < 200 || status > 599 {
			s.status = issue{"状态码须在 200–599 之间", strings.TrimSpace(e.status) == ""}
		}
		for _, f := range e.fields {
			var msg string
			switch {
			case f.name == "" && f.value == "":
			case model.HeaderNameIssue(f.name) != "":
				msg = model.HeaderNameIssue(f.name)
			default:
				msg = model.HeaderValueIssue(f.value)
			}
			s.fieldRows = append(s.fieldRows, issue{msg, false})
		}
		if e.bodySize() > model.MaxMapLocalBody {
			s.body = issue{"正文超过 2 MiB", false}
		} else if e.bodySize() > 0 && s.status.msg == "" {
			if status, _ := strconv.Atoi(strings.TrimSpace(e.status)); status == 204 || status == 205 || status == 304 {
				s.status = issue{"该状态码不能带正文", false}
			}
		}
	}
	return s
}

func (e *editor) bodySize() int {
	if e.binary != "" {
		return base64.StdEncoding.DecodedLen(len(e.binary)) - strings.Count(e.binary[max(0, len(e.binary)-2):], "=")
	}
	return len(e.body)
}

func (e *editor) edit(op *opDraft) *model.HeaderEdit {
	edit := &model.HeaderEdit{Action: op.action, Name: strings.TrimSpace(op.name), Value: op.value}
	switch op.action {
	case model.HeaderActionDelete:
		edit.Value = ""
	case model.HeaderActionReplaceRegex:
		edit.Regex = op.regex
	}
	return edit
}

func (e *editor) urlRule() *model.URLRewrite {
	r := &model.URLRewrite{ID: e.id, Name: strings.TrimSpace(e.name), Disabled: !e.enabled, Pattern: e.pattern, Action: e.action}
	if e.action == model.URLActionFound || e.action == model.URLActionTemporary || e.action == model.URLActionHeader {
		r.Target = strings.TrimSpace(e.target)
	}
	return r
}

// typeList is the content types of the draft, trimmed.
func (e *editor) typeList() []string {
	var out []string
	for _, t := range strings.FieldsFunc(e.contentTypes, func(r rune) bool { return r == ',' || r == '，' || r == '\n' }) {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func (e *editor) bodyEdit(d *editDraft) *model.BodyEdit {
	edit := &model.BodyEdit{Action: d.action}
	switch d.action {
	case model.BodyActionReplaceRegex:
		edit.Regex, edit.Value = d.regex, d.value
	case model.BodyActionJSONDelete:
		edit.Path = strings.TrimSpace(d.path)
	default:
		edit.Path, edit.Value = strings.TrimSpace(d.path), d.value
	}
	return edit
}

func (e *editor) bodyRule() *model.BodyRewrite {
	r := &model.BodyRewrite{ID: e.id, Name: strings.TrimSpace(e.name), Disabled: !e.enabled, Pattern: e.pattern,
		Scope: model.HeaderScopeRequest, ContentTypes: e.typeList()}
	if e.scope == 1 {
		r.Scope = model.HeaderScopeResponse
	}
	for _, d := range e.edits {
		r.Edits = append(r.Edits, e.bodyEdit(d))
	}
	return r
}

func (e *editor) headerRule() *model.HeaderRewrite {
	r := &model.HeaderRewrite{ID: e.id, Name: strings.TrimSpace(e.name), Disabled: !e.enabled, Pattern: e.pattern, Scope: model.HeaderScopeRequest}
	if e.scope == 1 {
		r.Scope = model.HeaderScopeResponse
	}
	for _, op := range e.ops {
		r.Ops = append(r.Ops, e.edit(op))
	}
	return r
}

func (e *editor) mapRule() *model.MapLocal {
	status, _ := strconv.Atoi(strings.TrimSpace(e.status))
	r := &model.MapLocal{ID: e.id, Name: strings.TrimSpace(e.name), Disabled: !e.enabled, Pattern: e.pattern, Status: status}
	for _, f := range e.fields {
		if f.name == "" && f.value == "" {
			continue
		}
		r.Headers = append(r.Headers, model.HeaderField{Name: strings.TrimSpace(f.name), Value: f.value})
	}
	if e.binary != "" {
		r.BodyBase64 = e.binary
	} else {
		r.Body = e.body
	}
	return r
}

// save checks the draft and puts the rule into the document: appended
// when new, in place when it exists. It returns whether the dialog may
// close.
func (x *Editors) save(e *editor) bool {
	e.submitted, e.saveErr = true, ""
	if e.compute().any() {
		return false
	}
	if x.doc == nil {
		e.saveErr = "无法保存"
		return false
	}
	// Nothing may be saved that the core config would drop.
	var ruleIssue string
	switch e.kind {
	case KindURL:
		ruleIssue = e.urlRule().Issue()
	case KindHeader:
		ruleIssue = e.headerRule().Issue()
	case KindBody:
		ruleIssue = e.bodyRule().Issue()
	case KindMapLocal:
		ruleIssue = e.mapRule().Issue()
	}
	if ruleIssue != "" {
		e.saveErr = ruleIssue
		return false
	}
	if e.kind == KindMapLocal {
		total := e.bodySize()
		if r := rulesOf(x.doc.Doc()); r != nil {
			for _, m := range r.MapLocal {
				if m.ID != e.id {
					total += m.BodyBytes()
				}
			}
		}
		if total > model.MaxMapLocalBodyTotal {
			e.saveErr = "本地映射的正文合计超过 4 MiB"
			return false
		}
	}
	id := e.id
	if e.isNew {
		id = model.NewID(e.kind.idPrefix())
	}
	gone := false
	err := x.doc.Update(func(d *model.Doc) {
		r := ensure(d)
		keep := !e.isNew
		switch e.kind {
		case KindURL:
			rule := e.urlRule()
			rule.ID = id
			r.URL, gone = upsert(r.URL, rule, keep, func(u *model.URLRewrite) bool { return u.ID == id })
		case KindHeader:
			rule := e.headerRule()
			rule.ID = id
			r.Headers, gone = upsert(r.Headers, rule, keep, func(h *model.HeaderRewrite) bool { return h.ID == id })
		case KindBody:
			rule := e.bodyRule()
			rule.ID = id
			r.Body, gone = upsert(r.Body, rule, keep, func(b *model.BodyRewrite) bool { return b.ID == id })
		case KindMapLocal:
			rule := e.mapRule()
			rule.ID = id
			r.MapLocal, gone = upsert(r.MapLocal, rule, keep, func(m *model.MapLocal) bool { return m.ID == id })
		}
	})
	if err != nil {
		e.saveErr = err.Error()
		return false
	}
	if gone {
		e.saveErr = "规则已被删除"
		return false
	}
	if x.OnSaved != nil {
		x.OnSaved(e.kind, id)
	}
	return true
}

// upsert replaces the item match finds with v, else appends v. With
// existing set, a missing item is not added and gone reports it.
func upsert[T any](list []T, v T, existing bool, match func(T) bool) (out []T, gone bool) {
	for i, item := range list {
		if match(item) {
			list[i] = v
			return list, false
		}
	}
	if existing {
		return list, true
	}
	return append(list, v), false
}

// picked is a file the user chose.
type picked struct {
	data []byte
	name string
	ok   bool
}

// chooseFile asks for a file and takes it as the body.
func (x *Editors) chooseFile(e *editor) {
	if e.importing {
		return
	}
	if x.env == nil || x.env.Shell == nil {
		e.fileErr = "无法选择文件"
		return
	}
	e.importing, e.fileErr = true, ""
	shell := x.env.Shell
	env.Go(x.env, func(context.Context) (picked, error) {
		data, name, ok, err := shell.OpenFile()
		return picked{data, name, ok}, err
	}, func(p picked, err error) {
		e.importing = false
		switch {
		case err != nil:
			e.fileErr = "读取失败：" + err.Error()
		case p.ok:
			e.takeFile(p.name, p.data)
		}
	})
}

// takeFile makes data the body: as text when it is text, else as base64.
// A file beyond the limit is refused and the body stays.
func (e *editor) takeFile(name string, data []byte) {
	if len(data) > model.MaxMapLocalBody {
		e.fileErr = "正文超过 2 MiB"
		return
	}
	e.fileErr = ""
	if IsText(data) {
		e.body, e.binary, e.binaryName = string(data), "", ""
		return
	}
	e.body, e.binary, e.binaryName = "", base64.StdEncoding.EncodeToString(data), name
}

// sync holds the store's edit while the dialog is open.
func (x *Editors) sync() {
	open := x.ed != nil
	if open == x.guarded {
		return
	}
	x.guarded = open
	if g, ok := x.doc.(editGuard); ok {
		if open {
			g.BeginEdit()
		} else {
			g.EndEdit()
		}
	}
}

// View builds the dialog while it is open.
func (x *Editors) View(c *ui.Context) {
	x.sync()
	e := x.ed
	if e == nil {
		return
	}
	title := "添加 " + e.kind.Label()
	if !e.isNew {
		title = "编辑 " + e.kind.Label()
	}
	saved := false
	kit.Dialog(c, &e.open, title, dialogWidth, func() { x.form(c, e) }, func() {
		kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(func() { e.open = false })
		save := kit.Button(c, "保存", kit.ButtonOptions{Variant: kit.Primary})
		if save.Clicked() {
			saved = x.save(e)
		}
	})
	if saved {
		e.open = false
	}
	if !e.open {
		x.ed = nil
		x.closed = true
	}
	x.sync()
}

// form builds the dialog's fields.
func (x *Editors) form(c *ui.Context, e *editor) {
	s := e.compute()
	if e.isNew {
		kit.FormRow(c, "类型", "", func() {
			sel := int(e.kind)
			segs := make([]kit.Segment, len(Kinds))
			for i, kind := range Kinds {
				segs[i] = kit.Segment{Label: kind.Label()}
			}
			kit.Segmented(c, "rw-kind", &sel, segs...).Label("规则类型")
			e.kind = Kind(sel)
		})
	}
	kit.FormRow(c, "名称", "", func() { kit.Input(c, "rw-name", &e.name, "可选").Label("名称") })
	kit.FormRow(c, "匹配模式", "", func() {
		kit.Input(c, "rw-pattern", &e.pattern, "^https://example\\.com/").Label("匹配模式（RE2）").Font(kit.MonoFont).AutoFocus()
		kit.FormError(c, s.pattern.shown(e.submitted))
	})
	x.patternTest(c, e)
	switch e.kind {
	case KindURL:
		x.urlForm(c, e, s)
	case KindHeader:
		x.headerForm(c, e, s)
	case KindBody:
		x.bodyForm(c, e, s)
	case KindMapLocal:
		x.mapForm(c, e, s)
	}
	kit.FormRow(c, "启用", "", func() { kit.Switch(c, &e.enabled, "启用") })
	if e.notice != "" {
		kit.Notice(c, kit.WarningTone, "", e.notice, nil)
	}
	if e.saveErr != "" {
		kit.Notice(c, kit.DangerTone, "", e.saveErr, nil)
	}
}

// patternTest is the test URL field with the verdict and the capture
// groups of the pattern.
func (x *Editors) patternTest(c *ui.Context, e *editor) {
	k := kit.T(c)
	kit.FormRow(c, "测试 URL", "", func() {
		kit.Input(c, "rw-test", &e.test, "https://example.com/path").Label("测试地址")
		if strings.TrimSpace(e.test) == "" {
			return
		}
		t := TestPattern(e.pattern, e.test)
		if t.Err != "" || t.re == nil {
			return
		}
		if !t.Hit {
			kit.Status(c, "未命中", kit.Neutral)
			return
		}
		kit.Status(c, "命中", kit.SuccessTone)
		for i, g := range t.Groups {
			if i >= 20 {
				ui.Text(c, "…").FontSize(kit.FontTable).TextColor(k.Label3)
				break
			}
			label := "$" + strconv.Itoa(i+1)
			if t.Names[i] != "" {
				label += " ${" + t.Names[i] + "}"
			}
			ui.Row(c.Key(i)).Gap(8).AlignItems(ui.Start).Children(func() {
				ui.Text(c, label).FontSize(kit.FontTable).Font(kit.MonoFont).TextColor(k.Label3).Shrink(0)
				ui.Text(c, g).FontSize(kit.FontTable).Font(kit.MonoFont).Selectable().Wrap().Grow(1).MinWidth(0)
			})
		}
		if e.kind == KindURL && (e.action == model.URLActionFound || e.action == model.URLActionTemporary || e.action == model.URLActionHeader) && e.target != "" {
			if out, ok := t.Expand(e.target); ok {
				ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
					ui.Text(c, "→").FontSize(kit.FontTable).TextColor(k.Label3).Shrink(0)
					ui.Text(c, out).FontSize(kit.FontTable).Font(kit.MonoFont).Selectable().Wrap().Grow(1).MinWidth(0)
				})
			}
		}
	})
}

func (x *Editors) urlForm(c *ui.Context, e *editor, s issues) {
	kit.FormRow(c, "动作", "", func() {
		options := make([]kit.Option, len(urlActions))
		for i, a := range urlActions {
			options[i] = kit.Option{Value: a, Label: URLActionLabel(a)}
		}
		kit.Select(c, "rw-url-action", &e.action, options, kit.SelectOptions{Width: 200, Label: "动作"})
	})
	if e.action == model.URLActionFound || e.action == model.URLActionTemporary {
		kit.FormRow(c, "跳转地址", "", func() {
			kit.Input(c, "rw-target", &e.target, "https://example.com/$1").Label("跳转地址").Font(kit.MonoFont)
			kit.FormError(c, s.target.shown(e.submitted))
		})
	}
	if e.action == model.URLActionHeader {
		kit.FormRow(c, "目标地址", "", func() {
			kit.Input(c, "rw-target", &e.target, "https://internal.example.com/$1").Label("目标地址").Font(kit.MonoFont)
			kit.FormError(c, s.target.shown(e.submitted))
		})
	}
}

func (x *Editors) bodyForm(c *ui.Context, e *editor, s issues) {
	kit.FormRow(c, "范围", "", func() {
		kit.Segmented(c, "rw-scope", &e.scope, kit.Segment{Label: "请求"}, kit.Segment{Label: "响应"}).Label("作用范围")
	})
	kit.FormRow(c, "内容类型", "", func() {
		kit.Input(c, "rw-ctypes", &e.contentTypes, "json, text/html").Label("内容类型").Font(kit.MonoFont)
		kit.FormError(c, s.types.shown(e.submitted))
	})
	kit.FormRow(c, "编辑", "", func() {
		actions := make([]kit.Option, len(bodyActions))
		for i, a := range bodyActions {
			actions[i] = kit.Option{Value: a, Label: BodyActionLabel(a)}
		}
		move, remove, delta := 0, -1, 0
		ui.Column(c).Gap(10).Children(func() {
			for i, d := range e.edits {
				ui.Column(c.Key(d.key)).Gap(4).Shrink(0).Children(func() {
					ui.Row(c).Gap(6).Shrink(0).Children(func() {
						kit.Select(c, fmt.Sprint("rw-ed-action", d.key), &d.action, actions, kit.SelectOptions{Width: 112, Label: "编辑动作"})
						ui.Row(c).Grow(1).MinWidth(0).Children(func() {
							if d.action == model.BodyActionReplaceRegex {
								kit.Input(c, fmt.Sprint("rw-ed-regex", d.key), &d.regex, "匹配的正则表达式").Label("匹配的正则表达式").Font(kit.MonoFont)
							} else {
								kit.Input(c, fmt.Sprint("rw-ed-path", d.key), &d.path, "$.data.items[*].id").Label("JSONPath").Font(kit.MonoFont)
							}
						})
						if kit.IconButton(c, kit.SymArrowUp, fmt.Sprint("上移编辑 ", i+1), kit.IconButtonOptions{Size: 26, IconSize: 14, Disabled: i == 0}).Clicked() {
							move, delta = i, -1
						}
						if kit.IconButton(c, kit.SymArrowDown, fmt.Sprint("下移编辑 ", i+1), kit.IconButtonOptions{Size: 26, IconSize: 14, Disabled: i == len(e.edits)-1}).Clicked() {
							move, delta = i, 1
						}
						if kit.IconButton(c, kit.SymX, fmt.Sprint("移除编辑 ", i+1), kit.IconButtonOptions{Size: 26, IconSize: 14}).Clicked() {
							remove = i
						}
					})
					switch d.action {
					case model.BodyActionReplaceRegex:
						kit.Input(c, fmt.Sprint("rw-ed-value", d.key), &d.value, "替换为（$1）").Label("替换为").Font(kit.MonoFont)
					case model.BodyActionJSONSet, model.BodyActionJSONAppend:
						kit.Input(c, fmt.Sprint("rw-ed-value", d.key), &d.value, `JSON 值，如 {"a":1}`).Label("JSON 值").Font(kit.MonoFont)
					}
					if i < len(s.editRows) {
						kit.FormError(c, s.editRows[i].shown(e.submitted))
					}
				})
			}
			kit.FormError(c, s.edits.shown(e.submitted))
			ui.Row(c).Children(func() {
				kit.Button(c, "添加编辑", kit.ButtonOptions{Small: true, Icon: kit.SymPlus, Disabled: len(e.edits) >= model.MaxBodyEdits}).
					OnClick(func() { e.addEdit(model.BodyActionReplaceRegex) })
			})
		})
		if delta != 0 {
			if j := move + delta; j >= 0 && j < len(e.edits) {
				e.edits[move], e.edits[j] = e.edits[j], e.edits[move]
			}
		}
		if remove >= 0 {
			e.edits = append(e.edits[:remove:remove], e.edits[remove+1:]...)
		}
	})
}

func (x *Editors) headerForm(c *ui.Context, e *editor, s issues) {
	kit.FormRow(c, "范围", "", func() {
		kit.Segmented(c, "rw-scope", &e.scope, kit.Segment{Label: "请求"}, kit.Segment{Label: "响应"}).Label("作用范围")
	})
	kit.FormRow(c, "操作", "", func() {
		actions := make([]kit.Option, len(headerActions))
		for i, a := range headerActions {
			actions[i] = kit.Option{Value: a, Label: HeaderActionLabel(a)}
		}
		move, remove := 0, -1
		delta := 0
		ui.Column(c).Gap(8).Children(func() {
			for i, op := range e.ops {
				ui.Column(c.Key(op.key)).Gap(4).Shrink(0).Children(func() {
					ui.Row(c).Gap(6).Shrink(0).Children(func() {
						kit.Select(c, fmt.Sprint("rw-op-action", op.key), &op.action, actions, kit.SelectOptions{Width: 112, Label: "操作类型"})
						ui.Row(c).Width(150).Shrink(0).Children(func() {
							kit.Input(c, fmt.Sprint("rw-op-name", op.key), &op.name, "头名称").Label("头名称")
						})
						ui.Row(c).Grow(1).MinWidth(0).Children(func() {
							switch op.action {
							case model.HeaderActionDelete:
								ui.Box(c).Grow(1)
							case model.HeaderActionReplaceRegex:
								kit.Input(c, fmt.Sprint("rw-op-value", op.key), &op.value, "替换为").Label("替换为").Font(kit.MonoFont)
							default:
								kit.Input(c, fmt.Sprint("rw-op-value", op.key), &op.value, "值").Label("值")
							}
						})
						if kit.IconButton(c, kit.SymArrowUp, fmt.Sprint("上移操作 ", i+1), kit.IconButtonOptions{Size: 26, IconSize: 14, Disabled: i == 0}).Clicked() {
							move, delta = i, -1
						}
						if kit.IconButton(c, kit.SymArrowDown, fmt.Sprint("下移操作 ", i+1), kit.IconButtonOptions{Size: 26, IconSize: 14, Disabled: i == len(e.ops)-1}).Clicked() {
							move, delta = i, 1
						}
						if kit.IconButton(c, kit.SymX, fmt.Sprint("移除操作 ", i+1), kit.IconButtonOptions{Size: 26, IconSize: 14}).Clicked() {
							remove = i
						}
					})
					if op.action == model.HeaderActionReplaceRegex {
						kit.Input(c, fmt.Sprint("rw-op-regex", op.key), &op.regex, "匹配的正则表达式").Label("匹配的正则表达式").Font(kit.MonoFont)
					}
					if i < len(s.opRows) {
						kit.FormError(c, s.opRows[i].shown(e.submitted))
					}
				})
			}
			kit.FormError(c, s.ops.shown(e.submitted))
			ui.Row(c).Children(func() {
				kit.Button(c, "添加操作", kit.ButtonOptions{Small: true, Icon: kit.SymPlus}).OnClick(func() { e.addOp(model.HeaderActionAdd) })
			})
		})
		if delta != 0 {
			if j := move + delta; j >= 0 && j < len(e.ops) {
				e.ops[move], e.ops[j] = e.ops[j], e.ops[move]
			}
		}
		if remove >= 0 {
			e.ops = append(e.ops[:remove:remove], e.ops[remove+1:]...)
		}
	})
}

func (x *Editors) mapForm(c *ui.Context, e *editor, s issues) {
	k := kit.T(c)
	kit.FormRow(c, "状态码", "", func() {
		ui.Row(c).Width(110).Children(func() { kit.Input(c, "rw-status", &e.status, "200").Label("状态码").FontFeatures("tnum") })
		kit.FormError(c, s.status.shown(e.submitted))
	})
	kit.FormRow(c, "响应头", "", func() {
		remove := -1
		ui.Column(c).Gap(6).Children(func() {
			for i, f := range e.fields {
				ui.Column(c.Key(f.key)).Gap(4).Shrink(0).Children(func() {
					ui.Row(c).Gap(6).Shrink(0).Children(func() {
						ui.Row(c).Width(170).Shrink(0).Children(func() {
							kit.Input(c, fmt.Sprint("rw-h-name", f.key), &f.name, "名称").Label("头名称")
						})
						ui.Row(c).Grow(1).MinWidth(0).Children(func() {
							kit.Input(c, fmt.Sprint("rw-h-value", f.key), &f.value, "值").Label("头的值")
						})
						if kit.IconButton(c, kit.SymX, "移除响应头", kit.IconButtonOptions{Size: 26, IconSize: 14}).Clicked() {
							remove = i
						}
					})
					if i < len(s.fieldRows) {
						kit.FormError(c, s.fieldRows[i].shown(e.submitted))
					}
				})
			}
			ui.Row(c).Children(func() {
				kit.Button(c, "添加响应头", kit.ButtonOptions{Small: true, Icon: kit.SymPlus}).OnClick(func() { e.addField("", "") })
			})
		})
		if remove >= 0 {
			e.fields = append(e.fields[:remove:remove], e.fields[remove+1:]...)
		}
	})
	kit.FormRow(c, "正文", "", func() {
		if e.binary != "" {
			ui.Row(c).Gap(8).Height(kit.ControlHeight).Children(func() {
				kit.Icon(c, kit.SymFileText, 15).TextColor(k.Label3)
				ui.Text(c, e.binaryName).FontSize(kit.FontBody).SingleLine().Ellipsis("…").MinWidth(0)
				ui.Text(c, kit.Bytes(float64(e.bodySize()))).FontSize(kit.FontTable).TextColor(k.Label3).FontFeatures("tnum").Shrink(0)
			})
		} else {
			kit.TextArea(c, "rw-body", &e.body, "", true).MinHeight(120).Label("响应正文")
		}
		ui.Row(c).Gap(8).Children(func() {
			label := "选择文件"
			if e.importing {
				label = "读取中…"
			}
			kit.Button(c, label, kit.ButtonOptions{Small: true, Icon: kit.SymFileUp, Disabled: e.importing}).OnClick(func() { x.chooseFile(e) })
			if e.binary != "" {
				kit.Button(c, "清除", kit.ButtonOptions{Small: true, Variant: kit.TextButton}).OnClick(func() { e.binary, e.binaryName = "", "" })
			}
			if e.binary == "" && e.body != "" {
				ui.Text(c, kit.Bytes(float64(e.bodySize()))).FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum")
			}
		})
		kit.FormError(c, firstText(e.fileErr, s.body.shown(e.submitted)))
	})
}
