package profiles

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

type jsonObject = map[string]any

// objects returns the objects of a JSON array, skipping other values.
func objects(value any) []jsonObject {
	list, _ := value.([]any)
	var out []jsonObject
	for _, item := range list {
		if o, ok := item.(jsonObject); ok {
			out = append(out, o)
		}
	}
	return out
}

func asObject(value any) jsonObject {
	o, _ := value.(jsonObject)
	if o == nil {
		return jsonObject{}
	}
	return o
}

func cloneJSON(value any) any {
	raw, _ := json.Marshal(value)
	var out any
	_ = json.Unmarshal(raw, &out)
	return out
}

// valueAt reads a dotted path of nested objects.
func valueAt(o jsonObject, path string) (any, bool) {
	var current any = o
	for _, key := range strings.Split(path, ".") {
		m, ok := current.(jsonObject)
		if !ok {
			return nil, false
		}
		if current, ok = m[key]; !ok {
			return nil, false
		}
	}
	return current, true
}

// fieldsFromSchema takes the schema's fields from a sing-box object,
// keyed by their dotted keys.
func fieldsFromSchema(source jsonObject, fields []schema.Field) model.Fields {
	// sing-box also writes a dial domain resolver as just the server's tag
	// ("domain_resolver": "dns-local"); valueAt would not reach
	// domain_resolver.server through the string.
	if tag, ok := source["domain_resolver"].(string); ok {
		source = maps.Clone(source)
		source["domain_resolver"] = jsonObject{"server": tag}
	}
	out := model.Fields{}
	for _, f := range fields {
		if value, ok := valueAt(source, f.Key); ok {
			out[f.Key] = cloneJSON(value)
		}
	}
	return out
}

// flattenFields keys nested objects' values by dotted paths, keeping the
// top-level headers and predefined maps whole.
func flattenFields(source jsonObject, excluded []string) model.Fields {
	out := model.Fields{}
	var visit func(value jsonObject, prefix string)
	visit = func(value jsonObject, prefix string) {
		for key, child := range value {
			if prefix == "" && slices.Contains(excluded, key) {
				continue
			}
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if o, ok := child.(jsonObject); ok && !(prefix == "" && (key == "headers" || key == "predefined")) {
				visit(o, path)
			} else {
				out[path] = cloneJSON(child)
			}
		}
	}
	visit(source, "")
	return out
}

func requiredArray(config jsonObject, key string) ([]jsonObject, error) {
	list, ok := config[key].([]any)
	if !ok {
		return nil, fmt.Errorf("配置缺少有效的 %s 数组", key)
	}
	out := make([]jsonObject, 0, len(list))
	for _, item := range list {
		o, ok := item.(jsonObject)
		if !ok {
			return nil, fmt.Errorf("%s 数组包含无效项目", key)
		}
		out = append(out, o)
	}
	return out, nil
}

// uniqueTag returns the trimmed tag, or fallback, numbered past the tags
// already used.
func uniqueTag(tag any, fallback string, used map[string]bool) string {
	base := fallback
	if s, ok := tag.(string); ok && strings.TrimSpace(s) != "" {
		base = strings.TrimSpace(s)
	}
	result := base
	for index := 2; used[result]; index++ {
		result = fmt.Sprintf("%s %d", base, index)
	}
	used[result] = true
	return result
}

// tagKey is the key a raw entity's references name it by: its tag as
// written, or the tag it was given.
func tagKey(raw jsonObject, given string) string {
	switch tag := raw["tag"].(type) {
	case nil:
		return given
	case string:
		return tag
	default:
		return fmt.Sprint(tag)
	}
}

func typeOf(raw jsonObject) string {
	s, _ := raw["type"].(string)
	return s
}

func orUnnamed(s string) string {
	if s == "" {
		return "未命名"
	}
	return s
}

