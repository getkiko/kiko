package routes

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/procicon"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// The Current Connections list of the topology's rule preview: the open connections
// the rule shown carries, from the server's connection history (GET
// /connections, by the rule's hit key), polled while the preview shows.
// A reject rule's list is the connections it refused last instead (Rejected),
// as its connections end at once.

const (
	// ruleConnLimit bounds the connections the list shows.
	ruleConnLimit = 50
	// ruleConnPoll is how often the list refreshes.
	ruleConnPoll = time.Second
	// ruleConnLinger is how long the poll goes on after the list last
	// showed: the preview closed, the topology left.
	ruleConnLinger = 2 * time.Second
	// ruleConnRowHeight is a row's height: the target and the App over
	// each other, the rates beside them.
	ruleConnRowHeight = 40
	// finalRuleLabel is how connections name a default outbound rule, as
	// the server's rule map labels it (ruleRef.label).
	finalRuleLabel = "默认出口"
)

// ruleConnSorts are the list's orders, largest first: the server's
// download rate and total columns, its connections' larger direction.
var ruleConnSorts = []kit.Option{{Value: "downloadRate", Label: "速率"}, {Value: "downloadTotal", Label: "总量"}}

// ruleLabel is how connections name the route table rule numbered
// ordinal, as the server's rule map labels it (ruleRef.label): the Connections
// filter pane's value of the rule.
func ruleLabel(ordinal int, name string) string { return fmt.Sprintf("%d · %s", ordinal, name) }

// ruleConnTarget is what the list shows: the connections of a rule hit
// key (ruleHitKey: a rule's ID, "final:<owner>" for a default outbound).
type ruleConnTarget struct {
	key string
	// rejected lists the refused connections, latest first, for a reject
	// rule.
	rejected bool
	// owner keeps the connections from that scope's sources, for a rule
	// several scopes share (Pre-rules, a module's, route.final) shown under
	// one of them; "" keeps all.
	owner string
}

// ruleConns is the state of the list.
type ruleConns struct {
	target ruleConnTarget
	// stop ends the poll; nil while none runs.
	stop context.CancelFunc
	// busy is that a request is under way; gen counts the targets, so
	// that an answer for an earlier one is dropped.
	busy bool
	gen  int
	// page is the last answer for target, nil until one arrived; err why
	// the last request failed.
	page *api.ConnectionPage
	err  string
	// sources are the source addresses of the target's connections (of
	// owner's, when set), sorted: the source filter of the list and of
	// "View in Connections".
	sources []string
	// shownAt is the last frame that showed the list.
	shownAt time.Time
	// sort is the list's order, a column of the server's connection
	// table (ruleConnSorts); "" for the first.
	sort string
	// freeze holds the page still while a row's menu shows.
	freeze kit.MenuFreeze[*api.ConnectionPage]
	icons  *procicon.Store
}

// ruleConnTargetOf is the list's target of node in the topology of
// w.owner; ok is false for a node no connection names: Sniff and Resolve,
// after which matching goes on, and Pre-rules' "Other connections".
func (w *Workspace) ruleConnTargetOf(node *topoRule) (ruleConnTarget, bool) {
	if node.hitKey == "" || node.exit == "" && !node.final {
		return ruleConnTarget{}, false
	}
	t := ruleConnTarget{key: node.hitKey, rejected: node.action == model.ActionReject}
	// A device's own rules and default outbound carry its connections
	// alone; the shared ones every scope's.
	if w.owner != model.PreDeviceID && (node.pre || node.hitKey == "final:route") {
		t.owner = w.owner
	}
	return t, true
}

// followRuleConns shows t in the list, polling the server for it while
// the list shows. UI thread.
func (w *Workspace) followRuleConns(t ruleConnTarget) {
	r := &w.conns
	r.shownAt = time.Now()
	if r.icons == nil {
		r.icons = procicon.For(w.env)
	}
	if r.target != t {
		r.target, r.page, r.err, r.sources, r.busy = t, nil, "", nil, false
		r.gen++
		w.fetchRuleConns()
	}
	if r.stop != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.stop = cancel
	go func() {
		tick := time.NewTicker(ruleConnPoll)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				w.env.Post(func() {
					if ctx.Err() == nil {
						w.tickRuleConns()
					}
				})
			}
		}
	}()
}

// tickRuleConns refreshes the list, or stops the poll once the list no
// longer shows. UI thread.
func (w *Workspace) tickRuleConns() {
	if time.Since(w.conns.shownAt) > ruleConnLinger {
		w.stopRuleConns()
		return
	}
	if w.env.Server.Runtime.Running() {
		w.fetchRuleConns()
	}
}

