// Package model is the configuration document the console edits. Its JSON
// is the document kiko-server stores (GET/PUT /api/v1/store), so every
// field keeps the name and optionality existing stored documents use.
//
// Internal ids are the identity of entities; tags are only display names.
// Cross references (detours, group members, rule outbounds, DNS servers)
// store ids and become tags only when the configuration is emitted.
package model

import (
	"encoding/json"
	"slices"
)

// Fields holds an entity's protocol fields, shaped by its type's schema.
type Fields = map[string]any

// NetworkConfigRefs maps a field group to the network config it uses.
type NetworkConfigRefs = map[string]string

// Network config kinds.
const (
	NetworkConfigNetwork   = "network"
	NetworkConfigTLSClient = "tls-client"
	NetworkConfigTLSServer = "tls-server"
)

// NetworkConfig is a reusable block of dial or TLS fields.
type NetworkConfig struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind,omitempty"`
	Fields    Fields `json:"fields"`
	UpdatedAt int64  `json:"updatedAt"`
}

// Protocol types.
var (
	InboundTypes = []string{
		"tun", "mixed", "socks", "http", "shadowsocks", "cloudflared", "hysteria", "snell",
		"vless", "vmess", "trojan", "hysteria2", "tuic", "anytls", "naive",
		"shadowtls", "burrow", "direct", "redirect", "tproxy",
	}
	OutboundTypes = []string{
		"direct", "block", "bridge", "socks", "http", "shadowsocks", "shadowsocksr", "hysteria", "snell", "tor",
		"vless", "vmess", "trojan", "hysteria2", "tuic", "anytls",
		"wireguard", "ssh", "shadowtls", "burrow", "naive",
		"selector", "urltest", "fallback", "loadbalance", "smart",
	}
	EndpointTypes = []string{"tailscale", "wireguard", "openconnect", "openvpn-client", "openvpn-server"}
	ServiceTypes  = []string{"usbip-server", "usbip-client"}
	GroupTypes    = []string{"selector", "urltest", "fallback", "loadbalance", "smart"}
)

// IsGroup reports whether an outbound type groups other outbounds.
func IsGroup(outboundType string) bool { return slices.Contains(GroupTypes, outboundType) }

// Entity is what inbounds, outbounds, endpoints and services share.
type Entity struct {
	NetworkConfigRefs NetworkConfigRefs `json:"networkConfigRefs,omitempty"`
	ID                string            `json:"id"`
	Tag               string            `json:"tag"`
	Enabled           bool              `json:"enabled"`
}

// Inbound listens for connections.
type Inbound struct {
	Entity
	Kind   string `json:"kind"` // "inbound"
	Type   string `json:"type"`
	Fields Fields `json:"fields"`
}

// Endpoint acts as both an inbound and an outbound; its tag shares the
// outbounds' namespace.
type Endpoint struct {
	Entity
	Kind   string `json:"kind"` // "endpoint"
	Type   string `json:"type"`
	Fields Fields `json:"fields"`
	// Chain is the proxy chain the endpoint dials through, as an
	// outbound's Chain.
	Chain []string `json:"chain,omitempty"`
}

// Service is a top-level service (USB/IP).
type Service struct {
	Entity
	Kind   string `json:"kind"` // "service"
	Type   string `json:"type"`
	Fields Fields `json:"fields"`
}

// Outbound dials connections. Policy groups (GroupTypes) group
// Members, ids of other outbounds or endpoints.
type Outbound struct {
	Entity
	Kind    string   `json:"kind"` // "outbound"
	Type    string   `json:"type"`
	Fields  Fields   `json:"fields"`
	Members []string `json:"members,omitempty"`
	// Filter adds the nodes it selects to a group's members when the
	// configuration is emitted (GroupMembers).
	Filter *GroupFilter `json:"filter,omitempty"`
	// SubID is the subscription a node came from.
	SubID string `json:"subId,omitempty"`
	// Chain is the proxy chain the outbound dials through, ids of
	// outbounds or endpoints in the order traffic takes them: Chain[0]
	// is the hop nearest this host, the outbound itself comes after the
	// last. It replaces the former detour field (MigrateProxyChains).
	Chain []string `json:"chain,omitempty"`
}

