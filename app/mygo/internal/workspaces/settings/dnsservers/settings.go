package dnsservers

import (
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/forms"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// settingsState is what the DNS Settings rows keep between frames: the cache
// capacity as typed, which saves only while it is a valid number.
type settingsState struct {
	capacity      string
	capacityDoc   int
	capacityReady bool
	capacityError string
}

// update changes the document, showing a refusal as a toast.
func (s *Section) update(fn func(doc *model.Doc)) {
	if err := s.store.Update(fn); err != nil {
		s.env.ToastError(err)
	}
}

// Settings builds the DNS Settings group of Settings › DNS: the query strategy, the
// default domain resolver with its detour and strategy, the cache and the
// client subnet, and the switches. Each row saves as it changes.
func (s *Section) Settings(c *ui.Context) {
	doc := s.store.Doc()
	kit.SettingsGroup(c, "DNS 设置", "", func() { s.settingsRows(c, doc) })
}

func (s *Section) settingsRows(c *ui.Context, doc *model.Doc) {
	strategies := strategyOptions(doc.IPv6Enabled)
	strategy := doc.DNS.Strategy
	kit.SettingsRow(c, "DNS 查询策略", "", func() {
		if kit.Select(c, "dns-strategy", &strategy, strategies, kit.SelectOptions{Width: 220, Label: "DNS 查询策略"}).Changed() {
			s.update(func(d *model.Doc) { d.DNS.Strategy = strategy })
		}
	})

	resolver := doc.Route.DefaultDomainResolver.ID()
	resolvers := []kit.Option{{Value: "", Label: "无"}}
	for _, server := range doc.DNS.Servers {
		if server.Enabled {
			resolvers = append(resolvers, kit.Option{Value: server.ID, Label: server.Tag})
		}
	}
	if resolver != "" && !slices.ContainsFunc(resolvers, func(o kit.Option) bool { return o.Value == resolver }) {
		resolvers = append(resolvers, kit.Option{Value: resolver, Label: resolver + " · 需修正引用"})
	}
	kit.SettingsRow(c, "默认域名解析器", "解析出站与端点的服务器域名", func() {
		if kit.Select(c, "resolver", &resolver, resolvers, kit.SelectOptions{Width: 220, Label: "默认域名解析器"}).Changed() {
			s.update(func(d *model.Doc) {
				if resolver == "" {
					d.Route.DefaultDomainResolver = model.Null()
				} else {
					d.Route.DefaultDomainResolver = model.Some(resolver)
				}
				if !model.DNSTargetSupportsDetour(model.DNSServerByID(d, resolver)) {
					d.Route.DefaultDomainResolverDetour = model.Null()
				}
			})
		}
	})
	if resolver != "" {
		detour := doc.Route.DefaultDomainResolverDetour.ID()
		kit.SettingsRow(c, "默认解析器出口", "", func() {
			ui.Column(c).Width(220).Children(func() {
				if forms.DNSDetourSelect(c, "detour", doc, resolver, &detour, "默认域名解析器查询出口", forms.DocRefOptions(doc)).FillWidth().Changed() {
					s.update(func(d *model.Doc) {
						if detour == "" || !model.DNSTargetSupportsDetour(model.DNSServerByID(d, resolver)) {
							d.Route.DefaultDomainResolverDetour = model.Null()
						} else {
							d.Route.DefaultDomainResolverDetour = model.Some(detour)
						}
					})
				}
			})
		})
	}
	resolverStrategy := doc.Route.DefaultDomainStrategy
	kit.SettingsRow(c, "默认解析器策略", "", func() {
		if kit.Select(c, "resolver-strategy", &resolverStrategy, strategies, kit.SelectOptions{Width: 220, Label: "默认解析器策略"}).Changed() {
			s.update(func(d *model.Doc) { d.Route.DefaultDomainStrategy = resolverStrategy })
		}
	})

	st := &s.settingsState
	if !st.capacityReady || st.capacityDoc != doc.DNS.CacheCapacity {
		st.capacity, st.capacityDoc, st.capacityReady, st.capacityError = "", doc.DNS.CacheCapacity, true, ""
		if doc.DNS.CacheCapacity != 0 {
			st.capacity = strconv.Itoa(doc.DNS.CacheCapacity)
		}
	}
	kit.SettingsRow(c, "缓存容量", st.capacityError, func() {
		ui.Column(c).Width(220).Children(func() {
			if kit.Input(c, "capacity", &st.capacity, "").Label("缓存容量").Changed() {
				capacity, ok := parseCapacity(st.capacity)
				if !ok {
					st.capacityError = "缓存容量应为非负整数"
					return
				}
				st.capacityError, st.capacityDoc = "", capacity
				s.update(func(d *model.Doc) { d.DNS.CacheCapacity = capacity })
			}
		})
	})
	subnet := doc.DNS.ClientSubnet
	kit.SettingsRow(c, "EDNS Client Subnet", "", func() {
		ui.Column(c).Width(220).Children(func() {
			if kit.Input(c, "subnet", &subnet, "203.0.113.0/24").Label("EDNS Client Subnet").Changed() {
				s.update(func(d *model.Doc) { d.DNS.ClientSubnet = subnet })
			}
		})
	})
	settingSwitch(c, s, "禁用缓存", doc.DNS.DisableCache, func(d *model.Doc, on bool) { d.DNS.DisableCache = on })
	settingSwitch(c, s, "禁用过期", doc.DNS.DisableExpire, func(d *model.Doc, on bool) { d.DNS.DisableExpire = on })
	settingSwitch(c, s, "反向映射", doc.DNS.ReverseMapping, func(d *model.Doc, on bool) { d.DNS.ReverseMapping = on })
}

// parseCapacity reads a cache capacity: empty is 0, else a non-negative
// integer.
func parseCapacity(text string) (int, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, true
	}
	n, err := strconv.ParseFloat(text, 64)
	if err != nil || n < 0 || n != float64(int(n)) {
		return 0, false
	}
	return int(n), true
}

