package forms

import (
	"slices"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// DocRefOptions are the outbounds and endpoints a reference may name:
// enabled ones, and nodes only of enabled subscriptions.
func DocRefOptions(doc *model.Doc) []RefOption {
	if doc == nil {
		return nil
	}
	enabledSubs := map[string]bool{}
	for _, s := range doc.Subscriptions {
		if s.Enabled {
			enabledSubs[s.ID] = true
		}
	}
	var out []RefOption
	for _, o := range doc.Outbounds {
		if o.Enabled && (o.SubID == "" || enabledSubs[o.SubID]) {
			out = append(out, RefOption{o.ID, o.Tag, o.Type})
		}
	}
	for _, e := range doc.Endpoints {
		if e.Enabled {
			out = append(out, RefOption{e.ID, e.Tag, e.Type})
		}
	}
	return out
}

// targetAddress is an outbound's server address, as "[::1]:443".
func targetAddress(t model.Target) string {
	if t.Endpoint != nil {
		return schema.EndpointAddress(t.Endpoint.Type, t.Endpoint.Fields)
	}
	o := t.Outbound
	if o.Type == "burrow" {
		return trimmed(stringify(o.Fields["signal_address"]))
	}
	server, port := trimmed(stringify(o.Fields["server"])), trimmed(stringify(o.Fields["server_port"]))
	if server == "" {
		return ""
	}
	if strings.Contains(server, ":") && !strings.HasPrefix(server, "[") {
		server = "[" + server + "]"
	}
	if port != "" {
		return server + ":" + port
	}
	return server
}

// OutboundOptions are the choices of an outbound reference, in the
// outbounds' order: the tag, with the protocol, the address and "Disabled"
// for disabled ones as detail (outboundSelectOptions).
func OutboundOptions(doc *model.Doc, exclude ...string) []kit.Option {
	if doc == nil {
		return nil
	}
	subs := map[string]*model.Subscription{}
	for _, s := range doc.Subscriptions {
		subs[s.ID] = s
	}
	var out []kit.Option
	for _, t := range model.AllOutboundTargets(doc) {
		if containsString(exclude, t.ID()) {
			continue
		}
		disabled := !t.Enabled()
		if t.Outbound != nil && t.Outbound.SubID != "" {
			if s := subs[t.Outbound.SubID]; s != nil && !s.Enabled {
				disabled = true
			}
		}
		detail := targetAddress(t)
		if disabled {
			detail = strings.TrimPrefix(detail+" · 已关闭", " · ")
		}
		out = append(out, kit.Option{Value: t.ID(), Label: t.Tag(), Tag: nodeview.ProtocolTag(t.Type()), Detail: detail})
	}
	return out
}

// dialerDNSTypes are DNS server types that dial through a detour.
var dialerDNSTypes = []string{"local", "udp", "tcp", "tls", "https", "quic", "h3", "dhcp"}

// DNSResolverDetourKey is the key of the detour paired with a DNS server
// reference field: "x.server" → "x.detour", "x" → "x.detour".
func DNSResolverDetourKey(key string) string {
	if strings.HasSuffix(key, ".server") {
		return strings.TrimSuffix(key, ".server") + ".detour"
	}
	return key + ".detour"
}

// DNSDetourSelect chooses the outbound DNS queries to server leave
// through (DnsDetourSelect): "Direct" for dialing servers, "Protocol default" for the
// others, which take no detour. A value that is no longer offered stays,
// marked for fixing.
func DNSDetourSelect(c *ui.Context, key any, doc *model.Doc, server string, value *string, label string, refs []RefOption) ui.Element {
	var target *model.DNSServer
	if doc != nil {
		target = model.DNSServerByID(doc, server)
	}
	supports := target != nil && target.Type != "local" && containsString(dialerDNSTypes, target.Type)
	first := "协议默认"
	if supports {
		first = "直连（服务器拨号）"
	}
	return OutboundSelect(c, key, doc, value, OutboundSelectOptions{
		Label: label, Lead: []kit.Option{{Value: "", Label: first}}, Only: refSet(refs),
		NoTargets: !supports, Disabled: !supports && *value == "",
	})
}

// refSet is the ids of refs, for OutboundSelectOptions.Only.
func refSet(refs []RefOption) func(string) bool {
	ids := map[string]bool{}
	for _, r := range refs {
		ids[r.ID] = true
	}
	return func(id string) bool { return ids[id] }
}

// refField chooses an outbound, an inbound or a DNS server by id.
func (f *form) refField(field schema.Field) {
	c := f.c
	k := kit.T(c)
	refs := f.fc.RefOptions
	if refs == nil {
		refs = DocRefOptions(f.fc.Doc)
	}
	value := stringify(f.shown[field.Key])
	empty := "（无）"
	var options []kit.Option
	available := 0
	switch field.Type {
	case schema.RefDNS:
		empty = "无"
		if f.fc.Doc != nil {
			for _, s := range f.fc.Doc.DNS.Servers {
				if s.Enabled && s.ID != f.fc.ExcludeDNSRef {
					options = append(options, kit.Option{Value: s.ID, Label: s.Tag})
				}
			}
		}
		available = len(options)
	case schema.RefIn:
		for _, o := range f.fc.InOptions {
			if o.ID != f.fc.ExcludeRef {
				options = append(options, kit.Option{Value: o.ID, Label: o.Tag + "（" + o.Type + "）"})
			}
		}
		available = len(options)
	default:
		usable := map[string]bool{}
		for _, r := range refs {
			if r.ID != f.fc.ExcludeRef {
				usable[r.ID] = true
			}
		}
		available = len(usable)
		var exclude []string
		if f.fc.ExcludeRef != "" {
			exclude = append(exclude, f.fc.ExcludeRef)
		}
		if OutboundSelect(c, "ref", f.fc.Doc, &value, OutboundSelectOptions{Label: field.Label, Lead: []kit.Option{{Value: "", Label: empty}},
			Exclude: exclude, Only: func(id string) bool { return usable[id] }}).Changed() {
			if value == "" {
				f.set(field.Key, nil)
			} else {
				f.set(field.Key, value)
			}
		}
	}
	if field.Type == schema.RefDNS || field.Type == schema.RefIn {
		options = append([]kit.Option{{Value: "", Label: empty}}, options...)
		// A reference to a target no longer offered (disabled, deleted or
		// the entity itself) stays visible, marked, instead of reading as
		// none. OutboundSelect does so for outbounds.
		if value != "" && !slices.ContainsFunc(options, func(o kit.Option) bool { return o.Value == value }) {
			options = append(options, kit.Option{Value: value, Label: f.staleRefLabel(field.Type, value), Detail: "需修正引用"})
		}
		if kit.Select(c, "ref", &value, options, kit.SelectOptions{Label: field.Label}).Changed() {
			if value == "" {
				f.set(field.Key, nil)
			} else {
				f.set(field.Key, value)
			}
		}
	}
	if available == 0 {
		text := "还没有其他出站可选"
		switch field.Type {
		case schema.RefDNS:
			text = "还没有其他 DNS 服务器可选"
		case schema.RefIn:
			text = "还没有其他入站可选"
		}
		ui.Text(c, text).FontSize(kit.FontCaption).TextColor(k.Label3)
	}
	if field.Type == schema.RefDNS && value != "" {
		detourKey := DNSResolverDetourKey(field.Key)
		detour := stringify(f.shown[detourKey])
		ui.Column(c).Gap(5).Margin(4, 0, 0).Children(func() {
			ui.Text(c, field.Label+"出口").FontSize(kit.FontTable).TextColor(k.Label2)
			if DNSDetourSelect(c, "detour", f.fc.Doc, value, &detour, field.Label+"查询出口", refs).Changed() {
				if detour == "" {
					f.set(detourKey, nil)
				} else {
					f.set(detourKey, detour)
				}
			}
		})
	}
}

// staleRefLabel names the target of a reference no longer offered: its
// tag, or the id of a deleted one.
func (f *form) staleRefLabel(typ schema.FieldType, id string) string {
	doc := f.fc.Doc
	switch typ {
	case schema.RefDNS:
		if doc != nil {
			if s := model.DNSServerByID(doc, id); s != nil {
				return s.Tag
			}
		}
	case schema.RefIn:
		for _, o := range f.fc.InOptions {
			if o.ID == id {
				return o.Tag
			}
		}
		if doc != nil {
			for _, in := range doc.Inbounds {
				if in.ID == id {
					return in.Tag
				}
			}
		}
	default:
		if doc != nil {
			if t := model.FindOutboundTarget(doc, id); t.Valid() {
				return t.Tag()
			}
		}
	}
	return id + "（已删除）"
}
