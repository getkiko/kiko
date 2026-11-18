// Package endpoints is the Endpoints section of the subscriptions
// workspace: endpoint (Tailscale, WireGuard, OpenConnect, OpenVPN) and USB/IP
// service hero cards with their editors, the USB/IP guide and wizard, and
// the runtime controls (OpenConnect and OpenVPN authentication; USB/IP
// sharing) fed by the server's events. The Tailscale endpoint's live
// state and control sheet (devices, exit node, ping, Taildrop and SSH)
// show on the Overview page through TailscalePanel.
package endpoints

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
	"github.com/getkiko/kiko/app/mygo/internal/store"
)

// Kinds of the section's items.
const (
	kindEndpoint = "endpoint"
	kindService  = "service"
)

// confirmation is a pending "Restore defaults" or Delete of an item, anchored to its
// card's more button or to the editor's button.
type confirmation struct {
	action, kind, id, tag string
	inEditor              bool
	state                 kit.ConfirmState
}

// control is the item whose runtime sheet is open.
type control struct{ kind, id string }

// Section is the Endpoints section. Its state lasts while the console runs.
type Section struct {
	env   *env.Env
	store *store.Store
	rt    *runtimeState

	// add is the add menu, at the heading's add button or a list's ＋
	// card, by its kind.
	add kit.MenuAnchor
	// confirmAt is the more button of the card confirming, as last built,
	// for Overlays to anchor to while they are built in the same pass.
	confirmAt ui.Element

	editor  editor
	wizard  wizard
	confirm confirmation
	control control

	// tailnet is the Tailscale endpoint's state and control sheet for
	// the Overview card.
	tailnet *TailscalePanel
	tunnel  tunnelSheet
	usbip   usbipSheet
}

// New returns the section.
func New(e *env.Env, s *store.Store) *Section {
	sec := &Section{env: e, store: s, rt: newRuntimeState(e)}
	sec.editor.s = sec
	sec.wizard.s = sec
	sec.tailnet = newTailscalePanel(sec)
	sec.tunnel.s = sec
	sec.usbip.s = sec
	return sec
}

// holderPage holds the runtime state while the section shows.
const holderPage = "page"

// SetActive follows the live status (snapshot, events) while the section
// shows, checking the host for USB/IP as it does.
func (s *Section) SetActive(active bool) {
	s.rt.hold(holderPage, active)
	if active {
		s.rt.refreshPreflight()
	}
}

func (s *Section) platform(doc *model.Doc) string {
	if doc.Platform == "" {
		return model.PlatformLinux
	}
	return doc.Platform
}

func typeLabel(t string) string {
	if label, ok := schema.TypeLabel[t]; ok && label != "" {
		return label
	}
	return t
}

// serviceAddress is a service's address on its card.
func serviceAddress(service *model.Service) string {
	f := service.Fields
	if service.Type == "usbip-client" {
		server, port := text(f["server"]), text(f["server_port"])
		if server == "" {
			return ""
		}
		if port != "" {
			return server + ":" + port
		}
		return server
	}
	listen := "0.0.0.0"
	if jsv.Truthy(f["listen"]) {
		listen = jsv.String(f["listen"])
	}
	port := jsv.StringOr(f["listen_port"], "")
	host := listen
	if strings.Contains(listen, ":") && !strings.HasPrefix(listen, "[") {
		host = "[" + listen + "]"
	}
	if port != "" {
		return host + ":" + port
	}
	return host
}

