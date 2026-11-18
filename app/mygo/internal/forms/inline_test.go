package forms

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

var inlineFields = []schema.Field{
	{Key: "url", Label: "测速地址", Type: schema.Text, Required: true},
	{Key: "max_failures", Label: "失败阈值", Type: schema.Number, Min: ptr(1), Max: ptr(100)},
	{Key: "no_return", Label: "保持当前成员", Type: schema.Bool},
}

// TestFormInline: an inline form puts each label left of its control in
// a column of InlineLabelWidth, numbers no wider than InlineShortWidth;
// LabeledRow lays out the same way, or stacked when not inline; typing
// and problems work as in the stacked form.
func TestFormInline(t *testing.T) {
	values := map[string]any{}
	inline := true
	name := ""
	var result Result
	tt := ui.NewTester(func(c *ui.Context) {
		kit.Apply(c)
		c.Root().Background(kit.T(c).Elevated)
		ui.Column(c).Fill().Padding(16).Gap(10).Children(func() {
			LabeledRow(c, inline, "名称", func() { kit.Input(c, "name", &name, "名称输入") })
			result = Form(c, "form", inlineFields, values, Context{Inline: inline, ShowErrors: true})
		})
	}, 600, 400)
	tt.Frame()
	find := func(s string) ui.Rect {
		r, ok := tt.Find(s)
		if !ok {
			t.Fatalf("no %q in %q", s, tt.Texts())
		}
		return r
	}
	label, control := find("名称"), find("名称输入")
	if control.X < label.X+InlineLabelWidth || control.Y > label.Y+10 {
		t.Errorf("LabeledRow: label %+v control %+v", label, control)
	}
	url, failures := find("测速地址"), find("失败阈值")
	if url.X != label.X || failures.X != label.X {
		t.Errorf("labels not aligned: %v %v %v", label.X, url.X, failures.X)
	}
	if result.Errors["url"] == "" || !tt.HasText(result.Errors["url"]) {
		t.Errorf("problem not shown: %v %q", result.Errors, tt.Texts())
	}
	// The number's input starts after the label column; typing sets it.
	tt.ClickAt(failures.X+InlineLabelWidth+12+20, failures.Y+failures.H/2)
	tt.Frame()
	tt.Type("7")
	tt.Frame()
	if values["max_failures"] != 7.0 {
		t.Fatalf("typed %v", values["max_failures"])
	}
	inline = false
	tt.Frame()
	label, control = find("名称"), find("名称输入")
	if control.Y <= label.Y {
		t.Errorf("stacked: label %+v control %+v", label, control)
	}
}
