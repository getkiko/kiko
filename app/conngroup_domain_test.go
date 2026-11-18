package main

import "testing"

// Domain groups use the registrable domain or the full one; connections
// without a domain share one IP group.
func TestDomainConnectionGroups(t *testing.T) {
	rows := []*connRow{}
	for i, domain := range []string{"a.b.example.com", "www.example.com", "www.foo.com.hk", "x.github.io", "", "1.2.3.4"} {
		c := sample
		c.ID, c.Domain = string(rune('a'+i)), domain
		rows = append(rows, &connRow{Conn: c})
	}
	labels := func(mode connGroupMode) map[string]int {
		out := map[string]int{}
		for _, n := range buildConnectionTree(rows, nil, mode, colHost, false) {
			out[n.Label] = len(n.Rows)
		}
		return out
	}
	root := labels(groupRootDomain)
	if len(root) != 4 || root["example.com"] != 2 || root["foo.com.hk"] != 1 || root["x.github.io"] != 1 || root[connIPGroupLabel] != 2 {
		t.Errorf("root domain groups %v", root)
	}
	host := labels(groupDomain)
	if len(host) != 5 || host["a.b.example.com"] != 1 || host["www.example.com"] != 1 || host[connIPGroupLabel] != 2 {
		t.Errorf("domain groups %v", host)
	}
}
