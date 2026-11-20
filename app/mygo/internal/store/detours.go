package store

// AppliedDetours maps each outbound and endpoint of the configuration the
// core runs to its detour, the outbound it dials through ("前置代理"); those
// without one are left out. It is nil until the applied configuration is
// read, which it asks for while the core runs.
func (s *Store) AppliedDetours() map[string]string {
	st := &s.applied
	if !st.known {
		if server := s.env.Server; server != nil && server.HasRuntime && server.Runtime.Running() && st.requested != server.Runtime.StartedAt {
			st.requested = server.Runtime.StartedAt
			s.RefreshApplied()
		}
		return nil
	}
	if st.detoursGeneration == st.generation && st.detours != nil {
		return st.detours
	}
	st.detours = appliedDetours(st.config)
	st.detoursGeneration = st.generation
	return st.detours
}

// appliedDetours reads the detours of a sing-box configuration's outbounds
// and endpoints, by tag.
func appliedDetours(config map[string]any) map[string]string {
	detours := map[string]string{}
	for _, section := range []string{"outbounds", "endpoints"} {
		list, _ := config[section].([]any)
		for _, item := range list {
			o, _ := item.(map[string]any)
			tag, _ := o["tag"].(string)
			detour, _ := o["detour"].(string)
			if tag != "" && detour != "" {
				detours[tag] = detour
			}
		}
	}
	return detours
}