// conditionTree turns a logical rule into an editable condition group.
func conditionTree(value any) *model.Condition {
	o, ok := value.(jsonObject)
	if !ok || o["type"] != "logical" {
		return nil
	}
	rules, ok := o["rules"].([]any)
	if !ok {
		return nil
	}
	group := &model.Condition{Kind: model.ConditionGroup, Mode: "and", Rules: []*model.Condition{}, Invert: o["invert"] == true}
	if o["mode"] == "or" {
		group.Mode = "or"
	}
	for _, rule := range rules {
		if nested := conditionTree(rule); nested != nil {
			group.Rules = append(group.Rules, nested)
			continue
		}
		r, ok := rule.(jsonObject)
		if !ok {
			continue
		}
		match := maps.Clone(r)
		for _, key := range []string{"type", "mode", "rules", "invert"} {
			delete(match, key)
		}
		group.Rules = append(group.Rules, &model.Condition{Kind: model.ConditionMatch, Match: match, Invert: r["invert"] == true})
	}
	return group
}

// requiredReference maps a tag to its id: "" for none, an error for a tag
// that names nothing.
func requiredReference(value any, tags map[string]string, context string) (string, error) {
	if value == nil || value == "" {
		return "", nil
	}
	s, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s引用格式无效", context)
	}
	id, ok := tags[s]
	if !ok {
		return "", fmt.Errorf("%s引用的「%s」不存在", context, s)
	}
	return id, nil
}

func refOf(id string, err error) (model.Ref, error) { return model.RefOf(id), err }

func remapMatch(match jsonObject, preferred, inbound map[string]string, context string) (jsonObject, error) {
	result := maps.Clone(match)
	for _, pair := range []struct {
		key  string
		tags map[string]string
	}{{"preferred_by", preferred}, {"inbound", inbound}} {
		value, ok := result[pair.key]
		if !ok {
			continue
		}
		values, isList := value.([]any)
		if !isList {
			values = []any{value}
		}
		mapped := make([]any, len(values))
		for i, v := range values {
			id, err := requiredReference(v, pair.tags, context+" "+pair.key)
			if err != nil {
				return nil, err
			}
			if id != "" {
				mapped[i] = id
			}
		}
		result[pair.key] = mapped
	}
	return result, nil
}

func remapTree(tree *model.Condition, preferred, inbound map[string]string, context string) (*model.Condition, error) {
	out := *tree
	out.Rules = make([]*model.Condition, len(tree.Rules))
	for i, rule := range tree.Rules {
		if rule.Kind == model.ConditionGroup {
			nested, err := remapTree(rule, preferred, inbound, context)
			if err != nil {
				return nil, err
			}
			out.Rules[i] = nested
			continue
		}
		match, err := remapMatch(rule.Match, preferred, inbound, context)
		if err != nil {
			return nil, err
		}
		leaf := *rule
		leaf.Match = match
		out.Rules[i] = &leaf
	}
	return &out, nil
}

func mergeTags(sets ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, set := range sets {
		maps.Copy(out, set)
	}
	return out
}