// stopRuleConns ends the poll and forgets the list. UI thread.
func (w *Workspace) stopRuleConns() {
	r := &w.conns
	if r.stop != nil {
		r.stop()
		r.stop = nil
	}
	r.target, r.page, r.err, r.sources, r.busy = ruleConnTarget{}, nil, "", nil, false
	r.gen++
}

// fetchRuleConns asks for the target's connections, fastest first, unless
// a request is under way. A target of one owner asks by the owner's
// sources: the source facet of each answer, which no source filter
// narrows, lists the target's sources for the next; the first answer
// only finds them. UI thread.
func (w *Workspace) fetchRuleConns() {
	r := &w.conns
	if r.busy || r.target.key == "" {
		return
	}
	t, gen, sources := r.target, r.gen, r.sources
	sort := r.sort
	if sort == "" {
		sort = ruleConnSorts[0].Value
	}
	state := env.ConnectionsActive
	if t.rejected {
		state, sort = env.ConnectionsRejected, "created"
	}
	q := api.ConnectionQuery{State: state, Rule: t.key, Sort: sort, Order: "desc", Limit: ruleConnLimit}
	probe := t.owner != "" && len(sources) == 0
	switch {
	case probe:
		q.Limit = 1
	case t.owner != "":
		q.Filter = map[string][]string{"source": slices.Clone(sources)}
	}
	r.busy = true
	env.Go(w.env, func(ctx context.Context) (api.ConnectionPage, error) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return w.env.API.QueryConnections(ctx, q)
	}, func(page api.ConnectionPage, err error) {
		if gen != r.gen {
			return
		}
		r.busy = false
		if err != nil {
			r.err = err.Error()
			return
		}
		r.err = ""
		owned := w.ownedSources(page.Facets["source"], t.owner)
		changed := !slices.Equal(owned, sources)
		r.sources = owned
		switch {
		case t.owner != "" && len(owned) == 0:
			// None of the owner's connections takes the rule.
			r.page = &api.ConnectionPage{}
		case !probe:
			r.page = &page
		}
		if t.owner != "" && changed && len(owned) > 0 {
			w.fetchRuleConns()
		}
	})
}

// ownedSources are the values of a source facet with connections, those
// from owner's addresses when owner is set, sorted.
func (w *Workspace) ownedSources(options []api.ConnectionFacet, owner string) []string {
	var out []string
	var doc *model.Doc
	if owner != "" {
		doc = w.doc()
	}
	for _, o := range options {
		if o.Count <= 0 || o.Value == "" || o.Value == "—" {
			continue
		}
		if owner != "" && !sourceOfScope(doc, o.Value, w.store.LocalAddresses, owner) {
			continue
		}
		out = append(out, o.Value)
	}
	slices.Sort(out)
	return out
}

// sourceOfScope reports whether connections from ip reach owner's rules:
// its scope as the configuration matches sources, or another profile of
// the same device.
func sourceOfScope(doc *model.Doc, ip string, local []string, owner string) bool {
	scope := model.DeviceForSource(doc, ip, local)
	if scope == owner {
		return true
	}
	d, _, ok := model.ScopeDevice(doc, owner)
	if !ok {
		return false
	}
	other, _, ok := model.ScopeDevice(doc, scope)
	return ok && other.ID == d.ID
}

// ruleConnsView is "View in Connections"'s view: the rule's connections from the
// sources of the topology's scope, on the list's tab (Active, or Rejected for
// a reject rule). Without connections a device's own single addresses
// stand in; Pre-rules shows every source.
func (w *Workspace) ruleConnsView(rule string) env.ConnectionsView {
	filter := map[string][]string{"rule": {rule}}
	state := env.ConnectionsActive
	if w.conns.target.rejected {
		state = env.ConnectionsRejected
	}
	if w.owner == model.PreDeviceID {
		return env.ConnectionsView{Filter: filter, State: state}
	}
	sources := slices.Clone(w.conns.sources)
	if len(sources) == 0 {
		doc := w.doc()
		if model.IsLocalScope(w.owner) {
			sources = localAddresses(doc, w.store.LocalAddresses)
		} else if d, _, ok := model.ScopeDevice(doc, w.owner); ok {
			sources = singleAddresses(d.Addresses)
		}
	}
	if len(sources) > 0 {
		filter["source"] = sources
	}
	return env.ConnectionsView{Filter: filter, State: state}
}

