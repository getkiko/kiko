package emit

import (
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

func depObject(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func depObjects(v any) []map[string]any {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, len(list))
	for i, e := range list {
		out[i] = depObject(e)
	}
	return out
}

func depText(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func depResolver(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return depText(depObject(v)["server"])
}

func depHostname(v any) string {
	address := depText(v)
	if strings.Contains(address, "://") {
		u, err := url.Parse(address)
		if err != nil {
			return ""
		}
		return strings.TrimSuffix(strings.TrimPrefix(u.Hostname(), "["), "]")
	}
	return strings.TrimSuffix(strings.TrimPrefix(address, "["), "]")
}

func depIsDomain(v any) bool {
	host := depHostname(v)
	return host != "" && !schema.IsIPAddress(strings.Split(host, "%")[0])
}

type depEdge struct{ target, reason string }

type depNode struct {
	label string
	edges []depEdge
}

// ConfigDependencyErrors finds dependency cycles in an emitted
// configuration: DNS bootstrap resolvers, detours, group members and
// inbound hand-offs. IP and domain destinations are tracked apart, so a
// direct detour to a DNS server by IP stays valid.
func ConfigDependencyErrors(config map[string]any) []string {
	var keys []string
	graph := map[string]*depNode{}
	dnsOptions := depObject(config["dns"])
	dns := depObjects(dnsOptions["servers"])
	plainOutbounds := depObjects(config["outbounds"])
	outbounds := append(slices.Clone(plainOutbounds), depObjects(config["endpoints"])...)
	defaultResolver := depResolver(depObject(config["route"])["default_domain_resolver"])
	routedResolver := ""
	if len(depObjects(dnsOptions["rules"])) == 0 {
		routedResolver = depText(dnsOptions["final"])
		if routedResolver == "" && len(dns) > 0 {
			routedResolver = depText(dns[0]["tag"])
		}
	}
	dnsKey := func(tag string) string { return "dns\x00" + tag }
	outKey := func(tag, destination string) string { return "outbound\x00" + tag + "\x00" + destination }
	inKey := func(tag string) string { return "inbound\x00" + tag }
	add := func(key, label string) {
		if _, ok := graph[key]; !ok {
			keys = append(keys, key)
		}
		graph[key] = &depNode{label: label}
	}
	edge := func(from, target, reason string) {
		if _, ok := graph[target]; ok {
			graph[from].edges = append(graph[from].edges, depEdge{target, reason})
		}
	}
	for _, server := range dns {
		add(dnsKey(depText(server["tag"])), "DNS「"+depText(server["tag"])+"」")
	}
	for i, o := range outbounds {
		kind := "出站"
		if i >= len(plainOutbounds) {
			kind = "端点"
		}
		label := kind + "「" + depText(o["tag"]) + "」"
		for _, destination := range []string{"ip", "domain"} {
			add(outKey(depText(o["tag"]), destination), label)
		}
	}
	inbounds := depObjects(config["inbounds"])
	for _, in := range inbounds {
		add(inKey(depText(in["tag"])), "入站「"+depText(in["tag"])+"」")
	}

	resolve := func(from string, options map[string]any, reason string) {
		explicit := depResolver(options["domain_resolver"])
		inherited := defaultResolver
		if inherited == "" && len(dns) == 1 {
			inherited = depText(dns[0]["tag"])
		}
		target, why := explicit, reason
		switch {
		case explicit != "":
		case inherited != "":
			target, why = inherited, "默认域名解析器"
		default:
			target, why = routedResolver, "DNS 分流兜底"
		}
		if target != "" {
			edge(from, dnsKey(target), why)
		}
	}
	dial := func(from string, options map[string]any, remoteIsDomain, resolverOnDetour bool) {
		detour := depText(options["detour"])
		resolves := remoteIsDomain && (detour == "" || resolverOnDetour || depResolver(options["domain_resolver"]) != "")
		if resolves {
			resolve(from, options, "域名解析器")
		}
		if detour != "" {
			destination := "ip"
			if remoteIsDomain && !resolves {
				destination = "domain"
			}
			edge(from, outKey(detour, destination), "前置代理")
		}
	}

	for _, server := range dns {
		from := dnsKey(depText(server["tag"]))
		typ := depText(server["type"])
		if typ == "tailscale" {
			edge(from, outKey(depText(server["endpoint"]), "ip"), "关联端点")
			continue
		}
		if !slices.Contains([]string{"udp", "tcp", "tls", "https", "quic", "h3", "local", "dhcp", "mdns"}, typ) {
			continue
		}
		bootstrap := depResolver(server["domain_resolver"])
		if bootstrap != "" {
			edge(from, dnsKey(bootstrap), "引导解析器")
		}
		domain := depIsDomain(server["server"])
		if detour := depText(server["detour"]); detour != "" {
			destination := "ip"
			if domain && bootstrap == "" {
				destination = "domain"
			}
			edge(from, outKey(detour, destination), "前置代理")
		}
	}
	for _, o := range outbounds {
		tag, typ := depText(o["tag"]), depText(o["type"])
		for _, destination := range []string{"ip", "domain"} {
			from := outKey(tag, destination)
			switch typ {
			case "selector", "urltest", "fallback", "loadbalance", "smart":
				members, _ := o["outbounds"].([]any)
				for _, member := range members {
					edge(from, outKey(depText(member), destination), "分组成员")
				}
			case "direct":
				if destination == "domain" {
					resolve(from, o, "域名解析器")
				}
			case "wireguard":
				peers := depObjects(o["peers"])
				dial(from, o, slices.ContainsFunc(peers, func(p map[string]any) bool { return depIsDomain(p["address"]) }), true)
			case "tailscale":
				dial(from, o, true, true)
			default:
				host := depHostname(o["server"])
				mapped, _ := depObject(o["hosts"])[host].([]any)
				domain := depIsDomain(host) && len(mapped) == 0
				dial(from, o, domain, slices.Contains([]string{"naive", "openconnect", "openvpn-client"}, typ))
			}
		}
	}
	for _, in := range inbounds {
		if target := depText(in["detour"]); target != "" {
			edge(inKey(depText(in["tag"])), inKey(target), "入站转交")
		}
	}

	type frame struct {
		key  string
		next int
		via  *depEdge
	}
	state := map[string]int{}
	var errorKeys []string
	errors := map[string]string{}
	for _, start := range keys {
		if _, ok := state[start]; ok {
			continue
		}
		stack := []*frame{{key: start}}
		position := map[string]int{start: 0}
		state[start] = 1
		for len(stack) > 0 {
			f := stack[len(stack)-1]
			edges := graph[f.key].edges
			if f.next >= len(edges) {
				state[f.key] = 2
				delete(position, f.key)
				stack = stack[:len(stack)-1]
				continue
			}
			next := edges[f.next]
			f.next++
			switch state[next.target] {
			case 1:
				cycle := stack[position[next.target]:]
				parts := make([]string, len(cycle))
				for i, entry := range cycle {
					via := next
					if i+1 < len(cycle) {
						via = *cycle[i+1].via
					}
					parts[i] = graph[entry.key].label + " —" + via.reason + "→ "
				}
				sorted := slices.Clone(parts)
				sort.Strings(sorted)
				signature := strings.Join(sorted, "")
				if _, ok := errors[signature]; !ok {
					errorKeys = append(errorKeys, signature)
				}
				errors[signature] = "循环依赖：" + strings.Join(parts, "") + graph[next.target].label + "。请调整解析器、前置代理或分组成员。"
				if len(errors) >= 10 {
					return collect(errorKeys, errors)
				}
			case 0:
				if _, seen := state[next.target]; !seen {
					position[next.target] = len(stack)
					state[next.target] = 1
					edge := next
					stack = append(stack, &frame{key: next.target, via: &edge})
				}
			}
		}
	}
	return collect(errorKeys, errors)
}

func collect(keys []string, values map[string]string) []string {
	out := []string{}
	for _, k := range keys {
		out = append(out, values[k])
	}
	return out
}
