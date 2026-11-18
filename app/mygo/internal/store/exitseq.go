package store

import (
	"slices"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// ExitSequence is where a scope sends its traffic, in rule order: for each
// enabled rule its outbound ("route:<id>") or its other action
// ("action:<name>"), then the default exit ("final:<id>"). Names, matchers
// and disabled rules are not part of it, so a copy of a profile has the
// sequence of its source.
func ExitSequence(doc *model.Doc, scope string) []string {
	var seq []string
	for _, r := range model.RouteRulesOf(doc, scope) {
		if !r.Enabled {
			continue
		}
		if r.Action == "" || r.Action == model.ActionRoute {
			seq = append(seq, "route:"+r.Outbound.ID())
		} else {
			seq = append(seq, "action:"+r.Action)
		}
	}
	if _, _, ok := model.ScopeDevice(doc, scope); ok {
		seq = append(seq, "final:"+model.CoreDefaultFinal(doc, scope))
	}
	return seq
}

// ExitSequencesDiffer reports whether connections of scope from would go
// elsewhere under scope to: their exit sequences are not equal.
func ExitSequencesDiffer(doc *model.Doc, from, to string) bool {
	return !slices.Equal(ExitSequence(doc, from), ExitSequence(doc, to))
}