// localAddresses are the host's single addresses as connections show
// sources: the detected interfaces' and TUN's, the supplementary single
// IPs, and 127.0.0.1 for the loopback range.
func localAddresses(doc *model.Doc, detected []string) []string {
	hosts := make([]string, 0, len(detected))
	for _, a := range detected {
		hosts = append(hosts, strings.Split(strings.TrimSpace(a), "/")[0])
	}
	return singleAddresses(append(model.LocalSourceAddresses(doc, hosts), "127.0.0.1"))
}

// singleAddresses are the single IPs among networks, as connections show
// sources.
func singleAddresses(networks []string) []string {
	var out []string
	for _, n := range networks {
		n = strings.TrimSpace(n)
		if a, err := netip.ParseAddr(n); err == nil {
			out = append(out, a.String())
			continue
		}
		if p, err := netip.ParsePrefix(n); err == nil && p.IsSingleIP() {
			out = append(out, p.Addr().String())
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// ruleConnsSection is the preview's Current Connections: the target's open
// connections, fastest first, or a reject rule's refused ones, latest
// first, with the way to them in Connections (by the rule's label, rule).
func (w *Workspace) ruleConnsSection(c *ui.Context, t ruleConnTarget, rule string) {
	w.followRuleConns(t)
	r := &w.conns
	r.freeze.Begin()
	page := r.freeze.Hold(func() *api.ConnectionPage { return r.page })
	running := w.env.Server.Runtime.Running()
	title := "当前连接"
	if t.rejected {
		title = "已拒绝"
	}
	if running && page != nil && page.Total > 0 {
		title += " " + kit.Count(page.Total)
	}
	kit.Section(c, title, func() {
		if r.sort == "" {
			r.sort = ruleConnSorts[0].Value
		}
		if !t.rejected && kit.Select(c, "rule-conn-sort", &r.sort, ruleConnSorts, kit.SelectOptions{Inline: true, Label: "排序"}).Changed() {
			// The new order at once, the answer under way dropped.
			r.gen++
			r.busy = false
			w.fetchRuleConns()
		}
		kit.Button(c, "在连接中查看", kit.ButtonOptions{Small: true, Variant: kit.TextButton, Icon: kit.SymArrowRight}).
			OnClick(func() { w.env.Navigate(env.PageConnections, w.ruleConnsView(rule)) })
	}, func() {
		empty := func(title string) {
			ui.Column(c).Height(132).Shrink(0).Children(func() { kit.EmptyState(c, kit.SymCable, title, "", nil) })
		}
		switch {
		case !running:
			empty("核心未运行")
		case page == nil && r.err != "":
			empty("无法读取连接")
		case page == nil:
		case len(page.Rows) == 0:
			empty("暂无连接")
		default:
			processes := make(map[int]api.ConnectionProcess, len(page.Processes))
			for _, p := range page.Processes {
				processes[p.PID] = p
			}
			now := timefmt.Now()
			ui.Column(c).Shrink(0).Children(func() {
				for i := range page.Rows {
					w.ruleConnRow(c, &page.Rows[i], processes, t.rejected, now)
				}
			})
		}
	})
	r.freeze.End(c)
}

// ruleConnRow is a connection of the list: its target over its App or
// process, its rates over its totals, or for a refused one (rejected) its
// time over its source; its menu copies the target or shows the
// connection in Connections on the tab of its state.
func (w *Workspace) ruleConnRow(c *ui.Context, row *api.ConnectionRow, processes map[int]api.ConnectionProcess, rejected bool, now time.Time) {
	k := kit.T(c)
	conn := &row.Connection
	target := connTarget(conn)
	p, name := connProcess(conn, processes)
	e := ui.Row(c.Key("conn-" + conn.ID)).Height(ruleConnRowHeight).PaddingX(6).Gap(8).Radius(6).Shrink(0).
		FontSize(kit.FontTable).TextColor(k.Label).Transition(kit.HoverMotion)
	if e.Hovered() {
		e.Background(k.RowHover)
	}
	id := conn.ID
	menu := w.conns.freeze.Menu(func(m *ui.Menu, _ int) {
		if m.Item("复制目标").Chosen() {
			c.WriteClipboard(target)
		}
		if m.Item("在连接中查看此连接").Chosen() {
			w.env.Navigate(env.PageConnections, env.ConnectionsView{ConnectionID: id, State: w.ruleConnState(row)})
		}
	})
	e.PopoverMenu(func(m *ui.Menu) { menu(m, 0) })
	e.Children(func() {
		procicon.View(c, w.conns.icons, p, procicon.Options{Size: 18})
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			kit.Clipped(c, target, kit.FontTable, 500)
			if name == "" && !rejected {
				name = sourceHost(conn.Source)
			}
			kit.Clipped(c, firstNonEmptyText(name, "—"), kit.FontCaption, 400).TextColor(k.Label3)
		})
		if rejected {
			ui.Column(c).Shrink(0).Gap(2).AlignItems(ui.End).FontSize(kit.FontCaption).Children(func() {
				kit.NumberCell(c, rejectedTime(int64(conn.CreatedAt), now))
				ui.Text(c, sourceHost(conn.Source)).SingleLine().TextColor(k.Label3)
			})
			return
		}
		ui.Column(c).Shrink(0).Gap(2).AlignItems(ui.End).FontSize(kit.FontCaption).Children(func() {
			pair := func(up, down string, tone ui.Color) {
				ui.Row(c).Gap(6).TextColor(tone).Children(func() {
					for _, v := range []struct {
						icon kit.Symbol
						text string
					}{{kit.SymArrowUp, up}, {kit.SymArrowDown, down}} {
						ui.Row(c).Gap(2).Children(func() {
							kit.Icon(c, v.icon, 10).TextColor(k.Label3)
							kit.NumberCell(c, v.text)
						})
					}
				})
			}
			pair(kit.Rate(row.UpRate), kit.Rate(row.DownRate), k.Label)
			pair(kit.Bytes(float64(row.UplinkTotal)), kit.Bytes(float64(row.DownlinkTotal)), k.Label3)
		})
	})
}

// ruleConnState is the Connections tab of row's connection as it stands, by the
// list's latest answer: "" (All) for an open connection that answer no
// longer has, as it may have closed since the row showed.
func (w *Workspace) ruleConnState(row *api.ConnectionRow) string {
	if p := w.conns.page; p != nil {
		found := false
		for i := range p.Rows {
			if p.Rows[i].ID == row.ID {
				row, found = &p.Rows[i], true
				break
			}
		}
		if !found && row.RejectCount == 0 {
			return ""
		}
	}
	switch {
	case row.RejectCount > 0:
		return env.ConnectionsRejected
	case row.ClosedAt > 0:
		return env.ConnectionsClosed
	}
	return env.ConnectionsActive
}

// rejectedTime is when a connection was refused, in the time zone set:
// the time of day for today's, with the date before for earlier ones.
func rejectedTime(at int64, now time.Time) string {
	if at <= 0 {
		return "—"
	}
	if at < 100_000_000_000 {
		// In seconds, as connection times may be.
		at *= 1000
	}
	t := timefmt.Milli(at)
	if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return t.Format("15:04:05")
	}
	return t.Format("01月02日 15:04:05")
}