// makeImportDoc is the document an import starts from.
func makeImportDoc(platform string) *model.Doc {
	direct := model.MakeOutbound("direct", "直连")
	mixed := model.MakeInbound("mixed", "mixed-in", platform, false)
	mixed.Fields["listen"] = "0.0.0.0"
	mixed.Fields["listen_port"] = float64(7890)
	localDNS := &model.DNSServer{ID: model.NewID("dns"), Tag: "dns-local", Type: "local", Enabled: true}
	cnDNS := &model.DNSServer{ID: model.NewID("dns"), Tag: "dns-cn", Type: "https", Enabled: true,
		Server: "223.5.5.5", Path: "/dns-query", Fields: model.Fields{"tls": map[string]any{"server_name": "dns.alidns.com"}}}
	findProcess := true
	var dnsGroups []*model.RouteGroup
	for _, device := range []string{"pre", "local", "other"} {
		dnsGroups = append(dnsGroups, &model.RouteGroup{ID: model.NewID("dg"), Name: "默认分组", DeviceID: device})
	}
	return &model.Doc{
		Version: 1, Platform: platform,
		Log:      model.Log{Level: "info", Timestamp: true},
		Inbounds: []*model.Inbound{mixed}, Outbounds: []*model.Outbound{direct},
		Endpoints: []*model.Endpoint{}, Services: []*model.Service{}, Subscriptions: []*model.Subscription{},
		Route: model.Route{
			Groups: []*model.RouteGroup{
				{ID: model.NewID("rg"), Name: "默认分组", DeviceID: "pre"},
				{ID: model.NewID("rg"), Name: "默认分组"},
				{ID: model.NewID("rg"), Name: "默认分组", DeviceID: "other"},
			},
			Rules: []*model.RouteRule{}, RuleSets: []*model.RuleSet{},
			Final: model.RefOf(direct.ID), LocalFinal: model.RefOf(direct.ID),
			AutoDetectInterface: true, FindProcess: &findProcess,
			DefaultDomainResolver: model.RefOf(cnDNS.ID),
		},
		DNS: model.DNS{
			DevicePolicyVersion: 1, RoutingVersion: 1, Groups: dnsGroups,
			DeviceFinals: map[string]model.Ref{"local": model.RefOf(cnDNS.ID)},
			Servers:      []*model.DNSServer{localDNS, cnDNS}, Rules: []*model.DNSRule{},
			Final: model.RefOf(cnDNS.ID), Strategy: "prefer_ipv4", CacheCapacity: 2048,
		},
		Experimental: model.Experimental{ClashAPI: true, ExternalController: "127.0.0.1:9090", DefaultMode: "Rule", CacheFile: true},
	}
}

// routeActions are the route rule actions the editor represents.
var routeActions = []string{"route", "reject", "hijack-dns", "sniff", "resolve"}

