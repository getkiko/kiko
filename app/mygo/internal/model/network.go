package model

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/schema"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// This file holds the shared library of network and TLS configs that
// entities reference by slot instead of storing the fields inline.

// ---- TLS configs ----

// TLSConfigField reports a TLS field that a shared TLS config holds: tls.*
// except the switch, the per-node server name, references, REALITY and
// uTLS.
func TLSConfigField(f schema.Field) bool {
	return strings.HasPrefix(f.Key, "tls.") && f.Key != "tls.enabled" && f.Key != "tls.server_name" &&
		!isReferenceType(f.Type) && !EntityTLSKey(f.Key)
}

// EntityTLSKey reports a TLS key each node keeps itself, beside its TLS
// switch and server name: REALITY's and uTLS's.
func EntityTLSKey(key string) bool {
	return strings.HasPrefix(key, "tls.reality.") || strings.HasPrefix(key, "tls.utls.")
}

func isReferenceType(t schema.FieldType) bool {
	return t == schema.Ref || t == schema.RefIn || t == schema.RefDNS
}

// ConfigKind is a library config's kind; network when unset.
func ConfigKind(cfg *NetworkConfig) string {
	if cfg.Kind == "" {
		return NetworkConfigNetwork
	}
	return cfg.Kind
}

// TLSKindForSchema is the TLS config kind an entity's schema takes: a
// server's when it has a key.
func TLSKindForSchema(fields []schema.Field) string {
	if slices.ContainsFunc(fields, func(f schema.Field) bool { return f.Key == "tls.key" }) {
		return NetworkConfigTLSServer
	}
	return NetworkConfigTLSClient
}

// TLSSchemaForKind is the form of a TLS config kind.
func TLSSchemaForKind(kind string) []schema.Field {
	if kind == NetworkConfigTLSServer {
		return schema.TLSServerSchema
	}
	return schema.TLSClientSchema
}

// FlattenTLSFields turns a nested tls object into tls.* keys.
func FlattenTLSFields(fields Fields) Fields {
	result := cloneFields(fields)
	tls, ok := fields["tls"].(map[string]any)
	if !ok {
		return result
	}
	var flatten func(value map[string]any, prefix string)
	flatten = func(value map[string]any, prefix string) {
		for k, v := range value {
			if nested, ok := v.(map[string]any); ok {
				flatten(nested, prefix+k+".")
			} else {
				result[prefix+k] = v
			}
		}
	}
	flatten(tls, "tls.")
	delete(result, "tls")
	return result
}

// NestTLSFields turns tls.* keys into a nested tls object.
func NestTLSFields(fields Fields) Fields {
	result := cloneFields(fields)
	tls := map[string]any{}
	for _, key := range sortedKeys(fields) {
		if !strings.HasPrefix(key, "tls.") {
			continue
		}
		parts := strings.Split(key[4:], ".")
		target := tls
		for _, part := range parts[:len(parts)-1] {
			next, ok := target[part].(map[string]any)
			if !ok {
				if jsv.Truthy(target[part]) {
					// `||=` keeps a truthy value; writing below it is lost.
					next = map[string]any{}
				} else {
					next = map[string]any{}
					target[part] = next
				}
			}
			target = next
		}
		target[parts[len(parts)-1]] = fields[key]
		delete(result, key)
	}
	if len(tls) > 0 {
		result["tls"] = tls
	}
	return result
}

// ---- network configs ----

// NetworkSchemaForKind is the form of a library config kind.
func NetworkSchemaForKind(kind string) []schema.Field {
	if kind == NetworkConfigNetwork {
		return schema.NetworkSchema
	}
	return TLSSchemaForKind(kind)
}

// NetworkSlotKind is the config kind a slot of a schema takes.
func NetworkSlotKind(fields []schema.Field, slot string) string {
	if slot == "tls" {
		return TLSKindForSchema(fields)
	}
	return NetworkConfigNetwork
}

var (
	dnsNetworkMu      sync.Mutex
	dnsNetworkSchemas = map[string][]schema.Field{}
)

