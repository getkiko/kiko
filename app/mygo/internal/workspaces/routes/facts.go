package routes

import (
	"net/netip"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// A sample's facts beyond its domain, IP and inbound: what the topology's
// test adds to narrow the rules a connection may take. Each decides the
// conditions on its fields; a condition on a field the sample does not
// tell stays unknown.

// sampleFact is a fact a test may add: its key in policyTestInput.facts,
// its label and placeholder, and the condition fields it decides.
type sampleFact struct {
	key, label, placeholder string
	options                 []string
	fields                  []string
}

// sampleFacts are the facts the test offers, in the order it lists them.
var sampleFacts = []sampleFact{
	{key: "port", label: "目标端口", placeholder: "443", fields: []string{"port", "port_range"}},
	{key: "network", label: "传输协议", options: []string{"tcp", "udp"}, fields: []string{"network"}},
	{key: "protocol", label: "应用协议", placeholder: "tls", fields: []string{"protocol"}},
	{key: "process_path", label: "进程路径", placeholder: "/Applications/Safari.app/Contents/MacOS/Safari",
		fields: []string{"process_path", "process_path_regex", "process_name"}},
	{key: "process_name", label: "进程名", placeholder: "Safari", fields: []string{"process_name"}},
	{key: "package_name", label: "Android 应用", placeholder: "com.example.app", fields: []string{"package_name"}},
	{key: "source_ip", label: "来源 IP", placeholder: "192.168.1.20", fields: []string{"source_ip_cidr", "source_ip_is_private"}},
	{key: "source_port", label: "来源端口", placeholder: "53421", fields: []string{"source_port", "source_port_range"}},
	{key: "user", label: "系统用户", placeholder: "root", fields: []string{"user"}},
	{key: "user_id", label: "用户 ID", placeholder: "0", fields: []string{"user_id"}},
	{key: "wifi_ssid", label: "Wi-Fi 名称", placeholder: "Home", fields: []string{"wifi_ssid"}},
	{key: "clash_mode", label: "Clash 模式", placeholder: "rule", fields: []string{"clash_mode"}},
}

// factOf is the fact of key, nil for none.
func factOf(key string) *sampleFact {
	for i := range sampleFacts {
		if sampleFacts[i].key == key {
			return &sampleFacts[i]
		}
	}
	return nil
}

// verdict is a match or a miss.
func verdict(ok bool) matchResult {
	if ok {
		return matchResult{state: stateMatch}
	}
	return matchResult{state: stateMiss}
}

// fact is the sample's value of key, trimmed, "" when it does not tell.
func (in policyTestInput) fact(key string) string {
	return strings.TrimSpace(in.facts[key])
}

// processName is the sample's process name: its own fact, else the base
// name of its process path.
func (in policyTestInput) processName() string {
	if name := in.fact("process_name"); name != "" {
		return name
	}
	if p := in.fact("process_path"); p != "" {
		return path.Base(strings.ReplaceAll(p, "\\", "/"))
	}
	return ""
}

// inPortRange reports whether port is in r, "a:b", ":b" or "a:".
func inPortRange(port int, r string) (bool, bool) {
	lo, hi, ok := strings.Cut(strings.TrimSpace(r), ":")
	if !ok {
		return false, false
	}
	from, to := 0, 65535
	var err error
	if lo != "" {
		if from, err = strconv.Atoi(lo); err != nil {
			return false, false
		}
	}
	if hi != "" {
		if to, err = strconv.Atoi(hi); err != nil {
			return false, false
		}
	}
	return port >= from && port <= to, true
}

// matchPort decides a port or port range condition for the fact key.
func matchPort(field string, values []string, in policyTestInput, key string) (matchResult, bool) {
	port, err := strconv.Atoi(in.fact(key))
	if err != nil {
		return matchResult{}, false
	}
	for _, v := range values {
		if strings.HasSuffix(field, "_range") {
			if hit, ok := inPortRange(port, v); ok && hit {
				return verdict(true), true
			}
		} else if n, err := strconv.Atoi(v); err == nil && n == port {
			return verdict(true), true
		}
	}
	return verdict(false), true
}

// matchEqual decides a condition its values must equal the fact of.
func matchEqual(values []string, fact string, fold bool) (matchResult, bool) {
	if fact == "" {
		return matchResult{}, false
	}
	return verdict(slices.ContainsFunc(values, func(v string) bool {
		return v == fact || fold && strings.EqualFold(v, fact)
	})), true
}

// evaluateFact decides the condition field from the sample's facts and
// its IP, ok false when they do not tell.
func evaluateFact(field string, values []string, in policyTestInput) (matchResult, bool) {
	switch field {
	case "port", "port_range":
		return matchPort(field, values, in, "port")
	case "source_port", "source_port_range":
		return matchPort(field, values, in, "source_port")
	case "network", "protocol", "clash_mode":
		return matchEqual(values, in.fact(field), true)
	case "package_name", "user", "user_id", "wifi_ssid", "process_path":
		return matchEqual(values, in.fact(field), false)
	case "process_name":
		return matchEqual(values, in.processName(), false)
	case "process_path_regex":
		p := in.fact("process_path")
		if p == "" {
			return matchResult{}, false
		}
		for _, v := range values {
			re, err := regexp.Compile(v)
			if err != nil {
				return matchResult{}, false
			}
			if re.MatchString(p) {
				return verdict(true), true
			}
		}
		return verdict(false), true
	case "source_ip_cidr":
		ip := in.fact("source_ip")
		if ip == "" {
			return matchResult{}, false
		}
		return verdict(slices.ContainsFunc(values, func(n string) bool { return model.AddressInNetwork(ip, n) })), true
	case "ip_is_private", "source_ip_is_private", "ip_version":
		ip := in.ip
		if field == "source_ip_is_private" {
			ip = in.fact("source_ip")
		}
		addr, err := netip.ParseAddr(strings.TrimSpace(ip))
		if err != nil {
			return matchResult{}, false
		}
		addr = addr.Unmap()
		if field == "ip_version" {
			version := "4"
			if addr.Is6() {
				version = "6"
			}
			return verdict(slices.Contains(values, version)), true
		}
		return verdict(slices.Contains(values, "true") && !publicAddr(addr)), true
	}
	return matchResult{}, false
}

// publicAddr reports whether addr is public as sing-box's ip_is_private
// tells it: none of private, loopback, multicast, link-local and
// unspecified.
func publicAddr(addr netip.Addr) bool {
	return !(addr.IsPrivate() || addr.IsLoopback() || addr.IsMulticast() || addr.IsLinkLocalUnicast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsUnspecified())
}
