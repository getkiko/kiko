package lanscan

import (
	"context"
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

// SSDP sends one multicast M-SEARCH per scanned interface and, for each
// responder, reads the UPnP device description for name and model.
type SSDP struct {
	Group string        // default 239.255.255.250:1900
	Wait  time.Duration // default 2.5s
	Fetch func(ctx context.Context, rawURL string) ([]byte, error)
}

func (SSDP) Name() string { return SourceSSDP }

// SSDPResponse is a parsed M-SEARCH reply.
type SSDPResponse struct{ ST, Server, Location string }

// ParseSSDP parses an HTTP-over-UDP search response.
func ParseSSDP(pkt []byte) (SSDPResponse, bool) {
	lines := strings.Split(string(pkt), "\n")
	if len(lines) == 0 || !strings.HasPrefix(strings.ToUpper(lines[0]), "HTTP/1.1 200") {
		return SSDPResponse{}, false
	}
	var r SSDPResponse
	for _, l := range lines[1:] {
		k, v, ok := strings.Cut(strings.TrimRight(l, "\r"), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.ToUpper(strings.TrimSpace(k)) {
		case "ST":
			r.ST = v
		case "SERVER":
			r.Server = v
		case "LOCATION":
			r.Location = v
		}
	}
	return r, true
}

// ssdpService maps a search target to a service key the classifier knows.
func ssdpService(st string) string {
	l := strings.ToLower(st)
	switch {
	case strings.Contains(l, "internetgatewaydevice"):
		return "_upnp-igd._tcp"
	case strings.Contains(l, "device:printer"):
		return "_printer._tcp"
	case strings.Contains(l, "mediarenderer"):
		return "_upnp-renderer._tcp"
	case strings.Contains(l, "roku"):
		return "_roku._tcp"
	}
	return ""
}

type upnpDescription struct {
	Device struct {
		FriendlyName string `xml:"friendlyName"`
		Manufacturer string `xml:"manufacturer"`
		ModelName    string `xml:"modelName"`
	} `xml:"device"`
}

// ParseUPnPDescription extracts name and model from a device description.
func ParseUPnPDescription(data []byte) (name, model string) {
	var d upnpDescription
	if xml.Unmarshal(data, &d) != nil {
		return "", ""
	}
	return cleanText(d.Device.FriendlyName), cleanText(d.Device.ModelName)
}

// boundFetch reads a device description over a connection bound to the
// interface of the responder's subnet.
func boundFetch(subnets []Subnet) func(ctx context.Context, rawURL string) ([]byte, error) {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			ap, err := netip.ParseAddrPort(addr)
			if err != nil {
				return nil, err
			}
			return dialerFor(subnets, ap.Addr().Unmap(), "tcp4", 2*time.Second).DialContext(ctx, "tcp4", addr)
		}}
	return func(ctx context.Context, rawURL string) ([]byte, error) { return fetchWith(ctx, transport, rawURL) }
}

func fetchWith(ctx context.Context, transport http.RoundTripper, rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 64<<10))
}

func (s SSDP) Identify(ctx context.Context, req IdentifyRequest) error {
	group, wait, fetch := s.Group, s.Wait, s.Fetch
	if group == "" {
		group = "239.255.255.250:1900"
	}
	if wait == 0 {
		wait = 2500 * time.Millisecond
	}
	if fetch == nil {
		fetch = boundFetch(req.Subnets)
	}
	if group == "-" {
		return nil
	}
	var mu sync.Mutex
	locations := map[netip.Addr]string{}
	services := map[netip.Addr][]string{}
	msearch := []byte("M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 2\r\nST: ssdp:all\r\n\r\n")
	err := multicastExchange(ctx, req.Subnets, group, [][]byte{msearch}, req.Pacer, wait, func(src netip.Addr, pkt []byte) {
		resp, parsed := ParseSSDP(pkt)
		if !parsed {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if resp.Location != "" && locations[src] == "" {
			locations[src] = resp.Location
		}
		if svc := ssdpService(resp.ST); svc != "" {
			services[src] = appendService(services[src], svc)
		}
	})
	if err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	var out sync.Mutex
	for ip, loc := range locations {
		hint := Hint{IP: ip, Source: SourceSSDP, Services: services[ip]}
		u, err := url.Parse(loc)
		if err != nil || (u.Scheme != "http") || u.Hostname() != ip.String() { // only the responder itself
			report(req, hint)
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if data, err := fetch(ctx, loc); err == nil {
				hint.Name, hint.Model = ParseUPnPDescription(data)
			}
			out.Lock()
			report(req, hint)
			out.Unlock()
		}()
	}
	wg.Wait()
	return ctx.Err()
}