// DNSNetworkSchema is the network fields of a DNS server type. Do not
// modify the result.
func DNSNetworkSchema(typ string) []schema.Field {
	if !IsDialerDNS(typ) {
		return nil
	}
	dnsNetworkMu.Lock()
	defer dnsNetworkMu.Unlock()
	if out, ok := dnsNetworkSchemas[typ]; ok {
		return out
	}
	out := slices.Clone(schema.DNSDialerSchema)
	if IsTLSDNS(typ) {
		out = append(out, schema.DNSTLSSchema...)
	}
	dnsNetworkSchemas[typ] = out
	return out
}

// NetworkKeys are the dialer fields a network config holds.
var NetworkKeys = map[string]bool{
	"bind_interface": true, "inet4_bind_address": true, "inet6_bind_address": true, "bind_address_no_port": true,
	"routing_mark": true, "reuse_addr": true, "netns": true, "protect_path": true, "connect_timeout": true,
	"tcp_fast_open": true, "tcp_multi_path": true, "disable_tcp_keep_alive": true, "tcp_keep_alive": true,
	"tcp_keep_alive_interval": true, "udp_fragment": true, "udp_timeout": true, "network_strategy": true,
	"network_type": true, "fallback_network_type": true, "fallback_delay": true,
}

// NetworkField returns the slot and config key a field belongs to: "tls",
// "default", or a nested dialer such as "control_dialer".
func NetworkField(f schema.Field) (slot, key string, ok bool) {
	if isReferenceType(f.Type) {
		return "", "", false
	}
	if TLSConfigField(f) {
		return "tls", f.Key, true
	}
	parts := strings.Split(f.Key, ".")
	slot, key = "default", f.Key
	if len(parts) == 2 && strings.HasSuffix(parts[0], "_dialer") {
		slot, key = parts[0], parts[1]
	}
	if !NetworkKeys[key] {
		return "", "", false
	}
	return slot, key, true
}

func networkSlotOf(f schema.Field) string {
	slot, _, _ := NetworkField(f)
	return slot
}

type slotsKey struct {
	first *schema.Field
	n     int
}

var (
	slotsMu    sync.Mutex
	slotsCache = map[slotsKey][]string{}
)

// NetworkSlots lists the slots of a schema, in order.
func NetworkSlots(fields []schema.Field) []string {
	if len(fields) == 0 {
		return nil
	}
	key := slotsKey{&fields[0], len(fields)}
	slotsMu.Lock()
	cached, ok := slotsCache[key]
	slotsMu.Unlock()
	if ok {
		return cached
	}
	var out []string
	for _, f := range fields {
		if slot, _, ok := NetworkField(f); ok && !slices.Contains(out, slot) {
			out = append(out, slot)
		}
	}
	slotsMu.Lock()
	if len(slotsCache) > 4096 {
		slotsCache = map[slotsKey][]string{}
	}
	slotsCache[key] = out
	slotsMu.Unlock()
	return out
}

// NetworkSlotLabel names a slot.
func NetworkSlotLabel(slot string) string {
	switch slot {
	case "default":
		return "通用网络配置"
	case "control_dialer":
		return "控制连接配置"
	case "tunnel_dialer":
		return "隧道连接配置"
	case "tls":
		return "高级 TLS 配置"
	}
	return slot
}

// NetworkLabel names a config field.
func NetworkLabel(key string) string {
	for _, list := range [][]schema.Field{schema.NetworkSchema, schema.TLSClientSchema, schema.TLSServerSchema} {
		for _, f := range list {
			if f.Key == key && f.Label != "" {
				return f.Label
			}
		}
	}
	return key
}

var reSecretKey = regexp.MustCompile(`certificate|(?:^|[._])key(?:[._]|$)|private_key|public_key|\.ech\.(?:config|key)`)

