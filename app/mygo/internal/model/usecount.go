package model

// OutboundUseCount is how many routes send traffic to outbound id: the
// enabled rules routing to it, but not those of the local scope while it
// is removed (the configuration leaves them out), and the scopes whose
// default outbound it is — each device profile's, the local scope's while it
// exists (inheriting route.final while unset) and the other devices'
// (route.final). Devices have no switch of their own. The Direct and Reject
// cards show it as "used in N places".
func OutboundUseCount(doc *Doc, id string) int {
	if doc == nil || id == "" {
		return 0
	}
	n := 0
	local := LocalEnabled(doc)
	for _, rule := range doc.Route.Rules {
		if !local && IsLocalScope(rule.PolicyOwner()) {
			continue
		}
		if rule.Enabled && rule.Action == ActionRoute && rule.Outbound.ID() == id {
			n++
		}
	}
	for _, d := range doc.Route.Devices {
		for _, final := range DeviceFinalOutbounds(d) {
			if final == id {
				n++
			}
		}
	}
	for _, scope := range LocalScopes(doc) {
		if RouteFinalFor(doc, scope).ID() == id {
			n++
		}
	}
	if RouteFinalFor(doc, OtherDeviceID).ID() == id {
		n++
	}
	return n
}
