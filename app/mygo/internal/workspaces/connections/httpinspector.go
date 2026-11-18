package connections

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// httpInspectorState is what the HTTP inspector keeps for the selected
// record; the monitor calls init when made, reset when another record is
// selected and stop when the page hides.
type httpInspectorState struct {
	// gen changes with the record shown: answers of earlier reads are
	// dropped.
	gen int
	// id is the record the state belongs to.
	id     string
	bodies [2]httpBody
	open   map[string]*bool
	// copied names the last copy action, for its label.
	copied string
	// skipMsg and skipErr are the result of "Skip decryption for this host".
	skipMsg string
	skipErr bool
}

func (s *httpInspectorState) init() { s.open = map[string]*bool{} }

func (s *httpInspectorState) reset() {
	s.gen++
	s.bodies = [2]httpBody{}
	s.open = map[string]*bool{}
	s.copied, s.skipMsg, s.skipErr = "", "", false
}

func (s *httpInspectorState) stop() { s.reset() }

// resetBody forgets one side's data, keeping how it is shown.
func (s *httpInspectorState) resetBody(side int) {
	b := &s.bodies[side]
	*b = httpBody{token: b.token + 1, raw: b.raw, view: b.view}
}

// disclosure is the open state of a fold of the inspector.
func (s *httpInspectorState) disclosure(key string, open bool) *bool {
	if state, ok := s.open[key]; ok {
		return state
	}
	state := &open
	s.open[key] = state
	return state
}

// inspectorView is the right-hand detail of the selected record.
func (m *httpMonitor) inspectorView(c *ui.Context, r *httpRow) {
	// The row shown can lag the selection by a frame, so the state follows
	// the record it is built for.
	if m.insp.id != r.ID {
		m.insp.reset()
		m.insp.id = r.ID
	}
	subtitle := fmt.Sprintf("%s · %s · %s", r.Method, httpStatusText(r), httpDuration(r.total))
	panel := kit.ScrollingInspector(c.Key(r.ID), firstNonEmpty(r.Host, r.URL), subtitle, nil, nil, m.closeInspector, func() {
		m.inspectorActions(c, r)
		if r.Error != "" {
			kit.Notice(c, kit.DangerTone, "", r.Error, nil)
		}
		m.overview(c, r)
		m.rewritesSection(c, r)
		m.timing(c, r)
		m.headers(c, r)
		m.bodySection(c, r, sideRequest)
		m.bodySection(c, r, sideResponse)
	})
	if panel.Shortcut(0, ui.KeyEscape) {
		m.closeInspector()
	}
}

// copyLabel is the label of a copy button, "Copied" right after its click.
func (s *httpInspectorState) copyLabel(key, label string) string {
	if s.copied == key {
		return "已复制"
	}
	return label
}

// inspectorActions are the buttons under the title.
func (m *httpMonitor) inspectorActions(c *ui.Context, r *httpRow) {
	row := func(fn func()) { ui.Row(c).Gap(8).Shrink(0).Children(fn) }
	row(func() {
		kit.Button(c, m.insp.copyLabel("url", "复制 URL"), kit.ButtonOptions{Small: true, Icon: kit.SymCopy}).OnClick(func() {
			c.WriteClipboard(r.URL)
			m.insp.copied = "url"
		})
		kit.Button(c, m.insp.copyLabel("curl", "复制为 cURL"), kit.ButtonOptions{Small: true, Icon: kit.SymSquareTerminal}).OnClick(func() {
			c.WriteClipboard(httpCurlBody(&r.HTTPRecord, m.insp.curlBody(), !m.insp.bodies[sideRequest].raw))
			m.insp.copied = "curl"
		})
	})
	excluded := hostExcluded(m.docOf(), r.Host)
	skip := "跳过解密此主机"
	if excluded {
		skip = "已跳过解密"
	}
	row(func() {
		kit.Button(c, "查看连接", kit.ButtonOptions{Small: true, Icon: kit.SymCable, Disabled: r.ConnectionID == ""}).
			OnClick(func() { m.env.Navigate(env.PageConnections, env.ConnectionsView{ConnectionID: r.ConnectionID}) })
		kit.Button(c, skip, kit.ButtonOptions{Small: true, Icon: kit.SymShieldAlert, Disabled: excluded || m.doc == nil || r.Host == ""}).
			OnClick(func() { m.skipDecryption(r.Host) })
	})
	m.rewriteButtons(c, r)
	if m.canReplay() {
		row(func() {
			replay := kit.Button(c, "重放", kit.ButtonOptions{Small: true, Icon: kit.SymRotateCw})
			if m.rp.focusNow && m.rp.fromInspector {
				replay.Focus()
				m.rp.focusNow = false
			}
			replay.OnClick(func() { m.openReplay(r, true) })
		})
	}
	row(func() {
		remove := kit.Button(c, "删除", kit.ButtonOptions{Small: true, Icon: kit.SymTrash2, Disabled: r.live()})
		remove.OnClick(func() { m.removeRow.Open = true })
		kit.ConfirmPopover(c, remove, &m.removeRow, kit.ConfirmOptions{
			Title:   "删除此请求？",
			Confirm: "删除请求", Danger: true,
			Run: func(done func(error)) { m.deleteRecord(r.ID, done) },
		})
	})
	if m.insp.skipMsg != "" {
		tone := kit.SuccessTone
		if m.insp.skipErr {
			tone = kit.DangerTone
		}
		kit.Notice(c, tone, "", m.insp.skipMsg, nil)
	}
}

