package kit

import "slices"

// FacetFilter is the conditions of a filtered list, for faceted counts: a
// filter group lists and counts its options over the records passing
// every condition but its own, so that choosing in one group narrows the
// others and not itself, and a segmented control counts its segments over
// the records passing every condition but the segment's. One pass over
// the records answers the list and every group:
//
//	var f kit.FacetFilter[*Row]
//	f.Add("mode", inMode)
//	f.Add("process", func(r *Row) bool { return r.Process == chosen })
//	f.Add("", matchesSearch)
//	for _, r := range rows {
//		miss := f.Miss(r)
//		if miss == kit.FacetNone {
//			continue
//		}
//		if miss == kit.FacetAll {
//			shown = append(shown, r)
//		}
//		if f.Counts(miss, "process") {
//			processes[r.Process]++
//		}
//	}
//
// A group without a condition, nothing chosen in it, counts the records
// passing them all.
type FacetFilter[R any] struct {
	conds []func(R) bool
	// own is the condition of each group.
	own map[string]int
}

// Miss values: a record passing every condition, and one failing two or
// more, which counts nowhere.
const (
	FacetAll  = -1
	FacetNone = -2
)

// Add adds the condition pass of group, "" for one of no group, as the
// search or a hidden limit. A group has one condition.
func (f *FacetFilter[R]) Add(group string, pass func(R) bool) {
	if group != "" {
		if f.own == nil {
			f.own = map[string]int{}
		}
		f.own[group] = len(f.conds)
	}
	f.conds = append(f.conds, pass)
}

// Miss evaluates r: FacetAll when it passes every condition, the index of
// the only one it fails, else FacetNone.
func (f *FacetFilter[R]) Miss(r R) int {
	miss := FacetAll
	for i, pass := range f.conds {
		if pass(r) {
			continue
		}
		if miss != FacetAll {
			return FacetNone
		}
		miss = i
	}
	return miss
}

// Counts reports whether a record of Miss miss counts toward group: it
// passes every condition but, at most, the group's own.
func (f *FacetFilter[R]) Counts(miss int, group string) bool {
	if miss == FacetAll {
		return true
	}
	i, ok := f.own[group]
	return ok && miss == i
}

// FacetChoice is the chosen options of each filter group, several per
// group: a record passes a group holding any of its chosen values (or)
// and must pass every group (and). Paired with FacetFilter, a group's own
// choice never narrows its options. A group with nothing chosen has no
// entry, so len(c) > 0 reports that something is chosen.
type FacetChoice[K comparable] map[K][]string

// Has reports whether value is chosen in group.
func (c FacetChoice[K]) Has(group K, value string) bool {
	return slices.Contains(c[group], value)
}

// Toggle chooses value in group, or unchooses it when chosen.
func (c FacetChoice[K]) Toggle(group K, value string) {
	if i := slices.Index(c[group], value); i >= 0 {
		c.remove(group, i)
		return
	}
	c[group] = append(c[group], value)
}

// Add chooses value in group, keeping the others chosen.
func (c FacetChoice[K]) Add(group K, value string) {
	if value != "" && !c.Has(group, value) {
		c[group] = append(c[group], value)
	}
}

// IsOnly reports whether value is the one value chosen in group.
func (c FacetChoice[K]) IsOnly(group K, value string) bool {
	return len(c[group]) == 1 && c[group][0] == value
}

// Only keeps value alone chosen in group, as a menu's "Show only this" does, or
// unchooses the group when value already is its one choice.
func (c FacetChoice[K]) Only(group K, value string) {
	if c.IsOnly(group, value) {
		delete(c, group)
		return
	}
	c[group] = []string{value}
}

func (c FacetChoice[K]) remove(group K, i int) {
	values := slices.Delete(slices.Clone(c[group]), i, i+1)
	if len(values) == 0 {
		delete(c, group)
		return
	}
	c[group] = values
}

// Clone copies c.
func (c FacetChoice[K]) Clone() FacetChoice[K] {
	next := make(FacetChoice[K], len(c))
	for group, values := range c {
		next[group] = slices.Clone(values)
	}
	return next
}

// Match is the condition of group over a record's value of it: true for
// any chosen value, nil when nothing is chosen in group.
func (c FacetChoice[K]) Match(group K) func(value string) bool {
	values := c[group]
	switch len(values) {
	case 0:
		return nil
	case 1:
		v := values[0]
		return func(value string) bool { return value == v }
	}
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return func(value string) bool { return set[value] }
}
