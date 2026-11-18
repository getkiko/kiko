package dnsservers

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

func itoa(n int) string { return strconv.Itoa(n) }

// protocolLabel is a resolver type's short name (dnsProtocolLabel).
func protocolLabel(typ string) string {
	labels := map[string]string{
		"local": "Local", "udp": "UDP", "tcp": "TCP", "tls": "DoT", "https": "DoH", "quic": "DoQ",
		"h3": "H3", "hosts": "Hosts", "fakeip": "FakeIP", "dhcp": "DHCP", "tailscale": "Tailscale",
	}
	if label, ok := labels[typ]; ok {
		return label
	}
	return strings.ToUpper(typ)
}

// serverEndpoint is what a card shows as a server's address.
func serverEndpoint(server *model.DNSServer) string {
	if address := model.FormatDNSAddress(server); address != "" {
		return address
	}
	switch server.Type {
	case "local":
		return "系统解析器"
	case "hosts":
		return "Hosts 映射"
	case "fakeip":
		return "FakeIP 地址池"
	case "dhcp":
		if name := jsv.StringOr(server.Fields["interface"], ""); jsv.Truthy(server.Fields["interface"]) {
			return name
		}
		return "自动选择网络接口"
	case "tailscale":
		return "Tailscale 端点"
	}
	return "待配置"
}

// ruleCount is how many DNS rules route to the server.
func ruleCount(doc *model.Doc, id string) int {
	n := 0
	for _, r := range doc.DNS.Rules {
		if slices.ContainsFunc(r.Targets(), func(t model.DNSTarget) bool { return t.Server.ID() == id }) {
			n++
		}
	}
	return n
}

// sortKey orders the cards: by name or protocol, ascending or not.
type sortKey struct {
	key       string // "", "name" or "protocol"
	ascending bool
}

func (s sortKey) value() string {
	if s.key == "" {
		return ""
	}
	if s.ascending {
		return s.key + ":asc"
	}
	return s.key + ":desc"
}

func parseSort(value string) sortKey {
	key, direction, _ := strings.Cut(value, ":")
	return sortKey{key: key, ascending: direction == "asc"}
}

// filter is the cards' search, status and protocol filters.
type filter struct {
	search    string
	status    string // "all", "enabled" or "disabled"
	protocols []string
	sort      sortKey
}

func (f filter) filtered() bool { return len(f.protocols) > 0 || (f.status != "" && f.status != "all") }

// visible returns the servers the filters keep, sorted.
func (f filter) visible(servers []*model.DNSServer) []*model.DNSServer {
	query := strings.ToLower(strings.TrimSpace(f.search))
	var out []*model.DNSServer
	for _, s := range servers {
		if len(f.protocols) > 0 && !slices.Contains(f.protocols, s.Type) {
			continue
		}
		if f.status == "enabled" && !s.Enabled || f.status == "disabled" && s.Enabled {
			continue
		}
		text := strings.ToLower(s.Tag + " " + serverEndpoint(s) + " " + protocolLabel(s.Type))
		if !strings.Contains(text, query) {
			continue
		}
		out = append(out, s)
	}
	if f.sort.key != "" {
		slices.SortStableFunc(out, func(a, b *model.DNSServer) int {
			var c int
			if f.sort.key == "name" {
				c = strings.Compare(a.Tag, b.Tag)
			} else {
				c = strings.Compare(protocolLabel(a.Type), protocolLabel(b.Type))
			}
			if !f.sort.ascending {
				c = -c
			}
			return c
		})
	}
	return out
}

// protocolOptions are the types some server has, in the editor's order.
func protocolOptions(servers []*model.DNSServer) []string {
	var out []string
	for _, t := range dnsTypes {
		if slices.ContainsFunc(servers, func(s *model.DNSServer) bool { return s.Type == t }) {
			out = append(out, t)
		}
	}
	return out
}

// countText is the section heading's count: the total, prefixed by the
// visible count ("2 / 5") while searching or filtering.
func countText(all []*model.DNSServer, visible int, narrowed bool) string {
	prefix := ""
	if narrowed {
		prefix = fmt.Sprintf("%d / ", visible)
	}
	return fmt.Sprintf("%s%d 个", prefix, len(all))
}

