package model

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

var reListSeparator = regexp.MustCompile(`[,\n]`)

// Values reads a list field as the console stores it over time: an array,
// or a comma or newline separated string. Items are trimmed and empty ones
// dropped.
func Values(value any) []string {
	if list, ok := jsv.Array(value); ok {
		out := []string{}
		for _, item := range list {
			if s := strings.TrimSpace(jsv.String(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	if value == nil {
		return []string{}
	}
	return splitText(jsv.String(value))
}

func splitText(s string) []string {
	out := []string{}
	for _, part := range reListSeparator.Split(s, -1) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// SplitList is Values for arrays and strings only: other values are
// empty.
func SplitList(v any) []string {
	if list, ok := jsv.Array(v); ok {
		return Values(list)
	}
	if s, ok := v.(string); ok {
		return splitText(s)
	}
	return []string{}
}

// unique keeps the first of each value.
func unique(list []string) []string {
	out := make([]string, 0, len(list))
	seen := map[string]bool{}
	for _, v := range list {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// anyStrings converts strings to a JSON array.
func anyStrings(list []string) []any {
	out := make([]any, len(list))
	for i, v := range list {
		out[i] = v
	}
	return out
}

// sortedKeys returns a map's keys in a stable order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// cloneFields copies an entity's fields one level deep, as {...fields}.
func cloneFields(fields Fields) Fields {
	out := make(Fields, len(fields))
	for k, v := range fields {
		out[k] = v
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }
