// Package profiles is the logic behind the overview's profiles: backups
// and their import, configurations downloaded from a URL, sing-box
// configurations turned into documents, the configuration preview's
// search and redaction, sharing and the profile list's operations.
package profiles

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Bundle format of profile backups.
const (
	BundleFormat = "kiko-profiles"
	BundleSchema = 2
)

// legacyBundleFormat marks backups exported before the app was renamed
// Kiko; they import like BundleFormat.
const legacyBundleFormat = "sbcfg-profiles"

type bundle struct {
	Format         string                 `json:"format"`
	Schema         int                    `json:"schema"`
	Profiles       []*model.Profile       `json:"profiles"`
	NetworkConfigs []*model.NetworkConfig `json:"networkConfigs"`
}

// SerializeBundle writes profiles as a backup, with the library's network
// configs they use, or all of them when includeAll.
func SerializeBundle(profiles []*model.Profile, library []*model.NetworkConfig, includeAll bool) ([]byte, error) {
	used := map[string]bool{}
	for _, p := range profiles {
		for _, refs := range networkRefs(p.Doc) {
			for _, id := range refs {
				used[id] = true
			}
		}
	}
	included := []*model.NetworkConfig{}
	for _, c := range library {
		if includeAll || used[c.ID] {
			included = append(included, c)
		}
	}
	if profiles == nil {
		profiles = []*model.Profile{}
	}
	return marshalIndent(bundle{Format: BundleFormat, Schema: BundleSchema, Profiles: profiles, NetworkConfigs: included})
}

func isObject(v any) bool {
	_, ok := v.(map[string]any)
	return ok
}

// isInternalDoc reports whether value is an Kiko document rather than a
// sing-box configuration.
func isInternalDoc(value map[string]any) bool {
	_, platform := value["platform"].(string)
	_, inbounds := value["inbounds"].([]any)
	_, outbounds := value["outbounds"].([]any)
	_, subscriptions := value["subscriptions"].([]any)
	return value["version"] == float64(1) && platform && isObject(value["route"]) && isObject(value["dns"]) &&
		isObject(value["experimental"]) && inbounds && outbounds && subscriptions
}

// ParseImport reads a file to import: a backup bundle, a list of profiles,
// a profile, an Kiko document or a sing-box configuration (made a
// document for platform). Entries without a version 1 document are
// skipped.
func ParseImport(content []byte, platform string) ([]*model.Profile, []*model.NetworkConfig, error) {
	var parsed any
	if err := json.Unmarshal(content, &parsed); err != nil {
		return nil, nil, fmt.Errorf("JSON 解析失败：%w", err)
	}
	var candidates []any
	var library []*model.NetworkConfig
	switch v := parsed.(type) {
	case []any:
		candidates = v
	case map[string]any:
		isBundle := v["format"] == BundleFormat || v["format"] == legacyBundleFormat
		if isBundle && v["schema"] != float64(BundleSchema) {
			return nil, nil, errors.New("备份版本需要升级应用后导入")
		}
		_, hasOutbounds := v["outbounds"].([]any)
		switch {
		case isBundle:
			candidates, _ = v["profiles"].([]any)
			if raw, ok := v["networkConfigs"].([]any); ok {
				encoded, _ := json.Marshal(raw)
				if err := json.Unmarshal(encoded, &library); err != nil {
					return nil, nil, errors.New("备份中的网络配置无效")
				}
			}
		case isInternalDoc(v):
			candidates = []any{map[string]any{"name": "导入配置", "doc": v}}
		case hasOutbounds:
			doc, err := ImportSingBoxConfig(content, platform)
			if err != nil {
				return nil, nil, err
			}
			return []*model.Profile{{Name: "导入配置", Doc: doc}}, []*model.NetworkConfig{}, nil
		case isObject(v["doc"]):
			candidates = []any{v}
		case v["version"] == float64(1):
			candidates = []any{map[string]any{"name": "导入配置", "doc": v}}
		}
	default:
		return nil, nil, errors.New("配置文件必须是 JSON 对象或配置列表")
	}
	profiles := []*model.Profile{}
	for _, candidate := range candidates {
		object, ok := candidate.(map[string]any)
		if !ok {
			continue
		}
		doc, ok := object["doc"].(map[string]any)
		if !ok || doc["version"] != float64(1) {
			continue
		}
		raw, _ := json.Marshal(object)
		var profile model.Profile
		// Like stored profiles, a value of the wrong type (a numeric name,
		// a fractional savedAt) is skipped rather than the whole profile,
		// which the former TypeScript console imported as it was.
		if model.UnmarshalLenient(raw, &profile) != nil || profile.Doc == nil {
			continue
		}
		// A document saved before ipv6Enabled existed infers it from its
		// inbounds (fillDefaults' "doc.ipv6Enabled ?? …"); decoded into
		// the bool it read as off, and FillDefaults then moved its IPv6
		// listeners and strategies to IPv4.
		if _, ok := doc["ipv6Enabled"]; !ok {
			rawDoc, _ := json.Marshal(doc)
			if filled := model.DecodeDoc(rawDoc); filled != nil {
				profile.Doc.IPv6Enabled = filled.IPv6Enabled
			}
		}
		if err := model.CheckDeviceImagesTotal(profile.Doc); err != nil {
			return nil, nil, fmt.Errorf("「%s」的%w", profile.Name, err)
		}
		profiles = append(profiles, &profile)
	}
	if library == nil {
		library = []*model.NetworkConfig{}
	}
	return profiles, library, nil
}