// Traffic is a subscription's usage from its response headers.
type Traffic struct {
	Upload   float64 `json:"upload"`
	Download float64 `json:"download"`
	Total    float64 `json:"total"`
	Expire   float64 `json:"expire"`
}

// Subscription is a source of nodes; fetched nodes are outbounds whose
// SubID is its id.
type Subscription struct {
	ID         string   `json:"id"`
	Tag        string   `json:"tag"`
	URL        string   `json:"url"`
	UserAgent  string   `json:"userAgent,omitempty"`
	Traffic    *Traffic `json:"traffic,omitempty"`
	Enabled    bool     `json:"enabled"`
	NamePrefix string   `json:"namePrefix,omitempty"`
	Raw        string   `json:"raw,omitempty"`
	UpdatedAt  int64    `json:"updatedAt,omitempty"`
	// Detour is the former one-hop chain of every node, moved to Chain
	// by MigrateProxyChains.
	Detour Ref `json:"detour,omitzero"`
	// Chain is the proxy chain every node of the subscription dials
	// through.
	Chain     []string `json:"chain,omitempty"`
	LastError string   `json:"lastError,omitempty"`
}

// UnmarshalJSON defaults Enabled to true, as fillDefaults does.
func (s *Subscription) UnmarshalJSON(data []byte) error {
	type plain Subscription
	value := plain{Enabled: true}
	if err := UnmarshalLenient(data, &value); err != nil {
		return err
	}
	*s = Subscription(value)
	return nil
}

// ---- routing ----

// DeviceLabel names a LAN device.
type DeviceLabel struct {
	Alias    string `json:"alias,omitempty"`
	Hostname string `json:"hostname,omitempty"`
}

// UIProcess is display metadata of a process condition.
type UIProcess struct {
	Source  string `json:"source"` // application, process or custom
	Label   string `json:"label,omitempty"`
	AppPath string `json:"appPath,omitempty"`
}

// Condition kinds.
const (
	ConditionMatch = "match"
	ConditionGroup = "group"
)

// Condition is a node of a rule's condition tree: a leaf matching sing-box
// rule fields (Kind "match"), or a group of conditions joined by Mode
// "and" or "or" (Kind "group").
type Condition struct {
	UIField   string         `json:"uiField,omitempty"`
	UIProcess *UIProcess     `json:"uiProcess,omitempty"`
	Kind      string         `json:"kind"`
	Match     map[string]any `json:"match,omitempty"`
	Mode      string         `json:"mode,omitempty"`
	Invert    bool           `json:"invert,omitempty"`
	Rules     []*Condition   `json:"rules,omitempty"`
}

// MarshalJSON writes a leaf or a group in its own shape: a group always
// has rules, a leaf always has match.
func (c Condition) MarshalJSON() ([]byte, error) {
	if c.Kind == ConditionGroup {
		rules := c.Rules
		if rules == nil {
			rules = []*Condition{}
		}
		return json.Marshal(struct {
			UIField string       `json:"uiField,omitempty"`
			Kind    string       `json:"kind"`
			Mode    string       `json:"mode"`
			Invert  bool         `json:"invert,omitempty"`
			Rules   []*Condition `json:"rules"`
		}{c.UIField, c.Kind, c.Mode, c.Invert, rules})
	}
	match := c.Match
	if match == nil {
		match = map[string]any{}
	}
	return json.Marshal(struct {
		UIField   string         `json:"uiField,omitempty"`
		UIProcess *UIProcess     `json:"uiProcess,omitempty"`
		Kind      string         `json:"kind"`
		Match     map[string]any `json:"match"`
		Invert    bool           `json:"invert,omitempty"`
	}{c.UIField, c.UIProcess, c.Kind, match, c.Invert})
}

