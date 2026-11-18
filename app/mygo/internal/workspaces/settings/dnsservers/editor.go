package dnsservers

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/forms"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// editor is the Add / Edit DNS Server dialog. A new server stays a draft
// until it is saved, so the cards never show unsaved servers.
type editor struct {
	section *Section

	open      bool
	editingID string
	draft     *model.DNSServer
	// generation keys the dialer form, so each opening starts fresh.
	generation int

	address      string
	addressError string
	addressFocus bool
	hostsText    string
	tlsOpen      bool

	dialerValid bool
	saving      bool
	saveError   string
	remove      kit.ConfirmState
}

func (e *editor) begin(draft *model.DNSServer, id string) {
	if !e.open {
		e.section.store.BeginEdit()
	}
	e.open, e.editingID, e.draft = true, id, draft
	e.generation++
	e.address = model.FormatDNSAddress(draft)
	e.addressError, e.saveError = "", ""
	e.dialerValid = true
	e.remove = kit.ConfirmState{}
	e.resetTypeState()
}

// resetTypeState follows the draft's type: the hosts text and whether TLS
// settings start open.
func (e *editor) resetTypeState() {
	e.hostsText = strings.Join(valuesOf(e.draft.Fields["path"]), "\n")
	tls := tlsMap(e.draft)
	e.tlsOpen = false
	for _, key := range []string{"server_name", "alpn", "insecure", "min_version", "max_version"} {
		if v, ok := tls[key]; ok && configured(v) {
			e.tlsOpen = true
		}
	}
}

func configured(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(x) != ""
	case []any:
		return len(x) > 0
	case bool:
		return x
	}
	return true
}

// openNew starts a custom server (UDP; the type changes in the editor).
func (e *editor) openNew(doc *model.Doc) {
	e.section.details, e.section.detailsOpen = "", false
	e.begin(newServer(doc, "udp"), "")
	e.address = ""
}

// openExisting edits a copy of a stored server.
func (e *editor) openExisting(doc *model.Doc, id string) {
	server := model.DNSServerByID(doc, id)
	if server == nil {
		return
	}
	e.section.details, e.section.detailsOpen = "", false
	copied := model.Clone(server)
	e.begin(model.WithFakeIPDefaults(model.DNSServerForType(copied, copied.Type)), id)
}

func (e *editor) close() {
	id := e.editingID
	if e.open {
		e.section.store.EndEdit()
	}
	e.open, e.draft, e.editingID = false, nil, ""
	e.remove.Open = false
	e.section.focusServer(id, false)
}

func (e *editor) changeType(typ string) {
	if typ == e.draft.Type {
		return
	}
	next := model.WithFakeIPDefaults(model.DNSServerForType(e.draft, typ))
	e.dialerValid, e.saveError = true, ""
	e.address = model.FormatDNSAddress(next)
	e.addressError = ""
	e.draft = next
	e.generation++
	e.resetTypeState()
}

// updateAddress applies the typed address when it parses; normalize, as
// the field loses the focus, also shows its problem and rewrites it.
func (e *editor) updateAddress(normalize bool) {
	parsed, problem := model.ParseDNSAddress(e.address, e.draft.Type)
	if problem != "" {
		if normalize || e.addressError != "" {
			e.addressError = problem
		}
		return
	}
	e.addressError = ""
	typ := e.draft.Type
	e.draft = model.DNSAddressPatch(e.draft, parsed)
	if e.draft.Type != typ {
		e.generation++
	}
	if normalize {
		e.address = model.FormatDNSAddress(e.draft)
	}
}

func (e *editor) save(doc *model.Doc, library []*model.NetworkConfig) {
	if e.saving || e.draft == nil {
		return
	}
	if !e.dialerValid {
		e.saveError = "请修正拨号选项中的 JSON 格式"
		return
	}
	draft := e.draft
	if problem := model.FakeIPRangeError(draft); problem != "" {
		e.saveError = problem
		return
	}
	var servers []*model.DNSServer
	index := -1
	for i, s := range doc.DNS.Servers {
		if s.ID == draft.ID {
			index = i
			continue
		}
		servers = append(servers, s)
	}
	if index < 0 {
		index = len(servers)
	}
	servers = slices.Insert(servers, index, draft)
	if problem := model.FakeIPServerError(servers, doc.DNS.Final); problem != "" {
		e.saveError = problem
		return
	}
	normalized := model.DNSServerForType(draft, draft.Type)
	if model.IsRemoteDNS(draft.Type) {
		parsed, problem := model.ParseDNSAddress(e.address, draft.Type)
		if problem != "" {
			e.addressError, e.saveError = problem, ""
			return
		}
		normalized = model.DNSAddressPatch(draft, parsed)
	}
	if problems := model.NetworkReferenceErrors(model.DNSNetworkSchema(normalized.Type), normalized.NetworkConfigRefs, library, normalized.Tag); len(problems) > 0 {
		e.saveError = strings.Join(problems, "；")
		return
	}
	if problem := model.TagValidationError(doc, draft.Tag, draft.ID); problem != "" {
		e.section.env.Toast(problem)
		return
	}
	normalized = model.Clone(normalized)
	if problem := normalizeJSONFields(normalized); problem != "" {
		e.section.env.Toast(problem)
		return
	}
	err := e.section.store.Update(func(doc *model.Doc) {
		for i, s := range doc.DNS.Servers {
			if s.ID == normalized.ID {
				doc.DNS.Servers[i] = normalized
				return
			}
		}
		doc.DNS.Servers = append(doc.DNS.Servers, normalized)
	})
	if err != nil {
		e.saveError = err.Error()
		return
	}
	e.editingID = normalized.ID
	e.close()
}

