package forms

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

// listState is a list field's item editor: closed, adding (index -1) or
// editing an item.
type listState struct {
	open    bool
	index   int
	text    string
	err     string
	touched bool
	item    string // the item being edited, to follow it when the list changes
}

// splitItems splits pasted text at newlines and commas.
func splitItems(text string) []string {
	var out []string
	for _, s := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == ',' }) {
		if s = trimmed(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// checkItem is an item's problem: empty, a duplicate, or its format's.
func checkItem(spec schema.ItemSpec, text string, exists []string) string {
	v := trimmed(text)
	switch {
	case v == "":
		return "不能为空"
	case slices.Contains(exists, v):
		return "该条目已存在"
	case spec.Validate != nil:
		return spec.Validate(v)
	}
	return ""
}

// submitItems checks the editor's text, one item or a pasted batch, and
// returns the items to add, or the problem.
func submitItems(spec schema.ItemSpec, text string, exists []string, adding bool) ([]string, string) {
	multi := splitItems(text)
	if !adding || len(multi) <= 1 {
		if problem := checkItem(spec, text, exists); problem != "" {
			return nil, problem
		}
		return []string{trimmed(text)}, ""
	}
	seen := append([]string(nil), exists...)
	var good, bad []string
	for _, one := range multi {
		if slices.Contains(seen, one) {
			continue
		}
		if spec.Validate != nil && spec.Validate(one) != "" {
			bad = append(bad, one)
			continue
		}
		good = append(good, one)
		seen = append(seen, one)
	}
	if len(bad) > 0 {
		more := ""
		if len(bad) > 3 {
			more = " 等"
		}
		return nil, fmt.Sprintf("%d 条格式不正确：%s%s", len(bad), strings.Join(bad[:min(3, len(bad))], "、"), more)
	}
	if len(good) == 0 {
		return nil, "没有可保存的新条目"
	}
	return good, ""
}

// listField shows a list's items, each editable and removable, and adds
// items through an editor checking their format; pasting several lines
// adds them at once.
func (f *form) listField(field schema.Field) {
	c := f.c
	k := kit.T(c)
	items := stringsOf(f.shown[field.Key])
	spec := specOf(field.Item)
	s, ok := f.st.lists[field.Key]
	if !ok {
		s = &listState{}
		f.st.lists[field.Key] = s
	}
	// The list may change under an open editor (an item removed, a new
	// value from elsewhere): keep editing the same item, or close.
	if s.open && s.index >= 0 && (s.index >= len(items) || items[s.index] != s.item) {
		s.index = slices.Index(items, s.item)
		s.open = s.index >= 0
	}
	store := func(list []string) {
		if len(list) == 0 {
			f.set(field.Key, nil)
			return
		}
		f.set(field.Key, toAny(list))
	}
	open := func(index int) {
		s.open, s.index, s.err, s.touched = true, index, "", false
		s.text, s.item = "", ""
		if index >= 0 {
			s.text, s.item = items[index], items[index]
		}
	}
	border := k.Hairline
	if f.fc.ShowErrors && f.errors[field.Key] != "" {
		border = k.Danger
	}
	ui.Column(c).Radius(kit.RadiusInput).Border(1, border).Background(k.Surface).Clip().Shrink(0).Children(func() {
		if len(items) == 0 {
			text := "未设置"
			if spec.Placeholder != "" {
				text = "未设置，例如 " + spec.Placeholder
			}
			ui.Text(c, text).FontSize(kit.FontBody).TextColor(k.Label3).Padding(8, 12)
		}
		ui.Column(c).Dividers(1, k.HairlineSoft).Children(func() {
			for i, item := range items {
				ui.Row(c.Key(fmt.Sprint(i, item))).MinHeight(kit.ControlHeight).PaddingStart(12).PaddingEnd(4).Gap(2).Children(func() {
					ui.Text(c, item).Font(kit.MonoFont).FontSize(kit.FontTable).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
					kit.IconButton(c, kit.SymPencil, "编辑 "+item, kit.IconButtonOptions{Size: 26, IconSize: 14}).OnClick(func() { open(i) })
					kit.IconButton(c, kit.SymTrash2, "删除 "+item, kit.IconButtonOptions{Size: 26, IconSize: 14}).OnClick(func() {
						store(append(append([]string(nil), items[:i]...), items[i+1:]...))
					})
				})
			}
		})
		add := ui.ButtonBase(c).MinHeight(kit.ControlHeight).Center().BorderWidth(1, 0, 0, 0).BorderColor(k.HairlineSoft).
			FontSize(kit.FontBody).TextColor(k.AccentText).Label("添加" + spec.Label).Transition(kit.HoverMotion)
		if add.Hovered() {
			add.Background(k.Fill)
		}
		add.Children(func() { ui.Text(c, "+ 添加"+spec.Label) })
		if add.Clicked() {
			open(-1)
		}
	})
	if s.open {
		f.itemEditor(field, spec, s, items, store)
	}
}

// itemEditor adds or edits an item of a list, below it.
func (f *form) itemEditor(field schema.Field, spec schema.ItemSpec, s *listState, items []string, store func([]string)) {
	c := f.c
	k := kit.T(c)
	adding := s.index < 0
	exists := items
	if !adding {
		exists = append(append([]string(nil), items[:s.index]...), items[s.index+1:]...)
	}
	title := "编辑" + spec.Label
	if adding {
		title = "添加" + spec.Label
	}
	submit := func() {
		values, problem := submitItems(spec, s.text, exists, adding)
		s.touched, s.err = true, problem
		if problem != "" {
			return
		}
		if adding {
			store(append(append([]string(nil), items...), values...))
		} else {
			next := append([]string(nil), items...)
			next[s.index] = values[0]
			store(next)
		}
		s.open = false
	}
	ui.Column(c).Gap(8).Padding(10, 12).Radius(12).Background(k.Surface2).Shrink(0).Label(title).Children(func() {
		ui.Text(c, title).FontSize(kit.FontBody).FontWeight(600)
		input := kit.Input(c, "item", &s.text, spec.Placeholder).Label(title).AutoFocus()
		if input.Changed() {
			s.touched = false
		}
		if input.Submitted() {
			submit()
		}
		if s.touched && s.err != "" {
			kit.FormError(c, s.err)
		} else if spec.Hint != "" {
			ui.Text(c, spec.Hint).FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
		}
		if multi := splitItems(s.text); adding && len(multi) > 1 {
			ui.Text(c, fmt.Sprintf("检测到多条内容，将一次性添加 %d 条", len(multi))).FontSize(kit.FontCaption).TextColor(k.AccentText)
		}
		if len(spec.Examples) > 0 {
			ui.Text(c, "点击填入示例：").FontSize(kit.FontCaption).TextColor(k.Label3)
			ui.Row(c).Wrap().Gap(6).Children(func() {
				for _, example := range spec.Examples {
					chip := ui.ButtonBase(c.Key(example)).Height(24).PaddingX(10).Radius(kit.RadiusCapsule).Background(k.Fill).
						Font(kit.MonoFont).FontSize(kit.FontTable).TextColor(k.Label2).Label(example)
					chip.Children(func() { ui.Text(c, example) })
					if chip.Clicked() {
						s.text, s.err, s.touched = example, "", false
					}
				}
			})
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			kit.Button(c, "取消", kit.ButtonOptions{Small: true}).OnClick(func() { s.open = false })
			kit.Button(c, "确定", kit.ButtonOptions{Small: true, Variant: kit.Primary, Disabled: s.text == "" && adding}).
				Label(title + "确定").OnClick(submit)
		})
	})
}