// RouteDevice is a LAN device with rules of its own.
type RouteDevice struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Icon      string   `json:"icon"` // computer, phone, server, router
	Addresses []string `json:"addresses"`
	// Final is the default outbound of the profile whose scope is the
	// device id.
	Final string `json:"final"`
	// Profiles are the device's rule sets; empty means one implicit
	// profile (see DeviceProfiles). ActiveProfile is the profile id in
	// effect; empty or unknown means the first.
	Profiles      []DeviceProfile `json:"profiles,omitempty"`
	ActiveProfile string          `json:"activeProfile,omitempty"`
	// MAC, Image and PendingAddresses are display and bookkeeping data:
	// they never reach the emitted configuration (see deviceextra.go).
	// MAC is lowercase colon-separated; Image is a jpeg, png or webp data
	// URL; PendingAddresses are addresses a LAN scan saw for this MAC,
	// kept until the user confirms them.
	MAC              string   `json:"mac,omitempty"`
	Image            string   `json:"image,omitempty"`
	PendingAddresses []string `json:"pendingAddresses,omitempty"`
	// PendingFrom pairs a pending address with the address of the device
	// it replaces once confirmed; a pending address without an entry is
	// added next to the existing ones.
	PendingFrom map[string]string `json:"pendingFrom,omitempty"`
	// ImageKey names a built-in device picture (DeviceImageKeys), shown
	// when Image is empty. Interface only; never emitted.
	ImageKey string `json:"imageKey,omitempty"`
}

// DeviceProfile is one rule set of a device. Scope is the owner key of its
// rules, groups and DNS defaults: the device id for the original profile,
// else "<device id>@<profile id>". Final is the default outbound of a
// profile whose scope is not the device id.
type DeviceProfile struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Scope string `json:"scope"`
	Final string `json:"final,omitempty"`
}

// Rule actions.
const (
	ActionRoute     = "route"
	ActionReject    = "reject"
	ActionHijackDNS = "hijack-dns"
	ActionSniff     = "sniff"
	ActionResolve   = "resolve"
	// ActionScript is a DNS rule action: the dns script Script answers.
	ActionScript = "script"
)

// ScriptOnErrorReject is the DNS script failure policy that refuses the
// query; the default continues with the next rule.
const ScriptOnErrorReject = "reject"

// RouteRule is a traffic rule.
type RouteRule struct {
	DeviceID      string         `json:"deviceId,omitempty"`
	ConditionTree *Condition     `json:"conditionTree,omitempty"`
	CatchAll      bool           `json:"catchAll,omitempty"`
	ID            string         `json:"id"`
	GroupID       string         `json:"groupId,omitempty"`
	Name          string         `json:"name,omitempty"`
	Enabled       bool           `json:"enabled"`
	Match         map[string]any `json:"match"`
	Logic         string         `json:"logic,omitempty"`
	Action        string         `json:"action"`
	Outbound      Ref            `json:"outbound,omitzero"`
	// OverrideDestination is for sniff: the sniffed domain replaces the
	// connection's destination.
	OverrideDestination bool `json:"overrideDestination,omitempty"`
}

// RouteGroup is a named group of rules of a device.
type RouteGroup struct {
	DeviceID string `json:"deviceId,omitempty"`
	ID       string `json:"id"`
	Name     string `json:"name"`
}

// RuleSet is an inline or remote rule set.
type RuleSet struct {
	ID             string `json:"id"`
	Tag            string `json:"tag"`
	Enabled        bool   `json:"enabled"`
	Mode           string `json:"mode"` // inline or remote
	Path           string `json:"path,omitempty"`
	URL            string `json:"url,omitempty"`
	UpdateInterval string `json:"updateInterval,omitempty"`
	Rules          []any  `json:"rules,omitempty"`
	SourceVersion  int    `json:"sourceVersion,omitempty"`
	HTTPClient     Ref    `json:"httpClient,omitzero"`
	LastUpdatedAt  int64  `json:"lastUpdatedAt,omitempty"`
	// Overrides add rules to and remove values from the source's rules,
	// applied by the core each time it loads the source.
	Overrides *RuleSetOverrides `json:"overrides,omitempty"`
}

// HTTPClient is a named HTTP client of the core.
type HTTPClient struct {
	ID      string `json:"id"`
	Tag     string `json:"tag"`
	Enabled bool   `json:"enabled"`
	Detour  Ref    `json:"detour,omitzero"`
}

// ---- DNS ----

// DNS server types.
var DNSServerTypes = []string{"local", "udp", "tcp", "tls", "https", "quic", "h3", "hosts", "fakeip", "dhcp", "tailscale"}

