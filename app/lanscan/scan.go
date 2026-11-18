// Package lanscan discovers devices on the local network: it wakes the
// neighbor table with light UDP probes, reads it, then names and classifies
// every host over mDNS, NetBIOS, LLMNR, SSDP and reverse DNS. Nothing leaves
// the machine and nothing is written to disk.
package lanscan

import (
	"context"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Device is one scan result. JSON keys are lower camel case.
type Device struct {
	IP        string   `json:"ip"`
	Name      string   `json:"name,omitempty"`
	Type      string   `json:"type"`
	Model     string   `json:"model,omitempty"`   // readable model name, or the raw identifier
	ModelID   string   `json:"modelId,omitempty"` // Apple model identifier when Model is its marketing name
	OS        string   `json:"os,omitempty"`      // system version, e.g. "iOS 27.0.1"
	MAC       string   `json:"mac,omitempty"`
	MACRandom bool     `json:"macRandom,omitempty"`
	Vendor    string   `json:"vendor,omitempty"`
	Image     string   `json:"image,omitempty"`
	Sources   []string `json:"sources"`
	Gateway   bool     `json:"gateway,omitempty"`
	Self      bool     `json:"self,omitempty"`
}

const publishInterval = 200 * time.Millisecond

// Limits bound the load a scan puts on the network.
type Limits struct {
	PacketsPerSecond int           // global send rate, default 200
	Timeout          time.Duration // whole scan, default 45s
}

// Env holds everything the scan touches, so tests can replace it.
type Env struct {
	Interfaces    func() ([]InterfaceInfo, error)
	Host          Host
	Wake          func(ctx context.Context, subnets []Subnet, targets []netip.Addr, p *Pacer) error
	Identifiers   []Identifier
	LateProbe     Identifier    // run on hosts found only after identification; nil skips
	LockdownRetry Identifier    // second lockdownd try on Apple / private-MAC hosts without a model; nil skips
	Settle        time.Duration // wait between wake and reading the neighbor table
	Hostname      func() string
	Limits        Limits
	Pacer         *Pacer // shared send limiter; Scan builds its own from Limits when nil
}

// DefaultEnv scans this machine's real network.
func DefaultEnv() Env {
	return Env{
		Interfaces: SystemInterfaces,
		Host:       defaultHost(),
		Wake:       WakeProbe,
		Identifiers: []Identifier{
			Ports{}, MDNS{}, NetBIOS{}, LLMNR{}, SSDP{}, ReverseDNS{},
		},
		LateProbe:     Ports{},
		LockdownRetry: Ports{Ports: []int{portLockdown}},
		Settle:        1500 * time.Millisecond,
		Hostname:      func() string { h, _ := os.Hostname(); return h },
	}
}

// Progress describes how far a scan is.
type Progress struct {
	Phase string `json:"phase"` // wake, neighbors, identify, done
	Step  int    `json:"step"`
	Steps int    `json:"steps"`
}

type host struct {
	ip      netip.Addr
	mac     net.HardwareAddr
	names   map[string]string
	model   string
	modelBy int // modelPriority of model's source
	os      string
	ports   []int
	svcs    []string
	netbios bool
	gateway bool
	self    bool
	iface   string
}

func (h *host) device() Device {
	d := Device{IP: h.ip.String(), Gateway: h.gateway, Self: h.self, Sources: []string{}}
	var vendor, kind string
	if len(h.mac) > 0 {
		d.MAC = strings.ToLower(h.mac.String())
		d.MACRandom = IsRandomMAC(h.mac)
		vendor, kind = LookupVendor(h.mac)
		d.Vendor = vendor
		d.Sources = append(d.Sources, "arp")
		if vendor != "" {
			d.Sources = append(d.Sources, "oui")
		}
	}
	best, bestPrio := "", 99
	var all []string
	for src, n := range h.names {
		if p := sourcePriority[src]; p < bestPrio {
			best, bestPrio = n, p
		}
		all = append(all, n)
		d.Sources = append(d.Sources, src)
	}
	sort.Strings(all)
	d.Name = cleanText(best)
	sort.Strings(d.Sources[len(d.Sources)-len(h.names):])
	if len(h.ports) > 0 {
		d.Sources = append(d.Sources, SourcePorts)
	}
	cls := Classify(ClassifyInput{
		Hostname: strings.Join(all, " "), Model: h.model, Vendor: vendor, VendorKind: kind,
		Gateway: h.gateway, Services: h.svcs, NetBIOS: h.netbios, Ports: h.ports,
	})
	d.Type, d.Model, d.Image = cls.Type, cleanText(cls.Model), cls.Image
	if name := MarketingName(d.Model); name != "" {
		d.Model, d.ModelID = name, d.Model
	}
	d.OS = cleanText(h.os)
	return d
}

// Scan runs one scan. report is called with the full current device set
// (sorted by IP) whenever it changes, and with progress as steps advance.
func Scan(ctx context.Context, env Env, progress func(Progress), report func([]Device)) error {
	lim := env.Limits
	if lim.PacketsPerSecond <= 0 {
		lim.PacketsPerSecond = 200
	}
	if lim.Timeout <= 0 {
		lim.Timeout = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, lim.Timeout)
	defer cancel()
	pacer := env.Pacer
	if pacer == nil {
		pacer = NewPacer(lim.PacketsPerSecond)
	}
	steps := 3 + len(env.Identifiers)
	step := 0
	var stepMu sync.Mutex
	advance := func(phase string) {
		stepMu.Lock()
		defer stepMu.Unlock()
		step++
		progress(Progress{Phase: phase, Step: step, Steps: steps})
	}
	progress(Progress{Phase: "wake", Step: 0, Steps: steps})

	ifs, err := env.Interfaces()
	if err != nil {
		return err
	}
	subnets := Subnets(ifs)

	var mu sync.Mutex
	hosts := map[netip.Addr]*host{}
	var (
		lastPub time.Time
		dirty   bool
		timer   *time.Timer
		closed  bool
	)
	emit := func() { // caller holds mu
		dirty, lastPub = false, time.Now()
		list := make([]Device, 0, len(hosts))
		for _, h := range hosts {
			list = append(list, h.device())
		}
		sort.Slice(list, func(i, j int) bool { return ipLess(list[i].IP, list[j].IP) })
		report(list)
	}
	// publish emits at once when idle and otherwise coalesces updates into
	// one report per publishInterval. Caller holds mu.
	publish := func() {
		wait := publishInterval - time.Since(lastPub)
		if wait <= 0 {
			emit()
			return
		}
		dirty = true
		if timer == nil {
			timer = time.AfterFunc(wait, func() {
				mu.Lock()
				defer mu.Unlock()
				timer = nil
				if dirty && !closed {
					emit()
				}
			})
		}
	}
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		if dirty {
			emit()
		}
		closed = true
	}()
	name := ""
	if env.Hostname != nil {
		name = env.Hostname()
	}
	for _, s := range subnets {
		h := &host{ip: s.Self, mac: s.MAC, names: map[string]string{}, self: true, iface: s.Iface}
		if name != "" {
			h.names[SourceSelf] = name
		}
		hosts[s.Self] = h
	}

	// wake probes targets, waits for ARP to settle and merges the neighbor
	// table. Hosts already known with a MAC are skipped.
	wake := func(targets []netip.Addr) error {
		if len(targets) == 0 {
			return ctx.Err()
		}
		if env.Wake != nil {
			if err := env.Wake(ctx, subnets, targets, pacer); err != nil {
				return err
			}
		}
		return sleep(ctx, env.Settle)
	}
	readNeighbors := func() error {
		neighbors, err := env.Host.Neighbors(ctx)
		mu.Lock()
		defer mu.Unlock()
		changed := false
		for _, n := range neighbors {
			if !Contains(subnets, n.IP) {
				continue
			}
			h := hosts[n.IP]
			switch {
			case h == nil:
				hosts[n.IP] = &host{ip: n.IP, mac: n.MAC, names: map[string]string{}, iface: n.Iface}
			case h.self || len(h.mac) > 0:
				continue
			default: // known from a hint or as gateway, MAC learned now
				h.mac, h.iface = n.MAC, n.Iface
			}
			changed = true
		}
		if changed {
			publish()
		}
		return err
	}
	unresolved := func(candidates []netip.Addr) []netip.Addr {
		mu.Lock()
		defer mu.Unlock()
		var res []netip.Addr
		for _, ip := range candidates {
			if h := hosts[ip]; h == nil || !h.self && len(h.mac) == 0 {
				res = append(res, ip)
			}
		}
		return res
	}

	var targets []netip.Addr
	for _, s := range subnets {
		targets = append(targets, s.Hosts()...)
	}
	if err := wake(targets); err != nil {
		return err
	}
	advance("neighbors")

	err = readNeighbors()
	if err != nil && ctx.Err() == nil && len(hosts) == 0 {
		return err
	}
	// A device in Wi-Fi power save (an idle iPhone) often misses the first
	// ARP request, and the kernel does not retry on its own: probe every
	// address still unresolved once more.
	if err := wake(unresolved(targets)); err != nil {
		return err
	}
	_ = readNeighbors()
	gateways, _ := env.Host.Gateways(ctx)
	var lanGateways []netip.Addr
	mu.Lock()
	for _, g := range gateways {
		if !Contains(subnets, g) {
			continue
		}
		lanGateways = append(lanGateways, g)
		h := hosts[g] // a default gateway is on the link even when ARP shows no entry
		if h == nil {
			h = &host{ip: g, names: map[string]string{}}
			hosts[g] = h
		}
		h.gateway = true
	}
	publish()
	mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	advance("identify")

	var live []netip.Addr
	for ip, h := range hosts {
		if !h.self {
			live = append(live, ip)
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].Less(live[j]) })

	apply := func(hint Hint) {
		mu.Lock()
		defer mu.Unlock()
		h := hosts[hint.IP]
		if h == nil {
			if !Contains(subnets, hint.IP) {
				return // only hosts of the scanned subnets
			}
			h = &host{ip: hint.IP, names: map[string]string{}} // answered without an ARP entry: MAC unknown
			hosts[hint.IP] = h
		}
		hint.Name, hint.Model = cleanText(hint.Name), cleanText(hint.Model)
		if hint.Name != "" && !h.self {
			if _, exists := h.names[hint.Source]; !exists {
				h.names[hint.Source] = hint.Name
			}
		}
		if p := modelPriority(hint.Source); hint.Model != "" && (h.model == "" || p < h.modelBy) {
			h.model, h.modelBy = hint.Model, p
		}
		if hint.OS = cleanText(hint.OS); hint.OS != "" {
			h.os = hint.OS
		}
		for _, port := range hint.Ports {
			h.ports = appendPort(h.ports, port)
		}
		for _, s := range hint.Services {
			h.svcs = appendService(h.svcs, s)
		}
		h.netbios = h.netbios || hint.NetBIOS
		publish()
	}
	// The protocols run side by side, sharing the pacer: the scan takes as
	// long as the slowest one rather than their sum.
	req := IdentifyRequest{Hosts: live, Subnets: subnets, Gateways: lanGateways, Pacer: pacer, Report: apply}
	var wg sync.WaitGroup
	for _, id := range env.Identifiers {
		wg.Add(1)
		go func(id Identifier) {
			defer wg.Done()
			_ = id.Identify(ctx, req) // a failing protocol only loses its own hints
			advance("identify")
		}(id)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Identification traffic resolves more neighbors (a phone woken by mDNS
	// answers ARP late): probe hosts still without a MAC and read the table
	// once more before finishing.
	var missing []netip.Addr
	mu.Lock()
	for ip, h := range hosts {
		if !h.self && len(h.mac) == 0 {
			missing = append(missing, ip)
		}
	}
	mu.Unlock()
	sort.Slice(missing, func(i, j int) bool { return missing[i].Less(missing[j]) })
	if err := wake(missing); err != nil {
		return err
	}
	_ = readNeighbors()
	// Hosts that surfaced only now (late ARP entries, mDNS-only answers)
	// still get the port fingerprint and the lockdownd model query.
	probed := map[netip.Addr]bool{}
	for _, ip := range live {
		probed[ip] = true
	}
	// Probed hosts that may be a dozing iPhone or iPad (Apple or private
	// MAC, no model yet) get one more lockdownd attempt.
	var late, retry []netip.Addr
	mu.Lock()
	for ip, h := range hosts {
		switch {
		case h.self:
		case !probed[ip]:
			late = append(late, ip)
		case h.model == "" && len(h.mac) > 0:
			if vendor, _ := LookupVendor(h.mac); IsRandomMAC(h.mac) || vendor == "Apple" {
				retry = append(retry, ip)
			}
		}
	}
	mu.Unlock()
	sort.Slice(late, func(i, j int) bool { return late[i].Less(late[j]) })
	sort.Slice(retry, func(i, j int) bool { return retry[i].Less(retry[j]) })
	var lateWG sync.WaitGroup
	for _, job := range []struct {
		id    Identifier
		hosts []netip.Addr
	}{{env.LateProbe, late}, {env.LockdownRetry, retry}} {
		if job.id == nil || len(job.hosts) == 0 {
			continue
		}
		lateWG.Add(1)
		go func(id Identifier, hosts []netip.Addr) {
			defer lateWG.Done()
			_ = id.Identify(ctx, IdentifyRequest{Hosts: hosts, Subnets: subnets, Gateways: lanGateways, Pacer: pacer, Report: apply})
		}(job.id, job.hosts)
	}
	lateWG.Wait()
	advance("neighbors")
	if err := ctx.Err(); err != nil {
		return err
	}
	progress(Progress{Phase: "done", Step: steps, Steps: steps})
	return nil
}

func ipLess(a, b string) bool {
	x, e1 := netip.ParseAddr(a)
	y, e2 := netip.ParseAddr(b)
	if e1 != nil || e2 != nil {
		return a < b
	}
	return x.Less(y)
}

func appendPort(list []int, p int) []int {
	for _, x := range list {
		if x == p {
			return list
		}
	}
	return append(list, p)
}