// connTarget is a connection's domain, or its destination's address.
func connTarget(conn *api.Connection) string {
	if d := strings.TrimSpace(conn.Domain); d != "" {
		return d
	}
	if host, _, err := net.SplitHostPort(conn.Destination); err == nil && host != "" {
		return host
	}
	return firstNonEmptyText(conn.Destination, "—")
}

// connProcess is the icon of a connection's process, from the page's
// processes when they know it, and the name the list shows: the App's,
// else the process file's; "" without a process.
func connProcess(conn *api.Connection, processes map[int]api.ConnectionProcess) (procicon.Process, string) {
	info := conn.ProcessInfo
	if info == nil {
		return procicon.Process{}, ""
	}
	name := path.Base(strings.ReplaceAll(info.ProcessPath, "\\", "/"))
	if info.ProcessPath == "" {
		name = ""
		if len(info.PackageNames) > 0 {
			name = info.PackageNames[0]
		}
	}
	p := procicon.Process{PID: info.ProcessID, Path: info.ProcessPath, Name: name}
	if known, ok := processes[info.ProcessID]; ok && info.ProcessID > 0 && (info.ProcessPath == "" || known.Path == info.ProcessPath) {
		p = procicon.Process{PID: known.PID, StartedAt: known.StartedAt, Path: known.Path, Name: known.Name, AppPath: known.AppPath}
		if known.AppName != "" {
			name = known.AppName
		} else if name == "" {
			name = known.Name
		}
	}
	return p, name
}

// sourceHost is a source address without its port, a bracketed IPv6
// address unbracketed, as connections' source filter values are.
func sourceHost(source string) string {
	value := strings.TrimSpace(source)
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	if strings.HasPrefix(value, "[") && strings.Contains(value, "]") {
		return value[1:strings.Index(value, "]")]
	}
	return value
}
