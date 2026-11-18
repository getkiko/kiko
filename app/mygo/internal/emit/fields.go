package emit

import (
	"regexp"
	"slices"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// setPath writes value at a dotted path, creating objects on the way.
func setPath(target *object, path string, value any) {
	parts := strings.Split(path, ".")
	cur := target
	for _, k := range parts[:len(parts)-1] {
		next, ok := asObject(cur.get(k))
		if !ok {
			next = obj()
		}
		cur.set(k, next)
		cur = next
	}
	cur.set(parts[len(parts)-1], value)
}

func splitList(v any) []string { return model.SplitList(v) }

func firstString(v any) string {
	if list := splitList(v); len(list) > 0 {
		return list[0]
	}
	return ""
}

var reDigits = regexp.MustCompile(`^\d+$`)

func validPorts(v any) (ports []any, invalid []string) {
	ports = []any{}
	for _, value := range splitList(v) {
		port := jsv.StringToNumber(value)
		if reDigits.MatchString(value) && jsv.IsInteger(port) && port >= 1 && port <= 65535 {
			ports = append(ports, port)
		} else {
			invalid = append(invalid, value)
		}
	}
	return ports, invalid
}

func anyStrings(list []string) []any {
	out := make([]any, len(list))
	for i, v := range list {
		out[i] = v
	}
	return out
}

func uniqueStrings(list []string) []string {
	var out []string
	for _, v := range list {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// resolver turns an id into the emitted tag.
type resolver func(id string) (string, bool)

// dnsResolver turns a DNS server id and a query detour into a tag.
type dnsResolver func(id, detour string) (string, bool)

func idOf(v any) string {
	s, _ := v.(string)
	return s
}

// refValue reads an optional reference value ("" when unset).
func refValue(v any) string {
	if !jsv.Truthy(v) {
		return ""
	}
	return jsv.String(v)
}

// applyFields writes the fields of an entity into target as its schema
// shapes them. Fields the platform does not support are skipped and noted.
func applyFields(target *object, fields model.Fields, table []schema.Field, resolve resolver, platform string,
	onSkip func(label string), resolveIn resolver, resolveDNS dnsResolver) {
	for _, f := range table {
		if f.ServerOnly || !schema.FieldVisible(f, table, fields) {
			continue
		}
		v := fields[f.Key]
		if isEmpty(v) && v != false && !isZero(v) {
			continue
		}
		if !schema.FieldAvailable(f, platform) {
			if v != false && onSkip != nil {
				onSkip(f.Label)
			}
			continue
		}
		if v == false && f.Key != "auto_route" {
			continue
		}
		switch f.Type {
		case schema.Ref:
			if tag, ok := resolve(idOf(v)); ok {
				setPath(target, f.Key, tag)
			}
		case schema.RefDNS:
			if resolveDNS != nil {
				if tag, ok := resolveDNS(idOf(v), refValue(fields[model.DNSResolverDetourKey(f.Key)])); ok {
					setPath(target, f.Key, tag)
				}
			}
		case schema.RefIn:
			if resolveIn != nil {
				if tag, ok := resolveIn(idOf(v)); ok {
					setPath(target, f.Key, tag)
				}
			}
		case schema.List:
			list := splitList(v)
			if slices.Contains([]string{"include_uid", "exclude_uid", "include_android_user"}, f.Key) {
				numbers := []any{}
				for _, item := range list {
					if n := jsv.StringToNumber(item); jsv.IsFinite(n) {
						numbers = append(numbers, n)
					}
				}
				setPath(target, f.Key, numbers)
			} else {
				setPath(target, f.Key, anyStrings(list))
			}
		case schema.MultiSelect:
			setPath(target, f.Key, anyStrings(splitList(v)))
		case schema.Number:
			n, ok := jsv.Float(v)
			if !ok {
				n = jsv.ParseInt(jsv.String(v))
			}
			if jsv.IsFinite(n) {
				setPath(target, f.Key, n)
			}
		case schema.Select:
			if f.NumericOptions {
				setPath(target, f.Key, jsv.Number(v))
			} else {
				setPath(target, f.Key, v)
			}
		default:
			setPath(target, f.Key, v)
		}
	}
}

// fixTransport renames the transport fields the schema keys apart and
// drops a transport without a type.
func fixTransport(o *object, f model.Fields) {
	tr, ok := o.get("transport").(*object)
	if !ok {
		return
	}
	if !jsv.Truthy(tr.get("headers")) && jsv.Truthy(f["transport.headers.Host"]) {
		tr.set("headers", obj("Host", f["transport.headers.Host"]))
	}
	for _, rename := range [][2]string{{"path_http", "path"}, {"path_hu", "path"}, {"host_hu", "host"}} {
		if tr.has(rename[0]) {
			tr.set(rename[1], tr.get(rename[0]))
			tr.del(rename[0])
		}
	}
	if !jsv.Truthy(tr.get("type")) {
		o.del("transport")
	}
}

// applyBurrow shapes Burrow fields; inbounds and outbounds embed the
// same options, so both use it.
func applyBurrow(o *object, f model.Fields) {
	o.set("mode", "header")
	o.del("signal_address")
	o.del("signal_server_ip")
	for _, kv := range schema.BurrowConfigFromFields(f) {
		o.set(kv[0].(string), kv[1])
	}
}

func postProcessInbound(typ string, o *object, f model.Fields, platform string) {
	uname := "user"
	if jsv.Truthy(f["user_name"]) {
		uname = jsv.String(f["user_name"])
	}
	hasUsers := jsv.IsArray(f["users"])
	switch typ {
	case "burrow":
		applyBurrow(o, f)
	case "mixed", "socks", "http":
		u, p := f["username"], f["password"]
		authEnabled := jsv.Truthy(u)
		if typ == "mixed" || typ == "socks" {
			authEnabled = jsv.Truthy(f["auth_enabled"])
		}
		o.del("auth_enabled")
		o.del("username")
		o.del("password")
		if !hasUsers && authEnabled && jsv.Truthy(u) {
			password := any("")
			if jsv.Truthy(p) {
				password = p
			}
			o.set("users", []any{obj("username", u, "password", password)})
		}
	case "vless", "vmess":
		for _, k := range []string{"user_name", "uuid", "alter_id", "flow"} {
			o.del(k)
		}
		user := obj("name", uname, "uuid", f["uuid"])
		if jsv.Truthy(f["flow"]) {
			user.set("flow", f["flow"])
		}
		if jsv.Truthy(f["alter_id"]) {
			user.set("alterId", jsv.Number(f["alter_id"]))
		}
		if !hasUsers {
			o.set("users", []any{user})
		}
	case "trojan", "hysteria2", "anytls", "naive":
		o.del("user_name")
		o.del("password")
		if !hasUsers {
			o.set("users", []any{obj("name", uname, "password", f["password"])})
		}
	case "tuic":
		o.del("user_name")
		o.del("uuid")
		o.del("password")
		if !hasUsers {
			o.set("users", []any{obj("name", uname, "uuid", f["uuid"], "password", f["password"])})
		}
	case "shadowtls":
		version := 3.0
		if v, ok := f["version"]; ok && v != nil {
			version = jsv.Number(v)
		}
		o.set("version", version)
		if version == 3 {
			o.del("password")
			if !hasUsers {
				o.set("users", []any{obj("name", uname, "password", f["password"])})
			}
		}
		o.del("user_name")
		if version == 1 {
			o.del("password")
		}
	case "tun":
		v6 := jsv.Truthy(f["ipv6"])
		addrs := splitList(f["address"])
		if len(addrs) == 0 {
			addrs = append(addrs, model.TunAddrV4)
		}
		v6Addr := model.TunAddrV6
		if platform == model.PlatformAndroid {
			v6Addr = model.TunAddrV6Android
		}
		hasV6 := slices.ContainsFunc(addrs, func(a string) bool { return strings.Contains(a, ":") })
		if v6 && !hasV6 {
			addrs = append(addrs, v6Addr)
		}
		if !v6 {
			var only4 []string
			for _, a := range addrs {
				if !strings.Contains(a, ":") {
					only4 = append(only4, a)
				}
			}
			if len(only4) == 0 {
				only4 = []string{model.TunAddrV4}
			}
			addrs = only4
		}
		o.set("address", anyStrings(addrs))
		exclude := splitList(f["route_exclude_address"])
		o.del("ipv6")
		if len(exclude) > 0 {
			o.set("route_exclude_address", anyStrings(uniqueStrings(exclude)))
		}
	}
	fixTransport(o, f)
}

func postProcessOutbound(typ string, o *object, f model.Fields) {
	if typ == "burrow" {
		applyBurrow(o, f)
	}
	if typ == "shadowtls" {
		version := 3.0
		if v, ok := f["version"]; ok && v != nil {
			version = jsv.Number(v)
		}
		if !jsv.IsInteger(version) || version != 1 && version != 2 && version != 3 {
			version = 3
		}
		o.set("version", version)
	}
	if typ == "shadowsocks" && jsv.Truthy(f["udp_over_tcp"]) {
		o.set("udp_over_tcp", true)
	}
	fixTransport(o, f)
}

// postProcessEndpoint collapses Tailscale's ssh_server: off is dropped,
// enabled without options becomes true.
func postProcessEndpoint(typ string, o *object, f model.Fields, warnings *[]string) {
	if typ != "tailscale" {
		return
	}
	if o.has("ssh_server") {
		ssh, isObject := o.get("ssh_server").(*object)
		switch {
		case !isObject:
			if jsv.Truthy(o.get("ssh_server")) {
				o.del("ssh_server")
			}
		case !jsv.Truthy(ssh.get("enabled")):
			o.del("ssh_server")
		case !slices.ContainsFunc(ssh.keys, func(k string) bool { return k != "enabled" }):
			o.set("ssh_server", true)
		}
	}
	if jsv.Truthy(f["advertise_exit_node"]) && jsv.Text(f["exit_node"]) != "" {
		*warnings = append(*warnings, "Tailscale 不能同时作为出口节点并使用其他出口节点，核心将拒绝启动")
	}
	fixUDPPath(o, warnings)
}

// fixUDPPath writes udp_path.peers as the core reads it: rules with a
// match list and a path (direct when unset); rules without a name drop.
func fixUDPPath(o *object, warnings *[]string) {
	udpPath, ok := asObject(o.get("udp_path"))
	if !ok {
		return
	}
	usesDetour := jsv.String(udpPath.get("default")) == "detour"
	if udpPath.has("peers") {
		list, _ := jsv.Array(udpPath.get("peers"))
		var peers []any
		for _, entry := range list {
			item, isMap := entry.(map[string]any)
			if !isMap {
				continue
			}
			var match []any
			if items, isList := jsv.Array(item["match"]); isList {
				for _, m := range items {
					if text := strings.TrimSpace(jsv.String(m)); text != "" {
						match = append(match, text)
					}
				}
			} else if text := jsv.Text(item["match"]); text != "" {
				match = []any{text}
			}
			if len(match) == 0 {
				continue
			}
			path := jsv.Text(item["path"])
			if path == "" {
				path = "direct"
			}
			usesDetour = usesDetour || path == "detour"
			peers = append(peers, obj("match", match, "path", path))
		}
		if len(peers) == 0 {
			udpPath.del("peers")
		} else {
			udpPath.set("peers", peers)
		}
	}
	if udpPath.len() == 0 {
		o.del("udp_path")
		return
	}
	o.set("udp_path", udpPath)
	if usesDetour && !o.has("detour") {
		*warnings = append(*warnings, "Tailscale 的 UDP 路径「经代理」需要前置代理，核心将拒绝启动")
	}
}

// compileCondition compiles a condition tree into a sing-box rule: leaves
// are converted matches, groups logical rules.
func compileCondition(node *model.Condition, convert func(map[string]any) *object) *object {
	if node.Kind == model.ConditionMatch {
		out := convert(node.Match)
		if node.Invert {
			out = spread(out, obj("invert", true))
		}
		return out
	}
	if len(node.Rules) == 0 {
		return obj()
	}
	rules := make([]any, len(node.Rules))
	for i, rule := range node.Rules {
		rules[i] = compileCondition(rule, convert)
	}
	out := obj("type", "logical", "mode", node.Mode, "rules", rules)
	if node.Invert {
		out.set("invert", true)
	}
	return out
}