// NetworkValueSummary shows a config value, hiding secrets.
func NetworkValueSummary(key string, value any) string {
	if reSecretKey.MatchString(key) {
		return "已配置"
	}
	if list, ok := jsv.Array(value); ok {
		parts := make([]string, len(list))
		for i, v := range list {
			if v != nil {
				parts[i] = jsv.String(v)
			}
		}
		return strings.Join(parts, ", ")
	}
	switch value {
	case true:
		return "开启"
	case false:
		return "关闭"
	}
	return jsv.String(value)
}

// NetworkSummary describes a config's values.
func NetworkSummary(cfg *NetworkConfig) string {
	if cfg == nil {
		return "核心默认"
	}
	var parts []string
	for _, k := range sortedKeys(cfg.Fields) {
		v := cfg.Fields[k]
		if v == nil || v == "" {
			continue
		}
		parts = append(parts, NetworkLabel(k)+" "+NetworkValueSummary(k, v))
	}
	if len(parts) == 0 {
		return "核心默认"
	}
	return strings.Join(parts, " · ")
}

var reDuration = regexp.MustCompile(`^(?:\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h))+$`)

// ValidateNetworkConfig returns why a library config is invalid, "" when
// it is valid.
func ValidateNetworkConfig(cfg *NetworkConfig) string {
	if cfg == nil || cfg.ID == "" || cfg.Fields == nil {
		return "配置格式无效"
	}
	if strings.TrimSpace(cfg.Name) == "" {
		return "请填写配置名称"
	}
	if cfg.Kind != "" && !slices.Contains([]string{NetworkConfigNetwork, NetworkConfigTLSClient, NetworkConfigTLSServer}, cfg.Kind) {
		return "配置类型无效"
	}
	table := NetworkSchemaForKind(ConfigKind(cfg))
	for _, key := range sortedKeys(cfg.Fields) {
		value := cfg.Fields[key]
		i := slices.IndexFunc(table, func(f schema.Field) bool { return f.Key == key })
		if i < 0 {
			return "字段 " + key + " 属于项目独立配置"
		}
		f := table[i]
		if value == nil || value == "" {
			continue
		}
		if (key == "inet4_bind_address" || key == "inet6_bind_address") && schema.ItemSpecs[schema.ItemIP].Validate(jsv.String(value)) != "" {
			return f.Label + "请填写有效 IP 地址"
		}
		_, isString := value.(string)
		_, isBool := value.(bool)
		switch f.Type {
		case schema.Text, schema.TextArea:
			if !isString {
				return f.Label + "请填写文本"
			}
		case schema.Bool:
			if !isBool {
				return f.Label + "需要布尔值"
			}
		case schema.Duration:
			if !isString || !reDuration.MatchString(value.(string)) {
				return f.Label + "请填写有效时长，例如 5s"
			}
		case schema.MultiSelect:
			list, ok := jsv.Array(value)
			if !ok || slices.ContainsFunc(list, func(v any) bool {
				s, isStr := v.(string)
				return !isStr || !f.AllowCustom && !slices.Contains(f.Options, s)
			}) {
				return f.Label + "请使用有效选项"
			}
		case schema.Select:
			if !slices.Contains(f.Options, jsv.String(value)) {
				return f.Label + "请使用有效选项"
			}
		case schema.List:
			list, ok := jsv.Array(value)
			if !ok || slices.ContainsFunc(list, func(v any) bool { _, s := v.(string); return !s }) {
				return f.Label + "请填写文本列表"
			}
		case schema.Number:
			n, ok := jsv.Float(value)
			if !ok || !jsv.IsFinite(n) || f.Min != nil && n < *f.Min || f.Max != nil && n > *f.Max {
				return f.Label + "数值超出范围"
			}
		}
	}
	if ConfigKind(cfg) != NetworkConfigNetwork {
		for _, f := range table {
			if !schema.FieldVisible(f, table, cfg.Fields) {
				continue
			}
			value := cfg.Fields[f.Key]
			list, isList := jsv.Array(value)
			if f.Required && (value == nil || value == "" || isList && len(list) == 0) {
				return "请填写" + f.Label
			}
			if f.Validate != nil {
				if problem := f.Validate(jsv.StringOr(value, ""), cfg.Fields); problem != "" {
					return problem
				}
			}
		}
		minVersion, maxVersion := cfg.Fields["tls.min_version"], cfg.Fields["tls.max_version"]
		if jsv.Truthy(minVersion) && jsv.Truthy(maxVersion) && jsv.Number(minVersion) > jsv.Number(maxVersion) {
			return "最低 TLS 版本应小于或等于最高版本"
		}
	}
	return ""
}