// curlBody is the request body to put in a copied command: the whole body
// when it was read.
func (s *httpInspectorState) curlBody() []byte {
	b := &s.bodies[sideRequest]
	if b.fetched && !b.more() {
		return b.data
	}
	return nil
}

// docOf is the document, nil without one.
func (m *httpMonitor) docOf() *model.Doc {
	if m.doc == nil {
		return nil
	}
	return m.doc.Doc()
}

// skipDecryption adds host to the exclude list of the HTTPS decryption
// settings and says how it went.
func (m *httpMonitor) skipDecryption(host string) {
	if m.doc == nil {
		return
	}
	name, err := excludeDecryption(m.doc, host)
	if err != nil {
		m.insp.skipMsg, m.insp.skipErr = err.Error(), true
		return
	}
	m.insp.skipMsg, m.insp.skipErr = "已加入排除："+name, false
}

func (m *httpMonitor) overview(c *ui.Context, r *httpRow) {
	kit.Section(c, "概览", nil, func() {
		property(c, "URL", r.URL)
		property(c, "方法", r.Method)
		kit.PropertyWith(c, "状态", func() {
			kit.Status(c, httpStatusText(r), httpClassTone(r.class))
			if len(r.Interim) > 0 {
				parts := make([]string, len(r.Interim))
				for i, code := range r.Interim {
					parts[i] = fmt.Sprint(code)
				}
				ui.Text(c, "临时 "+strings.Join(parts, " ")).FontSize(kit.FontTable).TextColor(kit.T(c).Label3)
			}
		})
		property(c, "协议", r.Proto)
		property(c, "内容类型", r.ctype)
		property(c, "请求大小", kit.Bytes(float64(r.RequestBytes)))
		property(c, "响应大小", httpSize(r))
		if flags := httpFlags(r); len(flags) > 0 {
			kit.PropertyWith(c, "标记", func() {
				ui.Row(c).Gap(4).Children(func() {
					for _, f := range flags {
						kit.Badge(c, f.label, f.tone)
					}
				})
			})
		}
		if conn := r.Connection; conn != nil {
			property(c, "连接", conn.ID)
			kit.NamedProperty(c, "源地址", firstNonEmpty(conn.Source, "—"), sourceName(m.names.get(), conn.Source))
			property(c, "目标地址", conn.Destination)
			property(c, "入站", join(" · ", conn.Inbound, conn.InboundType))
			property(c, "出站", join(" · ", conn.Outbound, conn.OutboundType))
			property(c, "规则", conn.Rule)
			property(c, "进程", join(" · ", conn.ProcessName, pidText(conn.ProcessID)))
		}
		property(c, "记录", fmt.Sprintf("#%s · 第 %d 个请求", r.ID, r.RequestIndex+1))
	})
}

func pidText(pid uint32) string {
	if pid == 0 {
		return ""
	}
	return fmt.Sprintf("PID %d", pid)
}

