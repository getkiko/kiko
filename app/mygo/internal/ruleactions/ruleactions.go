// Package ruleactions is the "Add to rule" menu of the Connections, Usage and Processes
// pages: a connection's domain, destination, port or process becomes the
// condition of a new traffic rule, which opens unsaved in the Routing editor
// of the device the connection came from, or of another device.
package ruleactions

import (
	"net"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"golang.org/x/net/publicsuffix"
)

// Target is what a rule can match of a connection, a usage group or a
// process; empty fields offer nothing.
type Target struct {
	// Domain is the host name; an IP address here counts as IP.
	Domain string
	// IP is the destination address, Port its port.
	IP   string
	Port int
	// ProcessName and ProcessPath name the executable; AppPath and AppName
	// its macOS application bundle.
	ProcessName, ProcessPath string
	AppPath, AppName         string
	// SourceIP is the connection's source address, which picks the device
	// the rule goes to; without one it is the local server / gateway.
	SourceIP string
}

// Condition is an item of the menu: a label and the conditions of its
// rule.
type Condition struct {
	Label string
	// Name is the new rule's name.
	Name string
	Tree *model.Condition
}

// RootDomain is a domain's registrable part, as Usage groups by root domain.
func RootDomain(domain string) string {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	if root, err := publicsuffix.EffectiveTLDPlusOne(domain); err == nil {
		return root
	}
	return domain
}

// Conditions are the rules t offers, in menu order: domain, suffix and
// keyword; destination IP; port; process name, path and application. App
// conditions need a macOS document.
func Conditions(doc *model.Doc, t Target) []Condition {
	var out []Condition
	typed := func(label, name, kind, field string, value any) {
		tree := model.AddMatchValues(model.EmptyConditionGroup(), kind, []model.MatchValue{{Field: field, Value: value}})
		out = append(out, Condition{Label: label, Name: name, Tree: tree})
	}
	process := func(label, name string, item model.ProcessSelection) {
		tree := model.EmptyConditionGroup()
		tree.Rules = append(tree.Rules, model.AddProcessSelection(model.EmptyProcessCondition(), item))
		out = append(out, Condition{Label: label, Name: name, Tree: tree})
	}
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(t.Domain)), ".")
	ip := strings.TrimSpace(t.IP)
	if domain != "" && net.ParseIP(domain) != nil {
		if ip == "" {
			ip = domain
		}
		domain = ""
	}
	if domain != "" {
		typed("域名 "+domain, domain, model.TypedDomain, "domain", domain)
		root := RootDomain(domain)
		typed("后缀 "+root, root, model.TypedDomain, "domain_suffix", root)
		if keyword, _, _ := strings.Cut(root, "."); keyword != "" {
			typed("关键词 "+keyword, keyword, model.TypedDomain, "domain_keyword", keyword)
		}
	}
	if parsed := net.ParseIP(ip); parsed != nil {
		cidr := parsed.String() + "/32"
		if parsed.To4() == nil {
			cidr = parsed.String() + "/128"
		}
		typed("目标 IP "+cidr, parsed.String(), model.TypedIP, "ip_cidr", cidr)
	}
	if t.Port > 0 && t.Port <= 65535 {
		port := strconv.Itoa(t.Port)
		typed("目标端口 "+port, "端口 "+port, model.TypedPort, "port", port)
	}
	if name := strings.TrimSpace(t.ProcessName); name != "" {
		process("进程名 "+name, name, model.ProcessSelection{Source: model.ProcessSourceCustom, Type: model.ProcessTypeName, Value: name})
	}
	if path := strings.TrimSpace(t.ProcessPath); path != "" {
		name := firstNonEmpty(t.ProcessName, baseName(path))
		process("进程路径 "+path, name, model.ProcessSelection{Source: model.ProcessSourceProcess, Type: model.ProcessTypePath, Value: path, Label: name})
	}
	if app := strings.TrimSpace(t.AppPath); app != "" && doc.Platform == model.PlatformDarwin {
		name := firstNonEmpty(t.AppName, strings.TrimSuffix(baseName(app), ".app"))
		process("所属 App "+name, name, model.ProcessSelection{Source: model.ProcessSourceApplication, Type: model.ProcessTypeRegex, Value: app, Label: name})
	}
	return out
}

// SourceDevice is the device whose rules a connection from t.SourceIP
// reaches; the local server / gateway without a source, or the other devices without that scope.
func SourceDevice(doc *model.Doc, s *store.Store, t Target) string {
	host := strings.TrimSpace(t.SourceIP)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" {
		if model.LocalEnabled(doc) {
			return model.LocalActiveScope(doc)
		}
		return model.OtherDeviceID
	}
	return model.DeviceForSource(doc, host, s.LocalAddresses)
}

// Available reports whether t offers a rule.
func Available(s *store.Store, t Target) bool {
	return s != nil && s.Loaded && len(Conditions(s.Doc(), t)) > 0
}

// Menu adds the "Add to rule" submenu: the conditions of t for the device
// the source belongs to, then "Add to other device" with the other devices.
func Menu(m *ui.Menu, e *env.Env, s *store.Store, t Target) {
	if !Available(s, t) {
		m.Item("添加到规则").Disabled(true)
		return
	}
	m.Submenu("添加到规则", func(sub *ui.Menu) { Items(sub, e, s, t) })
}

// Items adds the conditions of t to m, as Menu's submenu lists them, for a
// menu button of its own. Each opens a new rule in Routing, unsaved.
func Items(m *ui.Menu, e *env.Env, s *store.Store, t Target) {
	if s == nil || !s.Loaded {
		m.Item("配置尚未载入").Disabled(true)
		return
	}
	doc := s.Doc()
	conditions := Conditions(doc, t)
	if len(conditions) == 0 {
		m.Item("没有可添加的条件").Disabled(true)
		return
	}
	add := func(menu *ui.Menu, owner string) {
		for _, cond := range conditions {
			if menu.Item(cond.Label).Chosen() {
				e.Navigate(env.PageRoute, env.NewRouteRule{Owner: owner, Name: cond.Name, Tree: cond.Tree})
			}
		}
	}
	source := SourceDevice(doc, s, t)
	m.Item("添加到「" + model.PolicyOwnerName(doc, source) + "」").Disabled(true)
	add(m, source)
	m.Separator()
	m.Submenu("添加到其他设备", func(devices *ui.Menu) {
		for _, owner := range model.PolicyOwners(doc) {
			if owner != source {
				devices.Submenu(model.PolicyOwnerName(doc, owner), func(sub *ui.Menu) { add(sub, owner) })
			}
		}
	})
}

func baseName(path string) string {
	path = strings.TrimRight(path, `/\`)
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
