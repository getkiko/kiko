package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

// This file holds what the menu bar item shows, apart from the menus
// themselves: the rate title and the latency badges.

// formatRate is a rate in bytes per second in four characters, padded on
// the left, K the smallest unit so that a few hundred bytes do not read as
// a large number: "  0K", "0.1K" (any traffic under it), "0.2K", "1.2K",
// " 12K", "999K", "1.0M", "120M", "1.2G". The title keeps its width in the
// monospaced font as the rate changes.
func formatRate(rate float64) string {
	if rate <= 0 || math.IsNaN(rate) {
		return "  0K"
	}
	units := []string{"K", "M", "G", "T"}
	rate /= 1024
	unit := 0
	// A value that would round to 1000 moves to the next unit, so the
	// number never takes four digits.
	for unit < len(units)-1 && rate >= 999.5 {
		rate /= 1024
		unit++
	}
	var number string
	switch {
	case rate < 0.1:
		number = "0.1"
	case rate < 9.95:
		number = fmt.Sprintf("%.1f", rate)
	default:
		number = fmt.Sprintf("%.0f", rate)
	}
	return fmt.Sprintf("%3s%s", number, units[unit])
}

// speedTitle is the menu bar title of the rates: download first, as the
// one people watch.
func speedTitle(down, up float64) string {
	return "↓" + formatRate(down) + " ↑" + formatRate(up)
}

// Latency badge colors, as the console's latency colors.
const (
	delayGood = "#30A46C"
	delayFair = "#F5A524"
	delayBad  = "#E5484D"
)

// trayDelay is a member's latency as the menu shows it: Ms > 0 measured,
// Failed a test that did not get through, Testing one under way.
type trayDelay struct {
	Ms      int
	Failed  bool
	Testing bool
}

// delayOf is the latency of a test result.
func delayOf(r api.LatencyResult) trayDelay {
	if r.Error != "" || r.MedianConnectMs == nil || *r.MedianConnectMs <= 0 {
		return trayDelay{Failed: true}
	}
	return trayDelay{Ms: max(1, int(math.Round(*r.MedianConnectMs)))}
}

// badge is the text and color of the delay's badge, "" for none.
func (d trayDelay) badge() (text, color string) {
	switch {
	case d.Testing:
		return "…", ""
	case d.Failed:
		return "超时", delayBad
	case d.Ms <= 0:
		return "", ""
	case d.Ms < 300:
		return fmt.Sprintf("%d ms", d.Ms), delayGood
	case d.Ms < 800:
		return fmt.Sprintf("%d ms", d.Ms), delayFair
	}
	return fmt.Sprintf("%d ms", d.Ms), delayBad
}

// switchable reports whether the user chooses the group's member: a
// manual group, or a selector of a server that does not report modes.
func switchable(g api.Selector) bool {
	if g.Mode != "" {
		return g.Mode == "manual"
	}
	return strings.EqualFold(g.Type, "selector")
}

// trayShape is what the menus are built from: the groups' names, kinds
// and members, and whether the core runs. A change of anything else, as
// the chosen members or the latencies, updates the items in place.
func trayShape(running bool, groups []api.Selector, speedOption bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%t|%t", running, speedOption)
	for _, g := range groups {
		fmt.Fprintf(&b, "\x1e%s\x1f%t", g.Name, switchable(g))
		for _, m := range g.All {
			b.WriteString("\x1f" + m)
		}
	}
	return b.String()
}

// memberDelay is the latency the menu shows for member of g: the tray's
// own test, else the core's last test of it.
func memberDelay(g api.Selector, member string, own map[string]trayDelay) trayDelay {
	if d, ok := own[member]; ok {
		return d
	}
	if m, ok := g.Member(member); ok && m.DelayMS > 0 {
		return trayDelay{Ms: m.DelayMS}
	}
	return trayDelay{}
}
