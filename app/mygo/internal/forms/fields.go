package forms

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

func itoa(n int) string { return strconv.Itoa(n) }

func trimmed(s string) string { return strings.TrimSpace(s) }

// examplePlaceholder is a value to copy when the schema gives none.
func examplePlaceholder(field schema.Field) string {
	key := strings.ToLower(field.Key)
	switch {
	case field.Type == schema.TextArea && strings.Contains(key, "certificate"):
		return "-----BEGIN CERTIFICATE-----"
	case field.Type == schema.TextArea && strings.Contains(key, "key"):
		return "-----BEGIN PRIVATE KEY-----"
	case field.Type == schema.Number && strings.Contains(key, "port"):
		return "443"
	case field.Type == schema.Number:
		return "0"
	case field.Type == schema.Duration:
		return "30s"
	case field.Type == schema.TextArea:
		return "example value"
	case strings.Contains(key, "uuid"):
		return "550e8400-e29b-41d4-a716-446655440000"
	case strings.Contains(key, "url"):
		return "https://example.com"
	case strings.Contains(key, "host"), strings.Contains(key, "server_name"):
		return "example.com"
	case strings.Contains(key, "path"):
		return "/path"
	case strings.Contains(key, "interface"):
		return "eth0"
	case strings.Contains(key, "address"), strings.Contains(key, "listen"):
		return "0.0.0.0"
	case strings.Contains(key, "header"):
		return "Sec-WebSocket-Protocol"
	case strings.Contains(key, "key"), strings.Contains(key, "secret"), strings.Contains(key, "password"):
		return "example-secret"
	}
	return "example"
}

func placeholderOf(field schema.Field) string {
	if field.Placeholder != "" {
		return field.Placeholder
	}
	return examplePlaceholder(field)
}

// field builds one field: its label and hint, its control, and its
// problem when errors show.
func (f *form) field(field schema.Field) {
	c := f.c
	if f.fc.Inline {
		f.inlineField(field)
		return
	}
	ui.Column(c.Key("f:" + field.Key)).Gap(5).Shrink(0).Children(func() {
		if field.Type == schema.Bool {
			f.boolField(field)
		} else {
			f.label(field)
			f.control(field)
		}
		if message := f.errors[field.Key]; message != "" && (f.fc.ShowErrors || field.Type == schema.JSON) {
			kit.FormError(c, message)
		}
	})
}

// control builds a field's control by its type, bools aside.
func (f *form) control(field schema.Field) {
	switch field.Type {
	case schema.Combo:
		f.comboField(field)
	case schema.Select:
		f.selectField(field)
	case schema.MultiSelect:
		f.multiField(field)
	case schema.Ref, schema.RefIn, schema.RefDNS:
		f.refField(field)
	case schema.JSON:
		f.jsonField(field)
	case schema.KeyValue:
		f.keyValueField(field)
	case schema.List:
		f.listField(field)
	case schema.TextArea:
		f.textAreaField(field)
	default:
		f.textField(field)
	}
}

