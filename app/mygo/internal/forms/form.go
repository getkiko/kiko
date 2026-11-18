// Package forms renders the protocol forms of the native console from
// schema field tables. A form lists the basic fields first, then the
// shared network config references, then the advanced fields in folding
// groups by sing-box structure, and last the fields particular to a
// platform.
//
// Fields shown only while another field matches (ShowIf) follow their
// parent in a tinted block. Text, number, duration, combo, select,
// multiselect, bool, textarea, list, keyvalue, json and reference fields
// (outbounds, inbounds, DNS servers) each have their control; generator
// actions (UUID, random port, Base64) sit beside the input.
//
// Usage, from an editor's view:
//
//	result := forms.Form(c, "fields", schema.Fields(kind, typ), draft.Fields, forms.Context{
//		Doc: store.Doc(), NetworkConfigs: store.NetworkConfigs(),
//		NetworkRefs: draft.NetworkConfigRefs, OnNetworkRefs: func(r model.NetworkConfigRefs) { draft.NetworkConfigRefs = r },
//		ExcludeRef: draft.ID, ShowErrors: e.triedSave,
//	})
//	// on save: refuse while !result.Valid or len(result.Errors) > 0
//
// Form edits values in place and returns the problems of the visible
// fields by key, as the editor's save checks them, and whether the form
// is well formed (FieldForm's onValidityChange).
package forms