// join joins the non-empty parts.
func join(sep string, parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// headers shows the headers and trailers of both sides, each side folded.
func (m *httpMonitor) headers(c *ui.Context, r *httpRow) {
	doc := m.docOf()
	side := func(key, title, which string, headers, trailers []api.HTTPHeader) {
		marks := headerMarks(doc, r, which)
		count := len(headers) + len(trailers)
		if count == 0 {
			return
		}
		kit.Disclosure(c, fmt.Sprintf("%s · %d", title, count), m.insp.disclosure(key, false), func() {
			headerRows(c, headers, marks)
			if len(trailers) > 0 {
				ui.Text(c, "Trailers").FontSize(kit.FontCaption).FontWeight(700).TextColor(kit.T(c).Label3).Margin(6, 0, 2)
				headerRows(c, trailers, nil)
			}
		})
	}
	side("req-headers", "请求头", api.HTTPSideRequest, r.RequestHeaders, r.RequestTrailers)
	side("resp-headers", "响应头", api.HTTPSideResponse, r.ResponseHeaders, r.ResponseTrailers)
}

func headerRows(c *ui.Context, headers []api.HTTPHeader, marks map[string]changeMark) {
	k := kit.T(c)
	for _, h := range headers {
		mark, changed := marks[strings.ToLower(h.Name)]
		row := ui.Row(c).Gap(10).AlignItems(ui.Start).PaddingY(2).Children(func() {
			name := ui.Text(c, h.Name).FontSize(kit.FontTable).FontWeight(600).TextColor(k.Label2).Width(104).Shrink(0).Wrap().Selectable()
			value := ui.Text(c, h.Value).FontSize(kit.FontTable).Font(kit.MonoFont).Grow(1).MinWidth(0).Wrap().Selectable()
			if changed {
				name.TextColor(k.AccentText)
				value.TextColor(k.AccentText)
			}
		})
		if changed {
			kit.Hint(c, row, mark.tip())
		}
	}
}

// bodySection shows one side's body: what the record says, then the
// bytes read from the core.
func (m *httpMonitor) bodySection(c *ui.Context, r *httpRow, side int) {
	title := "请求正文"
	if side == sideResponse {
		title = "响应正文"
	}
	info := httpSideOf(r, side)
	b := &m.insp.bodies[side]
	k := kit.T(c)
	note := func(text string) { ui.Text(c, text).FontSize(kit.FontTable).TextColor(k.Label3).Wrap() }
	size := ""
	if info.bytes > 0 {
		size = kit.Bytes(float64(info.bytes))
	}
	kit.Section(c, title, func() {
		if size != "" {
			ui.Text(c, size).FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum")
		}
	}, func() {
		switch {
		case info.bytes == 0:
			note("无正文")
			return
		case r.BodyDropped:
			note("正文已丢弃")
			return
		case !info.saved:
			note("未保存正文")
			return
		}
		m.syncBody(r, side)
		if info.encoding != "" {
			sel := 0
			if b.raw {
				sel = 1
			}
			seg := kit.Segmented(c, "body-encoding"+title, &sel, kit.Segment{Label: "解码 " + info.encoding}, kit.Segment{Label: "原始"})
			seg.Label(title + "编码")
			if (sel == 1) != b.raw {
				m.insp.resetBody(side)
				b = &m.insp.bodies[side]
				b.raw = sel == 1
				m.syncBody(r, side)
			}
		}
		if info.truncated && info.stored > 0 {
			kit.Notice(c, kit.WarningTone, "", fmt.Sprintf("正文已截断，保存 %s / %s", kit.Bytes(float64(info.stored)), kit.Bytes(float64(info.bytes))), nil)
		}
		if b.err != "" {
			kit.Notice(c, kit.DangerTone, "", b.err, func() {
				kit.Button(c, "重试", kit.ButtonOptions{Small: true}).OnClick(func() { m.retryBody(r, side) })
			})
		}
		if b.info.DecodeError != "" {
			kit.Notice(c, kit.WarningTone, "", "解码失败："+b.info.DecodeError, nil)
		}
		if !b.fetched {
			if b.loading {
				note("读取中…")
			}
			return
		}
		m.bodyContent(c, r, side, b)
		if b.more() {
			label := fmt.Sprintf("加载更多 · %s / %s", kit.Bytes(float64(len(b.data))), kit.Bytes(float64(b.info.Total)))
			if b.loading {
				label = "读取中…"
			}
			full := int64(len(b.data)) >= httpBodyText
			kit.Button(c, label, kit.ButtonOptions{Small: true, Disabled: b.loading || full}).OnClick(func() { m.loadMore(r, side) })
			if full {
				note("已达显示上限")
			}
		}
	})
}

// bodyContent shows the data read as the kind it was recognised as.
func (m *httpMonitor) bodyContent(c *ui.Context, r *httpRow, side int, b *httpBody) {
	k := kit.T(c)
	switch b.kind {
	case bodyImage:
		if b.bitmap == nil && !b.treeFailed {
			bitmap, err := ui.DecodeBitmap(b.data)
			if err != nil {
				b.treeFailed = true
			} else {
				b.bitmap = bitmap
			}
		}
		if b.bitmap == nil {
			textBox(c, hexDump(b.data, httpHexLimit))
			return
		}
		w, h := b.bitmap.Size()
		scale := min(1, 300/float32(max(w, 1)), 260/float32(max(h, 1)))
		ui.Image(c, b.bitmap).Size(float32(w)*scale, float32(h)*scale).Radius(6).Shrink(0)
		ui.Text(c, fmt.Sprintf("%d × %d", w, h)).FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum")
	case bodyJSON:
		if b.tree == nil && !b.treeFailed && !b.more() {
			tree, err := parseJSONTree(b.data)
			if err != nil {
				b.treeFailed = true
			} else {
				b.tree = tree
			}
		}
		view := b.view
		if b.tree != nil {
			kit.Segmented(c, fmt.Sprint("body-json", side), &b.view, kit.Segment{Label: "树"}, kit.Segment{Label: "文本"}).Label("JSON 视图")
			view = b.view
		} else {
			view = 1
		}
		if view == 0 {
			m.jsonTree(c, b.tree, side)
		} else {
			textBox(c, b.text())
		}
		m.copyBody(c, b, side)
	case bodyText:
		textBox(c, b.text())
		m.copyBody(c, b, side)
	default:
		textBox(c, hexDump(b.data, httpHexLimit))
		if len(b.data) > httpHexLimit {
			ui.Text(c, fmt.Sprintf("仅显示前 %s", kit.Bytes(httpHexLimit))).FontSize(kit.FontCaption).TextColor(k.Label3)
		}
	}
}

func (m *httpMonitor) copyBody(c *ui.Context, b *httpBody, side int) {
	label := "复制正文"
	if b.copied {
		label = "已复制"
	}
	kit.Button(c, label, kit.ButtonOptions{Small: true, Variant: kit.TextButton, Icon: kit.SymCopy}).OnClick(func() {
		c.WriteClipboard(b.text())
		b.copied = true
	})
}

// jsonTree draws the parsed document as a tree.
func (m *httpMonitor) jsonTree(c *ui.Context, root *jsonNode, side int) {
	ui.Scroll(c).MaxHeight(httpBoxHeight).Shrink(0).Children(func() {
		ui.Tree(c, func() { m.jsonItem(c, root, fmt.Sprint(side, "$"), 0) })
	})
}

// httpBoxHeight is the tallest a body box grows before it scrolls.
const httpBoxHeight = 320

// textBox shows raw text in the monospaced font, selectable, scrolling
// within a box.
func textBox(c *ui.Context, text string) {
	k := kit.T(c)
	ui.Scroll(c).MaxHeight(httpBoxHeight).Shrink(0).Radius(8).Background(k.Surface2).Children(func() {
		ui.Text(c, text).Font(kit.MonoFont).FontSize(11).Selectable().Wrap().TextColor(k.Label2).Padding(8, 10)
	})
}

func (m *httpMonitor) jsonItem(c *ui.Context, n *jsonNode, path string, depth int) {
	if n.kind == 0 {
		ui.TreeItem(c.Key(path), n.label(), nil, nil)
		return
	}
	open := m.insp.disclosure(path, depth < 1)
	ui.TreeItem(c.Key(path), n.label(), open, func() {
		for i, kid := range n.kids {
			if i >= httpTreeKids {
				ui.TreeItem(c.Key(path+"…"), fmt.Sprintf("… 还有 %d 项", len(n.kids)-i+n.hidden), nil, nil)
				return
			}
			m.jsonItem(c, kid, fmt.Sprint(path, "/", i), depth+1)
		}
		if n.hidden > 0 {
			ui.TreeItem(c.Key(path+"…"), fmt.Sprintf("… 还有 %d 项", n.hidden), nil, nil)
		}
	})
}
