package forms

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

// Value helpers for the JSON values a form edits.

func findField(fields []schema.Field, key string) (schema.Field, bool) {
	for _, f := range fields {
		if f.Key == key {
			return f, true
		}
	}
	return schema.Field{}, false
}

// truthy is JavaScript's truthiness of a JSON value.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int:
		return x != 0
	}
	return true
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

func with(set map[string]bool, key string) map[string]bool {
	next := make(map[string]bool, len(set)+1)
	for k := range set {
		next[k] = true
	}
	next[key] = true
	return next
}

// stringify is JavaScript's String(value) for JSON values.
func stringify(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = stringify(e)
		}
		return strings.Join(parts, ",")
	case []string:
		return strings.Join(x, ",")
	}
	return fmt.Sprint(v)
}

// specOf is the format of a list's items, plain text by default.
func specOf(kind schema.ItemKind) schema.ItemSpec {
	if spec, ok := schema.ItemSpecs[kind]; ok {
		return spec
	}
	return schema.ItemSpecs[schema.ItemText]
}
