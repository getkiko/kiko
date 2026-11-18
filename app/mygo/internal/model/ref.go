package model

import (
	"bytes"
	"encoding/json"
)

// Ref is a reference to an entity by its id, as the document stores it:
// absent (the zero Ref), null, or an id. The document distinguishes the
// three: an absent localFinal inherits route.final, a null one selects the
// core default, and migrations fill in only absent detours. Tag Ref fields
// `json:",omitzero"` so absent stays absent.
type Ref struct {
	present bool
	id      string
}

// Some returns a reference to id; an empty id is null.
func Some(id string) Ref { return Ref{present: true, id: id} }

// Null returns an explicit null reference.
func Null() Ref { return Ref{present: true} }

// RefOf returns Some(id) for a non-empty id and Null() otherwise.
func RefOf(id string) Ref { return Ref{present: true, id: id} }

// ID returns the referenced id, "" when null or absent.
func (r Ref) ID() string { return r.id }

// Set reports whether the field is present, null or not.
func (r Ref) Set() bool { return r.present }

// Valid reports whether the reference names an id.
func (r Ref) Valid() bool { return r.id != "" }

// IsNull reports an explicit null.
func (r Ref) IsNull() bool { return r.present && r.id == "" }

// IsZero reports an absent reference, for omitzero.
func (r Ref) IsZero() bool { return !r.present }

// Or returns r when it is set, fallback otherwise.
func (r Ref) Or(fallback Ref) Ref {
	if r.present {
		return r
	}
	return fallback
}

// MarshalJSON writes null or the id. An absent Ref inside a map or slice
// is written as null.
func (r Ref) MarshalJSON() ([]byte, error) {
	if r.id == "" {
		return []byte("null"), nil
	}
	return json.Marshal(r.id)
}

// UnmarshalJSON reads null or a string. Any other value is left absent
// instead of failing the whole document.
func (r *Ref) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		*r = Null()
		return nil
	}
	if len(trimmed) == 0 || trimmed[0] != '"' {
		*r = Ref{}
		return nil
	}
	var id string
	if err := json.Unmarshal(trimmed, &id); err != nil {
		return err
	}
	*r = RefOf(id)
	return nil
}
