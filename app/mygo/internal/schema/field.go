// Package schema describes the protocol fields of inbounds, outbounds,
// endpoints and services. Both the editor forms and the emitted
// configuration are driven by it: a protocol is one more entry in a
// table, not more UI code.
package schema

// FieldType is how a field is edited and stored.
type FieldType string

const (
	Text        FieldType = "text"
	Number      FieldType = "number"
	Bool        FieldType = "bool"
	Select      FieldType = "select"
	MultiSelect FieldType = "multiselect"
	List        FieldType = "list"
	TextArea    FieldType = "textarea"
	Combo       FieldType = "combo"    // free text with common values
	KeyValue    FieldType = "keyvalue" // string map, as HTTP headers
	Ref         FieldType = "ref"      // another outbound's id
	RefDNS      FieldType = "refDns"   // a DNS server's id
	RefIn       FieldType = "refIn"    // another inbound's id
	Duration    FieldType = "duration"
	JSON        FieldType = "json" // any official object or array
	Group       FieldType = "group"
)

// Platforms the configuration targets.
var Platforms = []string{"linux", "darwin", "windows", "android", "ios"}

// PlatformLabel names a platform.
var PlatformLabel = map[string]string{
	"linux": "Linux", "darwin": "macOS", "windows": "Windows", "android": "Android", "ios": "iOS",
}

// Action is a generator button inside an input.
type Action string

const (
	ActionUUID       Action = "uuid"
	ActionRandomPort Action = "randomPort"
	ActionBase64     Action = "base64"
)

// ItemKind is the format of a list's items.
type ItemKind string

const (
	ItemCIDR        ItemKind = "cidr"
	ItemIP          ItemKind = "ip"
	ItemRuleSet     ItemKind = "ruleSet"
	ItemInterface   ItemKind = "interface"
	ItemUID         ItemKind = "uid"
	ItemUIDRange    ItemKind = "uidRange"
	ItemPackage     ItemKind = "package"
	ItemAndroidUser ItemKind = "androidUser"
	ItemText        ItemKind = "text"
)

// Preset is a common value of a combo field.
type Preset struct {
	Value string
	Label string
}

// ShowIf shows a field only while another field matches.
type ShowIf struct {
	Key    string
	Eq     any   // equal to this value, when set
	HasEq  bool  // Eq is set (Eq may be nil, false or 0)
	OneOf  []any // one of these values
	Truthy bool  // truthy
	Domain bool  // a domain rather than an IP address
}

// Field is a protocol field.
type Field struct {
	// Key is the JSON key, nested with dots ("tls.server_name").
	Key            string
	Label          string
	Type           FieldType
	Adv            bool // under advanced options
	Placeholder    string
	Hint           string
	HintByPlatform map[string]string
	Options        []string // select and multiselect
	AllowCustom    bool     // multiselect accepts values beyond Options
	NumericOptions bool     // select values are numbers in JSON
	Presets        []Preset // combo
	Action         Action
	Def            any // default value; nil for none
	Min, Max       *float64
	Span           int // columns of a 12-column grid
	ShowIf         *ShowIf
	// ShowIfStandalone keeps the field in the main grid while ShowIf only
	// shows and hides it.
	ShowIfStandalone bool
	// Validate checks a single value, returning the error to show.
	Validate func(value string, values map[string]any) string
	// ValidateOnSave defers validation to saving.
	ValidateOnSave bool
	Required       bool
	// Platforms that support the field; nil for all.
	Platforms []string
	// ServerOnly fields are managed by the desktop runtime, not sing-box.
	ServerOnly bool
	// Group is the sing-box structure the field belongs to; "" for basic.
	Group string
	Item  ItemKind
	// Locked fields show their default and cannot be edited.
	Locked bool
}

// FieldGroup is a titled group of a form.
type FieldGroup struct {
	Key, Label, Desc string
}

// Entity kinds the tables are keyed by.
const (
	KindInbound  = "inbound"
	KindOutbound = "outbound"
	KindEndpoint = "endpoint"
	KindService  = "service"
)

// Tables of fields by entity kind and type, in tables_gen.go. The file was
// generated once from the former web console's schema.ts and is now
// maintained here by hand.
var (
	InboundSchema  = map[string][]Field{}
	OutboundSchema = map[string][]Field{}
	EndpointSchema = map[string][]Field{}
	ServiceSchema  = map[string][]Field{}
	// FieldGroups orders and names the groups of a form.
	FieldGroups []FieldGroup
	// TypeLabel names protocol types for display.
	TypeLabel = map[string]string{}

	// DNSDialerSchema is the dialer of DNS servers (DNS_DIALER_SCHEMA).
	DNSDialerSchema []Field
	// OutboundTLS and InboundTLS are the TLS blocks of outbounds and
	// inbounds.
	OutboundTLS, InboundTLS []Field
	// TLSClientSchema and TLSServerSchema are the fields of shared TLS
	// configs.
	TLSClientSchema, TLSServerSchema []Field
	// DNSTLSSchema is the TLS of DNS servers, stored nested under "tls".
	DNSTLSSchema []Field
	// NetworkSchema is the fields of shared network configs.
	NetworkSchema []Field
	// ServicePlatforms lists the platforms each service type runs on.
	ServicePlatforms = map[string][]string{}
	// ServicePlatformHint explains why a service is unavailable on a
	// platform.
	ServicePlatformHint = map[string]map[string]string{}
)

// Fields returns the fields of an entity kind and type.
func Fields(kind, typ string) []Field {
	switch kind {
	case KindInbound:
		return InboundSchema[typ]
	case KindOutbound:
		return OutboundSchema[typ]
	case KindEndpoint:
		return EndpointSchema[typ]
	case KindService:
		return ServiceSchema[typ]
	}
	return nil
}

// DefaultFields returns the defaults of fields, as defaultFields.
func DefaultFields(fields []Field) map[string]any {
	out := map[string]any{}
	for _, f := range fields {
		if f.Def != nil {
			out[f.Key] = cloneValue(f.Def)
		}
	}
	return out
}

// cloneValue copies maps and slices so defaults are never shared.
func cloneValue(v any) any {
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = cloneValue(x[i])
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i := range x {
			out[i] = x[i]
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = cloneValue(e)
		}
		return out
	}
	return v
}
