package routes

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// moveState is a rule or group moving with the keyboard: Space picks it
// up, Up and Down move it, Space or Enter put it down and Escape puts it
// back where it was. It keeps the section's order as it was picked up:
// the ids of the groups and of the rules, and the moved rule's group.
type moveState struct {
	key    string
	groups []string
	rules  []string
	// group is the moved rule's group as it was picked up.
	group string
}

// pickUp records the order of the section's groups and rules as row key
// is picked up.
func pickUp(doc *model.Doc, sec section, key string) *moveState {
	m := &moveState{key: key}
	groups := doc.Route.Groups
	if sec == sectionDNS {
		groups = doc.DNS.Groups
		for _, r := range doc.DNS.Rules {
			m.rules = append(m.rules, r.ID)
			if "rule:"+r.ID == key {
				m.group = r.GroupID
			}
		}
	} else {
		for _, r := range doc.Route.Rules {
			m.rules = append(m.rules, r.ID)
			if "rule:"+r.ID == key {
				m.group = r.GroupID
			}
		}
	}
	for _, g := range groups {
		m.groups = append(m.groups, g.ID)
	}
	return m
}

// restoreOrder puts the items of current that saved lists back in its
// order, in the places they hold now; the others stay where they are.
func restoreOrder[T any](current []T, saved []string, id func(T) string) []T {
	rank := map[string]int{}
	for i, s := range saved {
		rank[s] = i
	}
	var slots []int
	var items []T
	for i, item := range current {
		if _, ok := rank[id(item)]; ok {
			slots = append(slots, i)
			items = append(items, item)
		}
	}
	slices.SortStableFunc(items, func(a, b T) int { return rank[id(a)] - rank[id(b)] })
	out := slices.Clone(current)
	for i, slot := range slots {
		out[slot] = items[i]
	}
	return out
}

// putBack restores the order m recorded in the section of doc, keeping
// the changes made since to anything else; it reports whether the order
// changed.
func (m *moveState) putBack(doc *model.Doc, sec section) bool {
	groupID := func(g *model.RouteGroup) string { return g.ID }
	ruleID := strings.TrimPrefix(m.key, "rule:")
	moved := strings.HasPrefix(m.key, "rule:")
	if sec == sectionDNS {
		groups := restoreOrder(doc.DNS.Groups, m.groups, groupID)
		rules := restoreOrder(doc.DNS.Rules, m.rules, func(r *model.DNSRule) string { return r.ID })
		changed := !slices.Equal(groups, doc.DNS.Groups) || !slices.Equal(rules, doc.DNS.Rules)
		for i, r := range rules {
			if moved && r.ID == ruleID && r.GroupID != m.group {
				c := *r
				c.GroupID = m.group
				rules[i], changed = &c, true
			}
		}
		doc.DNS.Groups, doc.DNS.Rules = groups, rules
		return changed
	}
	groups := restoreOrder(doc.Route.Groups, m.groups, groupID)
	rules := restoreOrder(doc.Route.Rules, m.rules, func(r *model.RouteRule) string { return r.ID })
	changed := !slices.Equal(groups, doc.Route.Groups) || !slices.Equal(rules, doc.Route.Rules)
	for i, r := range rules {
		if moved && r.ID == ruleID && r.GroupID != m.group {
			c := *r
			c.GroupID = m.group
			rules[i], changed = &c, true
		}
	}
	doc.Route.Groups, doc.Route.Rules = groups, rules
	return changed
}

// keyboardMove handles the moving keys of row i, keyed by key, on its
// button b.
func (w *Workspace) keyboardMove(b ui.Element, t policyTable, i int, key string) {
	if w.search != "" || w.editor != nil {
		return
	}
	moving := w.moving != nil && w.moving.key == key
	if b.Shortcut(0, ui.KeySpace) {
		if moving {
			w.moving = nil
			return
		}
		w.moving = pickUp(w.doc(), w.sec, key)
		return
	}
	if !moving {
		return
	}
	if b.Shortcut(0, ui.KeyEnter) {
		w.moving = nil
		return
	}
	if b.Shortcut(0, ui.KeyEscape) {
		saved := w.moving
		w.moving = nil
		next := model.Clone(w.doc())
		if saved.putBack(next, w.sec) {
			w.save(func(doc *model.Doc) error { *doc = *next; return nil })
		}
		return
	}
	to := -1
	switch {
	case b.Shortcut(0, ui.KeyUp):
		to = i - 1
		if t.rows[i].kind == rowGroup {
			// Before the group above.
			to = -1
			for j := i - 1; j >= 0; j-- {
				if t.rows[j].kind == rowGroup {
					to = j
					break
				}
			}
		}
	case b.Shortcut(0, ui.KeyDown):
		to = i + 2
		if t.rows[i].kind == rowGroup {
			// After the group below.
			to = -1
			seen := 0
			for j := i + 1; j < len(t.rows); j++ {
				if t.rows[j].kind == rowGroup {
					seen++
					if seen == 2 {
						to = j
						break
					}
				}
			}
			if seen == 1 {
				to = len(t.rows)
			}
		}
	}
	if to < 0 || to > len(t.rows) {
		return
	}
	if plan, ok := t.planReorder(i, to); ok {
		w.reorder(plan)
	}
}