func (e *editor) delete(done func(error)) {
	id := e.editingID
	err := e.section.store.Update(func(doc *model.Doc) {
		doc.DNS.Servers = slices.DeleteFunc(doc.DNS.Servers, func(s *model.DNSServer) bool { return s.ID == id })
	})
	if err != nil {
		done(err)
		return
	}
	done(nil)
	e.editingID = ""
	e.close()
}

func (e *editor) view(c *ui.Context, doc *model.Doc, library []*model.NetworkConfig) {
	if !e.open || e.draft == nil {
		return
	}
	open := true
	title := "添加 DNS 服务器"
	if e.editingID != "" {
		title = "编辑 DNS 服务器"
	}
	kit.Dialog(c, &open, title, 540, func() { e.form(c, doc, library) }, func() {
		if e.saveError != "" {
			ui.Text(c, e.saveError).FontSize(kit.FontCaption).TextColor(kit.T(c).Danger).Wrap().Grow(1).MinWidth(0)
		}
		if e.editingID != "" {
			remove := kit.Button(c, "删除", kit.ButtonOptions{Variant: kit.Danger, Disabled: e.saving})
			if remove.Clicked() {
				e.remove.Open = true
			}
			kit.ConfirmPopover(c, remove, &e.remove, kit.ConfirmOptions{
				Title:   "删除 DNS 服务器",
				Message: "删除「" + e.draft.Tag + "」后，关联规则与解析器引用需要重新选择。",
				Confirm: "删除服务器", Danger: true, Run: e.delete,
			})
		}
		ui.Spacer(c)
		kit.Button(c, "取消", kit.ButtonOptions{Disabled: e.saving}).OnClick(e.close)
		label := "保存服务器"
		if e.saving {
			label = "正在保存…"
		}
		kit.Button(c, label, kit.ButtonOptions{Variant: kit.Primary, Disabled: e.saving}).OnClick(func() { e.save(doc, library) })
	})
	if !open && e.open && !e.saving {
		e.close()
	}
}

