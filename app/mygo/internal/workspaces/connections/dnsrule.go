package connections

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// dnsRuleMap is the DNS rule map of the applied configuration, read again
// for each query the inspector shows, so that an apply in between names
// the rules of the configuration in effect.
type dnsRuleMap struct {
	// query keys the query the last read is for: its core instance and ID.
	query string
	token int
	// rules were read for the query keyed rulesFor.
	rules    api.DNSRuleMap
	rulesFor string
	// retryAt is when a failed read is tried again for the same query.
	retryAt time.Time
}

// reset forgets the map and any read in flight, as the core's instance
// changes and its queries are numbered again.
func (r *dnsRuleMap) reset() {
	*r = dnsRuleMap{token: r.token + 1}
}

// dnsRuleRetry is how long a failed read of the DNS rule map waits before
// the inspector reads it again.
const dnsRuleRetry = 5 * time.Second

// dnsRules is the DNS rule map for the query id of instance, read in the
// background when the inspector turns to it; none until it is read, or
// while a read failed. A failed read is tried again dnsRuleRetry later, or
// as another query shows.
func (m *dnsMonitor) dnsRules(c *ui.Context, instance, id string) api.DNSRuleMap {
	r := &m.rules
	key := instance + "\x00" + id
	if m.env == nil || m.env.API == nil {
		return api.DNSRuleMap{}
	}
	read := r.query != key
	if !read && !r.retryAt.IsZero() {
		if left := time.Until(r.retryAt); left > 0 {
			c.After(left)
		} else {
			read = true
		}
	}
	if read {
		r.query, r.retryAt = key, time.Time{}
		r.token++
		token := r.token
		env.Go(m.env, func(ctx context.Context) (api.DNSRuleMap, error) { return m.env.API.DNSRuleMap(ctx) }, func(rules api.DNSRuleMap, err error) {
			if token != r.token {
				return
			}
			if err != nil {
				r.rules, r.rulesFor = api.DNSRuleMap{}, ""
				r.retryAt = time.Now().Add(dnsRuleRetry)
				return
			}
			r.rules, r.rulesFor = rules, key
		})
	}
	if r.rulesFor != key {
		return api.DNSRuleMap{}
	}
	return r.rules
}

// dnsMatchedRule is the DNS table rule that decided q: the last rule its
// policy trace matched other than one only setting options, or dns.final
// (index -1) for a client query a resolver answered without one. A query
// that began before the map's configuration was applied has none.
func dnsMatchedRule(q *api.DNSQuery, rules api.DNSRuleMap) (api.RuleRef, bool) {
	if len(rules.Rules) == 0 || q.Role == "lookup_operation" {
		return api.RuleRef{}, false
	}
	if started := parseTime(q.StartedAt); started.IsZero() || started.Before(rules.AppliedAt) {
		return api.RuleRef{}, false
	}
	index, rule, action := -1, "", ""
	for _, line := range q.Policy {
		i, r, a, ok := dnsPolicyMatch(line)
		if ok && !strings.HasPrefix(a, "route-options") {
			index, rule, action = i, r, a
		}
	}
	if index < 0 && (q.Role != "query" || q.Resolver == "") {
		return api.RuleRef{}, false
	}
	if index >= 0 && !dnsRuleAnswered(q, rule, action) {
		return api.RuleRef{}, false
	}
	for _, ref := range rules.Rules {
		if ref.Index == index && ref.Section == "dns" {
			return ref, true
		}
	}
	return api.RuleRef{}, false
}

// responseCondition finds a condition on the response's addresses, which
// a rule the trace matched may still fail once the answer arrives.
var responseCondition = regexp.MustCompile(`(^|[\s(\[])(ip_cidr|ip_is_private|ip_accept_any)=`)

// dnsRuleAnswered reports whether the rule the trace matched last also
// answered q: a route rule whose server is not q's resolver passed it on
// (server missing, fake-ip skipped) and a rule on the response's
// addresses may have failed after matching, both then answered by
// dns.final, so neither is named.
func dnsRuleAnswered(q *api.DNSQuery, rule, action string) bool {
	if responseCondition.MatchString(rule) {
		return false
	}
	body, ok := strings.CutPrefix(action, "route(")
	if !ok {
		return true
	}
	if q.Resolver == "" {
		return false
	}
	// "route(<server>,<options>)", the server of a concurrent query being
	// "[<a>, <b>]".
	var tag string
	var servers []string
	if end := strings.Index(body, "]"); strings.HasPrefix(body, "[") && end > 0 {
		tag = body[:end+1]
		servers = strings.Split(body[1:end], ", ")
	} else {
		tag, _, _ = strings.Cut(strings.TrimSuffix(body, ")"), ",")
		servers = []string{tag}
	}
	return q.Resolver == tag || slices.Contains(servers, q.Resolver)
}

// dnsPolicyMatch reads a policy trace line "match[<index>] <rule> =>
// <action>".
func dnsPolicyMatch(line string) (int, string, string, bool) {
	rest, ok := strings.CutPrefix(line, "match[")
	if !ok {
		return 0, "", "", false
	}
	number, rest, ok := strings.Cut(rest, "]")
	if !ok {
		return 0, "", "", false
	}
	index, err := strconv.Atoi(number)
	if err != nil || index < 0 {
		return 0, "", "", false
	}
	rule, action := rest, ""
	if at := strings.LastIndex(rest, " => "); at >= 0 {
		rule, action = rest[:at], rest[at+len(" => "):]
	}
	return index, strings.TrimSpace(rule), action, true
}

// ruleProperty is the Rule row of q: the DNS table rule, or the device's
// fallback DNS, that decided it, linking to its row in Routing.
func (m *dnsMonitor) ruleProperty(c *ui.Context, q *api.DNSQuery) {
	ref, ok := dnsMatchedRule(q, m.dnsRules(c, m.displayed().instance, q.ID))
	if !ok || !ref.Final && ref.ID == "" {
		return
	}
	label, hint := "兜底 DNS", "在路由中查看兜底 DNS"
	if !ref.Final {
		label = fmt.Sprintf("%d · %s", ref.Ordinal, ref.Name)
		hint = "在路由中查看规则 " + label
	}
	target := env.RouteRule{ID: ref.ID, Owner: ref.Owner, Section: "dns", Final: ref.Final}
	kit.LinkProperty(c, "规则", label, hint, func() { m.env.Navigate(env.PageRoute, target) })
}