import (
	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

// RefOption is an entity a reference field may name.
type RefOption struct {
	ID, Tag, Type string
}

// Context is what a form needs beyond its fields and values.
type Context struct {
	// Doc is the document, for reference options and random ports.
	Doc *model.Doc
	// Platform is the target platform, Doc's when empty.
	Platform string
	// NetworkConfigs is the shared library of network and TLS configs.
	NetworkConfigs []*model.NetworkConfig
	// EditNetworkConfig edits a library config itself: network fields
	// show as fields and no references are offered.
	EditNetworkConfig bool
	// NetworkRefs are the entity's library references; OnNetworkRefs,
	// when set, shows the reference selects and receives changes.
	NetworkRefs   model.NetworkConfigRefs
	OnNetworkRefs func(model.NetworkConfigRefs)
	// ExcludeRef is the entity's own id, never offered as a reference;
	// ExcludeDNSRef the DNS server's own id.
	ExcludeRef    string
	ExcludeDNSRef string
	// RefOptions are the outbounds ref fields offer; the document's
	// enabled outbounds and endpoints when nil.
	RefOptions []RefOption
	// InOptions are the inbounds refIn fields offer.
	InOptions []RefOption
	// ShowErrors shows each field's problem under it, as after a failed
	// save.
	ShowErrors bool
	// OnChange is told of every change after values is updated.
	OnChange func(key string, value any)
	// OnTextImport receives text pasted or committed into a text field;
	// returning true takes it (a share link filling several fields).
	OnTextImport func(key, text string) bool
	// Leading builds rows above the basic fields, as a node's name.
	Leading func()
	// Trailing builds rows below the basic fields, as an editor of a
	// field the schema keeps out of the form.
	Trailing func()
	// Accessory, when set, returns what to build right of a text field's
	// input, by key, or nil.
	Accessory func(key string) func()
	// Placeholder, when set, returns a text field's placeholder by key,
	// "" for the schema's.
	Placeholder func(key string) string
	// Inline lays each field out as a LabeledRow: its label left in a
	// column of InlineLabelWidth, the control right. Switches keep their
	// row; numbers, durations and selects take at most InlineShortWidth.
	Inline bool
	// Narrow, with Inline, stands the labels above their controls, as
	// LabeledRow not inline, in narrow windows; the rows keep their gap.
	Narrow bool
}

// Inline forms' measures.
const (
	InlineLabelWidth = 96
	InlineShortWidth = 200
	inlineGap        = 10
)

// LabeledRow is a row of an inline form: label left in a column of
// InlineLabelWidth, the control filling the rest. With inline false the
// label stands above the control, for narrow windows.
func LabeledRow(c *ui.Context, inline bool, label string, control func()) ui.Element {
	if !inline {
		return ui.Column(c).Gap(5).Shrink(0).Children(func() {
			RowLabel(c, label, false)
			control()
		})
	}
	return ui.Row(c).Gap(12).AlignItems(ui.Start).MinHeight(kit.ControlHeight).Shrink(0).Children(func() {
		RowLabel(c, label, true).Width(InlineLabelWidth).Shrink(0).Margin(7, 0, 0)
		ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(control)
	})
}

// RowLabel is a field's label in an inline form: 13 DIPs left of its
// control, or 12 DIPs above it as the stacked form's labels; secondary
// color either way.
func RowLabel(c *ui.Context, label string, inline bool) ui.Element {
	size := float32(kit.FontTable)
	if inline {
		size = kit.FontBody
	}
	return ui.Text(c, label).FontSize(size).TextColor(kit.T(c).Label2).SingleLine().Ellipsis("…").MinWidth(0)
}

func (fc *Context) platform() string {
	if fc.Platform != "" {
		return fc.Platform
	}
	if fc.Doc != nil && fc.Doc.Platform != "" {
		return fc.Doc.Platform
	}
	return model.PlatformLinux
}

// NetworkRefsKey is the key of reference problems in Result.Errors.
const NetworkRefsKey = "networkConfigRefs"

// Result is what a form reports as it builds.
type Result struct {
	// Errors are the problems of the visible fields by key, with saving's
	// checks: required, range, format, JSON that does not parse, and
	// broken library references under NetworkRefsKey.
	Errors map[string]string
	// Valid is false while JSON text does not parse or a library
	// reference is broken; field checks do not affect it.
	Valid bool
}

// state is what a form keeps between frames.
type state struct {
	drafts map[string]string // number and JSON text being typed
	// base is the serialized value a draft was typed against; a draft
	// whose value changed from elsewhere (another entity, an import) is
	// dropped.
	base    map[string]string
	invalid map[string]string // JSON fields whose text does not parse
	kv      map[string]*kvState
	lists   map[string]*listState
	open    map[string]bool // advanced groups unfolded
	opened  map[string]bool // whether a group's default was taken
	multi   map[string]string
}

// form is one build of a form.
type form struct {
	c      *ui.Context
	fc     *Context
	st     *state
	fields []schema.Field // presented, inherited group and level
	all    []schema.Field // the schema as given
	values map[string]any // the edited values
	shown  map[string]any // values with library-provided fields
	errors map[string]string
}

// Form builds the form of fields editing values in place, keyed by key,
// and reports its problems.
func Form(c *ui.Context, key any, fields []schema.Field, values map[string]any, fc Context) Result {
	if values == nil {
		values = map[string]any{}
	}
	f := &form{c: c, fc: &fc, all: fields, values: values, errors: map[string]string{}}
	root := ui.Column(c.Key(key)).Gap(f.gap()).Shrink(0)
	f.st = ui.Local(root, "form", func() state {
		return state{drafts: map[string]string{}, base: map[string]string{}, invalid: map[string]string{}, kv: map[string]*kvState{},
			lists: map[string]*listState{}, open: map[string]bool{}, opened: map[string]bool{}, multi: map[string]string{}}
	})
	f.prepare()
	root.Children(f.build)
	valid := len(f.st.invalid) == 0 && f.errors[NetworkRefsKey] == ""
	return Result{Errors: f.errors, Valid: valid}
}

// gap is the space between rows.
func (f *form) gap() float32 {
	if f.fc.Inline {
		return inlineGap
	}
	return 12
}

// draft keeps text typed into key against the value it now has.
func (f *form) draft(key, text string) {
	f.st.drafts[key] = text
	f.st.base[key] = serialize(f.shown[key])
}

// prepare computes the shown values, the presented fields and the
// problems.
func (f *form) prepare() {
	platform := f.fc.platform()
	if f.fc.EditNetworkConfig {
		f.shown = f.values
	} else {
		f.shown = model.EffectiveNetworkFields(f.all, f.values, f.fc.NetworkRefs, f.fc.NetworkConfigs, platform)
		if problems := model.NetworkReferenceErrors(f.all, f.fc.NetworkRefs, f.fc.NetworkConfigs, "当前项目"); len(problems) > 0 {
			f.errors[NetworkRefsKey] = problems[0]
		}
	}
	// Forget drafts, and the JSON errors they carry, whose value is no
	// longer the one they were typed against.
	st := f.st
	for key := range st.drafts {
		if serialize(f.shown[key]) != st.base[key] {
			delete(st.drafts, key)
			delete(st.base, key)
			delete(st.invalid, key)
		}
	}
	for _, field := range f.all {
		if !f.fc.EditNetworkConfig {
			if _, _, ok := model.NetworkField(field); ok {
				continue
			}
		}
		f.fields = append(f.fields, inheritPresentation(field, f.all, map[string]bool{}))
	}
	for _, field := range f.fields {
		if !schema.FieldVisible(field, f.fields, f.shown) {
			continue
		}
		if message, ok := st.invalid[field.Key]; ok {
			f.errors[field.Key] = message
		} else if message := schema.FieldValidationError(field, f.fields, f.shown, platform, true); message != "" {
			f.errors[field.Key] = message
		}
	}
}

// inheritPresentation keeps a conditional field at its parent's level
// and group, so a transport's fields stay in the transport group.
func inheritPresentation(field schema.Field, fields []schema.Field, ancestors map[string]bool) schema.Field {
	if field.ShowIf == nil || ancestors[field.ShowIf.Key] {
		return field
	}
	parent, ok := findField(fields, field.ShowIf.Key)
	if !ok {
		return field
	}
	inherited := inheritPresentation(parent, fields, with(ancestors, field.Key))
	if !field.Adv {
		field.Adv = inherited.Adv
	}
	if field.Group == "" {
		field.Group = inherited.Group
	}
	return field
}

// set changes a value; nil removes it.
func (f *form) set(key string, value any) {
	if value == nil {
		delete(f.values, key)
	} else {
		f.values[key] = value
	}
	f.shown[key] = value
	if value == nil {
		delete(f.shown, key)
	}
	if f.fc.OnChange != nil {
		f.fc.OnChange(key, value)
	}
}

// fieldGroup is a folding group of advanced fields.
type fieldGroup struct {
	key, label, desc string
	fields           []schema.Field
}

func groupKey(field schema.Field) string {
	if field.Group == "" {
		return "basic"
	}
	return field.Group
}

// advancedGroups sorts advanced fields into FieldGroups' order, the rest
// under "Other".
func advancedGroups(fields []schema.Field) []fieldGroup {
	var groups []fieldGroup
	known := map[string]bool{}
	for _, g := range schema.FieldGroups {
		known[g.Key] = true
		var in []schema.Field
		for _, field := range fields {
			if groupKey(field) == g.Key {
				in = append(in, field)
			}
		}
		if len(in) > 0 {
			groups = append(groups, fieldGroup{g.Key, g.Label, g.Desc, in})
		}
	}
	var rest []schema.Field
	for _, field := range fields {
		if !known[groupKey(field)] {
			rest = append(rest, field)
		}
	}
	if len(rest) > 0 {
		groups = append(groups, fieldGroup{"_rest", "其他", "", rest})
	}
	return groups
}

func (f *form) build() {
	var usable []schema.Field
	for _, field := range f.fields {
		if schema.FieldVisible(field, f.fields, f.shown) {
			usable = append(usable, field)
		}
	}
	var basic, adv []schema.Field
	for _, field := range usable {
		switch {
		case field.Platforms != nil:
		case field.Adv:
			adv = append(adv, field)
		default:
			basic = append(basic, field)
		}
	}
	if len(basic) > 0 || f.fc.Leading != nil || f.fc.Trailing != nil {
		ui.Column(f.c).Gap(f.gap()).Shrink(0).Children(func() {
			if f.fc.Leading != nil {
				f.fc.Leading()
			}
			f.layout(basic)
			if f.fc.Trailing != nil {
				f.fc.Trailing()
			}
		})
	}
	if !f.fc.EditNetworkConfig && f.fc.OnNetworkRefs != nil {
		NetworkConfigSelect(f.c, "network-refs", f.all, f.shown, f.fc.NetworkRefs, f.fc.NetworkConfigs, f.fc.platform(), f.fc.OnNetworkRefs)
	}
	for _, g := range advancedGroups(adv) {
		label := g.label
		if !f.fc.EditNetworkConfig && g.key == "dialer" {
			label = "连接解析设置"
		}
		f.group("adv-"+g.key, label, g.desc, g.fields)
	}
	for _, target := range schema.Platforms {
		var fields []schema.Field
		for _, field := range usable {
			if field.Platforms != nil && containsString(field.Platforms, target) {
				fields = append(fields, field)
			}
		}
		if len(fields) == 0 {
			continue
		}
		var pbasic, padv []schema.Field
		for _, field := range fields {
			if field.Adv {
				padv = append(padv, field)
			} else {
				pbasic = append(pbasic, field)
			}
		}
		k := kit.T(f.c)
		ui.Column(f.c.Key("platform-" + target)).Gap(12).Shrink(0).Children(func() {
			ui.Text(f.c, schema.PlatformLabel[target]+" 专有配置").FontSize(kit.FontBody).FontWeight(600).TextColor(k.Label2).Margin(4, 4, 0)
			if len(pbasic) > 0 {
				f.layout(pbasic)
			}
			for _, g := range advancedGroups(padv) {
				f.group(target+"-"+g.key, g.label, g.desc, g.fields)
			}
		})
	}
}

func containsString(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

// configuredCount counts values set to something, as configuredFieldCount.
func configuredCount(values []any) int {
	n := 0
	for _, v := range values {
		switch x := v.(type) {
		case nil:
		case []any:
			if len(x) > 0 {
				n++
			}
		case map[string]any:
			if len(x) > 0 {
				n++
			}
		case string:
			if trimmed(x) != "" {
				n++
			}
		case bool:
			if x {
				n++
			}
		default:
			n++
		}
	}
	return n
}

// group is a folding group of advanced fields: open at first when some
// are filled, with a "Filled" badge and the field count.
func (f *form) group(key, title, desc string, fields []schema.Field) {
	k := kit.T(f.c)
	st := f.st
	values := make([]any, len(fields))
	for i, field := range fields {
		values[i] = f.shown[field.Key]
	}
	filled := configuredCount(values)
	if !st.opened[key] {
		st.opened[key] = true
		st.open[key] = filled > 0
	}
	ui.Column(f.c.Key(key)).Radius(14).Background(k.Surface).Border(1, k.HairlineSoft).Clip().Shrink(0).Children(func() {
		head := ui.ButtonBase(f.c).MinHeight(48).Padding(8, 14).Gap(8).Label(title).Transition(kit.HoverMotion)
		if head.Hovered() {
			head.Background(k.Fill)
		}
		head.Children(func() {
			ui.Column(f.c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(f.c, title).FontSize(kit.FontBody).FontWeight(600).SingleLine()
				if desc != "" {
					ui.Text(f.c, desc).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine().Ellipsis("…")
				}
			})
			if filled > 0 {
				kit.Badge(f.c, "已填 "+itoa(filled), kit.AccentTone)
			}
			ui.Text(f.c, itoa(len(fields))).FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum")
			chev := kit.Icon(f.c, kit.SymChevronDown, 13).TextColor(k.Label3)
			if !st.open[key] {
				chev.Rotate(-90)
			}
		})
		if head.Clicked() {
			st.open[key] = !st.open[key]
		}
		if st.open[key] {
			ui.Column(f.c).Padding(12, 14, 14).Gap(12).BorderWidth(1, 0, 0, 0).BorderColor(k.HairlineSoft).Children(func() {
				f.layout(fields)
			})
		}
	})
}

// layout keeps the schema's order and shows a parent with the fields that
// depend on it as one tinted block.
func (f *form) layout(fields []schema.Field) {
	keys := map[string]bool{}
	for _, field := range fields {
		keys[field.Key] = true
	}
	hasChildren := func(parent schema.Field) bool {
		for _, child := range fields {
			if !child.ShowIfStandalone && child.ShowIf != nil && child.ShowIf.Key == parent.Key {
				return true
			}
		}
		return false
	}
	for _, field := range fields {
		root := field.ShowIfStandalone || field.ShowIf == nil || !keys[field.ShowIf.Key]
		if !root {
			continue
		}
		if !hasChildren(field) {
			f.field(field)
			continue
		}
		k := kit.T(f.c)
		ui.Column(f.c.Key("tree-"+field.Key)).Gap(12).Padding(10, 12).Radius(12).Background(k.Surface2).Shrink(0).Children(func() {
			f.field(field)
			for _, child := range descendants(field, fields) {
				f.field(child)
			}
		})
	}
}

// descendants are the fields under field's conditions, at every depth.
func descendants(field schema.Field, fields []schema.Field) []schema.Field {
	var out []schema.Field
	for _, child := range fields {
		if !child.ShowIfStandalone && child.ShowIf != nil && child.ShowIf.Key == field.Key {
			out = append(out, child)
			out = append(out, descendants(child, fields)...)
		}
	}
	return out
}