// inlineField is field in an inline form: the label left (above when
// Narrow), its hint on hover, the control and its problem right; a switch
// at the row's end.
func (f *form) inlineField(field schema.Field) {
	c := f.c
	k := kit.T(c)
	beside := !f.fc.Narrow
	hint := schema.FieldHint(field, f.fc.platform())
	problem := ""
	if message := f.errors[field.Key]; message != "" && (f.fc.ShowErrors || field.Type == schema.JSON) {
		problem = message
	}
	markSize := float32(kit.FontTable)
	if beside {
		markSize = kit.FontBody
	}
	label := func() ui.Element {
		row := ui.Row(c).Gap(3).Shrink(0)
		row.Children(func() {
			kit.Hint(c, RowLabel(c, field.Label, beside), hint)
			if field.Required {
				ui.Text(c, "*").FontSize(markSize).FontWeight(600).TextColor(k.Danger)
			}
		})
		return row
	}
	if field.Type == schema.Bool {
		ui.Column(c.Key("f:" + field.Key)).Gap(4).Shrink(0).Children(func() {
			ui.Row(c).MinHeight(kit.ControlHeight).Gap(12).Shrink(0).Children(func() {
				ui.Row(c).Grow(1).MinWidth(0).Children(func() { label() })
				on := truthy(f.shown[field.Key])
				sw := kit.Switch(c, &on, field.Label)
				if field.Locked {
					sw.Disabled(true)
				} else if sw.Changed() {
					f.set(field.Key, on)
				}
			})
			kit.FormError(c, problem)
		})
		return
	}
	control := func(e ui.Element) {
		if beside {
			switch field.Type {
			case schema.Number, schema.Duration, schema.Select:
				e.MaxWidth(InlineShortWidth)
			}
		}
		e.Children(func() {
			f.control(field)
			kit.FormError(c, problem)
		})
	}
	if !beside {
		ui.Column(c.Key("f:" + field.Key)).Gap(5).Shrink(0).Children(func() {
			label()
			control(ui.Column(c).Gap(4).Shrink(0))
		})
		return
	}
	ui.Row(c.Key("f:" + field.Key)).Gap(12).AlignItems(ui.Start).MinHeight(kit.ControlHeight).Shrink(0).Children(func() {
		label().Width(InlineLabelWidth).Margin(7, 0, 0)
		control(ui.Column(c).Grow(1).MinWidth(0).Gap(4))
	})
}

// label is a field's name, a required mark and its hint for the platform.
func (f *form) label(field schema.Field) {
	k := kit.T(f.c)
	ui.Column(f.c).Gap(1).Shrink(0).Children(func() {
		ui.Row(f.c).Gap(3).Children(func() {
			ui.Text(f.c, field.Label).FontSize(kit.FontTable).TextColor(k.Label2).SingleLine().Ellipsis("…").MinWidth(0)
			if field.Required {
				ui.Text(f.c, "*").FontSize(kit.FontTable).FontWeight(600).TextColor(k.Danger)
			}
		})
		if hint := schema.FieldHint(field, f.fc.platform()); hint != "" {
			ui.Text(f.c, hint).FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
		}
	})
}

func (f *form) boolField(field schema.Field) {
	k := kit.T(f.c)
	row := ui.Row(f.c).MinHeight(kit.ControlHeight).Gap(12).Shrink(0)
	if field.Locked {
		row.Opacity(0.6)
	}
	row.Children(func() {
		ui.Column(f.c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			ui.Text(f.c, field.Label).FontSize(kit.FontBody).Wrap()
			if hint := schema.FieldHint(field, f.fc.platform()); hint != "" {
				ui.Text(f.c, hint).FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
			}
		})
		on := truthy(f.shown[field.Key])
		sw := kit.Switch(f.c, &on, field.Label)
		if field.Locked {
			sw.Disabled(true)
		} else if sw.Changed() {
			f.set(field.Key, on)
		}
	})
}

// grow is a column taking the room left in a row, for controls that do
// not grow themselves.
func grow(c *ui.Context, fn func()) ui.Element {
	return ui.Column(c).Grow(1).Basis(0).MinWidth(0).Children(fn)
}

