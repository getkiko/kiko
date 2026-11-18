package model

import "strings"

// HTTPTools holds the HTTP tooling settings of the document (HTTPS
// decryption today; rewrites and scripts join in later work packages).
type HTTPTools struct {
	MITM *MITMSettings `json:"mitm,omitempty"`
	// Rewrites are the URL, header and local-mapping rules.
	Rewrites *HTTPRewrites `json:"rewrites,omitempty"`
	// Scripts are the user scripts, in run order.
	Scripts []*HTTPScript `json:"scripts,omitempty"`
	// Modules are the installed modules (see httpmodules.go); the emitter
	// merges the enabled ones after the user's own entries.
	Modules []*HTTPModule `json:"modules,omitempty"`
}

// MITM quic modes.
const (
	MITMQUICBlock = "block"
	MITMQUICAllow = "allow"
)

// MITMSettings is the HTTPS decryption configuration.
type MITMSettings struct {
	// Enabled turns decryption on. It stays off until the user has
	// trusted the root certificate.
	Enabled bool `json:"enabled,omitempty"`
	// Acknowledged records that the user confirmed the first enable.
	Acknowledged bool `json:"acknowledged,omitempty"`
	// Hostnames select the hosts to decrypt; an empty include list decrypts
	// nothing.
	Hostnames *MITMHostnames `json:"hostnames,omitempty"`
	// BuiltinExclude keeps the core's built-in exclusion table (hosts that
	// break under interception) active. Absent means true.
	BuiltinExclude *bool `json:"builtinExclude,omitempty"`
	// DisabledBuiltin lists built-in table ids the user re-enabled for
	// decryption.
	DisabledBuiltin []string `json:"disabledBuiltin,omitempty"`
	// Devices are the ids of gateway devices (Doc.Devices) whose traffic is
	// decrypted besides this machine's own.
	Devices []string `json:"devices,omitempty"`
	// NoLocal leaves this machine's own traffic undecrypted; meaningful with
	// Devices only.
	NoLocal bool `json:"noLocal,omitempty"`
	// Upstream tunes the TLS connection from the core to the real server.
	Upstream *MITMUpstream `json:"upstream,omitempty"`
	// PinningSkipMinutes is how long a host that fails certificate
	// validation stays undecrypted; absent or 0 means 30.
	PinningSkipMinutes int `json:"pinningSkipMinutes,omitempty"`
	// QUIC is "block" (default) or "allow"; emitted as mitm.quic.
	QUIC string `json:"quic,omitempty"`
	// SaveBody keeps request and response bodies in the capture ring;
	// emitted as mitm.capture.body.
	SaveBody bool `json:"saveBody,omitempty"`
	// Persist keeps finished exchanges on disk. Absent means off; the
	// settings stay while it is switched off.
	Persist *MITMPersist `json:"persist,omitempty"`
}

// MITM persistence limits; they mirror capture.persist in the core.
const (
	MITMPersistDefaultMB            = 256
	MITMPersistDefaultMaxRecords    = 20000
	MITMPersistDefaultRetentionDays = 7
	MITMPersistMinMB                = 1
	MITMPersistMaxMB                = 65536
	MITMPersistMaxRecords           = 1000000
	MITMPersistMaxRetentionDays     = 3650
	MITMPersistMaxRedactHeaders     = 64
)

// MITMPersist is the on-disk copy of captured exchanges. Sensitive headers
// (Authorization, Cookie, Set-Cookie, Proxy-Authorization, X-Api-Key and
// more) are always masked before writing; RedactHeaders adds names.
type MITMPersist struct {
	Enabled bool `json:"enabled,omitempty"`
	// Body also writes request and response bodies; it needs SaveBody.
	Body bool `json:"body,omitempty"`
	// MaxMB is the disk budget in MiB; 0 means 256.
	MaxMB int `json:"maxMB,omitempty"`
	// MaxRecords is the most stored records; 0 means 20000.
	MaxRecords int `json:"maxRecords,omitempty"`
	// RetentionDays is how long a record is kept; 0 means 7.
	RetentionDays int      `json:"retentionDays,omitempty"`
	RedactHeaders []string `json:"redactHeaders,omitempty"`
}

