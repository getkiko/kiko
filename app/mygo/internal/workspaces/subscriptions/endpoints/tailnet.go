package endpoints

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// holderTailnet holds the runtime state for the Overview Tailscale card.
const holderTailnet = "tailnet"

// TailscalePanel is the document's Tailscale endpoint as another page
// shows it, the Overview card: which endpoint qualifies (Endpoint), its live
// state (Follow, Summary) and its control sheet (Open, View) — the
// devices, this device, the exit node, Taildrop and sign-out, each
// device's detail with its ping and SSH. It shares the section's
// runtime state, one subscription for both pages.
type TailscalePanel struct {
	s     *Section
	sheet tailscaleSheet
	// open is the endpoint's tag while its sheet shows.
	open string
	// following tells Follow last held the runtime state.
	following bool
	// endpoint is Endpoint's answer for the document version and the
	// library size in checked, as the page asks every frame.
	endpoint *model.Endpoint
	checked  [2]int
	known    bool
}

// Tailscale is the section's Tailscale panel.
func (s *Section) Tailscale() *TailscalePanel { return s.tailnet }

func newTailscalePanel(s *Section) *TailscalePanel {
	p := &TailscalePanel{s: s}
	p.sheet.s = s
	p.sheet.onClose = func() { p.open = "" }
	return p
}

// Endpoint is the document's Tailscale endpoint when it is enabled and
// its configuration is complete: a valid name, no broken reference, a
// UDP path its detour allows (what the editor refuses to save). Nil
// otherwise.
func (p *TailscalePanel) Endpoint() *model.Endpoint {
	key := [2]int{p.s.store.Version(), len(p.s.store.NetworkConfigs())}
	if !p.known || p.checked != key {
		p.endpoint, p.checked, p.known = p.qualifying(), key, true
	}
	return p.endpoint
}

func (p *TailscalePanel) qualifying() *model.Endpoint {
	doc := p.s.store.Doc()
	i := slices.IndexFunc(doc.Endpoints, func(x *model.Endpoint) bool { return x.Type == "tailscale" })
	if i < 0 {
		return nil
	}
	endpoint := doc.Endpoints[i]
	if !endpoint.Enabled || model.TagValidationError(doc, endpoint.Tag, endpoint.ID) != "" ||
		model.HasInvalidEndpointReferences(doc, endpoint, p.s.store.NetworkConfigs()) {
		return nil
	}
	if problem, _ := udpPathIssues(doc, endpoint.Fields); problem != "" {
		return nil
	}
	return endpoint
}

// Follow keeps the live state running while on, the Overview page showing
// the card; off, it also stops the sheet's ping and download. Cheap to
// call every frame. UI thread.
func (p *TailscalePanel) Follow(on bool) {
	if on == p.following {
		return
	}
	p.following = on
	p.s.rt.hold(holderTailnet, on)
	if !on {
		p.sheet.stop()
	}
}

// TailscaleSummary is what the Overview card shows of a Tailscale endpoint.
type TailscaleSummary struct {
	// Live tells that the core reports the endpoint's state now; without
	// it the facts are the last ones seen, or empty.
	Live bool
	// State is the state's words, as the control sheet shows them.
	State string
	// Self is this device's name, IPv4 its Tailscale IPv4 address,
	// Tailnet the tailnet's name, ExitNode the exit node's name.
	Self, IPv4, Tailnet, ExitNode string
	// UDPPath counts the online devices by the UDP path udp_path chose
	// ("Direct 2 · Via proxy 1"), empty while udp_path is off.
	UDPPath string
	// Online is how many other devices are online.
	Online int
	// AuthURL is the login page while the endpoint waits for a login.
	AuthURL string

	// Motion is how the logo's dots move: breathing while online, flowing
	// while connecting, still otherwise and with the core stopped.
	Motion kit.MarkMotion

	tone  stateTone
	peers []tailnetPeer
}

// reConnectingState matches the backend states of a device on its way
// online.
var reConnectingState = regexp.MustCompile(`connecting|starting|reconnect`)

// tailscaleMotion is the logo's motion for a live status and its state
// line: online breathes, connecting flows; a needed login, an expired
// or broken connection, an error and no live state stay still.
func tailscaleMotion(live *api.TailscaleStatus, line stateLine) kit.MarkMotion {
	switch {
	case live == nil:
		return kit.MarkStill
	case line.tone == toneOK:
		return kit.MarkBreathe
	case line.tone == toneWarn && reConnectingState.MatchString(strings.ToLower(live.BackendState)) &&
		line.text == tailscaleStateLabel(live.StateText, live.BackendState):
		return kit.MarkFlow
	}
	return kit.MarkStill
}

// tailscaleLine is a Tailscale endpoint's state line, "Not running" without a
// state.
func tailscaleLine(status *api.TailscaleStatus, now time.Time) stateLine {
	if status == nil {
		return stateLine{"未运行", toneIdle}
	}
	return tailscaleConnectionState(status, now)
}