// ImportSingBoxConfig turns a sing-box configuration into a document for
// platform: entities keep the fields their schema knows, references
// become ids, and rules apply to every device in their order.
func ImportSingBoxConfig(text []byte, platform string) (*model.Doc, error) {
	if platform == "" {
		platform = model.PlatformLinux
	}
	var parsed any
	if err := json.Unmarshal(text, &parsed); err != nil {
		return nil, errors.New("下载内容不是有效 JSON")
	}
	config, ok := parsed.(jsonObject)
	if !ok {
		return nil, errors.New("下载内容必须是 JSON 对象")
	}
	rawOutbounds, err := requiredArray(config, "outbounds")
	if err != nil {
		return nil, err
	}
	if len(rawOutbounds) == 0 {
		return nil, errors.New("配置没有有效的出站")
	}
	doc := makeImportDoc(platform)
	// The document's IPv6 switch follows the configuration: with it off,
	// FillDefaults would move IPv6 listeners and strategies to IPv4.
	doc.IPv6Enabled = configUsesIPv6(config)
	localEnabled := false
	doc.Route.LocalEnabled = &localEnabled
	doc.Route.LocalFinal = model.Null()
	doc.DNS.DeviceFinals = map[string]model.Ref{}

	outboundTags, inboundTags, endpointTags := map[string]string{}, map[string]string{}, map[string]string{}
	usedOutbounds, usedInbounds, usedEndpoints, usedServices := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}

	doc.Outbounds = nil
	defaults := map[string]any{}
	for index, raw := range rawOutbounds {
		typ := typeOf(raw)
		fieldsOf, known := schema.OutboundSchema[typ]
		if !known {
			return nil, fmt.Errorf("配置包含暂不支持的出站类型「%s」", orUnnamed(typ))
		}
		fallback := fmt.Sprintf("%s-%d", typ, index+1)
		if typ == "" {
			fallback = fmt.Sprintf("outbound-%d", index+1)
		}
		item := model.MakeOutbound(typ, uniqueTag(raw["tag"], fallback, usedOutbounds))
		fields := fieldsFromSchema(raw, fieldsOf)
		if model.IsGroup(typ) {
			if value, ok := raw["default"]; ok {
				defaults[item.ID] = value
			}
			item.Members = []string{}
			for _, member := range asList(raw["outbounds"]) {
				if s, ok := member.(string); ok {
					item.Members = append(item.Members, s)
				}
			}
		}
		item.Fields = fields
		outboundTags[tagKey(raw, item.Tag)] = item.ID
		doc.Outbounds = append(doc.Outbounds, item)
	}
	for _, outbound := range doc.Outbounds {
		if outbound.Members != nil {
			members := []string{}
			for _, member := range outbound.Members {
				id, err := requiredReference(member, outboundTags, fmt.Sprintf("出站「%s」成员", outbound.Tag))
				if err != nil {
					return nil, err
				}
				if id != "" {
					members = append(members, id)
				}
			}
			outbound.Members = members
		}
		if value, ok := defaults[outbound.ID]; ok {
			id, err := requiredReference(value, outboundTags, fmt.Sprintf("出站「%s」默认成员", outbound.Tag))
			if err != nil {
				return nil, err
			}
			outbound.Fields["default_ref"] = nullable(id)
		}
	}

	doc.Inbounds = []*model.Inbound{}
	for index, raw := range objects(config["inbounds"]) {
		typ := typeOf(raw)
		fieldsOf, known := schema.InboundSchema[typ]
		if !known {
			return nil, fmt.Errorf("配置包含暂不支持的入站类型「%s」", orUnnamed(typ))
		}
		item := model.MakeInbound(typ, uniqueTag(raw["tag"], fallbackTag(typ, "inbound", index), usedInbounds), platform, doc.IPv6Enabled)
		item.Fields = fieldsFromSchema(raw, fieldsOf)
		inboundTags[tagKey(raw, item.Tag)] = item.ID
		doc.Inbounds = append(doc.Inbounds, item)
	}
	doc.Endpoints = []*model.Endpoint{}
	for index, raw := range objects(config["endpoints"]) {
		typ := typeOf(raw)
		fieldsOf, known := schema.EndpointSchema[typ]
		if !known {
			return nil, fmt.Errorf("配置包含暂不支持的端点类型「%s」", orUnnamed(typ))
		}
		if model.CanAddEndpoint(doc, typ) != "" {
			return nil, fmt.Errorf("配置包含多个 Tailscale 端点，只能导入一个")
		}
		item := model.MakeEndpoint(typ, uniqueTag(raw["tag"], fallbackTag(typ, "endpoint", index), usedEndpoints), doc.IPv6Enabled)
		item.Fields = fieldsFromSchema(raw, fieldsOf)
		endpointTags[tagKey(raw, item.Tag)] = item.ID
		doc.Endpoints = append(doc.Endpoints, item)
	}
	doc.Services = []*model.Service{}
	for index, raw := range objects(config["services"]) {
		typ := typeOf(raw)
		fieldsOf, known := schema.ServiceSchema[typ]
		if !known {
			return nil, fmt.Errorf("配置包含暂不支持的服务类型「%s」", orUnnamed(typ))
		}
		item := model.MakeService(typ, uniqueTag(raw["tag"], fallbackTag(typ, "service", index), usedServices))
		item.Fields = fieldsFromSchema(raw, fieldsOf)
		doc.Services = append(doc.Services, item)
	}

	allTags := mergeTags(outboundTags, endpointTags)
	remapDetour := func(fields model.Fields, tags map[string]string, context string) error {
		value, ok := fields["detour"]
		if !ok {
			return nil
		}
		id, err := requiredReference(value, tags, context)
		fields["detour"] = nullable(id)
		return err
	}
	for _, o := range doc.Outbounds {
		if err := remapDetour(o.Fields, allTags, fmt.Sprintf("出站「%s」前置代理", o.Tag)); err != nil {
			return nil, err
		}
	}
	for _, in := range doc.Inbounds {
		if err := remapDetour(in.Fields, inboundTags, fmt.Sprintf("入站「%s」转发目标", in.Tag)); err != nil {
			return nil, err
		}
	}
	for _, e := range doc.Endpoints {
		if err := remapDetour(e.Fields, allTags, fmt.Sprintf("端点「%s」前置代理", e.Tag)); err != nil {
			return nil, err
		}
	}
	for _, s := range doc.Services {
		if err := remapDetour(s.Fields, allTags, fmt.Sprintf("服务「%s」前置代理", s.Tag)); err != nil {
			return nil, err
		}
	}

	route := asObject(config["route"])
	inboundLike := mergeTags(inboundTags, endpointTags)
	rules := []*model.RouteRule{}
	for index, raw := range objects(route["rules"]) {
		context := fmt.Sprintf("路由规则 %d", index+1)
		match := maps.Clone(raw)
		action, _ := match["action"].(string)
		outbound, _ := match["outbound"].(string)
		delete(match, "action")
		delete(match, "outbound")
		tree := conditionTree(match)
		if action != "" && !slices.Contains(routeActions, action) {
			return nil, fmt.Errorf("%s 使用暂不支持的动作「%s」", context, action)
		}
		if action == "" {
			action = model.ActionRoute
		}
		remapped, err := remapMatch(match, allTags, inboundLike, context)
		if err != nil {
			return nil, err
		}
		rule := &model.RouteRule{ID: model.NewID("route"), DeviceID: "other", Enabled: true, Action: action,
			Name: fmt.Sprintf("导入规则 %d", index+1)}
		if tree != nil {
			if rule.ConditionTree, err = remapTree(tree, allTags, inboundLike, context); err != nil {
				return nil, err
			}
			rule.Match = map[string]any{}
		} else {
			rule.Match = remapped
			rule.CatchAll = len(remapped) == 0
		}
		if outbound != "" {
			if rule.Outbound, err = refOf(requiredReference(outbound, allTags, context+" 出站")); err != nil {
				return nil, err
			}
		}
		rules = append(rules, rule)
	}
	doc.Route.Rules = rules
	if value, ok := route["final"]; ok {
		if _, err := requiredReference(value, allTags, "路由兜底出口"); err != nil {
			return nil, err
		}
	}
	doc.Route.AutoDetectInterface = route["auto_detect_interface"] != false
	findProcess := route["find_process"] != false
	doc.Route.FindProcess = &findProcess
	doc.Route.DefaultInterface, _ = route["default_interface"].(string)
	doc.Route.DefaultDomainResolver = model.Ref{}

	rawDNS := asObject(config["dns"])
	dnsTags := map[string]string{}
	usedDNS := map[string]bool{}
	rawServers := objects(rawDNS["servers"])
	doc.DNS.Servers = []*model.DNSServer{}
	for index, raw := range rawServers {
		typ := typeOf(raw)
		if !slices.Contains(model.DNSServerTypes, typ) {
			return nil, fmt.Errorf("配置包含暂不支持的 DNS 类型「%s」", orUnnamed(typ))
		}
		server := &model.DNSServer{ID: model.NewID("dns"), Tag: uniqueTag(raw["tag"], fmt.Sprintf("%s-%d", typ, index+1), usedDNS),
			Type: typ, Enabled: true,
			Fields: flattenFields(raw, []string{"type", "tag", "server", "server_port", "path", "detour", "domain_resolver", "domain_resolver_detour"})}
		server.Server, _ = raw["server"].(string)
		if port, ok := raw["server_port"].(float64); ok {
			p := int(port)
			server.ServerPort = &p
		}
		server.Path, _ = raw["path"].(string)
		dnsTags[tagKey(raw, server.Tag)] = server.ID
		doc.DNS.Servers = append(doc.DNS.Servers, server)
	}
	// Every dialing entity's resolver names a DNS server by tag; only the
	// outbounds' were remapped, leaving endpoints, inbounds and services
	// with a tag where the document keeps an id.
	remapResolver := func(fields model.Fields, context string) error {
		value, ok := fields["domain_resolver.server"]
		if !ok {
			return nil
		}
		id, err := requiredReference(value, dnsTags, context)
		fields["domain_resolver.server"] = nullable(id)
		return err
	}
	for _, o := range doc.Outbounds {
		if err := remapResolver(o.Fields, fmt.Sprintf("出站「%s」域名解析器", o.Tag)); err != nil {
			return nil, err
		}
	}
	for _, e := range doc.Endpoints {
		if err := remapResolver(e.Fields, fmt.Sprintf("端点「%s」域名解析器", e.Tag)); err != nil {
			return nil, err
		}
	}
	for _, in := range doc.Inbounds {
		if err := remapResolver(in.Fields, fmt.Sprintf("入站「%s」域名解析器", in.Tag)); err != nil {
			return nil, err
		}
	}
	for _, sv := range doc.Services {
		if err := remapResolver(sv.Fields, fmt.Sprintf("服务「%s」域名解析器", sv.Tag)); err != nil {
			return nil, err
		}
	}
	otherGroup := ""
	for _, g := range doc.DNS.Groups {
		if g.DeviceID == "other" {
			otherGroup = g.ID
			break
		}
	}
	dnsRules := []*model.DNSRule{}
	for index, raw := range objects(rawDNS["rules"]) {
		context := fmt.Sprintf("DNS 规则 %d", index+1)
		match := maps.Clone(raw)
		action, _ := match["action"].(string)
		server, _ := match["server"].(string)
		detour, _ := match["detour"].(string)
		servers := match["servers"]
		for _, key := range []string{"action", "server", "servers", "detour"} {
			delete(match, key)
		}
		tree := conditionTree(match)
		if action != "" && action != "route" && action != "reject" {
			return nil, fmt.Errorf("%s 使用暂不支持的动作「%s」", context, action)
		}
		remapped, err := remapMatch(match, dnsTags, map[string]string{}, context)
		if err != nil {
			return nil, err
		}
		rule := &model.DNSRule{ID: model.NewID("dns"), DeviceID: "other", Enabled: true, GroupID: otherGroup,
			Action: action, Name: fmt.Sprintf("导入 DNS 规则 %d", index+1)}
		if tree != nil {
			if rule.ConditionTree, err = remapTree(tree, dnsTags, map[string]string{}, context); err != nil {
				return nil, err
			}
			rule.Match = map[string]any{}
		} else {
			rule.Match = remapped
			rule.CatchAll = len(remapped) == 0
		}
		if rule.Server, err = refOf(requiredReference(server, dnsTags, context+" 服务器")); err != nil {
			return nil, err
		}
		if rule.Detour, err = refOf(requiredReference(detour, allTags, context+" 查询出口")); err != nil {
			return nil, err
		}
		// servers: queried concurrently; the migration gives each the detour
		// of its server.
		if servers != nil {
			list, isList := servers.([]any)
			if !isList {
				list = []any{servers}
			}
			var targets []model.DNSTarget
			for _, value := range list {
				id, err := requiredReference(value, dnsTags, context+" 服务器")
				if err != nil {
					return nil, err
				}
				targets = append(targets, model.DNSTarget{Server: model.RefOf(id)})
			}
			rule.SetTargets(targets)
		}
		dnsRules = append(dnsRules, rule)
	}
	for index, server := range doc.DNS.Servers {
		raw := rawServers[index]
		var err error
		if server.Type == "tailscale" {
			id, err := requiredReference(server.Fields["endpoint"], endpointTags, fmt.Sprintf("DNS「%s」Tailscale 端点", server.Tag))
			if err != nil {
				return nil, err
			}
			if server.Fields == nil {
				server.Fields = model.Fields{}
			}
			server.Fields["endpoint"] = nullable(id)
		}
		if server.Detour, err = refOf(requiredReference(raw["detour"], allTags, fmt.Sprintf("DNS「%s」查询出口", server.Tag))); err != nil {
			return nil, err
		}
		// The resolver may be an object: its server, strategy and query
		// options (requiredReference rejected the object outright).
		resolver := raw["domain_resolver"]
		if o, ok := resolver.(jsonObject); ok {
			resolver = o["server"]
			server.DomainStrategy, _ = o["strategy"].(string)
			for _, key := range []string{"disable_cache", "rewrite_ttl", "client_subnet"} {
				if v, ok := o[key]; ok {
					if server.Fields == nil {
						server.Fields = model.Fields{}
					}
					server.Fields["domain_resolver_"+key] = cloneJSON(v)
				}
			}
		}
		if server.DomainResolver, err = refOf(requiredReference(resolver, dnsTags, fmt.Sprintf("DNS「%s」域名解析器", server.Tag))); err != nil {
			return nil, err
		}
		if server.DomainResolverDetour, err = refOf(requiredReference(raw["domain_resolver_detour"], allTags, fmt.Sprintf("DNS「%s」域名解析出口", server.Tag))); err != nil {
			return nil, err
		}
	}
	doc.DNS.Rules = dnsRules
	if value, ok := rawDNS["final"]; ok {
		id, err := requiredReference(value, dnsTags, "DNS 兜底服务器")
		if err != nil {
			return nil, err
		}
		doc.DNS.Final = model.RefOf(id)
	} else {
		doc.DNS.Final = model.Null()
		for _, s := range doc.DNS.Servers {
			if s.Type != "local" {
				doc.DNS.Final = model.RefOf(s.ID)
				break
			}
		}
		if !doc.DNS.Final.Valid() && len(doc.DNS.Servers) > 0 {
			doc.DNS.Final = model.RefOf(doc.DNS.Servers[0].ID)
		}
	}
	doc.DNS.DeviceFinals = map[string]model.Ref{}
	if s, ok := rawDNS["strategy"].(string); ok {
		doc.DNS.Strategy = s
	}
	doc.DNS.DisableCache = rawDNS["disable_cache"] == true
	doc.DNS.DisableExpire = rawDNS["disable_expire"] == true
	if n, ok := rawDNS["cache_capacity"].(float64); ok {
		doc.DNS.CacheCapacity = int(n)
	}
	doc.DNS.ClientSubnet, _ = rawDNS["client_subnet"].(string)
	doc.DNS.ReverseMapping = rawDNS["reverse_mapping"] == true

	rawRuleSets := objects(route["rule_set"])
	ruleSetTags := map[string]bool{}
	doc.Route.RuleSets = []*model.RuleSet{}
	for index, raw := range rawRuleSets {
		tag := uniqueTag(raw["tag"], fmt.Sprintf("rule-set-%d", index+1), ruleSetTags)
		ruleSet := &model.RuleSet{ID: model.NewID("rs"), Tag: tag, Enabled: true}
		switch typ := typeOf(raw); typ {
		case "remote":
			ruleSet.Mode = "remote"
			ruleSet.URL, _ = raw["url"].(string)
			ruleSet.UpdateInterval, _ = raw["update_interval"].(string)
		case "inline":
			ruleSet.Mode = "inline"
			if rules, ok := raw["rules"].([]any); ok {
				ruleSet.Rules = cloneJSON(rules).([]any)
			}
			if v, ok := raw["source_version"].(float64); ok && (v == 1 || v == 2 || v == 3 || v == 4) {
				ruleSet.SourceVersion = int(v)
			}
		case "local":
			return nil, fmt.Errorf("本地规则集「%s」需要先在本机准备对应文件，暂不支持 URL 导入", tag)
		default:
			return nil, fmt.Errorf("规则集「%s」使用暂不支持的类型「%s」", tag, orUnnamed(typ))
		}
		doc.Route.RuleSets = append(doc.Route.RuleSets, ruleSet)
	}
	httpClientTags := map[string]string{}
	usedHTTPClients := map[string]bool{}
	doc.HTTPClients = []*model.HTTPClient{}
	for index, raw := range objects(config["http_clients"]) {
		name := fmt.Sprint(index + 1)
		if s := tagKey(raw, ""); s != "" {
			name = s
		}
		client := &model.HTTPClient{ID: model.NewID("http"), Tag: uniqueTag(raw["tag"], fmt.Sprintf("http-client-%d", index+1), usedHTTPClients), Enabled: true}
		var err error
		if client.Detour, err = refOf(requiredReference(raw["detour"], allTags, fmt.Sprintf("HTTP 客户端「%s」前置代理", name))); err != nil {
			return nil, err
		}
		httpClientTags[tagKey(raw, client.Tag)] = client.ID
		doc.HTTPClients = append(doc.HTTPClients, client)
	}
	for index, ruleSet := range doc.Route.RuleSets {
		if tag, ok := rawRuleSets[index]["http_client"].(string); ok {
			id, err := requiredReference(tag, httpClientTags, fmt.Sprintf("规则集「%s」HTTP 客户端", ruleSet.Tag))
			if err != nil {
				return nil, err
			}
			if id != "" {
				ruleSet.HTTPClient = model.RefOf(id)
			}
		}
	}
	if tag, ok := route["default_http_client"].(string); ok {
		id, err := requiredReference(tag, httpClientTags, "默认 HTTP 客户端")
		if err != nil {
			return nil, err
		}
		if id != "" {
			doc.Route.DefaultHTTPClient = model.RefOf(id)
		}
	}
	resolver := route["default_domain_resolver"]
	if o, ok := resolver.(jsonObject); ok {
		resolver = o["server"]
		doc.Route.DefaultDomainStrategy, _ = o["strategy"].(string)
	}
	if doc.Route.DefaultDomainResolver, err = refOf(requiredReference(resolver, dnsTags, "路由默认域名解析器")); err != nil {
		return nil, err
	}
	if !doc.Route.DefaultDomainResolver.Valid() {
		doc.Route.DefaultDomainResolver = doc.DNS.Final
	}
	if value, ok := route["final"]; ok {
		if doc.Route.Final, err = refOf(requiredReference(value, allTags, "路由兜底出口")); err != nil {
			return nil, err
		}
	} else {
		doc.Route.Final = model.Null()
		for _, o := range doc.Outbounds {
			if o.Type == "direct" {
				doc.Route.Final = model.RefOf(o.ID)
				break
			}
		}
		if !doc.Route.Final.Valid() {
			doc.Route.Final = model.RefOf(doc.Outbounds[0].ID)
		}
	}
	log := asObject(config["log"])
	if level, ok := log["level"].(string); ok {
		doc.Log.Level = level
	}
	doc.Log.Timestamp = log["timestamp"] != false
	experimental := asObject(config["experimental"])
	clash := asObject(experimental["clash_api"])
	doc.Experimental.ClashAPI = len(clash) > 0
	if s, ok := clash["external_controller"].(string); ok {
		doc.Experimental.ExternalController = s
	}
	if s, ok := clash["default_mode"].(string); ok {
		doc.Experimental.DefaultMode = s
	}
	cache, ok := experimental["cache_file"].(jsonObject)
	doc.Experimental.CacheFile = ok && cache["enabled"] != false
	return doc, nil
}

