package connections

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// harScope says which records an export covers.
const (
	harSelected = iota
	harFiltered
)

// harExportLimit is the most records one export takes, as the core's.
const harExportLimit = 5000

// harSizeLimit is the most bytes one export holds in memory.
const harSizeLimit = 256 << 20

// errHARTooLarge ends an export that outgrew harSizeLimit.
var errHARTooLarge = errors.New("导出过大，关闭包含正文后重试")

// harSaver asks where to save the finished document; ok is false when the
// user cancels. The desktop's save dialog by default, replaced in tests.
type harSaver func(name string, data []byte, filter env.FileFilter) (ok bool, err error)

// harOptions are the choices of the export popover.
type harOptions struct {
	scope  int
	bodies bool
	auth   bool
	cookie bool
	query  bool
}

func defaultHARoptions() harOptions {
	return harOptions{scope: harFiltered, bodies: true, auth: true, cookie: true}
}

// harFileName is the name suggested for an export made at t.
func harFileName(t time.Time) string { return "kiko-" + timefmt.Format(t, "20060102-150405") + ".har" }

// harRequest builds the export request: the IDs of the records covered, in
// the list's order, at most harExportLimit, and the choices as the core
// takes them. Excluding bodies also sanitizes them, so that a core that
// reads either finds them out.
func harRequest(o harOptions, selected string, rows []*httpRow) api.HARRequest {
	var ids []string
	if o.scope == harSelected && selected != "" {
		ids = []string{selected}
	} else {
		for _, r := range rows {
			if len(ids) >= harExportLimit {
				break
			}
			ids = append(ids, r.ID)
		}
	}
	return api.HARRequest{
		IDs: ids, Bodies: o.bodies,
		Sanitize: api.HARSanitize{AuthHeaders: o.auth, Cookies: o.cookie, QuerySecrets: o.query, Bodies: !o.bodies},
	}
}

// countingBuffer collects the document and counts the bytes for the
// progress shown.
type countingBuffer struct {
	buf     bytes.Buffer
	written *atomic.Int64
	// limit is the most bytes held; zero means harSizeLimit. abort ends the
	// transfer when it is exceeded.
	limit int64
	abort context.CancelFunc
	// exceeded: a write was refused for the limit.
	exceeded bool
}

// Write is all the buffer offers, so that io.Copy cannot bypass the count.
func (b *countingBuffer) Write(p []byte) (int, error) {
	limit := b.limit
	if limit == 0 {
		limit = harSizeLimit
	}
	if int64(b.buf.Len())+int64(len(p)) > limit {
		b.exceeded = true
		if b.abort != nil {
			b.abort()
		}
		return 0, errHARTooLarge
	}
	n, err := b.buf.Write(p)
	b.written.Add(int64(n))
	return n, err
}

// over reports whether the document outgrew the limit.
func (b *countingBuffer) over() bool { return b.exceeded }

// harState is the export popover.
type harState struct {
	open bool
	opts harOptions
	busy bool
	err  string
	// written counts the bytes received so far.
	written atomic.Int64
	cancel  context.CancelFunc
	// stopped: the user cancelled the export under way.
	stopped bool
	// gen tells an export from the one before it.
	gen int
	// saver replaces the save dialog when set.
	saver harSaver
}

func (m *httpMonitor) save(name string, data []byte, filter env.FileFilter) (bool, error) {
	if m.har.saver != nil {
		return m.har.saver(name, data, filter)
	}
	return m.env.Shell.SaveFile(name, data, filter)
}