// MITMPersistLimits is MITMPersist with defaults applied and values clamped
// into the core's ranges.
type MITMPersistLimits struct {
	MaxMB         int
	MaxRecords    int
	RetentionDays int
	RedactHeaders []string
	// Invalid lists the header names that were dropped.
	Invalid []string
}

// ValidMITMHeaderName reports whether name is usable as an extra redacted
// header: non-empty, without whitespace or a colon (the core's rule).
func ValidMITMHeaderName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && !strings.ContainsAny(name, " \t:\r\n")
}

// Limits resolves defaults and ranges. A nil receiver yields the defaults.
func (p *MITMPersist) Limits() MITMPersistLimits {
	limits := MITMPersistLimits{MaxMB: MITMPersistDefaultMB, MaxRecords: MITMPersistDefaultMaxRecords, RetentionDays: MITMPersistDefaultRetentionDays}
	if p == nil {
		return limits
	}
	if p.MaxMB > 0 {
		limits.MaxMB = min(max(p.MaxMB, MITMPersistMinMB), MITMPersistMaxMB)
	}
	if p.MaxRecords > 0 {
		limits.MaxRecords = min(p.MaxRecords, MITMPersistMaxRecords)
	}
	if p.RetentionDays > 0 {
		limits.RetentionDays = min(p.RetentionDays, MITMPersistMaxRetentionDays)
	}
	seen := map[string]bool{}
	for _, name := range p.RedactHeaders {
		name = strings.TrimSpace(name)
		switch {
		case !ValidMITMHeaderName(name):
			if name != "" {
				limits.Invalid = append(limits.Invalid, name)
			}
		case seen[strings.ToLower(name)]:
		case len(limits.RedactHeaders) >= MITMPersistMaxRedactHeaders:
			limits.Invalid = append(limits.Invalid, name)
		default:
			seen[strings.ToLower(name)] = true
			limits.RedactHeaders = append(limits.RedactHeaders, name)
		}
	}
	return limits
}

// MITM upstream TLS fingerprints.
const (
	MITMFingerprintNone    = "none"
	MITMFingerprintChrome  = "chrome"
	MITMFingerprintSafari  = "safari"
	MITMFingerprintFirefox = "firefox"
)

// MITMUpstream is the upstream TLS configuration of decrypted connections.
type MITMUpstream struct {
	// Fingerprint is chrome, safari, firefox or none (standard TLS). Empty
	// means chrome, the core default; chrome is not written out.
	Fingerprint string `json:"fingerprint,omitempty"`
	// SkipVerify lists hosts whose server certificate is not verified.
	SkipVerify []string `json:"skipVerify,omitempty"`
	// CustomRoots are PEM file paths of extra trusted roots.
	CustomRoots []string `json:"customRoots,omitempty"`
}

// MITMDefaultPinningSkipMinutes is the default skip time of a host that
// failed certificate validation.
const MITMDefaultPinningSkipMinutes = 30

// BuiltinExcludeOn reports whether the built-in exclusion table applies.
func (m *MITMSettings) BuiltinExcludeOn() bool {
	return m == nil || m.BuiltinExclude == nil || *m.BuiltinExclude
}

// PinningSkip returns the skip time in minutes, defaulting to 30.
func (m *MITMSettings) PinningSkip() int {
	if m == nil || m.PinningSkipMinutes <= 0 {
		return MITMDefaultPinningSkipMinutes
	}
	return m.PinningSkipMinutes
}

// ValidMITMFingerprint reports whether f is a known fingerprint or empty.
func ValidMITMFingerprint(f string) bool {
	switch f {
	case "", MITMFingerprintNone, MITMFingerprintChrome, MITMFingerprintSafari, MITMFingerprintFirefox:
		return true
	}
	return false
}

// MITMHostnames are the include and exclude host patterns.
type MITMHostnames struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}
