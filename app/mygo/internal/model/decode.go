package model

import (
	"encoding/json"
	"errors"

	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// Stored documents written by older consoles may hold a rule's enabled as
// a non-boolean (such as the string "false"). The former TypeScript
// console read a route rule's enabled by truthiness and a DNS rule's as
// `enabled === true`; decoding keeps those meanings instead of failing the
// whole document.
//
// More generally, the former console read a stored document without
// checking its value types, so one mistyped value (a fractional cache
// capacity, a numeric name) must not discard the document: decoding skips
// such values and keeps the rest.

type looseBool struct{ value any }

func (b *looseBool) UnmarshalJSON(data []byte) error { return json.Unmarshal(data, &b.value) }

// UnmarshalLenient decodes as json.Unmarshal, skipping values of the wrong
// type instead of failing: only malformed JSON is an error.
func UnmarshalLenient(data []byte, v any) error {
	err := json.Unmarshal(data, v)
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return nil
	}
	return err
}

// UnmarshalJSON reads a route rule; a non-boolean enabled counts by its
// truthiness.
func (r *RouteRule) UnmarshalJSON(data []byte) error {
	type plain RouteRule
	var v struct {
		plain
		Enabled looseBool `json:"enabled"`
	}
	if err := UnmarshalLenient(data, &v); err != nil {
		return err
	}
	*r = RouteRule(v.plain)
	r.Enabled = jsv.Truthy(v.Enabled.value)
	return nil
}

// UnmarshalJSON reads a DNS rule; only a boolean true enables it.
func (r *DNSRule) UnmarshalJSON(data []byte) error {
	type plain DNSRule
	var v struct {
		plain
		Enabled looseBool `json:"enabled"`
	}
	if err := UnmarshalLenient(data, &v); err != nil {
		return err
	}
	*r = DNSRule(v.plain)
	r.Enabled = v.Enabled.value == true
	return nil
}
