// Package jsv gives Go values decoded from JSON the JavaScript semantics
// the former TypeScript console relied on: truthiness, String(), Number(),
// parseInt() and strict equality. The ported configuration logic uses it
// wherever the TypeScript coerced loosely typed form values.
package jsv

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Float returns v as a float64 when it is a number of any Go numeric type.
func Float(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int8:
		return float64(x), true
	case int16:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint:
		return float64(x), true
	case uint8:
		return float64(x), true
	case uint16:
		return float64(x), true
	case uint32:
		return float64(x), true
	case uint64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// IsNumber reports whether v is a number.
func IsNumber(v any) bool {
	_, ok := Float(v)
	return ok
}

// Truthy is JavaScript's !!v.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	}
	if f, ok := Float(v); ok {
		return f != 0 && !math.IsNaN(f)
	}
	return true
}

// FormatNumber is JavaScript's Number.prototype.toString().
func FormatNumber(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		return "0"
	}
	abs := math.Abs(f)
	if abs >= 1e21 || abs < 1e-6 {
		s := strconv.FormatFloat(f, 'e', -1, 64)
		// Go writes e+21 / e-07; JavaScript writes e+21 / e-7.
		mantissa, exp, _ := strings.Cut(s, "e")
		sign := exp[0]
		digits := strings.TrimLeft(exp[1:], "0")
		return mantissa + "e" + string(sign) + digits
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// String is JavaScript's String(v). A nil value is "null"; callers that
// mean undefined use StringOr.
func String(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			if e != nil {
				parts[i] = String(e)
			}
		}
		return strings.Join(parts, ",")
	case []string:
		return strings.Join(x, ",")
	case map[string]any:
		return "[object Object]"
	}
	if f, ok := Float(v); ok {
		return FormatNumber(f)
	}
	return "[object Object]"
}

// StringOr is String(v ?? fallback): nil becomes fallback.
func StringOr(v any, fallback string) string {
	if v == nil {
		return fallback
	}
	return String(v)
}

// Text is String(v ?? "").trim(), the common form-value reading.
func Text(v any) string { return strings.TrimSpace(StringOr(v, "")) }

// Number is JavaScript's Number(v); NaN when it does not convert. A nil
// value converts as null (0); use NumberOrNaN for undefined.
func Number(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		return StringToNumber(x)
	case []any:
		switch len(x) {
		case 0:
			return 0
		case 1:
			return Number(String(x[0]))
		}
		return math.NaN()
	case []string:
		switch len(x) {
		case 0:
			return 0
		case 1:
			return StringToNumber(x[0])
		}
		return math.NaN()
	}
	if f, ok := Float(v); ok {
		return f
	}
	return math.NaN()
}

// StringToNumber converts a string as JavaScript's Number(string).
func StringToNumber(s string) float64 {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0
	}
	switch t {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	lower := strings.ToLower(t)
	for _, p := range []struct {
		prefix string
		base   int
	}{{"0x", 16}, {"0o", 8}, {"0b", 2}} {
		if strings.HasPrefix(lower, p.prefix) {
			n, err := strconv.ParseUint(t[2:], p.base, 64)
			if err != nil {
				return math.NaN()
			}
			return float64(n)
		}
	}
	for _, c := range t {
		if !(c >= '0' && c <= '9' || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-') {
			return math.NaN()
		}
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return f
		}
		return math.NaN()
	}
	return f
}

// NumberOrNaN is Number(v) where nil is undefined (NaN).
func NumberOrNaN(v any) float64 {
	if v == nil {
		return math.NaN()
	}
	return Number(v)
}

// ParseInt is JavaScript's parseInt(s, 10).
func ParseInt(s string) float64 {
	t := strings.TrimLeft(s, " \t\n\r\v\f\u00a0\ufeff")
	end := 0
	if end < len(t) && (t[end] == '+' || t[end] == '-') {
		end++
	}
	start := end
	for end < len(t) && t[end] >= '0' && t[end] <= '9' {
		end++
	}
	if end == start {
		return math.NaN()
	}
	f, _ := strconv.ParseFloat(t[:end], 64)
	return f
}

// IsInteger is Number.isInteger for a converted number.
func IsInteger(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0) && f == math.Trunc(f)
}

// IsFinite is Number.isFinite.
func IsFinite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// StrictEqual is JavaScript's a === b for JSON values. Objects and arrays
// are never equal unless both are nil.
func StrictEqual(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if fa, ok := Float(a); ok {
		fb, ok := Float(b)
		return ok && fa == fb
	}
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	}
	return false
}

// IsObject reports a plain JSON object (not an array).
func IsObject(v any) bool {
	_, ok := v.(map[string]any)
	return ok
}

// Object returns v as a JSON object, or nil.
func Object(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// Array returns v as a JSON array: []any directly, []string converted.
// ok is false when v is not an array.
func Array(v any) ([]any, bool) {
	switch x := v.(type) {
	case []any:
		return x, true
	case []string:
		out := make([]any, len(x))
		for i := range x {
			out[i] = x[i]
		}
		return out, true
	case []map[string]any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = x[i]
		}
		return out, true
	}
	return nil, false
}

// IsArray is Array.isArray.
func IsArray(v any) bool {
	_, ok := Array(v)
	return ok
}

// Clone deep-copies a JSON value.
func Clone(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if x == nil {
			return x
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = Clone(e)
		}
		return out
	case []any:
		if x == nil {
			return x
		}
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = Clone(e)
		}
		return out
	case []string:
		return append([]string(nil), x...)
	}
	return v
}

// CloneMap deep-copies a JSON object; nil stays nil.
func CloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	return Clone(m).(map[string]any)
}
