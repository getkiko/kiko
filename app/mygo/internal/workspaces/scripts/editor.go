package scripts

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Seed prefills a new script. Pattern and TestURL fill the pattern field
// and its test; Type is a script type (the request script when empty);
// Record, when set, is the captured exchange the test area starts from.
type Seed struct {
	Pattern string
	TestURL string
	Type    string
	Record  *TestSeed
}

// dialogWidth is the editor dialog's width.
const dialogWidth = 1040

// bodySizes are the body caps the editor offers, in KiB.
var bodySizes = []int{256, 512, 1024, 2048, 4096, 8192, 16384, 32768}

// sourceNearLimit is the source size from which the editor shows it against
// the limit.
const sourceNearLimit = model.MaxScriptSource / 10 * 9

// editor is the draft of the script the dialog edits.
type editor struct {
	open    bool
	isNew   bool
	id      string
	name    string
	tag     string
	typ     int
	enabled bool
	pattern string
	cron    string
	test    string

	requires, binary bool
	maxSize          string
	timeout          string
	argument         string

	code kit.CodeState
	// srcVer, srcIssue and srcOK cache the check of the source for one
	// version of its text.
	srcVer   uint64
	srcIssue string
	srcOK    bool
	template string
	tab      int
	// fieldsOpen unfolds the settings on the test tab.
	fieldsOpen bool

	submitted bool
	saveErr   string
	discard   kit.ConfirmState
	base      string

	test2   *tester
	seedRec *TestSeed
}

func typeIndex(t string) int {
	for i, v := range types {
		if v == t {
			return i
		}
	}
	return 0
}

// effectiveTag is the tag the script has: the entered one, else its ID.
func (e *editor) effectiveTag() string {
	if t := strings.TrimSpace(e.tag); t != "" {
		return t
	}
	return e.id
}

// maxBytes is the body cap of the draft in bytes, 0 for the default.
func (e *editor) maxBytes() int {
	n, _ := strconv.Atoi(strings.TrimSpace(e.maxSize))
	return n * 1024
}

// snapshot is the draft's content as one string, to tell edits.
func (e *editor) snapshot() string {
	return strings.Join([]string{
		e.name, e.tag, strconv.Itoa(e.typ), strconv.FormatBool(e.enabled), e.pattern, e.cron,
		strconv.FormatBool(e.requires), strconv.FormatBool(e.binary), e.maxSize, e.timeout, e.argument, e.code.Text(),
	}, "\x00")
}