// valuesOf reads a list field: an array, or text split at newlines and
// commas.
func valuesOf(value any) []string {
	var out []string
	switch v := value.(type) {
	case nil:
		return nil
	case []any:
		for _, item := range v {
			if s := strings.TrimSpace(jsv.Text(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		for _, item := range v {
			if s := strings.TrimSpace(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	for _, part := range strings.FieldsFunc(jsv.Text(value), func(r rune) bool { return r == '\n' || r == ',' }) {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// objectText is a JSON object field as editable text: text as is, an
// object indented, anything else empty.
func objectText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case map[string]any:
		raw, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return ""
		}
		return string(raw)
	}
	return ""
}

// typeOptions are the types the editor offers a server: local and dhcp
// once per document.
func typeOptions(doc *model.Doc, server *model.DNSServer) []string {
	var out []string
	for _, t := range dnsTypes {
		if (t == "local" || t == "dhcp") && t != server.Type &&
			slices.ContainsFunc(doc.DNS.Servers, func(s *model.DNSServer) bool { return s.Type == t && s.ID != server.ID }) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// newServer is a custom server of the add button: UDP by default.
func newServer(doc *model.Doc, typ string) *model.DNSServer {
	s := &model.DNSServer{ID: model.NewID("dns"), Tag: fmt.Sprintf("dns-%s-%d", typ, len(doc.DNS.Servers)+1), Enabled: true, Type: typ}
	if model.IsTLSDNS(typ) {
		s.Fields = model.Fields{"tls": map[string]any{}}
	}
	return model.WithFakeIPDefaults(s)
}

// dialerValues are the values of the dialer form: the fields with the
// server's own resolver settings under their schema keys.
func dialerValues(s *model.DNSServer) map[string]any {
	values := map[string]any{}
	for k, v := range s.Fields {
		values[k] = v
	}
	set := func(key string, v any, ok bool) {
		if ok {
			values[key] = v
		} else {
			delete(values, key)
		}
	}
	set("server", s.Server, s.Server != "")
	set("domain_resolver.server", s.DomainResolver.ID(), s.DomainResolver.Valid())
	set("domain_resolver.detour", s.DomainResolverDetour.ID(), s.DomainResolverDetour.Valid())
	set("domain_resolver.strategy", s.DomainStrategy, s.DomainStrategy != "")
	for _, key := range []string{"disable_cache", "rewrite_ttl", "client_subnet"} {
		v, ok := s.Fields["domain_resolver_"+key]
		set("domain_resolver."+key, v, ok && v != nil)
	}
	return values
}

// setDialerValue applies a dialer form change to the server
// (updateDialerField).
func setDialerValue(doc *model.Doc, s *model.DNSServer, key string, value any) {
	id := jsv.Text(value)
	switch key {
	case "domain_resolver.detour":
		if jsv.Truthy(value) {
			s.DomainResolverDetour = model.Some(id)
		} else {
			s.DomainResolverDetour = model.Null()
		}
		return
	case "domain_resolver.server":
		if jsv.Truthy(value) {
			s.DomainResolver = model.Some(id)
		} else {
			s.DomainResolver = model.Null()
		}
		if !model.DNSTargetSupportsDetour(model.DNSServerByID(doc, id)) {
			s.DomainResolverDetour = model.Null()
		}
		return
	case "domain_resolver.strategy":
		if value == nil {
			s.DomainStrategy = ""
		} else {
			s.DomainStrategy = jsv.Text(value)
		}
		return
	}
	mapped := map[string]string{
		"domain_resolver.disable_cache": "domain_resolver_disable_cache",
		"domain_resolver.rewrite_ttl":   "domain_resolver_rewrite_ttl",
		"domain_resolver.client_subnet": "domain_resolver_client_subnet",
	}
	if m, ok := mapped[key]; ok {
		key = m
	}
	setField(s, key, value)
}

func setField(s *model.DNSServer, key string, value any) {
	if s.Fields == nil {
		s.Fields = model.Fields{}
	}
	s.Fields[key] = value
}

// tlsMap is the server's tls object, created when missing.
func tlsMap(s *model.DNSServer) map[string]any {
	if tls, ok := s.Fields["tls"].(map[string]any); ok {
		return tls
	}
	return map[string]any{}
}

// normalizeJSONFields parses the headers and predefined text the editor
// keeps while typing; the error names the field that is not an object.
func normalizeJSONFields(s *model.DNSServer) string {
	for _, key := range []string{"headers", "predefined"} {
		text, ok := s.Fields[key].(string)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		var parsed any
		if json.Unmarshal([]byte(text), &parsed) != nil {
			return jsonFieldError(key)
		}
		object, ok := parsed.(map[string]any)
		if !ok {
			return jsonFieldError(key)
		}
		s.Fields[key] = object
	}
	return ""
}

func jsonFieldError(key string) string {
	if key == "headers" {
		return "HTTP 请求头必须是 JSON 对象"
	}
	return "预定义 Hosts必须是 JSON 对象"
}