func (f *form) textField(field schema.Field) {
	c := f.c
	st := f.st
	key := field.Key
	text, drafting := st.drafts[key]
	if !drafting {
		text = stringify(f.shown[key])
	}
	ui.Row(c).Gap(6).Shrink(0).Children(func() {
		var input ui.Element
		grow(c, func() {
			before := text
			placeholder := placeholderOf(field)
			if f.fc.Placeholder != nil {
				if p := f.fc.Placeholder(key); p != "" {
					placeholder = p
				}
			}
			input = kit.Input(c, "input", &text, placeholder)
			input.Label(field.Label)
			if field.Locked {
				input.Disabled(true).Opacity(0.6)
				return
			}
			if input.Changed() {
				pasted := len([]rune(text))-len([]rune(before)) > 1
				if pasted && f.fc.OnTextImport != nil && f.fc.OnTextImport(key, text) {
					delete(st.drafts, key)
					return
				}
				f.typed(field, text)
			}
			if input.Submitted() && f.fc.OnTextImport != nil {
				f.fc.OnTextImport(key, text)
			}
		})
		if !input.Focused() {
			delete(st.drafts, key)
		}
		if field.Action != "" && !field.Locked {
			kit.Button(c, schema.FieldActions[field.Action].Label, kit.ButtonOptions{Variant: kit.TextButton, Small: true}).
				OnClick(func() { f.runAction(field) })
		}
		if f.fc.Accessory != nil {
			if build := f.fc.Accessory(key); build != nil {
				build()
			}
		}
	})
}

// typed takes text typed into a text, duration or number field. Numbers
// keep the text while it is typed and store the clamped number.
func (f *form) typed(field schema.Field, text string) {
	if field.Type != schema.Number {
		f.set(field.Key, text)
		return
	}
	defer f.draft(field.Key, text)
	if trimmed(text) == "" {
		f.set(field.Key, nil)
		return
	}
	n, err := strconv.ParseFloat(trimmed(text), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return
	}
	if field.Min != nil {
		n = max(n, *field.Min)
	}
	if field.Max != nil {
		n = min(n, *field.Max)
	}
	f.set(field.Key, n)
}

// runAction fills a field from its generator.
func (f *form) runAction(field schema.Field) {
	switch field.Action {
	case schema.ActionUUID:
		f.set(field.Key, schema.FieldActions[schema.ActionUUID].Run())
	case schema.ActionRandomPort:
		f.set(field.Key, float64(f.randomPort()))
	case schema.ActionBase64:
		f.set(field.Key, base64.StdEncoding.EncodeToString([]byte(stringify(f.shown[field.Key]))))
	}
	delete(f.st.drafts, field.Key)
}

// randomPort picks a port from 10000 that no other inbound listens on.
func (f *form) randomPort() int {
	used := map[float64]bool{}
	if f.fc.Doc != nil {
		for _, in := range f.fc.Doc.Inbounds {
			if in.ID == f.fc.ExcludeRef {
				continue
			}
			if n, ok := number(in.Fields["listen_port"]); ok {
				used[n] = true
			} else if s, ok := in.Fields["listen_port"].(string); ok {
				if n, err := strconv.ParseFloat(s, 64); err == nil {
					used[n] = true
				}
			}
		}
	}
	for range 200 {
		port := rand.IntN(50000) + 10000
		if !used[float64(port)] {
			return port
		}
	}
	return 20000
}

func (f *form) textAreaField(field schema.Field) {
	text := stringify(f.shown[field.Key])
	area := kit.TextArea(f.c, "area", &text, placeholderOf(field), true).Label(field.Label).MinHeight(80)
	if area.Changed() {
		f.set(field.Key, text)
	}
}

func (f *form) selectField(field schema.Field) {
	value := stringify(f.shown[field.Key])
	options := make([]kit.Option, 0, len(field.Options))
	for _, o := range field.Options {
		label := o
		if o == "" {
			label = "默认"
		}
		for _, p := range field.Presets {
			if p.Value == o {
				label = p.Label
			}
		}
		options = append(options, kit.Option{Value: o, Label: label})
	}
	if kit.Select(f.c, "select", &value, options, kit.SelectOptions{Label: field.Label}).Changed() {
		f.set(field.Key, value)
	}
}