func findConfig(library []*NetworkConfig, id string) *NetworkConfig {
	for _, c := range library {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// EffectiveNetworkFields returns an entity's fields with its referenced
// configs' values in place of the referenced slots. platform, when set,
// drops fields the platform does not support.
func EffectiveNetworkFields(table []schema.Field, fields Fields, refs NetworkConfigRefs, library []*NetworkConfig, platform string) Fields {
	result := FlattenTLSFields(fields)
	if len(refs) == 0 {
		return result
	}
	for _, f := range table {
		slot, key, ok := NetworkField(f)
		if !ok || refs[slot] == "" {
			continue
		}
		delete(result, f.Key)
		cfg := findConfig(library, refs[slot])
		if cfg == nil || ConfigKind(cfg) != NetworkSlotKind(table, slot) {
			continue
		}
		if platform != "" && !schema.FieldAvailable(f, platform) {
			continue
		}
		if slot == "tls" {
			tlsTable := TLSSchemaForKind(ConfigKind(cfg))
			i := slices.IndexFunc(tlsTable, func(t schema.Field) bool { return t.Key == f.Key })
			if i < 0 || !schema.FieldVisible(tlsTable[i], tlsTable, cfg.Fields) {
				continue
			}
		}
		if value, ok := cfg.Fields[key]; ok {
			result[f.Key] = value
		}
	}
	return result
}

// NetworkReferenceErrors returns the problems of an entity's references:
// missing configs, kind mismatches and invalid configs.
func NetworkReferenceErrors(table []schema.Field, refs NetworkConfigRefs, library []*NetworkConfig, label string) []string {
	var out []string
	for _, slot := range NetworkSlots(table) {
		id := refs[slot]
		if id == "" {
			continue
		}
		cfg := findConfig(library, id)
		switch {
		case cfg == nil:
			out = append(out, label+"的"+NetworkSlotLabel(slot)+"已缺失，请重新选择")
		case ConfigKind(cfg) != NetworkSlotKind(table, slot):
			out = append(out, label+"的"+NetworkSlotLabel(slot)+"类型不匹配，请重新选择")
		default:
			if problem := ValidateNetworkConfig(cfg); problem != "" {
				out = append(out, label+"引用的「"+cfg.Name+"」："+problem)
			}
		}
	}
	return out
}

// networkEntity is an entity that may use library configs.
type networkEntity struct {
	id, tag string
	enabled bool
	fields  *Fields
	refs    *NetworkConfigRefs
	schema  []schema.Field
	kind    string // inbound, outbound, endpoint, service or DNS, in Chinese
	present bool   // fields was set (DNS servers may have none)
}

func entityOf(e *Entity, fields *Fields, table []schema.Field, kind string) networkEntity {
	return networkEntity{id: e.ID, tag: e.Tag, enabled: e.Enabled, fields: fields, refs: &e.NetworkConfigRefs, schema: table, kind: kind, present: *fields != nil}
}

func networkEntities(doc *Doc) []networkEntity {
	var out []networkEntity
	for _, in := range doc.Inbounds {
		out = append(out, entityOf(&in.Entity, &in.Fields, schema.InboundSchema[in.Type], "入站"))
	}
	for _, o := range doc.Outbounds {
		out = append(out, entityOf(&o.Entity, &o.Fields, schema.OutboundSchema[o.Type], "出站"))
	}
	for _, e := range doc.Endpoints {
		out = append(out, entityOf(&e.Entity, &e.Fields, schema.EndpointSchema[e.Type], "端点"))
	}
	for _, s := range doc.Services {
		out = append(out, entityOf(&s.Entity, &s.Fields, schema.ServiceSchema[s.Type], "服务"))
	}
	for _, s := range doc.DNS.Servers {
		if slices.Contains([]string{"hosts", "fakeip", "tailscale"}, s.Type) {
			continue
		}
		out = append(out, networkEntity{id: s.ID, tag: s.Tag, enabled: s.Enabled, fields: &s.Fields, refs: &s.NetworkConfigRefs,
			schema: DNSNetworkSchema(s.Type), kind: "DNS", present: s.Fields != nil})
	}
	return out
}

func canonicalFields(fields Fields) string {
	raw, _ := json.Marshal(fields)
	return string(raw)
}

func uniqueNetworkName(base string, library []*NetworkConfig) string {
	trimmed := strings.TrimSpace(base)
	name := trimmed
	taken := func(n string) bool {
		return slices.ContainsFunc(library, func(c *NetworkConfig) bool { return c.Name == n })
	}
	for index := 2; taken(name); index++ {
		name = trimmed + " " + itoa(index)
	}
	return name
}

// DefaultNetworkProfileName names configs migrated from the document.
const DefaultNetworkProfileName = "当前配置"

// MigrateNetworkDoc moves inline network and TLS fields of a document into
// the library, reusing identical configs, and leaves references in their
// place. name prefixes the names of new configs. It does not modify its
// inputs.
func MigrateNetworkDoc(input *Doc, initial []*NetworkConfig, name string) (*Doc, []*NetworkConfig) {
	doc := Clone(input)
	library := slices.Clone(initial)
	restoreEntityTLS(doc, library)
	for _, entity := range networkEntities(doc) {
		var source Fields
		if entity.present {
			source = *entity.fields
		}
		fields := FlattenTLSFields(source)
		for _, slot := range NetworkSlots(entity.schema) {
			extracted := Fields{}
			for _, f := range entity.schema {
				s, key, ok := NetworkField(f)
				if !ok || s != slot {
					continue
				}
				if v := fields[f.Key]; v != nil && v != "" {
					extracted[key] = v
				}
			}
			if len(extracted) == 0 {
				continue
			}
			if (*entity.refs)[slot] == "" {
				kind := NetworkSlotKind(entity.schema, slot)
				var cfg *NetworkConfig
				for _, c := range library {
					if ConfigKind(c) == kind && canonicalFields(c.Fields) == canonicalFields(extracted) {
						cfg = c
						break
					}
				}
				if cfg == nil {
					label := entity.tag
					if label == "" {
						label = entity.kind
					}
					base := name + " · " + label
					if slot != "default" {
						base += " · " + NetworkSlotLabel(slot)
					}
					cfg = &NetworkConfig{ID: NewID("net"), Name: uniqueNetworkName(base, library), Fields: extracted, UpdatedAt: time.Now().UnixMilli()}
					if slot == "tls" {
						cfg.Kind = kind
					}
					library = append(library, cfg)
				}
				refs := NetworkConfigRefs{}
				for k, v := range *entity.refs {
					refs[k] = v
				}
				refs[slot] = cfg.ID
				*entity.refs = refs
			}
			for _, f := range entity.schema {
				if networkSlotOf(f) == slot {
					delete(fields, f.Key)
				}
			}
		}
		if entity.present {
			if entity.kind == "DNS" {
				*entity.fields = NestTLSFields(fields)
			} else {
				*entity.fields = fields
			}
		}
	}
	return doc, library
}

// MigrateNetworkStore migrates the document and every profile, sharing one
// library. The active profile's copy is the document.
func MigrateNetworkStore(doc *Doc, profiles []*Profile, initial []*NetworkConfig, activeID string) (*Doc, []*Profile, []*NetworkConfig) {
	library := slices.Clone(initial)
	migrated := make([]*Profile, len(profiles))
	var current *Doc
	for i, p := range profiles {
		source := p.Doc
		if p.ID == activeID {
			source = doc
		}
		next, lib := MigrateNetworkDoc(source, library, p.Name)
		library = lib
		copied := *p
		copied.Doc = next
		migrated[i] = &copied
		if p.ID == activeID && current == nil {
			current = next
		}
	}
	if current != nil {
		return current, migrated, stripEntityTLS(library)
	}
	next, lib := MigrateNetworkDoc(doc, library, DefaultNetworkProfileName)
	return next, migrated, stripEntityTLS(lib)
}

// restoreEntityTLS gives doc's entities back the REALITY and uTLS values
// their TLS configs held for them, which each node now keeps itself
// (EntityTLSKey); a value an entity has of its own wins. A reference to a
// config that held nothing else goes, as stripEntityTLS drops the config.
func restoreEntityTLS(doc *Doc, library []*NetworkConfig) {
	for _, entity := range networkEntities(doc) {
		id := (*entity.refs)["tls"]
		cfg := findConfig(library, id)
		if id == "" || cfg == nil {
			continue
		}
		moved, rest := Fields{}, 0
		for k, v := range cfg.Fields {
			switch {
			case EntityTLSKey(k):
				moved[k] = v
			case v != nil && v != "":
				rest++
			}
		}
		if len(moved) == 0 {
			continue
		}
		var source Fields
		if entity.present {
			source = *entity.fields
		}
		fields := FlattenTLSFields(source)
		for k, v := range moved {
			if own := fields[k]; (own == nil || own == "") && slices.ContainsFunc(entity.schema, func(f schema.Field) bool { return f.Key == k }) {
				fields[k] = v
			}
		}
		if entity.kind == "DNS" {
			*entity.fields = NestTLSFields(fields)
		} else {
			*entity.fields = fields
		}
		if rest == 0 {
			refs := NetworkConfigRefs{}
			for k, v := range *entity.refs {
				if k != "tls" {
					refs[k] = v
				}
			}
			*entity.refs = refs
		}
	}
}

// stripEntityTLS takes the REALITY and uTLS values out of the TLS configs
// of library, once restoreEntityTLS gave them to the entities, and drops
// the configs that held nothing else.
func stripEntityTLS(library []*NetworkConfig) []*NetworkConfig {
	if !slices.ContainsFunc(library, func(cfg *NetworkConfig) bool {
		return ConfigKind(cfg) != NetworkConfigNetwork && slices.ContainsFunc(sortedKeys(cfg.Fields), EntityTLSKey)
	}) {
		return library
	}
	out := make([]*NetworkConfig, 0, len(library))
	for _, cfg := range library {
		if ConfigKind(cfg) == NetworkConfigNetwork || !slices.ContainsFunc(sortedKeys(cfg.Fields), EntityTLSKey) {
			out = append(out, cfg)
			continue
		}
		fields, rest := Fields{}, 0
		for k, v := range cfg.Fields {
			if EntityTLSKey(k) {
				continue
			}
			fields[k] = v
			if v != nil && v != "" {
				rest++
			}
		}
		if rest == 0 {
			continue
		}
		next := *cfg
		next.Fields, next.UpdatedAt = fields, time.Now().UnixMilli()
		out = append(out, &next)
	}
	return out
}

// NetworkUse is a reference to a library config.
type NetworkUse struct {
	ProfileID string
	EntityID  string
	Label     string // profile / kind / tag / slot
}

// NetworkUses lists where a library config is used across profiles.
func NetworkUses(profiles []*Profile, id string) []NetworkUse {
	var out []NetworkUse
	for _, p := range profiles {
		if p.Doc == nil {
			continue
		}
		for _, entity := range networkEntities(p.Doc) {
			for _, slot := range sortedKeys(*entity.refs) {
				if (*entity.refs)[slot] == id {
					out = append(out, NetworkUse{ProfileID: p.ID, EntityID: entity.id,
						Label: p.Name + " / " + entity.kind + " / " + entity.tag + " / " + NetworkSlotLabel(slot)})
				}
			}
		}
	}
	return out
}

// NetworkUsage lists the labels of where a library config is used.
func NetworkUsage(profiles []*Profile, id string) []string {
	var out []string
	for _, use := range NetworkUses(profiles, id) {
		out = append(out, use.Label)
	}
	return out
}

// RemapNetworkRefs returns doc with library ids replaced by ids.
func RemapNetworkRefs(doc *Doc, ids map[string]string) *Doc {
	next := Clone(doc)
	for _, entity := range networkEntities(next) {
		for slot, id := range *entity.refs {
			if to := ids[id]; to != "" {
				(*entity.refs)[slot] = to
			}
		}
	}
	return next
}

// ImportNetworkBundle adds imported configs to the library: identical ones
// are shared, colliding ids get new ids and the profiles are remapped.
func ImportNetworkBundle(profiles []*Profile, incoming, existing []*NetworkConfig) ([]*Profile, []*NetworkConfig, error) {
	library := slices.Clone(existing)
	ids := map[string]string{}
	for _, c := range incoming {
		// A bundle from before REALITY and uTLS left the TLS configs
		// passes; the store's migration gives them to the nodes.
		check := Clone(c)
		for k := range check.Fields {
			if EntityTLSKey(k) {
				delete(check.Fields, k)
			}
		}
		if problem := ValidateNetworkConfig(check); problem != "" {
			return nil, nil, errors.New(problem)
		}
		collision := findConfig(library, c.ID)
		switch {
		case collision != nil && (ConfigKind(collision) != ConfigKind(c) || canonicalFields(collision.Fields) != canonicalFields(c.Fields)):
			next := Clone(c)
			next.ID, next.Name, next.UpdatedAt = NewID("net"), uniqueNetworkName(c.Name, library), time.Now().UnixMilli()
			library = append(library, next)
			ids[c.ID] = next.ID
		case collision == nil:
			next := Clone(c)
			next.Name = uniqueNetworkName(c.Name, library)
			library = append(library, next)
		}
	}
	out := make([]*Profile, len(profiles))
	for i, p := range profiles {
		copied := *p
		copied.Doc = RemapNetworkRefs(p.Doc, ids)
		out[i] = &copied
	}
	return out, library, nil
}

// ExpandNetworkDoc returns a transient copy of doc with referenced configs
// expanded inline, as schema consumers (the emitter) read them, and the
// reference problems of enabled entities. Stored entities keep only the
// references.
func ExpandNetworkDoc(input *Doc, library []*NetworkConfig) (*Doc, []string) {
	doc := Clone(input)
	errs := []string{}
	enabledSubscriptions := map[string]bool{}
	for _, s := range doc.Subscriptions {
		if s.Enabled {
			enabledSubscriptions[s.ID] = true
		}
	}
	for _, entity := range networkEntities(doc) {
		var outbound *Outbound
		for _, o := range doc.Outbounds {
			if o.ID == entity.id {
				outbound = o
				break
			}
		}
		enabled := entity.enabled && (outbound == nil || outbound.SubID == "" || enabledSubscriptions[outbound.SubID])
		if enabled {
			errs = append(errs, NetworkReferenceErrors(entity.schema, *entity.refs, library, entity.kind+"「"+entity.tag+"」")...)
		}
		var source Fields
		if entity.present {
			source = *entity.fields
		}
		fields := EffectiveNetworkFields(entity.schema, source, *entity.refs, library, doc.Platform)
		if entity.kind == "DNS" {
			*entity.fields = NestTLSFields(fields)
		} else {
			*entity.fields = fields
		}
	}
	return doc, errs
}

// RebaseNetworkConfigs merges a library changed remotely while an editor
// was open: the remote library plus the configs added locally since base.
func RebaseNetworkConfigs(base, local, remote []*NetworkConfig) []*NetworkConfig {
	baseline := map[string]bool{}
	for _, c := range base {
		baseline[c.ID] = true
	}
	out := slices.Clone(remote)
	for _, c := range local {
		if !baseline[c.ID] && findConfig(remote, c.ID) == nil {
			out = append(out, c)
		}
	}
	if out == nil {
		out = []*NetworkConfig{}
	}
	return out
}
