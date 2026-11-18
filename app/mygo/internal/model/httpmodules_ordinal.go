package model

// ModuleRuleOrdinals numbers the module routing rules of ModuleRouteRules as
// the route table shows them: 1-based, continuing after the user's own
// pre-device rules. The route table, the topology and the emitter's rule map share
// this numbering, so connections name a module rule by the number the table
// shows.
func ModuleRuleOrdinals(doc *Doc) map[string]int {
	rules := ModuleRouteRules(doc)
	if len(rules) == 0 {
		return nil
	}
	start := len(RouteRuleOrdinals(doc, PreDeviceID))
	out := make(map[string]int, len(rules))
	for i, r := range rules {
		out[r.ID] = start + i + 1
	}
	return out
}
