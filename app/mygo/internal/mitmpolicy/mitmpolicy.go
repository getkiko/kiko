// Package mitmpolicy evaluates, from the document alone, whether HTTPS
// decryption would take a host for connections from this machine. It
// decides what the document decides (feature off, certificate trust,
// exclude and include lists) and leaves the built-in exclusion table and
// the skip table to the core, whose policy test result is merged in with
// Merge.
package mitmpolicy

import (
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Decisions.
const (
	// None: decryption does not apply (the connection is not TLS to a host
	// the policy names, or the core did not report any).
	None = "none"
	// Decrypted: the host is decrypted.
	Decrypted = "decrypted"
	// Skipped: the host is left alone; Reason says why.
	Skipped = "skipped"
	// BlockedQUIC: the core blocks QUIC to the host so the client falls
	// back to TCP.
	BlockedQUIC = "blocked-quic"
)

// Skip reasons.
const (
	ReasonFeatureOff       = "feature-off"
	ReasonNotTrusted       = "not-trusted"
	ReasonExcluded         = "excluded"
	ReasonNotIncluded      = "not-included"
	ReasonBuiltin          = "builtin"
	ReasonPinning          = "pinning"
	ReasonCoreOther        = "core"
	ReasonAuthorityPending = "authority-pending"
	ReasonScope            = "scope"
	ReasonOverrideSkip     = "override-skip"
	ReasonNotTLS           = "not-tls"
	ReasonOutboundHandler  = "outbound-handler"
	reasonCorePrefixSkip   = "skipped:"
)

// Outcome is the result of an evaluation. Detail carries the core's reason
// text of a skip the document cannot name.
type Outcome struct {
	Decision string
	Reason   string
	Detail   string
	// Pending is true for a local "decrypted" that still waits for the
	// core's word (built-in table, skip table).
	Pending bool
}

// Label is a short Chinese label of the outcome for the route-test panel.
func (o Outcome) Label() string {
	switch o.Decision {
	case Decrypted:
		return "解密"
	case BlockedQUIC:
		return "阻止 QUIC"
	case Skipped:
		switch o.Reason {
		case ReasonFeatureOff:
			return "未开启"
		case ReasonNotTrusted:
			return "证书未信任"
		case ReasonExcluded:
			return "已排除"
		case ReasonNotIncluded:
			return "不在包含列表"
		case ReasonBuiltin:
			return "内置排除"
		case ReasonPinning:
			return "证书固定"
		case ReasonAuthorityPending:
			return "证书未就绪"
		case ReasonScope:
			return "设备未启用"
		case ReasonOverrideSkip:
			return "规则跳过"
		case ReasonNotTLS:
			return "非 TLS"
		case ReasonOutboundHandler:
			return "出站直接处理"
		}
		return "跳过"
	}
	return "不解密"
}

// MatchHost reports whether host matches one pattern of the document's
// lists: an exact host, or "*.example.com" for any subdomain at any depth
// but not the apex, as the core's hostmatch does. Names are
// case-insensitive and a trailing dot is ignored.
func MatchHost(pattern, host string) bool {
	pattern, ok := model.NormalizeMITMHostname(pattern)
	host = normalizeHost(host)
	if !ok || host == "" {
		return false
	}
	if rest, wildcard := strings.CutPrefix(pattern, "*."); wildcard {
		return strings.HasSuffix(host, "."+rest)
	}
	return host == pattern
}

func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

func matchAny(patterns []string, host string) bool {
	for _, pattern := range patterns {
		if MatchHost(pattern, host) {
			return true
		}
	}
	return false
}

// DefaultPort is the port the core decrypts: the document's host lists
// carry no ports, so no other port is ever included.
const DefaultPort = 443

// Source is where the tested connection comes from: the device (a
// Doc.Route.Devices id, model.LocalDeviceID for this machine; empty counts
// as this machine) and the destination port (0 counts as DefaultPort).
type Source struct {
	Device string
	Port   int
}

// inScope reports whether decryption covers connections from the device:
// this machine unless NoLocal is set beside a device list, and the listed
// gateway devices.
func inScope(settings *model.MITMSettings, device string) bool {
	switch device {
	case "", model.LocalDeviceID, model.PreDeviceID:
		return !(settings.NoLocal && len(settings.Devices) > 0)
	}
	for _, id := range settings.Devices {
		if id == device {
			return true
		}
	}
	return false
}

// Evaluate decides host for a connection from src. trusted is whether the
// root certificate is trusted on this system. Device scope comes first,
// then exclude over include; an empty include list decrypts nothing, and
// so does a port other than 443.
func Evaluate(doc *model.Doc, host string, trusted bool, src Source) Outcome {
	var settings *model.MITMSettings
	if doc != nil && doc.HTTPTools != nil {
		settings = doc.HTTPTools.MITM
	}
	if settings == nil || !settings.Enabled {
		return Outcome{Decision: Skipped, Reason: ReasonFeatureOff}
	}
	host = normalizeHost(host)
	if host == "" {
		return Outcome{Decision: None}
	}
	device := src.Device
	if d, _, ok := model.ScopeDevice(doc, device); ok {
		device = d.ID
	}
	if !inScope(settings, device) {
		return Outcome{Decision: Skipped, Reason: ReasonScope}
	}
	if !trusted {
		return Outcome{Decision: Skipped, Reason: ReasonNotTrusted}
	}
	var include, exclude []string
	if settings.Hostnames != nil {
		include, exclude = settings.Hostnames.Include, settings.Hostnames.Exclude
	}
	if matchAny(exclude, host) {
		return Outcome{Decision: Skipped, Reason: ReasonExcluded}
	}
	if !matchAny(include, host) || src.Port != 0 && src.Port != DefaultPort {
		return Outcome{Decision: Skipped, Reason: ReasonNotIncluded}
	}
	return Outcome{Decision: Decrypted, Pending: true}
}

// FromCore reads the core's policy test "mitm" value: "" (none),
// "decrypted", "skipped:<reason>" or "blocked-quic". The older spellings
// "decrypt", "skip:<reason>" and "block_quic" are accepted too.
func FromCore(value string) Outcome {
	value = strings.TrimSpace(value)
	switch value {
	case "":
		return Outcome{Decision: None}
	case "decrypted", "decrypt":
		return Outcome{Decision: Decrypted}
	case "blocked-quic", "block_quic":
		return Outcome{Decision: BlockedQUIC}
	}
	for _, prefix := range []string{reasonCorePrefixSkip, "skip:"} {
		if !strings.HasPrefix(value, prefix) {
			continue
		}
		detail := strings.TrimPrefix(value, prefix)
		reason := ReasonCoreOther
		switch strings.ToLower(detail) {
		case "builtin", "builtin_exclude", "built-in":
			reason = ReasonBuiltin
		case "pinning", "pinned", "certificate_pinning":
			reason = ReasonPinning
		case "authority_pending":
			reason = ReasonAuthorityPending
		case "excluded", "exclude":
			reason = ReasonExcluded
		case "not_included", "not-included":
			reason = ReasonNotIncluded
		case "scope":
			reason = ReasonScope
		case "override_skip":
			reason = ReasonOverrideSkip
		case "not_tls":
			reason = ReasonNotTLS
		case "outbound_handler":
			reason = ReasonOutboundHandler
		}
		return Outcome{Decision: Skipped, Reason: reason, Detail: detail}
	}
	return Outcome{Decision: None, Detail: value}
}

// reasonTexts are the user copy of the reason codes the core reports for
// a skipped or failed decryption.
var reasonTexts = map[string]string{
	"authority_pending": "证书未就绪",
	"pinned":            "证书固定",
	"pinning":           "证书固定",
	"builtin":           "内置排除",
	"builtin_exclude":   "内置排除",
	"excluded":          "已排除",
	"not_included":      "不在包含列表",
	"scope":             "设备未启用",
	"override_skip":     "规则跳过",
	"not_tls":           "非 TLS",
	"outbound_handler":  "出站直接处理",
}

// ReasonText maps a core reason code to its label; unknown ones show as
// they are.
func ReasonText(reason string) string {
	if text, ok := reasonTexts[strings.ToLower(strings.TrimSpace(reason))]; ok {
		return text
	}
	return reason
}

// Merge combines the local evaluation with the core's result. A local skip
// for the feature being off or the certificate not being trusted stands,
// because the core cannot see the system trust. Otherwise the core's word
// wins once it was received (answered), an empty one included: a core that
// reports policy leaves the value empty when it does not decrypt. Before
// the answer, or when the test failed, the local result stands, a decrypt
// as pending.
func Merge(local Outcome, coreValue string, answered bool) Outcome {
	if local.Decision == Skipped && (local.Reason == ReasonFeatureOff || local.Reason == ReasonNotTrusted) {
		return local
	}
	if !answered {
		return local
	}
	return FromCore(coreValue)
}
