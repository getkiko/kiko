package profiles

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// object is a JSON object that keeps its keys in order, as JavaScript
// objects do, so a configuration reads the same after a round trip.
type object struct {
	keys   []string
	values map[string]any
}

// decodeOrdered parses JSON into nil, bool, json.Number, string, []any and
// *object values.
func decodeOrdered(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("invalid JSON: trailing data")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := &object{values: map[string]any{}}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key := keyTok.(string)
				value, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				if _, seen := o.values[key]; !seen {
					o.keys = append(o.keys, key)
				}
				o.values[key] = value
			}
			_, err := dec.Token()
			return o, err
		case '[':
			list := []any{}
			for dec.More() {
				value, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				list = append(list, value)
			}
			_, err := dec.Token()
			return list, err
		}
		return nil, errors.New("invalid JSON")
	}
	return tok, nil
}

// encodeOrdered writes v as JSON.stringify(v, null, 2) does.
func encodeOrdered(v any) string {
	var b strings.Builder
	writeOrdered(&b, v, "")
	return b.String()
}

func writeOrdered(b *strings.Builder, v any, indent string) {
	inner := indent + "  "
	switch x := v.(type) {
	case *object:
		if len(x.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, key := range x.keys {
			b.WriteString(inner)
			b.WriteString(quote(key))
			b.WriteString(": ")
			writeOrdered(b, x.values[key], inner)
			if i < len(x.keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "}")
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, item := range x {
			b.WriteString(inner)
			writeOrdered(b, item, inner)
			if i < len(x)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "]")
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case json.Number:
		b.WriteString(x.String())
	case int:
		b.WriteString(json.Number(itoa(x)).String())
	case string:
		b.WriteString(quote(x))
	default:
		raw, _ := json.Marshal(x)
		b.Write(raw)
	}
}

func itoa(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

// quote writes a JSON string without escaping HTML characters, as
// JSON.stringify does.
func quote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// marshalIndent is json.MarshalIndent with two spaces and without HTML
// escaping, as JSON.stringify(v, null, 2).
func marshalIndent(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}
