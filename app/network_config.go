package main

import (
	"encoding/json"
	"strings"
)

const networkSyncPrefix = "network:"

type networkConfig struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	ConfigKind string          `json:"kind,omitempty"`
	Fields     json.RawMessage `json:"fields"`
	UpdatedAt  int64           `json:"updatedAt"`
}

type networkSyncDocument struct {
	ConfigKind string          `json:"configKind"`
	Fields     json.RawMessage `json:"fields"`
}

func networkConfigDocument(c networkConfig) json.RawMessage {
	if c.ConfigKind == "" || c.ConfigKind == "network" {
		return c.Fields
	}
	raw, _ := json.Marshal(networkSyncDocument{ConfigKind: c.ConfigKind, Fields: c.Fields})
	return raw
}

func networkMergeEntries(raw json.RawMessage) []mergeProfile {
	var configs []networkConfig
	if json.Unmarshal(raw, &configs) != nil {
		return nil
	}
	var out []mergeProfile
	for _, c := range configs {
		if c.ID == "" || len(c.Fields) == 0 {
			continue
		}
		doc := networkConfigDocument(c)
		out = append(out, mergeProfile{ID: networkSyncPrefix + c.ID, Name: c.Name, Doc: doc, UpdatedAt: c.UpdatedAt, Hash: syncProfileHash(c.Name, doc), Kind: "network"})
	}
	return out
}

// Shared entries use a separate namespace within the existing three-way merge.
func storeSyncEntries(st storeState) []storeProfile {
	out := parseStoreProfiles(st.Profiles)
	for _, c := range networkMergeEntries(st.NetworkConfigs) {
		out = append(out, storeProfile{ID: c.ID, Name: c.Name, Doc: c.Doc, SavedAt: c.UpdatedAt})
	}
	return out
}
func splitNetworkEntries(entries []mergeProfile) ([]mergeProfile, []networkConfig) {
	var profiles []mergeProfile
	configs := []networkConfig{}
	for _, e := range entries {
		if e.Kind == "network" {
			config := networkConfig{ID: strings.TrimPrefix(e.ID, networkSyncPrefix), Name: e.Name, Fields: e.Doc, UpdatedAt: e.UpdatedAt}
			var wrapped networkSyncDocument
			if json.Unmarshal(e.Doc, &wrapped) == nil && wrapped.ConfigKind != "" && len(wrapped.Fields) > 0 {
				config.ConfigKind = wrapped.ConfigKind
				config.Fields = wrapped.Fields
			}
			configs = append(configs, config)
		} else {
			profiles = append(profiles, e)
		}
	}
	return profiles, configs
}
