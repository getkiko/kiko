package forms

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

func ptr(f float64) *float64 { return &f }

var testFields = []schema.Field{
	{Key: "server", Label: "服务器", Type: schema.Text, Required: true},
	{Key: "server_port", Label: "端口", Type: schema.Number, Min: ptr(1), Max: ptr(65535), Action: schema.ActionRandomPort},
	{Key: "tls.enabled", Label: "TLS", Type: schema.Bool},
	{Key: "tls.server_name", Label: "SNI", Type: schema.Text, ShowIf: &schema.ShowIf{Key: "tls.enabled", Truthy: true},
		Validate: func(v string, _ map[string]any) string {
			if v == "bad" {
				return "SNI 无效"
			}
			return ""
		}},
	{Key: "network", Label: "网络", Type: schema.Select, Options: []string{"", "tcp", "udp"}},
	{Key: "route_address", Label: "路由地址", Type: schema.List, Item: schema.ItemCIDR},
	{Key: "headers", Label: "请求头", Type: schema.KeyValue, Adv: true, Group: "transport"},
	{Key: "extra", Label: "附加", Type: schema.JSON, Adv: true, Group: "transport"},
	{Key: "detour", Label: "前置出站", Type: schema.Ref, Adv: true},
	{Key: "auto_redirect", Label: "自动重定向", Type: schema.Bool, Platforms: []string{"linux"}},
}

type harness struct {
	values map[string]any
	errors map[string]string
	show   bool
	valid  bool
	doc    *model.Doc
}

func (h *harness) view(c *ui.Context) {
	kit.Apply(c)
	c.Root().Background(kit.T(c).Elevated)
	ui.Scroll(c).Fill().Padding(16).Children(func() {
		r := Form(c, "form", testFields, h.values, Context{Doc: h.doc, ShowErrors: h.show})
		h.errors, h.valid = r.Errors, r.Valid
	})
}

func newHarness() *harness {
	doc := model.EmptyDoc("linux")
	doc.Outbounds = []*model.Outbound{{Entity: model.Entity{ID: "o1", Tag: "直连", Enabled: true}, Kind: "outbound", Type: "direct", Fields: model.Fields{}}}
	return &harness{values: map[string]any{}, doc: doc}
}

