package endpoints

import (
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// udpPathPeersKey holds a Tailscale endpoint's per-peer UDP path rules,
// emitted as udp_path.peers: [{match: [...], path}].
const udpPathPeersKey = "udp_path.peers"

// udpPathOptions are the paths a rule chooses.
var udpPathOptions = []kit.Option{
	{Value: "detour", Label: "经代理"},
	{Value: "direct", Label: "直连"},
	{Value: "relay", Label: "中继"},
}

// udpPathLabel names a path as the status and the rules show it.
func udpPathLabel(value string) string {
	for _, o := range udpPathOptions {
		if o.Value == value {
			return o.Label
		}
	}
	return value
}

// udpPathBadge marks the path a peer's UDP takes.
func udpPathBadge(c *ui.Context, value string) ui.Element {
	return kit.Badge(c, udpPathLabel(value), kit.Neutral)
}

// udpPathRule is one row of udp_path.peers.
type udpPathRule struct {
	match []string
	path  string
}

// udpPathRules reads udp_path.peers; match may be a string or a list.
func udpPathRules(value any) []udpPathRule {
	list, _ := jsv.Array(value)
	out := make([]udpPathRule, 0, len(list))
	for _, entry := range list {
		item, _ := entry.(map[string]any)
		rule := udpPathRule{path: jsv.Text(item["path"])}
		if items, ok := jsv.Array(item["match"]); ok {
			for _, m := range items {
				if text := jsv.Text(m); text != "" {
					rule.match = append(rule.match, text)
				}
			}
		} else if text := jsv.Text(item["match"]); text != "" {
			rule.match = []string{text}
		}
		out = append(out, rule)
	}
	return out
}

func udpPathRulesValue(rules []udpPathRule) []any {
	out := make([]any, len(rules))
	for i, r := range rules {
		match := make([]any, len(r.match))
		for j, m := range r.match {
			match[j] = m
		}
		out[i] = map[string]any{"match": match, "path": r.path}
	}
	return out
}

// pruneUDPPathRules drops the rules without a peer name from fields, and
// udp_path.peers when none remain.
func pruneUDPPathRules(fields model.Fields) {
	if _, ok := fields[udpPathPeersKey]; !ok {
		return
	}
	rules := slices.DeleteFunc(udpPathRules(fields[udpPathPeersKey]), func(r udpPathRule) bool { return len(r.match) == 0 })
	if len(rules) == 0 {
		delete(fields, udpPathPeersKey)
		return
	}
	fields[udpPathPeersKey] = udpPathRulesValue(rules)
}

// udpPathPattern checks a typed peer name or wildcard as the core does.
func udpPathPattern(text string) (string, string) {
	pattern := strings.TrimSuffix(strings.TrimSpace(text), ".")
	if pattern == "" {
		return "", ""
	}
	if _, err := path.Match(strings.ToLower(pattern), ""); err != nil {
		return "", "通配格式无效：" + pattern
	}
	return pattern, ""
}

// udpPathPeerSuggestions are the tailnet's devices matching query, as
// short names with their MagicDNS names, leaving out those taken.
func udpPathPeerSuggestions(status *api.TailscaleStatus, query string, taken []string) []kit.TokenSuggestion {
	if status == nil {
		return nil
	}
	query = strings.ToLower(strings.TrimSpace(query))
	var out []kit.TokenSuggestion
	seen := map[string]bool{}
	for _, group := range status.UserGroups {
		for _, peer := range group.Peers {
			name := peerName(peer)
			dnsName := peerDNSName(peer)
			id := strings.ToLower(name)
			if name == "" || seen[id] || slices.ContainsFunc(taken, func(t string) bool { return strings.EqualFold(t, name) }) {
				continue
			}
			if query != "" && !strings.Contains(strings.ToLower(name), query) && !strings.Contains(strings.ToLower(dnsName), query) &&
				!strings.Contains(strings.ToLower(peer.HostName), query) {
				continue
			}
			seen[id] = true
			out = append(out, kit.TokenSuggestion{ID: name, Label: name, Detail: dnsName})
		}
	}
	return out
}

// udpPathIssues checks a Tailscale endpoint's UDP path against its proxy
// chain: a blocking problem, and notes to show beside the setting. Rules
// without a peer name are ignored, as emit drops them.
func udpPathIssues(doc *model.Doc, fields model.Fields, chain []string) (problem string, notes []string) {
	defaultPath := jsv.Text(fields["udp_path.default"])
	rules := slices.DeleteFunc(udpPathRules(fields[udpPathPeersKey]), func(r udpPathRule) bool { return len(r.match) == 0 })
	configured := defaultPath != "" || len(rules) > 0
	if !configured {
		return "", nil
	}
	usesDetour := defaultPath == "detour" || slices.ContainsFunc(rules, func(r udpPathRule) bool { return r.path == "detour" })
	switch {
	case usesDetour && len(chain) == 0:
		problem = "UDP 路径「经代理」需要先设置代理链"
	case usesDetour:
		seen := map[string]bool{}
		for _, hop := range chain {
			for _, tag := range destinationBoundUDP(doc, hop, seen) {
				notes = append(notes, "「"+tag+"」按目的地绑定 UDP，需 xudp 或 packetaddr 封包编码")
			}
		}
		if hops := model.ChainWithoutUDP(doc, chain); len(hops) > 0 {
			problem = "UDP 路径「经代理」需要转发 UDP 的代理链，「" + strings.Join(hops, "」「") + "」不转发 UDP"
		}
	default:
		notes = append(notes, "未使用「经代理」，UDP 路径不生效")
	}
	if jsv.Truthy(fields["disable_udp_direct"]) {
		notes = append(notes, "「禁用 UDP 直连」已启用，对端只走 DERP")
	}
	return problem, notes
}

// destinationBoundUDP lists the outbounds behind detour, through
// Selector / URLTest members, that bind UDP to its destination: VMess
// without xudp or packetaddr.
func destinationBoundUDP(doc *model.Doc, id string, seen map[string]bool) []string {
	if seen[id] {
		return nil
	}
	seen[id] = true
	o := model.FindOutboundTarget(doc, id).Outbound
	switch {
	case o == nil:
		return nil
	case model.IsGroup(o.Type):
		var out []string
		for _, member := range model.GroupMembers(doc, o) {
			out = append(out, destinationBoundUDP(doc, member, seen)...)
		}
		return out
	case o.Type == "vmess":
		if encoding := jsv.Text(o.Fields["packet_encoding"]); encoding != "xudp" && encoding != "packetaddr" {
			return []string{o.Tag}
		}
	}
	return nil
}

// udpPathField edits udp_path.peers: rows of peer names and a path. ids
// key the rows in editor state only, so removing one keeps the others'
// inputs in place.
type udpPathField struct {
	ids     []int
	next    int
	queries map[int]string
	errors  map[int]string
}

// sync gives each of n rules an id, keeping the ids it has.
func (u *udpPathField) sync(n int) {
	if u.queries == nil {
		u.queries, u.errors = map[int]string{}, map[int]string{}
	}
	if len(u.ids) > n {
		u.ids = u.ids[:n]
	}
	for len(u.ids) < n {
		u.next++
		u.ids = append(u.ids, u.next)
	}
}

func (u *udpPathField) view(c *ui.Context, s *Section, doc *model.Doc, endpoint *model.Endpoint, effective model.Fields) {
	k := kit.T(c)
	rules := udpPathRules(endpoint.Fields[udpPathPeersKey])
	u.sync(len(rules))
	set := func(next []udpPathRule) {
		if len(next) == 0 {
			delete(endpoint.Fields, udpPathPeersKey)
			return
		}
		endpoint.Fields[udpPathPeersKey] = udpPathRulesValue(next)
	}
	status := s.rt.tailscaleStatus(endpoint.Tag)
	ui.Column(c.Key("udp-path")).Gap(8).Shrink(0).Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.Text(c, "对端 UDP 路径").FontSize(kit.FontBody).FontWeight(500)
			ui.Spacer(c)
			kit.Button(c, "添加规则", kit.ButtonOptions{Small: true, Icon: kit.SymPlus}).OnClick(func() {
				set(append(rules, udpPathRule{path: "direct"}))
				u.sync(len(rules) + 1)
			})
		})
		for i, rule := range rules {
			id := u.ids[i]
			ui.Row(c.Key(id)).Gap(8).AlignItems(ui.Start).Radius(kit.RadiusInput).Border(1, k.HairlineSoft).Background(k.Surface).Padding(8).Shrink(0).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Children(func() {
					query := u.queries[id]
					tokens := make([]kit.Token, len(rule.match))
					for j, m := range rule.match {
						tokens[j] = kit.Token{ID: m, Label: m}
					}
					add := func(next []udpPathRule, text string) bool {
						pattern, problem := udpPathPattern(text)
						if problem != "" {
							u.errors[id] = problem
							return false
						}
						if pattern != "" && !slices.ContainsFunc(next[i].match, func(m string) bool { return strings.EqualFold(m, pattern) }) {
							next[i].match = append(next[i].match, pattern)
						}
						return true
					}
					input := kit.TokenInput(c, "match", &query, kit.TokenInputOptions{
						Label: "第 " + strconv.Itoa(i+1) + " 条对端", Placeholder: "对端名或通配", Error: u.errors[id], Tokens: tokens,
						Suggestions: udpPathPeerSuggestions(status, query, rule.match),
						Commit: func(suggestion string) bool {
							next := udpPathRules(endpoint.Fields[udpPathPeersKey])
							if suggestion != "" {
								if !add(next, suggestion) {
									return false
								}
							} else {
								for _, part := range strings.FieldsFunc(query, func(r rune) bool { return r == ',' || r == '，' || r == ' ' }) {
									if !add(next, part) {
										return false
									}
								}
							}
							set(next)
							u.errors[id] = ""
							return true
						},
						Remove: func(token string) {
							next := udpPathRules(endpoint.Fields[udpPathPeersKey])
							next[i].match = slices.DeleteFunc(next[i].match, func(m string) bool { return m == token })
							set(next)
						},
					})
					if input.Changed() {
						u.errors[id] = ""
					}
					u.queries[id] = query
				})
				value := rule.path
				if kit.Select(c, "path", &value, udpPathOptions, kit.SelectOptions{Width: 104, Label: "第 " + strconv.Itoa(i+1) + " 条路径"}).Changed() {
					next := udpPathRules(endpoint.Fields[udpPathPeersKey])
					next[i].path = value
					set(next)
				}
				kit.IconButton(c, kit.SymTrash2, "删除规则", kit.IconButtonOptions{Danger: true}).OnClick(func() {
					next := udpPathRules(endpoint.Fields[udpPathPeersKey])
					set(slices.Delete(next, i, i+1))
					u.ids = slices.Delete(u.ids, i, i+1)
					delete(u.queries, id)
					delete(u.errors, id)
				})
			})
		}
		problem, notes := udpPathIssues(doc, effective, endpoint.Chain)
		if problem != "" {
			note(c, kit.DangerTone, problem)
		}
		for _, message := range notes {
			note(c, kit.WarningTone, message)
		}
	})
}
