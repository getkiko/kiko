package settings

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

// This file holds the rules of the network and TLS config libraries: their
// editor sections and fields, and how a library is saved.

// librarySection is a titled part of a library config's editor.
type librarySection struct {
	name string
	keys []string
}

var networkSections = []librarySection{
	{"套接字", []string{"bind_interface", "inet4_bind_address", "inet6_bind_address", "bind_address_no_port", "routing_mark", "reuse_addr", "netns", "protect_path"}},
	{"连接", []string{"connect_timeout"}},
	{"TCP", []string{"tcp_fast_open", "tcp_multi_path", "disable_tcp_keep_alive", "tcp_keep_alive", "tcp_keep_alive_interval"}},
	{"UDP", []string{"udp_fragment", "udp_timeout"}},
	{"网络选择", []string{"network_strategy", "network_type", "fallback_network_type", "fallback_delay"}},
}

// tlsSections sorts TLS fields into the editor's sections, the rest last.
func tlsSections(fields []schema.Field) []librarySection {
	patterns := []struct {
		name string
		re   *regexp.Regexp
	}{
		{"连接安全", regexp.MustCompile(`^tls\.(insecure|disable_sni|alpn|min_version|max_version|cipher_suites|curve_preferences)$`)},
		{"握手与伪装", regexp.MustCompile(`^tls\.(engine|handshake_timeout|spoof|spoof_method)$`)},
		{"证书与认证", regexp.MustCompile(`^tls\.(certificate|key|client_)`)},
		{"ECH", regexp.MustCompile(`^tls\.ech\.`)},
	}
	classified := map[string]bool{}
	var sections []librarySection
	for _, p := range patterns {
		s := librarySection{name: p.name}
		for _, f := range fields {
			if p.re.MatchString(f.Key) {
				s.keys = append(s.keys, f.Key)
				classified[f.Key] = true
			}
		}
		sections = append(sections, s)
	}
	rest := librarySection{name: "分片与内核 TLS"}
	for _, f := range fields {
		if !classified[f.Key] {
			rest.keys = append(rest.keys, f.Key)
		}
	}
	sections = append(sections, rest)
	return slices.DeleteFunc(sections, func(s librarySection) bool { return len(s.keys) == 0 })
}

// editorFields presents a section's fields in the library editor: all at
// the base level, labels without their sing-box key, platforms as a hint.
func editorFields(all []schema.Field, keys []string) []schema.Field {
	var out []schema.Field
	for _, f := range all {
		if !slices.Contains(keys, f.Key) {
			continue
		}
		parts := strings.Split(f.Key, ".")
		last := parts[len(parts)-1]
		if strings.HasSuffix(f.Key, ".short_id") {
			f.Label = strings.ReplaceAll(f.Label, "short_id", "短 ID")
		} else {
			f.Label = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(f.Label, f.Key, ""), last, ""))
		}
		var hints []string
		if f.Hint != last && f.Hint != "" {
			hints = append(hints, f.Hint)
		}
		if f.Platforms != nil {
			labels := make([]string, len(f.Platforms))
			for i, p := range f.Platforms {
				labels[i] = schema.PlatformLabel[p]
			}
			hints = append(hints, "适用 "+strings.Join(labels, "、"))
		}
		f.Hint = strings.Join(hints, " · ")
		f.HintByPlatform = nil
		f.Adv, f.Group, f.Platforms = false, "", nil
		if f.Key == "inet6_bind_address" {
			f.Placeholder = "::"
		}
		out = append(out, f)
	}
	return out
}

// usageProfiles are the saved profiles with the active one's current
// document, or the document alone when there are none.
func usageProfiles(profiles []*model.Profile, activeID string, doc *model.Doc) []*model.Profile {
	out := make([]*model.Profile, 0, len(profiles)+1)
	for _, p := range profiles {
		if p.ID == activeID {
			copied := *p
			copied.Doc = doc
			p = &copied
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		id := activeID
		if id == "" {
			id = "current"
		}
		out = append(out, &model.Profile{ID: id, Name: "当前配置", Doc: doc})
	}
	return out
}

// errLibrary is a library change refused by checkLibrary.
var errLibrary = errors.New("保存失败，编辑内容已保留，请重试")

// checkLibrary checks a new library before it replaces old, as
// saveNetworkConfigs does: every config valid, none removed while in use,
// and every profile's references complete with it.
func checkLibrary(library, old []*model.NetworkConfig, profiles []*model.Profile) error {
	for _, c := range library {
		if model.ValidateNetworkConfig(c) != "" {
			return errLibrary
		}
	}
	for _, previous := range old {
		if findConfigByID(library, previous.ID) == nil && len(model.NetworkUses(profiles, previous.ID)) > 0 {
			return errLibrary
		}
	}
	for _, p := range profiles {
		if problems := emit.Emit(p.Doc, emit.Options{NetworkConfigs: library}).DependencyErrors; len(problems) > 0 {
			return fmt.Errorf("配置引用不完整：%s", problems[0])
		}
	}
	return nil
}

func findConfigByID(library []*model.NetworkConfig, id string) *model.NetworkConfig {
	for _, c := range library {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// uniqueConfigName is base, or base with the first free number.
func uniqueConfigName(base string, library []*model.NetworkConfig) string {
	taken := func(name string) bool {
		return slices.ContainsFunc(library, func(c *model.NetworkConfig) bool { return c.Name == name })
	}
	name := base
	for n := 2; taken(name); n++ {
		name = fmt.Sprintf("%s %d", base, n)
	}
	return name
}

// cleanFields drops empty values before a config is saved.
func cleanFields(fields model.Fields) model.Fields {
	out := model.Fields{}
	for k, v := range fields {
		if v == nil || v == "" {
			continue
		}
		out[k] = v
	}
	return out
}