func TestFormFieldsAndShowIf(t *testing.T) {
	h := newHarness()
	tt := ui.NewTester(h.view, 520, 1200)
	if h.errors["server"] != "必填" {
		t.Fatalf("required error %v", h.errors)
	}
	if tt.HasText("SNI") {
		t.Fatal("SNI shown while TLS is off")
	}
	if !tt.HasText("Linux 专有配置") || !tt.HasText("自动重定向") {
		t.Errorf("platform section missing: %q", tt.Texts())
	}
	clickBelow(t, tt, "服务器")
	tt.Type("example.com")
	if h.values["server"] != "example.com" || h.errors["server"] != "" {
		t.Errorf("server %v errors %v", h.values["server"], h.errors)
	}
	clickBelow(t, tt, "端口")
	tt.Type("70000")
	if h.values["server_port"] != 65535.0 {
		t.Errorf("port not clamped: %v", h.values["server_port"])
	}
	clickSwitch(t, tt, "TLS", 520)
	if h.values["tls.enabled"] != true || !tt.HasText("SNI") {
		t.Fatalf("TLS switch %v; texts %q", h.values["tls.enabled"], tt.Texts())
	}
	clickBelow(t, tt, "SNI")
	tt.Type("bad")
	if h.errors["tls.server_name"] != "SNI 无效" {
		t.Errorf("validation %v", h.errors)
	}
	h.show = true
	tt.Frame()
	if !tt.HasText("SNI 无效") {
		t.Errorf("error not shown")
	}
	// List: add an invalid then a valid CIDR.
	if err := tt.Click("+ 添加网段"); err != nil {
		t.Fatal(err)
	}
	tt.Type("nope")
	if err := tt.Click("确定"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("不是合法的 CIDR，例：192.168.0.0/16") {
		t.Errorf("list validation not shown: %q", tt.Texts())
	}
	if err := tt.Click("10.0.0.0/8"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("确定"); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.values["route_address"].([]any); len(got) != 1 || got[0] != "10.0.0.0/8" {
		t.Errorf("list value %v", h.values["route_address"])
	}
	// Advanced groups fold until opened.
	if tt.HasText("添加请求头") {
		t.Error("empty advanced group open")
	}
	savePNG(t, tt, "forms-light.png")
}

func TestFormJSONAndKeyValue(t *testing.T) {
	h := newHarness()
	h.values["headers"] = map[string]any{"Host": "a.com"}
	tt := ui.NewTester(h.view, 520, 1400)
	if !tt.HasText("已填 1") {
		t.Fatalf("filled group should open: %q", tt.Texts())
	}
	if err := tt.Click("添加请求头"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("请求头 2 名称"); err != nil {
		t.Fatal(err)
	}
	tt.Type("Host")
	if err := tt.Click("请求头 2 值"); err != nil {
		t.Fatal(err)
	}
	tt.Type("b.com")
	if got, _ := h.values["headers"].(map[string]any)["Host"].([]any); len(got) != 2 {
		t.Errorf("repeated header %v", h.values["headers"])
	}
	clickBelow(t, tt, "附加")
	tt.Type(`{"a":`)
	if h.errors["extra"] != "JSON 格式不完整" || !tt.HasText("JSON 格式不完整") || h.valid {
		t.Errorf("json error %v", h.errors)
	}
	tt.Type(`1}`)
	if m, _ := h.values["extra"].(map[string]any); m["a"] != 1.0 || h.errors["extra"] != "" {
		t.Errorf("json value %v errors %v", h.values["extra"], h.errors)
	}
	tt.SetDark(true)
	tt.Frame()
	savePNG(t, tt, "forms-dark.png")
}

func TestFormRefOptions(t *testing.T) {
	doc := model.EmptyDoc("linux")
	doc.Outbounds = []*model.Outbound{
		{Entity: model.Entity{ID: "a", Tag: "A", Enabled: true}, Type: "vless", Fields: model.Fields{"server": "::1", "server_port": 443.0}},
		{Entity: model.Entity{ID: "b", Tag: "B", Enabled: false}, Type: "direct", Fields: model.Fields{}},
	}
	options := OutboundOptions(doc, "x")
	if len(options) != 2 || options[0].Detail != "Vless · [::1]:443" || options[1].Detail != "Direct · 已关闭" {
		t.Errorf("options %+v", options)
	}
	refs := DocRefOptions(doc)
	if len(refs) != 1 || refs[0].ID != "a" {
		t.Errorf("refs %+v", refs)
	}
	if DNSResolverDetourKey("dns.server") != "dns.detour" || DNSResolverDetourKey("resolver") != "resolver.detour" {
		t.Error("detour key")
	}
}

func TestSubmitItems(t *testing.T) {
	spec := specOf(schema.ItemCIDR)
	if _, err := submitItems(spec, "10.0.0.0/8", []string{"10.0.0.0/8"}, true); err != "该条目已存在" {
		t.Errorf("duplicate: %q", err)
	}
	got, err := submitItems(spec, "10.0.0.0/8\n192.168.0.0/16, 10.0.0.0/8", nil, true)
	if err != "" || len(got) != 2 {
		t.Errorf("batch %v %q", got, err)
	}
	if _, err := submitItems(spec, "a\nb\nc\nd", nil, true); err != "4 条格式不正确：a、b、c 等" {
		t.Errorf("batch errors %q", err)
	}
	if specOf(schema.ItemUIDRange).Validate("5:1") != "起始值不能大于结束值" {
		t.Error("uid range")
	}
}

// clickBelow clicks the control under a field's label.
func clickBelow(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	r, ok := tt.Find(label)
	if !ok {
		t.Fatalf("no %q in %q", label, tt.Texts())
	}
	tt.ClickAt(r.X+20, r.Y+r.H+5+15)
}

// clickSwitch clicks the switch at the end of a bool field's row.
func clickSwitch(t *testing.T, tt *ui.Tester, label string, width float32) {
	t.Helper()
	r, ok := tt.Find(label)
	if !ok {
		t.Fatalf("no %q in %q", label, tt.Texts())
	}
	tt.ClickAt(width-16-20, r.Y+r.H/2)
}

func savePNG(t *testing.T, tt *ui.Tester, name string) {
	dir := os.Getenv("FORMS_PREVIEW_DIR")
	if dir == "" {
		return
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

func TestFormNumberRejectsNonFinite(t *testing.T) {
	h := newHarness()
	tt := ui.NewTester(h.view, 520, 1200)
	clickBelow(t, tt, "端口")
	tt.Type("nan")
	if v, ok := h.values["server_port"]; ok {
		t.Errorf("port %v", v)
	}
}

// A JSON draft that does not parse is dropped when the value changes from
// elsewhere, as another entity shown in the same form.
func TestFormDraftFollowsValue(t *testing.T) {
	h := newHarness()
	h.values["extra"] = map[string]any{"a": 1.0}
	tt := ui.NewTester(h.view, 520, 1400)
	clickBelow(t, tt, "附加")
	tt.Type(`x`)
	if h.valid || h.errors["extra"] == "" {
		t.Fatalf("invalid draft: valid %v errors %v", h.valid, h.errors)
	}
	h.values = map[string]any{"extra": map[string]any{"b": 2.0}}
	tt.Frame()
	tt.Frame()
	if !h.valid || h.errors["extra"] != "" {
		t.Errorf("stale draft: valid %v errors %v", h.valid, h.errors)
	}
}

// Removing an item while another is edited keeps editing that item.
func TestFormListEditorFollowsItem(t *testing.T) {
	h := newHarness()
	h.values["route_address"] = []any{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}
	tt := ui.NewTester(h.view, 520, 1400)
	if err := tt.Click("编辑 192.168.0.0/16"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("删除 10.0.0.0/8"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("10.0.0.0/8"); err != nil { // the example chip
		t.Fatal(err)
	}
	if err := tt.Click("编辑网段确定"); err != nil {
		t.Fatal(err)
	}
	got, _ := h.values["route_address"].([]any)
	if len(got) != 2 || got[0] != "172.16.0.0/12" || got[1] != "10.0.0.0/8" {
		t.Errorf("list %v", got)
	}
}

// A reference to a disabled outbound shows it, marked, not as none.
func TestFormStaleReference(t *testing.T) {
	h := newHarness()
	h.doc.Outbounds = append(h.doc.Outbounds, &model.Outbound{Entity: model.Entity{ID: "off", Tag: "停用出站", Enabled: false}, Kind: "outbound", Type: "direct", Fields: model.Fields{}})
	h.values["detour"] = "off"
	tt := ui.NewTester(h.view, 520, 1400)
	if !tt.HasText("停用出站") {
		t.Errorf("texts %q", tt.Texts())
	}
}