// runtimeLine is an endpoint's state on its card; ok is false for types
// without one. Tailscale's shows on the Overview card (TailscalePanel).
func (s *Section) runtimeLine(endpoint *model.Endpoint) (stateLine, bool) {
	idle := stateLine{"未运行", toneIdle}
	switch endpoint.Type {
	case "openconnect":
		if st := s.rt.openconnect[endpoint.Tag]; st != nil {
			return stateLine{firstOf(st.Error, st.StateText, st.State, "未知"), toneOf(st.State, st.Error)}, true
		}
		return idle, true
	case "openvpn-client":
		if st := s.rt.openvpn[endpoint.Tag]; st != nil {
			return stateLine{firstOf(st.Error, st.StateText, st.State, "未知"), toneOf(st.State, st.Error)}, true
		}
		return idle, true
	}
	return stateLine{}, false
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// View builds the section heading and its two lists, then its overlays:
// the section as one column, for a page scrolling it whole.
func (s *Section) View(c *ui.Context) {
	ui.Column(c.Key("endpoints-section")).FillWidth().Shrink(0).Label("端点与 USB/IP 列表").Children(func() {
		s.Heading(c)
		s.Content(c)
	})
	s.Overlays(c)
}

// Heading is the section heading and its counts; the lists' ＋ cards
// add.
func (s *Section) Heading(c *ui.Context) {
	doc := s.store.Doc()
	count := fmt.Sprintf("%d 个 · %d 个 USB/IP", len(doc.Endpoints), len(doc.Services))
	kit.SectionHeading(c, "端点", count, nil)
}

// Content is the section's two lists, endpoints then USB/IP services.
func (s *Section) Content(c *ui.Context) {
	doc := s.store.Doc()
	ui.Column(c.Key("endpoints-content")).FillWidth().Shrink(0).Label("端点与 USB/IP").Children(func() {
		s.endpointList(c, doc)
		ui.Box(c).Height(24).Shrink(0)
		s.serviceList(c, doc)
	})
}

// Overlays are what opens over the page: the add menu and a card's
// confirmation, at their buttons when Heading and Content built them in
// this pass, the editors, the wizard and the runtime sheets. Build them
// after Heading and Content, outside a list that may not build those.
func (s *Section) Overlays(c *ui.Context) {
	doc := s.store.Doc()
	if anchor, _, ok := s.add.At(); ok {
		s.addMenu(c, doc, anchor)
	}
	if !s.confirm.inEditor && s.confirm.state.Open && s.confirmAt.Valid() {
		s.confirmPopover(c, s.confirmAt)
	}
	s.editor.view(c, doc)
	s.wizard.view(c, doc)
	s.controlSheets(c, doc)
}

// addMenu is the add menu at anchor, the add button or a list's ＋ card:
// Tailscale's quick add, the endpoint types and the USB/IP services.
func (s *Section) addMenu(c *ui.Context, doc *model.Doc, anchor ui.Element) {
	platform := s.platform(doc)
	// A document keeps one Tailscale endpoint: its choices show as taken.
	tailscaleTaken := model.CanAddEndpoint(doc, "tailscale") != ""
	const taken = "（已有 Tailscale 端点）"
	quickItem := kit.ActionItem{ID: "tailscale-quick", Label: "Tailscale 链接登录",
		SearchText: "tailscale 快速添加", Icon: kit.SymSparkles, Run: func() { s.editor.quickAddTailscale(doc) }}
	if tailscaleTaken {
		quickItem.Detail, quickItem.Disabled = taken, true
	}
	quick := kit.ActionGroup{Label: "快速添加", Items: []kit.ActionItem{quickItem}}
	endpoints := kit.ActionGroup{Label: "Endpoint"}
	for _, t := range model.EndpointTypes {
		item := kit.ActionItem{ID: t, Label: typeLabel(t), SearchText: t + " " + typeLabel(t),
			Icon: kit.SymCable, Run: func() { s.editor.addEndpoint(doc, t) }}
		if t == "tailscale" && tailscaleTaken {
			item.Detail, item.Disabled = taken, true
		}
		endpoints.Items = append(endpoints.Items, item)
	}
	services := kit.ActionGroup{Label: "USB/IP"}
	for _, t := range model.ServiceTypes {
		item := kit.ActionItem{ID: t, Label: typeLabel(t), SearchText: t + " " + typeLabel(t) + " usb", Icon: kit.SymUsb,
			Run: func() { s.editor.addService(doc, t) }}
		if !schema.ServiceAvailable(t, platform) {
			item.Detail = "（" + schema.PlatformLabel[platform] + " 不可用）"
			item.Disabled = true
		}
		services.Items = append(services.Items, item)
	}
	kit.ActionMenu(c, anchor, &s.add.Open, []kit.ActionGroup{quick, endpoints, services}, kit.ActionMenuOptions{Width: 260, Label: "添加端点或 USB/IP"})
}

// dragItem is an endpoint card being dragged to reorder.
type dragItem struct{ id string }

// endpointList is the endpoints' hero cards, reordered by dragging,
// then "+ Add endpoint", opening the add menu at it.
func (s *Section) endpointList(c *ui.Context, doc *model.Doc) {
	library := s.store.NetworkConfigs()
	subHeading(c, "Endpoint", fmt.Sprint(len(doc.Endpoints)), nil)
	ui.Box(c).Height(8).Shrink(0)
	ids := make([]string, len(doc.Endpoints))
	for i, endpoint := range doc.Endpoints {
		ids[i] = endpoint.ID
	}
	heroGrid(c, "endpoint-cards", ids, s.reorderEndpoint, func(i int) {
		endpoint := doc.Endpoints[i]
		h := heroItem{
			kind: kindEndpoint, id: endpoint.ID, tag: endpoint.Tag, typ: endpoint.Type,
			title:   endpoint.Tag,
			enabled: endpoint.Enabled,
			invalid: model.HasInvalidEndpointReferences(doc, endpoint, library),
			main:    schema.EndpointAddress(endpoint.Type, endpoint.Fields),
			mono:    true,
		}
		if h.title == "" {
			h.title = typeLabel(endpoint.Type)
		}
		switch endpoint.Type {
		case "tailscale":
			// One endpoint, named by its type. The configuration only: the live
			// state is the Overview card's.
			facts := tailscaleFacts(endpoint.Fields)
			switch {
			case facts.hostname != "":
				h.main = facts.hostname
			case facts.control != "":
				h.main = facts.control
			case s.systemHostname() != "":
				h.main = s.systemHostname()
			default:
				h.main, h.mono = "系统主机名", false
			}
			h.title, h.note = "Tailscale", facts.login
			if facts.udpDefault != "" {
				h.badges = append(h.badges, "UDP "+facts.udpDefault)
			} else if facts.udpPath != "" {
				h.badges = append(h.badges, "UDP 路径")
			}
			if facts.systemInterface {
				h.badges = append(h.badges, "系统接口")
			}
		case "wireguard":
			if peers, _ := jsv.Array(endpoint.Fields["peers"]); len(peers) > 0 {
				h.note = fmt.Sprintf("%d 个对端", len(peers))
			}
		}
		if line, ok := s.runtimeLine(endpoint); ok {
			h.state, h.stateLabel = &line, h.title+" 运行状态"
		}
		s.heroCard(c, h)
	}, func() ui.Element {
		return s.add.Card(kindEndpoint, kit.AddCard(c.Key("add-endpoint"), "添加端点"))
	})
}

// serviceList is the USB/IP services' hero cards, then "+ Add USB/IP",
// opening the add menu at it.
func (s *Section) serviceList(c *ui.Context, doc *model.Doc) {
	library := s.store.NetworkConfigs()
	platform := s.platform(doc)
	subHeading(c, "USB/IP", fmt.Sprint(len(doc.Services)), func() {
		kit.Button(c, "连接向导", kit.ButtonOptions{Small: true, Icon: kit.SymWandSparkles}).OnClick(s.wizard.open)
	})
	ui.Box(c).Height(8).Shrink(0)
	ctx := s.rt.guideContext()
	ids := make([]string, len(doc.Services))
	for i, service := range doc.Services {
		ids[i] = service.ID
	}
	heroGrid(c, "service-cards", ids, nil, func(i int) {
		service := doc.Services[i]
		unavailable := !schema.ServiceAvailable(service.Type, platform)
		h := heroItem{
			kind: kindService, id: service.ID, tag: service.Tag, typ: service.Type,
			title:   service.Tag,
			enabled: service.Enabled,
			main:    serviceAddress(service),
			mono:    true,
		}
		if h.title == "" {
			h.title = typeLabel(service.Type)
		}
		level := guideLevel("")
		if unavailable {
			h.main, h.mono = schema.ServiceUnavailableReason(service.Type, platform), false
		} else {
			level = worstLevel(analyzeService(doc, service, ctx))
			h.note = pathInfo(detectPath(doc, service)).Short
		}
		if !unavailable && service.Type == "usbip-server" && service.Fields["provider"] == "dynamic" {
			line := stateLine{"设备与共享", toneIdle}
			if s.rt.share != nil && s.rt.share.Running && s.rt.share.ServiceTag == service.Tag {
				line = stateLine{"共享中", toneOK}
			}
			h.state, h.stateLabel = &line, service.Tag+" 设备与共享"
		}
		switch {
		case unavailable:
			h.badge, h.badgeTone = "当前平台不可用", kit.DangerTone
		case model.HasInvalidServiceReferences(doc, service, library):
			h.invalid = true
		case level == levelError:
			h.badge, h.badgeTone = "需检查", kit.DangerTone
		case level != "":
			h.badge, h.badgeTone = "需检查", kit.WarningTone
		}
		s.heroCard(c, h)
	}, func() ui.Element {
		return s.add.Card(kindService, kit.AddCard(c.Key("add-service"), "添加 USB/IP"))
	})
}

// heroItem is what an endpoint's or a service's hero card shows.
type heroItem struct {
	kind, id, tag, typ, title string
	enabled                   bool
	// invalid marks a card needing a fix ("Needs fix"); badge, in
	// badgeTone, marks one otherwise, as "Needs check".
	invalid   bool
	badge     string
	badgeTone kit.Tone
	// main is the main value, an address when mono; note the bottom
	// line's text, badges the tags after it.
	main   string
	mono   bool
	note   string
	badges []string
	// state is the runtime state, nil for items without one; stateLabel
	// names its button at the bottom right, opening the control sheet.
	state      *stateLine
	stateLabel string
}

// heroCard is an item's compact hero card, as the inbounds' and
// outbounds': its state dot, name and type over its address and a
// bottom line of its note, tags and, for items with one, the runtime
// state, a button opening the control sheet. The card opens the editor;
// its menu switches, restores the defaults or deletes after a
// confirmation at the more button.
func (s *Section) heroCard(c *ui.Context, h heroItem) {
	_, glow := kit.TypeLook(c, h.kind, h.typ)
	kind, id := h.kind, h.id
	more := kit.HeroCard(c, id, kit.HeroCardOptions{
		Label: "编辑 " + h.title,
		Glow:  glow,
		Muted: !h.enabled,
		Header: func() {
			kit.HeroDot(c, h.enabled)
			kit.HeroName(c, h.title)
			switch {
			case h.invalid:
				kit.Badge(c, "需修正", kit.DangerTone)
			case h.badge != "":
				kit.Badge(c, h.badge, h.badgeTone)
			}
			ui.Spacer(c)
			kit.HeroTag(c, strings.ToUpper(h.typ))
		},
		Main: func() {
			switch {
			case h.main == "":
				kit.HeroValue(c, "—")
			case h.mono:
				kit.HeroMono(c, h.main)
			default:
				kit.HeroValue(c, h.main).FontSize(18)
			}
		},
		Footer: func() {
			if h.note != "" {
				ui.Text(c, h.note).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
			}
			ui.Spacer(c)
			for _, badge := range h.badges {
				kit.Badge(c, badge, kit.Neutral)
			}
			if h.state != nil && stateButton(c, *h.state, h.stateLabel) {
				s.control = control{kind, id}
			}
		},
		OnOpen:          func() { s.editor.edit(kind, id) },
		Enabled:         h.enabled,
		OnEnabledChange: func(enabled bool) { s.setEnabled(kind, id, enabled) },
		Menu:            s.itemMenu(kind, id, h.tag),
		KeepMore:        s.confirm.onCard(kind, id),
	})
	if s.confirm.onCard(kind, id) {
		s.confirmAt = more
	}
}

// itemMenu is a card's Edit, "Restore defaults" and Delete.
func (s *Section) itemMenu(kind, id, tag string) []kit.EntityMenuItem {
	return []kit.EntityMenuItem{
		{Label: "编辑", Icon: kit.SymPencil, Run: func(ui.Element) { s.editor.edit(kind, id) }},
		{Label: "恢复默认", Icon: kit.SymRotateCcw, Run: func(ui.Element) { s.askConfirm("reset", kind, id, tag, false) }},
		{Label: "删除", Icon: kit.SymTrash2, Danger: true, Run: func(ui.Element) { s.askConfirm("delete", kind, id, tag, false) }},
	}
}

func (s *Section) askConfirm(action, kind, id, tag string, inEditor bool) {
	s.confirm = confirmation{action: action, kind: kind, id: id, tag: tag, inEditor: inEditor}
	s.confirm.state.Open = true
}

// onCard tells that the confirmation is open at the more button of
// item id's card, which keeps that button shown and anchors it.
func (cf *confirmation) onCard(kind, id string) bool {
	return !cf.inEditor && cf.state.Open && cf.kind == kind && cf.id == id
}

func (s *Section) confirmPopover(c *ui.Context, anchor ui.Element) {
	cf := &s.confirm
	o := kit.ConfirmOptions{Title: "恢复默认值？", Message: "「" + cf.tag + "」当前填写的参数将恢复默认值。", Confirm: "恢复", Danger: true}
	if cf.action == "delete" {
		o.Title, o.Message, o.Confirm = "删除？", "「"+cf.tag+"」将被删除。", "删除"
	}
	o.Run = func(done func(error)) { done(s.runConfirm()) }
	kit.ConfirmPopover(c, anchor, &cf.state, o)
	if !cf.state.Open {
		*cf = confirmation{}
	}
}

// runConfirm resets or deletes the item. A reset inside the editor
// changes the draft only, saved with it.
func (s *Section) runConfirm() error {
	cf := s.confirm
	if cf.inEditor && cf.action == "reset" && s.editor.draftID() == cf.id {
		s.editor.resetDraft()
		return nil
	}
	err := s.store.Update(func(doc *model.Doc) {
		switch {
		case cf.action == "delete" && cf.kind == kindService:
			doc.Services = slices.DeleteFunc(doc.Services, func(x *model.Service) bool { return x.ID == cf.id })
		case cf.action == "delete":
			model.RemoveEndpoint(doc, cf.id)
		case cf.kind == kindService:
			for _, x := range doc.Services {
				if x.ID == cf.id {
					x.Fields = model.MakeService(x.Type, x.Tag).Fields
				}
			}
		default:
			for _, x := range doc.Endpoints {
				if x.ID == cf.id {
					x.Fields = model.MakeEndpoint(x.Type, x.Tag, doc.IPv6Enabled).Fields
				}
			}
		}
	})
	if err != nil {
		return err
	}
	if cf.action == "delete" && s.editor.draftID() == cf.id {
		s.editor.close()
	}
	return nil
}

func (s *Section) setEnabled(kind, id string, enabled bool) {
	err := s.store.Update(func(doc *model.Doc) {
		if kind == kindService {
			for _, x := range doc.Services {
				if x.ID == id {
					x.Enabled = enabled
				}
			}
			return
		}
		for _, x := range doc.Endpoints {
			if x.ID == id {
				x.Enabled = enabled
			}
		}
	})
	s.env.ToastError(err)
}

func (s *Section) reorderEndpoint(id, target string) {
	// A drag onto itself, or of a card that is gone, changes nothing to
	// save.
	endpoints := s.store.Doc().Endpoints
	has := func(id string) bool {
		return slices.ContainsFunc(endpoints, func(e *model.Endpoint) bool { return e.ID == id })
	}
	if id == target || !has(id) || !has(target) {
		return
	}
	err := s.store.Update(func(doc *model.Doc) {
		from := slices.IndexFunc(doc.Endpoints, func(e *model.Endpoint) bool { return e.ID == id })
		to := slices.IndexFunc(doc.Endpoints, func(e *model.Endpoint) bool { return e.ID == target })
		if from < 0 || to < 0 {
			return
		}
		item := doc.Endpoints[from]
		doc.Endpoints = slices.Delete(doc.Endpoints, from, from+1)
		doc.Endpoints = slices.Insert(doc.Endpoints, to, item)
	})
	s.env.ToastError(err)
}

// controlSheets shows the runtime sheet of the item s.control names.
func (s *Section) controlSheets(c *ui.Context, doc *model.Doc) {
	if s.control.id == "" {
		s.tunnel.forget()
		return
	}
	if s.control.kind == kindService {
		i := slices.IndexFunc(doc.Services, func(x *model.Service) bool { return x.ID == s.control.id })
		if i < 0 {
			s.control = control{}
			return
		}
		s.usbip.view(c, doc.Services[i].Tag)
		return
	}
	i := slices.IndexFunc(doc.Endpoints, func(x *model.Endpoint) bool { return x.ID == s.control.id })
	if i < 0 {
		s.control = control{}
		return
	}
	endpoint := doc.Endpoints[i]
	switch endpoint.Type {
	case "openconnect":
		s.tunnel.view(c, endpoint.Tag, "openconnect")
	case "openvpn-client":
		s.tunnel.view(c, endpoint.Tag, "openvpn")
	default:
		s.control = control{}
	}
}

// openURL opens a page in the browser.
func (s *Section) openURL(url string) {
	s.env.Run(func(context.Context) error { return s.env.Shell.OpenURL(url) }, s.env.ToastError)
}

// copy puts text on the clipboard and says so.
func (s *Section) copy(c *ui.Context, text, done string) {
	c.WriteClipboard(text)
	s.env.Toast(done)
}