// startExport fetches the HAR document, then asks where to save it. A
// cancelled save leaves the popover as it was; a failure shows in it.
func (m *httpMonitor) startExport(rows []*httpRow) {
	h := &m.har
	if h.busy {
		return
	}
	req := harRequest(h.opts, m.selected, rows)
	if len(req.IDs) == 0 {
		h.err = "没有可导出的记录"
		return
	}
	h.busy, h.err, h.stopped = true, "", false
	h.written.Store(0)
	h.gen++
	gen := h.gen
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	env.Go(m.env, func(parent context.Context) (bool, error) {
		defer cancel()
		stop := context.AfterFunc(parent, cancel)
		defer stop()
		buf := &countingBuffer{written: &h.written, abort: cancel}
		if err := m.env.API.ExportHAR(ctx, req, buf); err != nil {
			if buf.over() {
				return false, errHARTooLarge
			}
			return false, err
		}
		return m.save(harFileName(time.Now()), buf.buf.Bytes(), env.FileFilter{Name: "HAR", Extensions: []string{"har"}})
	}, func(saved bool, err error) {
		if h.gen != gen {
			return
		}
		h.busy, h.cancel = false, nil
		switch {
		case err != nil && h.stopped:
			// Cancelled by the user.
		case err != nil:
			h.err = err.Error()
		case saved:
			h.open = false
		}
	})
}

// cancelExport stops the export under way.
func (m *httpMonitor) cancelExport() {
	if m.har.cancel != nil {
		m.har.stopped = true
		m.har.cancel()
	}
}

// exportPopover is the export options beside the toolbar's button.
func (m *httpMonitor) exportPopover(c *ui.Context, anchor ui.Element, d *httpDerived) {
	h := &m.har
	k := kit.T(c)
	if !h.open {
		h.err = ""
	}
	ui.PopoverBase(c, anchor, &h.open, func(panel ui.Element) {
		kit.Popup(c, panel).Width(300).Padding(14).Label("导出 HAR")
		kit.AttachPopover(c, panel, anchor, ui.AnchorBottomStart, ui.AnchorTopStart, 6, 0)
		kit.PopoverBody(c, 10, func() {
			ui.Text(c, "导出 HAR").FontSize(kit.FontBody).FontWeight(600)
			hasSelection := m.selected != ""
			if !hasSelection {
				h.opts.scope = harFiltered
			}
			if hasSelection {
				scope := h.opts.scope
				seg := kit.Segmented(c, "har-scope", &scope, kit.Segment{Label: "所选记录", Count: "1"}, kit.Segment{Label: "当前筛选", Count: kit.Count(min(len(d.rows), harExportLimit))})
				seg.Label("导出范围")
				h.opts.scope = scope
			} else {
				ui.Text(c, fmt.Sprintf("当前筛选 · %s 条", kit.Count(min(len(d.rows), harExportLimit)))).FontSize(kit.FontTable).TextColor(k.Label2).FontFeatures("tnum")
			}
			ui.Column(c).Gap(8).Children(func() {
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, "包含正文").FontSize(kit.FontTable).Grow(1)
					kit.Switch(c, &h.opts.bodies, "包含正文开关")
				})
				ui.Text(c, "脱敏").FontSize(kit.FontCaption).FontWeight(700).TextColor(k.Label3).Margin(2, 0, 0)
				kit.Checkbox(c, &h.opts.auth, "认证头")
				kit.Checkbox(c, &h.opts.cookie, "Cookie")
				kit.Checkbox(c, &h.opts.query, "查询参数密钥")
			})
			if h.busy {
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, "导出中 · "+kit.Bytes(float64(h.written.Load()))).FontSize(kit.FontTable).TextColor(k.Label2).FontFeatures("tnum").Grow(1)
				})
				c.After(200 * time.Millisecond)
			}
			if h.err != "" {
				ui.Text(c, h.err).FontSize(kit.FontTable).TextColor(k.Danger).Selectable().Wrap()
			}
			ui.Row(c).Gap(8).Justify(ui.End).Margin(4, 0, 0).Children(func() {
				if h.busy {
					kit.Button(c, "取消导出", kit.ButtonOptions{Small: true}).OnClick(m.cancelExport)
					return
				}
				kit.Button(c, "关闭", kit.ButtonOptions{Small: true}).OnClick(func() { h.open = false })
				label := "导出"
				if h.err != "" {
					label = "重试"
				}
				kit.Button(c, label, kit.ButtonOptions{Small: true, Variant: kit.Primary}).AutoFocus().OnClick(func() { m.startExport(d.rows) })
			})
		})
	})
}