// DNSServer is a resolver.
type DNSServer struct {
	NetworkConfigRefs    NetworkConfigRefs `json:"networkConfigRefs,omitempty"`
	ID                   string            `json:"id"`
	Tag                  string            `json:"tag"`
	Enabled              bool              `json:"enabled"`
	Type                 string            `json:"type"`
	Server               string            `json:"server,omitempty"`
	ServerPort           *int              `json:"serverPort,omitempty"`
	Path                 string            `json:"path,omitempty"`
	Detour               Ref               `json:"detour,omitzero"`
	DomainResolver       Ref               `json:"domainResolver,omitzero"`
	DomainResolverDetour Ref               `json:"domainResolverDetour,omitzero"`
	DomainStrategy       string            `json:"domainStrategy,omitempty"`
	Fields               Fields            `json:"fields,omitempty"`
}

// DNSRule routes queries to a server.
type DNSRule struct {
	DeviceID      string         `json:"deviceId,omitempty"`
	GroupID       string         `json:"groupId,omitempty"`
	Name          string         `json:"name,omitempty"`
	ConditionTree *Condition     `json:"conditionTree,omitempty"`
	CatchAll      bool           `json:"catchAll,omitempty"`
	ID            string         `json:"id"`
	Enabled       bool           `json:"enabled"`
	Match         map[string]any `json:"match"`
	Server        Ref            `json:"server,omitzero"`
	Detour        Ref            `json:"detour,omitzero"`
	// MoreServers are queried with Server at the same time, each with its
	// own detour; the first successful answer wins. Server and Detour stay
	// the first target, so documents without it read as one server.
	MoreServers []DNSTarget `json:"moreServers,omitempty"`
	Action      string      `json:"action,omitempty"` // route, reject or script
	// Script is the tag of the dns script of a script action; OnError is
	// "reject" to refuse the query when the script fails (default: continue
	// with the next rule).
	Script  string `json:"script,omitempty"`
	OnError string `json:"onError,omitempty"`
}

// DNSTarget is one server of a DNS rule with its query detour.
type DNSTarget struct {
	Server Ref `json:"server,omitzero"`
	Detour Ref `json:"detour,omitzero"`
}

// Targets returns the servers a DNS rule queries, the first one first; a
// rule without a server has none.
func (r *DNSRule) Targets() []DNSTarget {
	if !r.Server.Valid() && len(r.MoreServers) == 0 {
		return nil
	}
	return append([]DNSTarget{{Server: r.Server, Detour: r.Detour}}, r.MoreServers...)
}

// SetTargets stores the servers of a DNS rule: the first in Server and
// Detour, the rest in MoreServers.
func (r *DNSRule) SetTargets(targets []DNSTarget) {
	if len(targets) == 0 {
		r.Server, r.Detour, r.MoreServers = Ref{}, Ref{}, nil
		return
	}
	r.Server, r.Detour = targets[0].Server, targets[0].Detour
	r.MoreServers = append([]DNSTarget(nil), targets[1:]...)
	if len(r.MoreServers) == 0 {
		r.MoreServers = nil
	}
}

// ---- document ----

// Log is the core's log settings.
type Log struct {
	Level     string `json:"level"`
	Timestamp bool   `json:"timestamp"`
	Disabled  bool   `json:"disabled,omitempty"`
}