// Editors is the script editor dialog, shared by the Scripts list and the HTTP
// inspector. Build it each frame with View; open it with New or Edit.
type Editors struct {
	env *env.Env
	doc Doc

	// OnSaved, when set, is told the ID of a script the dialog saved.
	OnSaved func(id string)

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
		x.ed.test2.stop()
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

func (x *Editors) fresh() *editor {
	return &editor{open: true, enabled: true, test2: newTester(x.env)}
}

// New opens the dialog for a new script.
func (x *Editors) New(seed Seed) {
	x.Close()
	e := x.fresh()
	e.isNew, e.id = true, model.NewID("script")
	e.typ = typeIndex(seed.Type)
	e.pattern, e.test = seed.Pattern, seed.TestURL
	if seed.Type == "" && seed.Record != nil && seed.Record.Side == api.HTTPSideResponse {
		e.typ = typeIndex(model.ScriptHTTPResponse)
	}
	e.template = template(types[e.typ])
	e.resetCode(e.template)
	e.seedRec = seed.Record
	e.base = e.snapshot()
	x.ed, x.closed = e, false
}

// Edit opens the dialog for the script with id; false when it is gone.
func (x *Editors) Edit(id string) bool {
	if x.doc == nil {
		return false
	}
	list := scriptsOf(x.doc.Doc())
	i := indexOf(list, id)
	if i < 0 {
		return false
	}
	s := list[i]
	x.Close()
	e := x.fresh()
	e.id, e.name, e.tag, e.typ, e.enabled = s.ID, s.Name, s.Tag, typeIndex(s.Type), !s.Disabled
	e.pattern, e.requires, e.binary = s.Pattern, s.RequiresBody, s.BinaryBody
	e.cron = s.Cron
	if s.MaxSize > 0 {
		e.maxSize = strconv.Itoa(s.MaxSize / 1024)
	}
	if s.Timeout > 0 {
		e.timeout = strconv.Itoa(s.Timeout)
	}
	e.argument = s.Argument
	e.resetCode(s.Source)
	e.base = e.snapshot()
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
	tag, pattern, cron, source, timeout, argument issue
}

func (s issues) any() bool {
	return s.tag.msg != "" || s.pattern.msg != "" || s.cron.msg != "" || s.source.msg != "" || s.timeout.msg != "" || s.argument.msg != ""
}

// resetCode loads text as a new document; its versions start over, so the
// cached check goes.
func (e *editor) resetCode(text string) {
	e.code.Reset(text)
	e.srcOK = false
}

// sourceChecker checks a script's source; a variable so tests can count
// the scans.
var sourceChecker = sourceIssue

// sourceCheck is the problem of the source, scanned again only when the
// text changed: the source may be near 1 MiB, and compute runs each frame.
func (e *editor) sourceCheck() issue {
	text := e.code.Text()
	if v := e.code.Version(); !e.srcOK || v != e.srcVer {
		e.srcVer, e.srcIssue, e.srcOK = v, sourceChecker(text), true
	}
	return issue{e.srcIssue, strings.TrimSpace(text) == ""}
}

func (e *editor) typeName() string { return types[min(max(e.typ, 0), len(types)-1)] }

// http reports whether the draft is a request or response script, which has
// a pattern and a body.
func (e *editor) http() bool { return !isRunType(e.typeName()) && !e.decision() }

// decision reports whether the draft is a rule or dns script, which takes
// no pattern or body and keeps its timeout in milliseconds.
func (e *editor) decision() bool { return isDecisionType(e.typeName()) }

// isCron reports whether the draft is a scheduled script.
func (e *editor) isCron() bool { return e.typeName() == model.ScriptCron }

// compute checks the draft with the model's validation, field by field.
func (x *Editors) compute(e *editor) issues {
	var s issues
	if tag := strings.TrimSpace(e.tag); tag != "" {
		s.tag.msg = model.ScriptTagIssue(tag)
	}
	if s.tag.msg == "" && x.doc != nil {
		mine := e.effectiveTag()
		for _, o := range scriptsOf(x.doc.Doc()) {
			if o != nil && o.ID != e.id && o.EffectiveTag() == mine {
				s.tag.msg = "标识已被其他脚本使用"
			}
		}
	}
	if e.http() {
		s.pattern = issue{model.PatternIssue(e.pattern), strings.TrimSpace(e.pattern) == ""}
		if s.pattern.msg == "" && len(e.pattern) > model.MaxScriptPattern {
			s.pattern.msg = "匹配模式过长"
		}
	}
	if e.isCron() {
		s.cron = issue{model.CronIssue(e.cron), strings.TrimSpace(e.cron) == ""}
	}
	s.source = e.sourceCheck()
	if t := strings.TrimSpace(e.timeout); t != "" {
		n, err := strconv.Atoi(t)
		if err != nil {
			s.timeout.msg = "超时须为整数秒"
			if e.decision() {
				s.timeout.msg = "超时须为整数毫秒"
			}
		} else if n == 0 {
			s.timeout.msg = "超时须大于 0"
		} else {
			s.timeout.msg = model.ScriptTimeoutIssue(e.typeName(), n)
		}
	}
	s.argument.msg = model.ScriptArgumentIssue(e.argument)
	return s
}

// script builds the model's script of the draft.
func (e *editor) script() *model.HTTPScript {
	s := &model.HTTPScript{
		ID: e.id, Name: strings.TrimSpace(e.name), Disabled: !e.enabled, Tag: strings.TrimSpace(e.tag),
		Type: e.typeName(), Source: e.code.Text(), Argument: e.argument,
	}
	if e.http() {
		s.Pattern, s.RequiresBody = e.pattern, e.requires
		s.BinaryBody = e.binary && e.requires
		s.MaxSize = e.maxBytes()
	}
	if e.isCron() {
		s.Cron = strings.TrimSpace(e.cron)
	}
	s.Timeout, _ = strconv.Atoi(strings.TrimSpace(e.timeout))
	return s
}

// save checks the draft and puts the script into the document: appended
// when new, in place when it exists. It returns whether the dialog may
// close.
func (x *Editors) save(e *editor) bool {
	e.submitted, e.saveErr = true, ""
	if x.compute(e).any() {
		return false
	}
	if x.doc == nil {
		e.saveErr = "无法保存"
		return false
	}
	script := e.script()
	if msg := script.Issue(); msg != "" {
		e.saveErr = msg
		return false
	}
	if e.isNew && len(scriptsOf(x.doc.Doc())) >= model.MaxScripts {
		e.saveErr = "脚本数量已达上限"
		return false
	}
	gone := false
	err := x.doc.Update(func(d *model.Doc) {
		t := ensure(d)
		if i := indexOf(t.Scripts, e.id); i >= 0 {
			t.Scripts[i] = script
			return
		}
		if e.isNew {
			t.Scripts = append(t.Scripts, script)
			return
		}
		gone = true
	})
	if err != nil {
		e.saveErr = err.Error()
		return false
	}
	if gone {
		e.saveErr = "脚本已被删除"
		return false
	}
	// The draft is the saved script now, whichever way the dialog closes.
	e.base = e.snapshot()
	if x.OnSaved != nil {
		x.OnSaved(e.id)
	}
	return true
}

// reveal brings the problem of a refused save into view: a problem of a
// setting unfolds the settings of the test tab, one of the source alone
// switches to the source.
func (x *Editors) reveal(e *editor) {
	if e.tab != 1 {
		return
	}
	s := x.compute(e)
	switch {
	case s.tag.msg != "" || s.pattern.msg != "" || s.cron.msg != "" || s.timeout.msg != "" || s.argument.msg != "":
		e.fieldsOpen = true
	case s.source.msg != "":
		e.tab = 0
	}
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
	title := "添加脚本"
	if !e.isNew {
		title = "编辑脚本"
	}
	saved := false
	open := e.open
	kit.Dialog(c, &open, title, dialogWidth, func() { x.form(c, e) }, func() {
		cancel := kit.Button(c, "取消", kit.ButtonOptions{})
		if cancel.Clicked() {
			open = false
		}
		kit.ConfirmPopover(c, cancel, &e.discard, kit.ConfirmOptions{
			Title: "放弃未保存的更改？", Confirm: "放弃", Danger: true,
			Run: func(done func(error)) {
				e.open = false
				done(nil)
			},
		})
		save := kit.Button(c, "保存", kit.ButtonOptions{Variant: kit.Primary})
		if save.Clicked() {
			if saved = x.save(e); !saved {
				x.reveal(e)
			}
		}
	})
	switch {
	case saved:
		e.open = false
	case !open && e.open:
		// A change of the draft is asked about before it is dropped.
		if e.snapshot() != e.base {
			e.discard.Open = true
		} else {
			e.open = false
		}
	}
	if !e.open {
		e.test2.stop()
		x.ed = nil
		x.closed = true
	}
	x.sync()
}

// pair puts two form rows side by side, one over the other on a narrow
// window.
func pair(c *ui.Context, narrow bool, a, b func()) {
	build := func() {
		a()
		if b != nil {
			b()
		}
	}
	if narrow {
		ui.Column(c).Gap(12).Shrink(0).Children(build)
		return
	}
	ui.Row(c).Gap(28).AlignItems(ui.Start).Shrink(0).Children(build)
}

// half is a form row taking half of a pair.
func half(c *ui.Context, narrow bool, label string, control func()) func() {
	return func() {
		row := kit.FormRow(c, label, "", control)
		if !narrow {
			row.Grow(1).Basis(0).MinWidth(0)
		}
	}
}

// form builds the dialog's fields: the settings above the source and test
// tabs. The settings fold under a title on the test tab, which makes room
// for its input and result.
func (x *Editors) form(c *ui.Context, e *editor) {
	width, height := c.Size()
	narrow := width < 760
	s := x.compute(e)
	if e.tab == 0 {
		x.fields(c, e, s, narrow)
	}
	ui.Row(c).Shrink(0).Margin(4, 0, 0).Children(func() {
		kit.Segmented(c, "sc-tab", &e.tab, kit.Segment{Label: "源码"}, kit.Segment{Label: "测试"}).Label("源码与测试")
	})
	if e.tab == 1 {
		kit.Disclosure(c, "设置", &e.fieldsOpen, func() {
			ui.Column(c).Gap(12).Shrink(0).Margin(4, 0, 0).Children(func() { x.fields(c, e, s, narrow) })
		})
		if e.seedRec != nil {
			e.test2.seed(e.seedRec, e.maxBytes())
			e.seedRec = nil
		}
		e.test2.view(c, e, narrow)
	} else {
		x.sourceView(c, e, s, height)
	}
	if e.saveErr != "" {
		kit.Notice(c, kit.DangerTone, "", e.saveErr, nil)
	}
}

// switchRow is a switch beside its label, 30 DIPs high; it reports a change.
func switchRow(c *ui.Context, on *bool, label string, disabled bool) (changed bool) {
	k := kit.T(c)
	ui.Row(c.Key(label)).Gap(8).Height(kit.ControlHeight).Shrink(0).Children(func() {
		sw := kit.Switch(c, on, label)
		if disabled {
			sw.Disabled(true)
		}
		changed = sw.Changed()
		ui.Text(c, label).FontSize(kit.FontBody).TextColor(k.Label2).SingleLine()
	})
	return changed
}

// fields are the rows of the script's settings.
func (x *Editors) fields(c *ui.Context, e *editor, s issues, narrow bool) {
	pair(c, narrow, half(c, narrow, "名称", func() {
		in := kit.Input(c, "sc-name", &e.name, "可选").Label("名称")
		if e.isNew {
			in.AutoFocus()
		}
	}), half(c, narrow, "标识", func() {
		kit.Input(c, "sc-tag", &e.tag, e.id).Label("标识").Font(kit.MonoFont)
		kit.FormError(c, s.tag.shown(e.submitted))
	}))
	pair(c, narrow, half(c, narrow, "类型", func() {
		sel := e.typ
		if narrow {
			// Six segments do not fit a phone-width dialog.
			value := strconv.Itoa(sel)
			options := make([]kit.Option, len(types))
			for i, typ := range types {
				options[i] = kit.Option{Value: strconv.Itoa(i), Label: TypeLabel(typ)}
			}
			kit.Select(c, "sc-type-narrow", &value, options, kit.SelectOptions{Label: "脚本类型"})
			sel, _ = strconv.Atoi(value)
		} else {
			kit.Segmented(c, "sc-type", &sel, kit.Segment{Label: "请求"}, kit.Segment{Label: "响应"}, kit.Segment{Label: "通用"}, kit.Segment{Label: "定时"}, kit.Segment{Label: "规则"}, kit.Segment{Label: "DNS"}).Label("脚本类型")
		}
		if sel != e.typ {
			e.setType(sel)
		}
	}), half(c, narrow, "启用", func() {
		ui.Row(c).Height(kit.ControlHeight).Children(func() { kit.Switch(c, &e.enabled, "启用") })
	}))
	if e.http() {
		kit.FormRow(c, "匹配模式", "", func() {
			kit.Input(c, "sc-pattern", &e.pattern, "^https://example\\.com/").Label("匹配模式（RE2）").Font(kit.MonoFont)
			kit.FormError(c, s.pattern.shown(e.submitted))
		})
		kit.FormRow(c, "测试 URL", "", func() {
			kit.Input(c, "sc-test-url-field", &e.test, "https://example.com/path").Label("测试地址")
			t := TestPattern(e.pattern, e.test)
			switch {
			case t.Err != "":
			case t.Tried && t.Hit:
				kit.Status(c, "命中", kit.SuccessTone)
			case t.Tried:
				kit.Status(c, "未命中", kit.Neutral)
			}
		})
	}
	if e.isCron() {
		kit.FormRow(c, "定时表达式", "", func() {
			kit.Input(c, "sc-cron", &e.cron, "*/5 * * * *").Label("定时表达式").Font(kit.MonoFont)
			kit.FormError(c, s.cron.shown(e.submitted))
		})
	}
	kit.FormRow(c, "选项", "", func() {
		build := func() {
			if e.http() {
				if switchRow(c, &e.requires, "读取正文", false) && !e.requires {
					e.binary = false
				}
				switchRow(c, &e.binary, "二进制正文", !e.requires)
				ui.Row(c).Gap(8).Height(kit.ControlHeight).Shrink(0).Children(func() {
					x.sizeSelect(c, e)
				})
			}
			ui.Row(c).Gap(8).Height(kit.ControlHeight).Shrink(0).Children(func() {
				x.timeoutInput(c, e)
			})
		}
		if narrow {
			ui.Column(c).Gap(8).AlignItems(ui.Start).Children(build)
		} else {
			ui.Row(c).Gap(20).Children(build)
		}
		kit.FormError(c, s.timeout.msg)
	})
	kit.FormRow(c, "参数", "", func() {
		kit.Input(c, "sc-argument", &e.argument, "").Label("脚本参数").Font(kit.MonoFont)
		kit.FormError(c, s.argument.msg)
	})
}

// setType changes the type and, while the source is still the type's
// template, the template with it.
func (e *editor) setType(typ int) {
	old := e.template
	wasDecision := e.decision()
	e.typ = typ
	if e.decision() != wasDecision {
		// The unit of the timeout differs: seconds or milliseconds.
		e.timeout = ""
	}
	e.template = template(e.typeName())
	if text := e.code.Text(); text == old || strings.TrimSpace(text) == "" {
		e.code.SetText(e.template)
	}
	if !e.http() {
		e.requires, e.binary = false, false
	}
	if e.decision() {
		e.pattern, e.cron, e.maxSize = "", "", ""
	}
}

func (x *Editors) timeoutInput(c *ui.Context, e *editor) {
	k := kit.T(c)
	def, unit := model.DefaultScriptTimeoutHTTP, "秒"
	if e.decision() {
		def, unit = model.DefaultScriptTimeoutDecision, "毫秒"
	} else if !e.http() {
		def = model.DefaultScriptTimeoutRun
	}
	ui.Text(c, "超时（"+unit+"）").FontSize(kit.FontBody).TextColor(k.Label2).SingleLine()
	ui.Row(c).Width(96).Children(func() {
		kit.Input(c, "sc-timeout", &e.timeout, "默认 "+strconv.Itoa(def)).Label("超时").FontFeatures("tnum")
	})
}

func (x *Editors) sizeSelect(c *ui.Context, e *editor) {
	options := []kit.Option{{Value: "", Label: "默认"}}
	known := e.maxSize == ""
	for _, kib := range bodySizes {
		known = known || strconv.Itoa(kib) == e.maxSize
		options = append(options, kit.Option{Value: strconv.Itoa(kib), Label: kit.Bytes(float64(kib * 1024))})
	}
	if !known {
		n, _ := strconv.Atoi(e.maxSize)
		options = append(options, kit.Option{Value: e.maxSize, Label: kit.Bytes(float64(n * 1024))})
	}
	ui.Text(c, "最大正文").FontSize(kit.FontBody).TextColor(kit.T(c).Label2).SingleLine()
	kit.Select(c, "sc-maxsize", &e.maxSize, options, kit.SelectOptions{Width: 128, Label: "最大正文"})
}

// sourceView is the code editor with the problem of the source under it.
func (x *Editors) sourceView(c *ui.Context, e *editor, s issues, windowHeight float32) {
	k := kit.T(c)
	h := min(max(windowHeight-32-560, 180), 520)
	ed := kit.CodeEditor(c.Key("sc-code"), &e.code, kit.CodeOptions{Language: kit.LangJavaScript, Label: "脚本源码", MinHeight: 200}).Height(h)
	_ = ed
	if msg := s.source.shown(e.submitted); msg != "" {
		ui.Text(c, msg).FontSize(kit.FontTable).Font(kit.MonoFont).TextColor(k.Danger).Wrap().Selectable()
	}
	if n := len(e.code.Text()); n >= sourceNearLimit {
		ui.Text(c, kit.Bytes(float64(n))+" / "+kit.Bytes(model.MaxScriptSource)).FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum")
	}
}