func asList(value any) []any {
	list, _ := value.([]any)
	return list
}

func fallbackTag(typ, kind string, index int) string {
	if typ == "" {
		typ = kind
	}
	return fmt.Sprintf("%s-%d", typ, index+1)
}

// nullable is an id as a field value: nil for none.
func nullable(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// configUsesIPv6 reports whether a sing-box configuration uses IPv6: an
// inbound listening on an IPv6 address, a TUN interface with an IPv6
// address, or an IPv6-preferring or IPv6-only strategy anywhere.
func configUsesIPv6(config jsonObject) bool {
	isIPv6 := func(v any) bool {
		s, ok := v.(string)
		return ok && strings.Contains(s, ":")
	}
	for _, in := range objects(config["inbounds"]) {
		if isIPv6(in["listen"]) {
			return true
		}
		if in["type"] == "tun" {
			for _, key := range []string{"address", "inet6_address"} {
				values, isList := in[key].([]any)
				if !isList {
					values = []any{in[key]}
				}
				if slices.ContainsFunc(values, isIPv6) {
					return true
				}
			}
		}
	}
	found := false
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case jsonObject:
			for key, child := range x {
				if s, ok := child.(string); ok && (key == "strategy" || key == "domain_strategy") &&
					(s == "prefer_ipv6" || s == "ipv6_only") {
					found = true
				}
				visit(child)
			}
		case []any:
			for _, child := range x {
				visit(child)
			}
		}
	}
	visit(config)
	return found
}