// Route is the routing section.
type Route struct {
	Rules                       []*RouteRule           `json:"rules"`
	Devices                     []*RouteDevice         `json:"devices,omitempty"`
	LocalEnabled                *bool                  `json:"localEnabled,omitempty"`
	LocalAddresses              []string               `json:"localAddresses,omitempty"`
	LocalFinal                  Ref                    `json:"localFinal,omitzero"`
	Groups                      []*RouteGroup          `json:"groups,omitempty"`
	DeviceLabels                map[string]DeviceLabel `json:"deviceLabels,omitempty"`
	DHCPLeaseFiles              []string               `json:"dhcpLeaseFiles,omitempty"`
	RuleSets                    []*RuleSet             `json:"ruleSets"`
	Final                       Ref                    `json:"final,omitzero"`
	AutoDetectInterface         bool                   `json:"autoDetectInterface"`
	FindProcess                 *bool                  `json:"findProcess,omitempty"`
	DefaultInterface            string                 `json:"defaultInterface,omitempty"`
	DefaultDomainResolver       Ref                    `json:"defaultDomainResolver,omitzero"`
	DefaultDomainResolverDetour Ref                    `json:"defaultDomainResolverDetour,omitzero"`
	DefaultDomainStrategy       string                 `json:"defaultDomainStrategy,omitempty"`
	DefaultHTTPClient           Ref                    `json:"defaultHttpClient,omitzero"`

	// LocalProfiles and LocalActiveProfile are the local scope's profiles,
	// as RouteDevice.Profiles and ActiveProfile: empty means the one
	// implicit profile whose scope is LocalDeviceID and whose default
	// outbound is LocalFinal.
	LocalProfiles      []DeviceProfile `json:"localProfiles,omitempty"`
	LocalActiveProfile string          `json:"localActiveProfile,omitempty"`
}

// DNS is the DNS section.
type DNS struct {
	Servers             []*DNSServer   `json:"servers"`
	Rules               []*DNSRule     `json:"rules"`
	DevicePolicyVersion int            `json:"devicePolicyVersion,omitempty"`
	RoutingVersion      int            `json:"routingVersion,omitempty"`
	Groups              []*RouteGroup  `json:"groups,omitempty"`
	DeviceFinals        map[string]Ref `json:"deviceFinals,omitempty"`
	DeviceFinalDetours  map[string]Ref `json:"deviceFinalDetours,omitempty"`
	Final               Ref            `json:"final,omitzero"`
	FinalDetour         Ref            `json:"finalDetour,omitzero"`
	Strategy            string         `json:"strategy,omitempty"`
	DisableCache        bool           `json:"disableCache,omitempty"`
	DisableExpire       bool           `json:"disableExpire,omitempty"`
	CacheCapacity       int            `json:"cacheCapacity,omitempty"`
	ClientSubnet        string         `json:"clientSubnet,omitempty"`
	ReverseMapping      bool           `json:"reverseMapping,omitempty"`
	// PresetsVersion records the DNS presets added (AddDNSPresets).
	PresetsVersion int `json:"presetsVersion,omitempty"`
}

// Experimental holds the Clash API and cache settings.
type Experimental struct {
	ClashAPI           bool   `json:"clashApi"`
	ExternalController string `json:"externalController"`
	DefaultMode        string `json:"defaultMode,omitempty"`
	CacheFile          bool   `json:"cacheFile"`
}

// Doc is the whole configuration document.
type Doc struct {
	Version        int             `json:"version"`
	Platform       string          `json:"platform"`
	IPv6Enabled    bool            `json:"ipv6Enabled"`
	Log            Log             `json:"log"`
	Inbounds       []*Inbound      `json:"inbounds"`
	Outbounds      []*Outbound     `json:"outbounds"`
	Endpoints      []*Endpoint     `json:"endpoints,omitempty"`
	Services       []*Service      `json:"services,omitempty"`
	Subscriptions  []*Subscription `json:"subscriptions"`
	HTTPClients    []*HTTPClient   `json:"httpClients,omitempty"`
	OutboundOrder  []string        `json:"outboundOrder,omitempty"`
	Route          Route           `json:"route"`
	DNS            DNS             `json:"dns"`
	Experimental   Experimental    `json:"experimental"`
	LatencyTestURL string          `json:"latencyTestURL,omitempty"`
	// LatencyPlain turns the unified delay off: latency tests then time
	// the first HTTPS request of a fresh connection. Unset means unified.
	LatencyPlain bool       `json:"latencyPlain,omitempty"`
	HTTPTools    *HTTPTools `json:"httpTools,omitempty"`
	// NodeRegions is the region table of node names, the defaults when
	// unset (Regions).
	NodeRegions *RegionSettings `json:"nodeRegions,omitempty"`
}

// Profile is a saved, named document.
type Profile struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	SavedAt int64  `json:"savedAt"`
	Doc     *Doc   `json:"doc"`
}

// Clone returns a deep copy of v through JSON, as cloneJson does.
func Clone[T any](v T) T {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return out
}