func (e *editor) form(c *ui.Context, doc *model.Doc, library []*model.NetworkConfig) {
	draft := e.draft
	tagError := model.TagValidationError(doc, draft.Tag, draft.ID)
	formGroup(c, func() {
		fieldRow(c, "名称", func() {
			kit.Input(c, "tag", &draft.Tag, "").Label("DNS 服务器名称")
			kit.FormError(c, tagError)
		})
		fieldRow(c, "类型", func() {
			typ := draft.Type
			var options []kit.Option
			for _, t := range typeOptions(doc, draft) {
				options = append(options, kit.Option{Value: t, Label: t})
			}
			if kit.Select(c, "type", &typ, options, kit.SelectOptions{Label: "DNS 服务器协议"}).FillWidth().Changed() {
				e.changeType(typ)
			}
		})
		draft = e.draft
		switch {
		case draft.Type == "dhcp":
			fieldRow(c, "网络接口", func() {
				text := jsv.StringOr(draft.Fields["interface"], "")
				if kit.Input(c, "interface", &text, "留空自动选择接口，例如 en0").Label("网络接口").Changed() {
					setField(draft, "interface", text)
				}
			})
		case draft.Type == "tailscale":
			fieldRow(c, "Tailscale 端点", func() {
				value := jsv.StringOr(draft.Fields["endpoint"], "")
				options := []kit.Option{{Value: "", Label: "请选择端点"}}
				for _, ep := range doc.Endpoints {
					if ep.Enabled && ep.Type == "tailscale" {
						options = append(options, kit.Option{Value: ep.ID, Label: ep.Tag})
					}
				}
				if kit.Select(c, "endpoint", &value, options, kit.SelectOptions{Label: "Tailscale 端点"}).FillWidth().Changed() {
					setField(draft, "endpoint", value)
				}
			})
			e.switchField(c, "接受默认 DNS 回退", "accept_default_resolvers")
			e.switchField(c, "接受搜索域", "accept_search_domain")
		case model.IsRemoteDNS(draft.Type):
			fieldRow(c, "服务器地址", func() {
				input := kit.Input(c, "address", &e.address, model.DNSAddressPlaceholder(draft.Type)).Label("服务器地址")
				if input.Changed() {
					e.updateAddress(false)
				}
				focused := input.Focused()
				if e.addressFocus && !focused {
					e.updateAddress(true)
				}
				e.addressFocus = focused
				kit.FormError(c, e.addressError)
			})
		}
		draft = e.draft
		if model.IsHTTPDNS(draft.Type) {
			fieldRow(c, "HTTP 请求头", func() {
				text := objectText(draft.Fields["headers"])
				if kit.TextArea(c, "headers", &text, "{\n  \"Authorization\": \"Bearer token\"\n}", true).Label("HTTP 请求头").Changed() {
					setField(draft, "headers", text)
				}
			})
		}
		if draft.Type == "hosts" {
			fieldRow(c, "Hosts 文件路径", func() {
				if kit.TextArea(c, "hosts-path", &e.hostsText, "/etc/hosts\n/opt/kiko/hosts", true).Label("Hosts 文件路径").Changed() {
					var paths []any
					for _, line := range strings.Split(e.hostsText, "\n") {
						if line = strings.TrimSpace(line); line != "" {
							paths = append(paths, line)
						}
					}
					if paths == nil {
						paths = []any{}
					}
					setField(draft, "path", paths)
				}
			})
			fieldRow(c, "预定义 Hosts", func() {
				text := objectText(draft.Fields["predefined"])
				if kit.TextArea(c, "predefined", &text, "{\n  \"example.com\": [\"203.0.113.1\", \"2001:db8::1\"]\n}", true).Label("预定义 Hosts").Changed() {
					setField(draft, "predefined", text)
				}
			})
		}
		if draft.Type == "fakeip" {
			for _, r := range []struct{ key, label string }{{"inet4_range", "FakeIP IPv4 网段"}, {"inet6_range", "FakeIP IPv6 网段"}} {
				fieldRow(c, r.label, func() {
					text := jsv.StringOr(draft.Fields[r.key], "")
					if kit.Input(c, r.key, &text, model.FakeIPDefaultRanges[r.key]).Label(r.label).Changed() {
						setField(draft, r.key, text)
					}
				})
			}
		}
		if draft.Type == "local" || draft.Type == "dhcp" {
			e.switchField(c, "优先使用 Go 解析器", "prefer_go")
		}
	})
	if model.IsTLSDNS(draft.Type) {
		formGroup(c, func() {
			kit.Disclosure(c, "TLS 设置", &e.tlsOpen, func() {
				fieldRow(c, "SNI / Server Name", func() {
					tls := tlsMap(draft)
					text := jsv.StringOr(tls["server_name"], "")
					if kit.Input(c, "sni", &text, "dns.example.com").Label("SNI / Server Name").Changed() {
						next := map[string]any{}
						for k, v := range tls {
							next[k] = v
						}
						next["server_name"] = text
						setField(draft, "tls", next)
					}
				})
			})
		})
	}
	if model.IsDialerDNS(draft.Type) {
		var fields []schema.Field
		for _, f := range schema.DNSDialerSchema {
			if f.Key != "detour" {
				fields = append(fields, f)
			}
		}
		if model.IsTLSDNS(draft.Type) {
			for _, f := range schema.DNSTLSSchema {
				if model.TLSConfigField(f) || model.EntityTLSKey(f.Key) && !strings.HasPrefix(f.Key, "tls.reality.handshake.domain_resolver.") {
					fields = append(fields, f)
				}
			}
		}
		result := forms.Form(c, []any{"dialer", draft.ID, e.generation}, fields, dialerValues(draft), forms.Context{
			Doc:            doc,
			NetworkConfigs: library,
			NetworkRefs:    draft.NetworkConfigRefs,
			OnNetworkRefs:  func(refs model.NetworkConfigRefs) { draft.NetworkConfigRefs = refs },
			ExcludeDNSRef:  draft.ID,
			OnChange:       func(key string, value any) { setDialerValue(doc, draft, key, value) },
		})
		e.dialerValid = result.Valid
	}
}

func (e *editor) switchField(c *ui.Context, label, key string) {
	on := jsv.Truthy(e.draft.Fields[key])
	switchRow(c, label, &on)
	if on != jsv.Truthy(e.draft.Fields[key]) {
		setField(e.draft, key, on)
	}
}
