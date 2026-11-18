package emit

import (
	"bytes"
	"encoding/json"
	"math"
	"slices"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// object is a JSON object that keeps insertion order, so the emitted
// configuration keeps a stable, readable field order.
type object struct {
	keys   []string
	values map[string]any
}

// obj returns an object of key/value pairs.
func obj(pairs ...any) *object {
	o := &object{values: map[string]any{}}
	for i := 0; i+1 < len(pairs); i += 2 {
		o.set(pairs[i].(string), pairs[i+1])
	}
	return o
}

// fromMap converts a decoded JSON object, keys in sorted order.
func fromMap(m map[string]any) *object {
	o := obj()
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		o.set(k, m[k])
	}
	return o
}

func (o *object) set(key string, value any) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

func (o *object) get(key string) any { return o.values[key] }

func (o *object) has(key string) bool {
	_, ok := o.values[key]
	return ok
}

func (o *object) del(key string) {
	if _, ok := o.values[key]; !ok {
		return
	}
	delete(o.values, key)
	o.keys = slices.DeleteFunc(o.keys, func(k string) bool { return k == key })
}

func (o *object) len() int { return len(o.keys) }

// assign copies src's entries into o, as Object.assign.
func (o *object) assign(src *object) {
	for _, k := range src.keys {
		o.set(k, src.values[k])
	}
}

// spread returns {...a, ...b}.
func spread(parts ...*object) *object {
	out := obj()
	for _, p := range parts {
		if p != nil {
			out.assign(p)
		}
	}
	return out
}

// asObject returns v as an object when it is one.
func asObject(v any) (*object, bool) {
	switch x := v.(type) {
	case *object:
		return x, true
	case map[string]any:
		return fromMap(x), true
	}
	return nil, false
}

// isEmpty reports whether v counts as empty: missing, blank text, empty
// arrays and objects.
func isEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case *object:
		return x.len() == 0
	case map[string]any:
		return len(x) == 0
	}
	if list, ok := jsv.Array(v); ok {
		return len(list) == 0
	}
	return false
}

// prune drops empty values recursively; false and 0 are meaningful.
func prune(v any) any {
	switch x := v.(type) {
	case *object:
		out := obj()
		for _, k := range x.keys {
			p := prune(x.values[k])
			if p == false || isZero(p) || !isEmpty(p) {
				out.set(k, p)
			}
		}
		return out
	case map[string]any:
		return prune(fromMap(x))
	}
	if list, ok := jsv.Array(v); ok {
		out := []any{}
		for _, e := range list {
			if p := prune(e); !isEmpty(p) {
				out = append(out, p)
			}
		}
		return out
	}
	return v
}

func isZero(v any) bool {
	f, ok := jsv.Float(v)
	return ok && f == 0
}

// MarshalJSON writes the object in insertion order.
func (o *object) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	encode(&buf, o)
	return buf.Bytes(), nil
}

// encode writes v as JSON.stringify does: NaN and infinities are null,
// HTML characters are not escaped, map keys are sorted.
func encode(buf *bytes.Buffer, v any) {
	switch x := v.(type) {
	case nil:
		buf.WriteString("null")
	case *object:
		buf.WriteByte('{')
		for i, k := range x.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			encodeString(buf, k)
			buf.WriteByte(':')
			encode(buf, x.values[k])
		}
		buf.WriteByte('}')
	case map[string]any:
		encode(buf, fromMap(x))
	case string:
		encodeString(buf, x)
	case bool:
		if x {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case []any:
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			encode(buf, e)
		}
		buf.WriteByte(']')
	default:
		if f, ok := jsv.Float(v); ok {
			if math.IsNaN(f) || math.IsInf(f, 0) {
				buf.WriteString("null")
			} else {
				buf.WriteString(jsv.FormatNumber(f))
			}
			return
		}
		if list, ok := jsv.Array(v); ok {
			encode(buf, list)
			return
		}
		// Typed values (structs) go through encoding/json.
		raw, err := json.Marshal(v)
		if err != nil {
			buf.WriteString("null")
			return
		}
		buf.Write(raw)
	}
}

func encodeString(buf *bytes.Buffer, s string) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	buf.Write(bytes.TrimRight(b.Bytes(), "\n"))
}
