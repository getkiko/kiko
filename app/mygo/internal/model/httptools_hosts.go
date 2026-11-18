package model

import "strings"

// Host pattern validation of the HTTPS decryption lists.

func trimHostEdge(s string) string {
	return strings.Trim(s, " \t\r\n")
}

// NormalizeMITMHostname canonicalises one HTTPS decryption host pattern:
// an exact host (example.com) or a wildcard (*.example.com), lower-cased.
// ok is false for empty values, a bare "*", ports, schemes, paths, spaces
// and a "*" anywhere but the leading "*." label.
func NormalizeMITMHostname(raw string) (string, bool) {
	s := strings.ToLower(trimHostEdge(raw))
	if s == "" {
		return "", false
	}
	if len(s) > 253 {
		return "", false
	}
	rest, wildcard := strings.CutPrefix(s, "*.")
	// Same rules as the core's hostmatch: labels of a-z 0-9 _ -, and a
	// wildcard covers at least two labels. Names are ASCII (punycode).
	if wildcard && !strings.Contains(rest, ".") {
		return "", false
	}
	for _, label := range strings.Split(rest, ".") {
		if label == "" || len(label) > 63 {
			return "", false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return "", false
			}
		}
	}
	return s, true
}

// MITMHostList is the result of NormalizeMITMHostnames.
type MITMHostList struct {
	Hosts   []string `json:"hosts"`
	Invalid []string `json:"invalid"`
}

// NormalizeMITMHostnames normalises and de-duplicates a host list, keeping
// the first occurrence order; invalid non-blank entries are returned apart.
func NormalizeMITMHostnames(list []string) MITMHostList {
	out := MITMHostList{Hosts: []string{}, Invalid: []string{}}
	seen := map[string]bool{}
	for _, raw := range list {
		host, ok := NormalizeMITMHostname(raw)
		if !ok {
			if t := trimHostEdge(raw); t != "" {
				out.Invalid = append(out.Invalid, t)
			}
			continue
		}
		if seen[host] {
			continue
		}
		seen[host] = true
		out.Hosts = append(out.Hosts, host)
	}
	return out
}