// Summary is endpoint tag's state and facts: live, else the last seen.
// While live the view redraws now and then, so an observation grown old
// loses its green state while the stream is quiet.
func (p *TailscalePanel) Summary(c *ui.Context, tag string) TailscaleSummary {
	tag = p.s.rt.liveTailscaleTag(tag)
	live := p.s.rt.tailscaleStatus(tag)
	if live != nil {
		c.After(freshness)
	}
	return summarize(live, p.s.rt.lastTailscale[tag], time.Now())
}

func summarize(live, last *api.TailscaleStatus, now time.Time) TailscaleSummary {
	line := tailscaleLine(live, now)
	m := TailscaleSummary{Live: live != nil, State: line.text, tone: line.tone, Motion: tailscaleMotion(live, line)}
	status := live
	if status == nil {
		status = last
	}
	if status == nil {
		return m
	}
	if status.Self != nil {
		m.Self, m.IPv4 = peerName(*status.Self), preferV4(status.Self.TailscaleIPs)
	}
	m.Tailnet = firstOf(status.NetworkName, strings.TrimSuffix(status.MagicDNSSuffix, "."))
	if live == nil {
		return m
	}
	if status.ExitNode != nil {
		m.ExitNode = peerName(*status.ExitNode)
	}
	m.AuthURL = status.AuthURL
	for _, peer := range tailnetPeers(status) {
		if peer.Online {
			m.peers = append(m.peers, peer)
		}
	}
	m.Online = len(m.peers)
	paths := map[string]int{}
	for _, g := range status.UserGroups {
		for _, peer := range g.Peers {
			if peer.Online && peer.UDPPath != "" {
				paths[peer.UDPPath]++
			}
		}
	}
	var parts []string
	for _, o := range udpPathOptions {
		if n := paths[o.Value]; n > 0 {
			parts = append(parts, o.Label+" "+strconv.Itoa(n))
		}
	}
	m.UDPPath = strings.Join(parts, " · ")
	return m
}

// StateColor is the state's color: green online, orange on the way or
// waiting, red failing, gray stopped.
func (m TailscaleSummary) StateColor(c *ui.Context) ui.Color { return toneColor(kit.T(c), m.tone) }

// Avatars are up to three online devices' initials over how many are
// online; nothing without one.
func (m TailscaleSummary) Avatars(c *ui.Context) {
	if len(m.peers) > 0 {
		peerAvatars(c, m.peers)
	}
}

// Open shows the control sheet of endpoint tag.
func (p *TailscalePanel) Open(tag string) { p.open = tag }

// Opened is the tag of the endpoint whose sheet shows, "" for none.
func (p *TailscalePanel) Opened() string { return p.open }

// View builds the sheet while it is open, closing it as its endpoint
// stops qualifying. Build it after the page's content.
func (p *TailscalePanel) View(c *ui.Context) {
	if p.open != "" {
		if endpoint := p.Endpoint(); endpoint == nil || endpoint.Tag != p.open {
			p.open = ""
		}
	}
	if p.open == "" {
		if p.sheet.tag != "" {
			p.sheet.closed()
		}
		return
	}
	if len(p.s.rt.tailscale) > 0 {
		c.After(freshness)
	}
	p.sheet.view(c, p.open)
}

// tailscaleConfig is what a Tailscale endpoint's configuration says, as
// its card and its sheet show it.
type tailscaleConfig struct {
	// hostname is the device name set, control the control server set,
	// without its scheme; empty for the defaults.
	hostname, control string
	// login is "Auth key set" with an auth key, else "Log in with link".
	login string
	// udpPath is the default UDP path and the count of per-device rules,
	// empty without udp_path; udpDefault the default path alone.
	udpPath, udpDefault string
	// systemInterface tells a system TUN interface is created, named
	// interfaceName when set.
	systemInterface bool
	interfaceName   string
}

func tailscaleFacts(f model.Fields) tailscaleConfig {
	cfg := tailscaleConfig{hostname: text(f["hostname"]), login: "链接登录"}
	control := text(f["control_url"])
	if _, rest, ok := strings.Cut(control, "://"); ok {
		control = rest
	}
	cfg.control = strings.TrimSuffix(control, "/")
	if text(f["auth_key"]) != "" {
		cfg.login = "已填 Auth Key"
	}
	var parts []string
	if path := text(f["udp_path.default"]); path != "" {
		cfg.udpDefault = udpPathLabel(path)
		parts = append(parts, cfg.udpDefault)
	}
	rules := slices.DeleteFunc(udpPathRules(f[udpPathPeersKey]), func(r udpPathRule) bool { return len(r.match) == 0 })
	if len(rules) > 0 {
		parts = append(parts, strconv.Itoa(len(rules))+" 条对端规则")
	}
	cfg.udpPath = strings.Join(parts, " · ")
	cfg.systemInterface = jsv.Truthy(f["system_interface"])
	cfg.interfaceName = text(f["system_interface_name"])
	return cfg
}
