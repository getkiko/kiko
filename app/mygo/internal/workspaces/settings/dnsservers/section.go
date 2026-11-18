// Package dnsservers is Settings › DNS of the settings workspace: the
// global DNS settings as settings rows (Settings), and the DNS servers as
// rows of a settings group (View) — every public preset listed
// (model.DNSPresets, added disabled),
// the server editor with its eleven resolver types for a custom server,
// server details, and deletion.
package dnsservers

import (
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/store"
)

// Section is the DNS section. Its state lasts while the console runs.
type Section struct {
	env   *env.Env
	store *store.Store

	filter      filter
	filtersOpen bool
	statusOpen  bool
	protoOpen   bool

	// details is the server whose details dialog is open.
	details     string
	detailsOpen bool

	settingsState settingsState
	editor        editor

	// commitError is the last failed toggle (usePolicyCommit's error).
	commitError string

	// focus is the card that takes the focus next frame: its body, or
	// its more button with focusMore; focusAdd focuses the add button.
	focus     string
	focusMore bool
	focusAdd  bool
}

// New returns the section.
func New(e *env.Env, s *store.Store) *Section {
	sec := &Section{env: e, store: s, statusOpen: true, protoOpen: true}
	sec.filter.status = "all"
	sec.editor.section = sec
	return sec
}

// focusServer moves the focus to a server's card, or to the add button
// when the card is gone (focusServer of DnsPanel).
func (s *Section) focusServer(id string, more bool) {
	s.focus, s.focusMore, s.focusAdd = id, more, id == ""
}

func (s *Section) copyAddress(c *ui.Context, server *model.DNSServer) {
	c.WriteClipboard(serverEndpoint(server))
	s.env.Toast("服务器地址已复制")
}

// toggle enables or disables a server and saves.
func (s *Section) toggle(id string, enabled bool) {
	s.commitError = ""
	err := s.store.Update(func(doc *model.Doc) {
		for _, server := range doc.DNS.Servers {
			if server.ID == id {
				server.Enabled = enabled
			}
		}
	})
	if err != nil {
		s.commitError = err.Error()
	}
}

// View builds the servers' group: a heading with the count, the search
// and the add button, then a row per server (its switch and a menu of
// details and copying), and the dialogs.
func (s *Section) View(c *ui.Context) {
	k := kit.T(c)
	doc := s.store.Doc()
	library := s.store.NetworkConfigs()
	servers := doc.DNS.Servers
	visible := s.filter.visible(servers)
	if s.detailsOpen && !slices.ContainsFunc(visible, func(v *model.DNSServer) bool { return v.ID == s.details }) {
		s.detailsOpen = false
	}
	narrowed := s.filter.search != "" || s.filter.filtered()
	count := countText(servers, len(visible), narrowed)

	ui.Column(c.Key("dns-section")).FillWidth().Shrink(0).Label("DNS 服务器列表").Children(func() {
		// The title and the count keep their width; the search narrows.
		ui.Row(c).MinHeight(kit.SegmentHeight).Gap(8).Margin(0, 14, 9).Shrink(0).Children(func() {
			ui.Text(c, "服务器").FontSize(kit.FontBody).FontWeight(600).SingleLine().Shrink(0)
			ui.Text(c, count).FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum").SingleLine().Shrink(0)
			ui.Spacer(c)
			if len(servers) > 0 {
				kit.Search(c, "dns-search", &s.filter.search, kit.SearchOptions{Placeholder: "搜索名称、地址与协议", Width: 220}).
					Label("搜索 DNS 服务器")
			}
			add := kit.Button(c, "添加服务器", kit.ButtonOptions{Small: true, Icon: kit.SymPlus})
			if s.focusAdd {
				add.Focus()
				s.focusAdd = false
			}
			if add.Clicked() {
				s.editor.openNew(doc)
			}
		})
		if s.commitError != "" {
			kit.Notice(c, kit.DangerTone, "", s.commitError, nil).Margin(0, 0, 12)
		}
		kit.SettingsGroup(c, "", "", func() {
			for _, server := range visible {
				ui.Column(c.Key(server.ID)).Children(func() { s.row(c, doc, library, server) })
			}
			if len(visible) > 0 {
				return
			}
			ui.Row(c).MinHeight(kit.SettingsRowH).Gap(12).Children(func() {
				text := "没有服务器"
				if len(servers) > 0 {
					text = "没有匹配的服务器"
				}
				ui.Text(c, text).FontSize(kit.FontBody).TextColor(k.Label3).Grow(1)
				if len(servers) > 0 {
					kit.Button(c, "清除搜索", kit.ButtonOptions{Small: true}).OnClick(func() {
						s.clearFilters()
						s.filter.search = ""
					})
				}
			})
		})
		s.focus, s.focusMore = "", false
	})
	s.detailsDialog(c, doc)
	s.editor.view(c, doc, library)
}

func (s *Section) clearFilters() {
	s.filter.protocols = nil
	s.filter.status = "all"
}

// row is a server: its name with the "Default" and "Needs fix" badges,
// the protocol and the
// address, opening the editor; then its switch and its more menu.
func (s *Section) row(c *ui.Context, doc *model.Doc, library []*model.NetworkConfig, server *model.DNSServer) {
	k := kit.T(c)
	invalid := model.HasInvalidDNSServerReferences(doc, server, library)
	id := server.ID
	row := ui.Row(c).Gap(10)
	menuOpen := ui.Local(row, "menu", func() bool { return false })
	row.Children(func() {
		body := ui.ButtonBase(c).Grow(1).MinWidth(0).MinHeight(kit.SettingsRowH).PaddingY(9).Gap(12).
			Justify(ui.Start).Label("编辑 " + server.Tag).Transition(kit.HoverMotion)
		if body.Hovered() {
			body.Opacity(0.85)
		}
		if s.focus == id && !s.focusMore {
			body.Focus()
		}
		body.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
				ui.Row(c).Gap(6).MinWidth(0).Children(func() {
					name := ui.Text(c, server.Tag).FontSize(kit.FontBody).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
					if !server.Enabled {
						name.TextColor(k.Label2)
					}
					if doc.DNS.Final.ID() == id {
						kit.Badge(c, "默认", kit.Neutral)
					}
					if invalid {
						kit.Badge(c, "需修正", kit.DangerTone)
					}
				})
				ui.Row(c).Gap(6).MinWidth(0).Children(func() {
					kit.TypeChip(c, protocolLabel(server.Type))
					ui.Text(c, serverEndpoint(server)).FontSize(kit.FontCaption).TextColor(k.Label3).
						SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
				})
			})
		})
		if body.Clicked() {
			s.editor.openExisting(doc, id)
		}
		on := server.Enabled
		if kit.Switch(c, &on, "启用 "+server.Tag).Changed() {
			s.toggle(id, on)
		}
		more := kit.IconButton(c, kit.SymEllipsis, server.Tag+" 操作", kit.IconButtonOptions{Size: kit.SmallControl})
		if s.focus == id && s.focusMore {
			more.Focus()
		}
		if more.Clicked() {
			*menuOpen = !*menuOpen
		}
		kit.ActionMenu(c, more, menuOpen, []kit.ActionGroup{{Label: server.Tag, Items: []kit.ActionItem{
			{ID: "details", Label: "查看详情", Icon: kit.SymInfo, Run: func() { s.details, s.detailsOpen = id, true }},
			{ID: "copy", Label: "复制地址", Icon: kit.SymCopy, Run: func() { s.copyAddress(c, server) }},
		}}}, kit.ActionMenuOptions{Label: server.Tag + " 操作"})
	})
}