// settingSwitch is a settings row with a switch that saves set.
func settingSwitch(c *ui.Context, s *Section, label string, value bool, set func(d *model.Doc, on bool)) {
	kit.SettingsRow(c, label, "", func() {
		on := value
		if kit.Switch(c, &on, label).Changed() {
			s.update(func(d *model.Doc) { set(d, on) })
		}
	})
}

// formGroup is a rounded group of form rows (NativeSettingsGroup).
func formGroup(c *ui.Context, fn func()) ui.Element {
	k := kit.T(c)
	return ui.Column(c).Radius(14).Background(k.Surface).Border(1, k.HairlineSoft).PaddingX(14).PaddingY(4).
		Dividers(1, k.HairlineSoft).Shrink(0).Children(fn)
}

// fieldRow is a form row: the label on the left, the control filling the
// right (PolicyField).
func fieldRow(c *ui.Context, label string, control func()) ui.Element {
	k := kit.T(c)
	return ui.Row(c.Key(label)).MinHeight(44).PaddingY(7).Gap(12).AlignItems(ui.Start).Shrink(0).Children(func() {
		ui.Text(c, label).FontSize(kit.FontBody).TextColor(k.Label2).Width(118).Shrink(0).Margin(7, 0, 0)
		ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(control)
	})
}

// switchRow is a form row with a switch on the right.
func switchRow(c *ui.Context, label string, on *bool) ui.Element {
	k := kit.T(c)
	return ui.Row(c.Key(label)).MinHeight(44).PaddingY(7).Gap(12).Shrink(0).Children(func() {
		// The label toggles too, as a <label> around the switch does.
		text := ui.ButtonBase(c).Grow(1).MinWidth(0).Justify(ui.Start).Children(func() {
			ui.Text(c, label).FontSize(kit.FontBody).TextColor(k.Label)
		})
		if text.Clicked() {
			*on = !*on
		}
		kit.Switch(c, on, label)
	})
}