// comboField is free text with a menu of common values.
func (f *form) comboField(field schema.Field) {
	c := f.c
	presets := field.Presets
	if field.Key == "listen" && f.fc.Doc != nil && !f.fc.Doc.IPv6Enabled {
		presets = nil
		for _, p := range field.Presets {
			if p.Value != "::" {
				presets = append(presets, p)
			}
		}
	}
	text := stringify(f.shown[field.Key])
	ui.Row(c).Gap(6).Shrink(0).Children(func() {
		grow(c, func() {
			if kit.Input(c, "input", &text, placeholderOf(field)).Label(field.Label).Changed() {
				f.set(field.Key, text)
			}
		})
		if len(presets) == 0 {
			return
		}
		kit.IconButton(c, kit.SymChevronDown, field.Label+"常用值", kit.IconButtonOptions{}).PopoverMenuButton(func(m *ui.Menu) {
			for _, p := range presets {
				label := p.Value
				if p.Label != "" && p.Label != p.Value {
					label = p.Value + " · " + p.Label
				}
				if m.Item(label).Checked(p.Value == text).Chosen() {
					f.set(field.Key, p.Value)
				}
			}
		})
	})
}

// stringsOf reads a list value: an array, or text split at newlines and
// commas, trimmed, without empty entries.
func stringsOf(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if s := trimmed(stringify(e)); s != "" {
				out = append(out, s)
			}
		}
	case []string:
		for _, e := range x {
			if s := trimmed(e); s != "" {
				out = append(out, s)
			}
		}
	case string:
		for _, e := range strings.FieldsFunc(x, func(r rune) bool { return r == '\n' || r == ',' }) {
			if s := trimmed(e); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func toAny(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}

// multiField is a set of chips with a menu adding options and, when the
// field allows, a field adding custom values.
func (f *form) multiField(field schema.Field) {
	c := f.c
	k := kit.T(c)
	selected := stringsOf(f.shown[field.Key])
	setList := func(list []string) {
		if len(list) == 0 {
			f.set(field.Key, nil)
			return
		}
		f.set(field.Key, toAny(list))
	}
	ui.Column(c).Gap(6).Padding(6, 8).Radius(kit.RadiusInput).Background(k.Fill).Shrink(0).Children(func() {
		ui.Row(c).Wrap().Gap(6).Children(func() {
			if len(selected) == 0 {
				placeholder := field.Placeholder
				if placeholder == "" {
					placeholder = "请选择"
				}
				ui.Text(c, placeholder).FontSize(kit.FontBody).TextColor(k.Label3).Height(22)
			}
			for i, value := range selected {
				ui.Row(c.Key(value)).Children(func() {
					kit.Tag(c, value, func() { setList(append(append([]string(nil), selected[:i]...), selected[i+1:]...)) })
				})
			}
			var rest []string
			for _, o := range field.Options {
				if !containsString(selected, o) {
					rest = append(rest, o)
				}
			}
			if len(rest) > 0 {
				kit.Button(c, "添加", kit.ButtonOptions{Variant: kit.TextButton, Small: true, Icon: kit.SymPlus}).
					Label("添加" + field.Label).PopoverMenuButton(func(m *ui.Menu) {
					for _, o := range rest {
						if m.Item(o).Chosen() {
							setList(append(append([]string(nil), selected...), o))
						}
					}
				})
			}
		})
		if field.AllowCustom {
			custom := f.st.multi[field.Key]
			ui.Row(c).Gap(6).Children(func() {
				var input ui.Element
				grow(c, func() {
					input = kit.Input(c, "custom", &custom, "输入自定义值").Label(field.Label + "自定义值")
				})
				f.st.multi[field.Key] = custom
				add := func() {
					value := trimmed(custom)
					if value != "" && !containsString(selected, value) {
						setList(append(append([]string(nil), selected...), value))
					}
					f.st.multi[field.Key] = ""
				}
				if input.Submitted() {
					add()
				}
				kit.Button(c, "添加", kit.ButtonOptions{Small: true, Disabled: trimmed(custom) == ""}).OnClick(add)
			})
		}
	})
}

// jsonField edits any JSON value as indented text; text that does not
// parse stays, marked, until fixed.
func (f *form) jsonField(field schema.Field) {
	st := f.st
	key := field.Key
	text, drafting := st.drafts[key]
	if !drafting {
		if v := f.shown[key]; v != nil {
			raw, _ := json.MarshalIndent(v, "", "  ")
			text = string(raw)
		}
	}
	area := kit.TextArea(f.c, "json", &text, placeholderOf(field), true).Label(field.Label).MinHeight(96)
	if area.Changed() {
		if trimmed(text) == "" {
			delete(st.invalid, key)
			f.set(key, nil)
		} else {
			var v any
			if err := json.Unmarshal([]byte(text), &v); err != nil {
				st.invalid[key] = "JSON 格式不完整"
				f.errors[key] = st.invalid[key]
			} else {
				delete(st.invalid, key)
				delete(f.errors, key)
				f.set(key, v)
			}
		}
		f.draft(key, text)
	}
	if !area.Focused() && st.invalid[key] == "" {
		delete(st.drafts, key)
	}
}

// kvRow is a row of a key-value field.
type kvRow struct {
	id         int
	key, value string
}

type kvState struct {
	rows []kvRow
	last string
	next int
}

func serialize(v any) string {
	if v == nil {
		v = map[string]any{}
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

// keyValueField edits a string map, as HTTP headers; a key repeated
// stores its values as a list.
func (f *form) keyValueField(field schema.Field) {
	c := f.c
	k := kit.T(c)
	value := f.shown[field.Key]
	s, ok := f.st.kv[field.Key]
	if !ok {
		s = &kvState{last: "\x00"}
		f.st.kv[field.Key] = s
	}
	if current := serialize(value); current != s.last {
		s.last = current
		s.rows = nil
		if m, ok := value.(map[string]any); ok {
			for _, key := range sortedKeys(m) {
				items, isList := m[key].([]any)
				if !isList {
					items = []any{m[key]}
				}
				for _, item := range items {
					s.next++
					s.rows = append(s.rows, kvRow{id: s.next, key: key, value: stringify(item)})
				}
			}
		}
	}
	commit := func() {
		result := map[string]any{}
		for _, row := range s.rows {
			key := trimmed(row.key)
			if key == "" {
				continue
			}
			switch previous := result[key].(type) {
			case nil:
				result[key] = row.value
			case []any:
				result[key] = append(previous, row.value)
			default:
				result[key] = []any{previous, row.value}
			}
		}
		var next any
		if len(result) > 0 {
			next = result
		}
		s.last = serialize(next)
		f.set(field.Key, next)
	}
	ui.Column(c).Gap(6).Shrink(0).Children(func() {
		for i := range s.rows {
			row := &s.rows[i]
			n := itoa(i + 1)
			ui.Row(c.Key(row.id)).Gap(6).Padding(4, 6).Radius(kit.RadiusInput).Background(k.Surface2).Children(func() {
				grow(c, func() {
					if kit.Input(c, "key", &row.key, "Key: Host").Label("请求头 " + n + " 名称").Changed() {
						commit()
					}
				})
				grow(c, func() {
					if kit.Input(c, "value", &row.value, "Value: example.com").Label("请求头 " + n + " 值").Changed() {
						commit()
					}
				})
				id := row.id
				kit.IconButton(c, kit.SymTrash2, "删除请求头 "+n, kit.IconButtonOptions{Size: 28, IconSize: 15}).OnClick(func() {
					for j := range s.rows {
						if s.rows[j].id == id {
							s.rows = append(s.rows[:j], s.rows[j+1:]...)
							break
						}
					}
					commit()
				})
			})
		}
		ui.Row(c).Children(func() {
			kit.Button(c, "添加请求头", kit.ButtonOptions{Variant: kit.TextButton, Small: true, Icon: kit.SymPlus}).OnClick(func() {
				s.next++
				s.rows = append(s.rows, kvRow{id: s.next})
			})
		})
	})
}
