package kit

import "github.com/egoist/mygo/ui"

// System colors of macOS, light and dark: the hues of the hero cards'
// glows and state dots.
type sysColor int

const (
	sysBlue sysColor = iota
	sysPurple
	sysGreen
	sysRed
	sysOrange
	sysCyan
	sysTeal
	sysIndigo
	sysSlate
	sysGray
)

var sysColors = [...][2]ui.Color{
	sysBlue:   {ui.Hex("#007AFF"), ui.Hex("#0A84FF")},
	sysPurple: {ui.Hex("#AF52DE"), ui.Hex("#BF5AF2")},
	sysGreen:  {ui.Hex("#34C759"), ui.Hex("#30D158")},
	sysRed:    {ui.Hex("#FF3B30"), ui.Hex("#FF453A")},
	sysOrange: {ui.Hex("#FF9500"), ui.Hex("#FF9F0A")},
	sysCyan:   {ui.Hex("#64D2FF"), ui.Hex("#64D2FF")},
	sysTeal:   {ui.Hex("#30B0C7"), ui.Hex("#40C8E0")},
	sysIndigo: {ui.Hex("#5856D6"), ui.Hex("#5E5CE6")},
	sysSlate:  {ui.Hex("#6E7F99"), ui.Hex("#8796B0")},
	sysGray:   {ui.Hex("#8E8E93"), ui.Hex("#98989D")},
}

// systemColor is s in the window's appearance.
func systemColor(c *ui.Context, s sysColor) ui.Color {
	if T(c).Dark {
		return sysColors[s][1]
	}
	return sysColors[s][0]
}

// TypeLook is the symbol and the glow color of an entity's type: kind is
// "subscription", "inbound", "outbound", "endpoint", "service" or "module", typ
// its type, as "mixed" or "selector". Subscriptions are teal, local
// proxies (mixed, http, socks) blue, TUN purple, direct and URLTest
// green, block red, Selector orange, the other automatic groups green, proxy protocols cyan, Tailscale
// indigo, OpenVPN and OpenConnect slate, USB/IP gray; the colors follow
// the window's appearance.
func TypeLook(c *ui.Context, kind, typ string) (Symbol, ui.Color) {
	look := func(s Symbol, color sysColor) (Symbol, ui.Color) { return s, systemColor(c, color) }
	switch kind {
	case "subscription":
		return look(SymRss, sysTeal)
	case "inbound":
		switch typ {
		case "mixed", "http", "socks", "redirect", "tproxy", "direct":
			return look(SymRadio, sysBlue)
		case "tun":
			return look(SymNetwork, sysPurple)
		}
		return look(SymGlobe, sysCyan)
	case "outbound":
		switch typ {
		case "direct":
			return look(SymArrowRight, sysGreen)
		case "block":
			return look(SymBan, sysRed)
		case "selector":
			return look(SymLayers3, sysOrange)
		case "urltest":
			return look(SymGauge, sysGreen)
		case "fallback":
			return look(SymRoute, sysGreen)
		case "loadbalance":
			return look(SymArrowDownUp, sysGreen)
		case "smart":
			return look(SymSparkles, sysGreen)
		}
		return look(SymGlobe, sysCyan)
	case "endpoint":
		switch typ {
		case "tailscale":
			return look(SymNetwork, sysIndigo)
		case "openvpn-client", "openvpn-server", "openconnect":
			return look(SymLock, sysSlate)
		}
		return look(SymShieldCheck, sysCyan)
	case "service":
		return look(SymUsb, sysGray)
	case "module":
		return look(SymLayers, sysIndigo)
	}
	return look(SymCircleDot, sysGray)
}
