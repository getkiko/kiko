package lanscan

import (
	"context"
	"net/netip"
)

// Name sources, ordered by priority (lower wins).
const (
	SourceSelf     = "self"
	SourceLockdown = "lockdown"
	SourceMDNS     = "mdns"
	SourceNetBIOS  = "netbios"
	SourceLLMNR    = "llmnr"
	SourceSSDP     = "ssdp"
	SourceDNS      = "dns"
)

// SourcePorts marks hosts identified by their open TCP ports; it carries no name.
const SourcePorts = "ports"

var sourcePriority = map[string]int{SourceSelf: 0, SourceLockdown: 1, SourceMDNS: 2, SourceNetBIOS: 3, SourceLLMNR: 4, SourceSSDP: 5, SourceDNS: 6}

// modelPriority ranks model sources: the lockdownd ProductType is exact, the
// mDNS model identifier next, any other source (UPnP modelName) last.
func modelPriority(source string) int {
	switch source {
	case SourceLockdown:
		return 0
	case SourceMDNS:
		return 1
	}
	return 2
}

// Hint is what one protocol learned about one host.
type Hint struct {
	IP       netip.Addr
	Source   string
	Name     string // friendly or host name
	Model    string // model identifier (iPhone18,2, MacBookPro18,3) or UPnP model name
	OS       string // system version, e.g. "iOS 27.0.1"
	Ports    []int  // open TCP ports
	Services []string
	NetBIOS  bool
}

// IdentifyRequest is the input of an Identifier.
type IdentifyRequest struct {
	Hosts    []netip.Addr
	Subnets  []Subnet
	Gateways []netip.Addr // default gateways inside Subnets
	Pacer    *Pacer
	Report   func(Hint)
}

// Identifier learns names and models over one protocol. It must return
// promptly after ctx ends.
type Identifier interface {
	Name() string
	Identify(ctx context.Context, req IdentifyRequest) error
}
