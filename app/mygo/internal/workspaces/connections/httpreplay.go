package connections

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/forms"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// replayBodyLimit is the largest request body the dialog prefills.
const replayBodyLimit = httpBodyText

// replayMethods are the methods the dialog offers; the record's own is
// added when it is another.
var replayMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

// replayDropped are the headers a replay does not take over: those that
// belong to the connection of the first exchange, and the ones the sender
// sets itself (Host from the URL, Content-Length from the body).
var replayDropped = []string{"host", "content-length", "connection", "keep-alive", "proxy-connection", "proxy-authorization",
	"proxy-authenticate", "te", "trailer", "transfer-encoding", "upgrade"}

// replayHeaders are the headers of a request to put in the replay form:
// without HTTP/2 pseudo headers, hop-by-hop headers (also those the
// Connection header names) and, when the original body was encoded, its
// Content-Encoding, since the body is prefilled decoded.
func replayHeaders(headers []api.HTTPHeader, encoded bool) []api.HTTPHeader {
	named := map[string]bool{}
	for _, h := range headers {
		if strings.EqualFold(strings.TrimSpace(h.Name), "connection") {
			for _, token := range strings.Split(h.Value, ",") {
				named[lower(trim(token))] = true
			}
		}
	}
	var out []api.HTTPHeader
	for _, h := range headers {
		name := lower(trim(h.Name))
		switch {
		case name == "", strings.HasPrefix(name, ":"), slices.Contains(replayDropped, name), named[name], encoded && name == "content-encoding":
			continue
		}
		out = append(out, api.HTTPHeader{Name: trim(h.Name), Value: h.Value})
	}
	return out
}

// How the dialog's body field stands.
const (
	// replayBodyEditable: the field holds the text to send, empty when
	// the original had no body.
	replayBodyEditable = iota
	// replayBodyLoading: the original body is being read.
	replayBodyLoading
	// replayBodyBlocked: the original body cannot be taken over (note says
	// why); the replay sends none.
	replayBodyBlocked
)

// replayBodyPlan says whether the original request body is to be read for
// the replay form, and else why it cannot be taken over (note is empty when
// there is none to take).
func replayBodyPlan(r *httpRow) (read bool, note string) {
	switch {
	case r.RequestBytes == 0:
		return false, ""
	case r.BodyDropped:
		return false, "原请求正文已丢弃，将以空正文发送"
	case !r.RequestBodySaved:
		return false, "原请求正文未保存，将以空正文发送"
	case r.RequestBodyTruncated:
		return false, "原请求正文已截断，将以空正文发送"
	case r.RequestBytes > replayBodyLimit:
		return false, "原请求正文超过 1.0 MB，将以空正文发送"
	}
	return true, ""
}

// replayBodyText is the text of a body read for the form, or why it is not
// usable.
func replayBodyText(body api.HTTPBody, err error) (text, note string) {
	switch {
	case err != nil:
		return "", "读取原请求正文失败：" + err.Error() + "，将以空正文发送"
	case body.Truncated || !body.Complete || len(body.Data) > replayBodyLimit:
		return "", "原请求正文已截断，将以空正文发送"
	case body.DecodeError != "":
		return "", "原请求正文解码失败，将以空正文发送"
	case !utf8.Valid(body.Data) || len(body.Data) > 0 && !looksText(body.Data):
		return "", "原请求正文不是文本，将以空正文发送"
	}
	return string(body.Data), ""
}

// replayHeader is one editable header of the form.
type replayHeader struct {
	key         int
	name, value string
}

// replayForm is what the replay dialog edits.
type replayForm struct {
	sourceID string
	method   string
	url      string
	headers  []*replayHeader
	nextKey  int
	body     string
	bodyMode int
	bodyNote string
	// outbound is a tag, "" following the routing rules.
	outbound string
	verify   bool
}

// newReplayForm fills the form from a record: its method, URL and headers.
func newReplayForm(r *httpRow) *replayForm {
	f := &replayForm{sourceID: r.ID, method: firstNonEmpty(r.Method, "GET"), url: r.URL, verify: true}
	for _, h := range replayHeaders(r.RequestHeaders, r.RequestEncoding != "") {
		f.addHeader(h.Name, h.Value)
	}
	read, note := replayBodyPlan(r)
	switch {
	case read:
		f.bodyMode = replayBodyLoading
	case note != "":
		f.bodyMode, f.bodyNote = replayBodyBlocked, note
	}
	return f
}

func (f *replayForm) addHeader(name, value string) {
	f.nextKey++
	f.headers = append(f.headers, &replayHeader{key: f.nextKey, name: name, value: value})
}

func (f *replayForm) removeHeader(key int) {
	f.headers = slices.DeleteFunc(f.headers, func(h *replayHeader) bool { return h.key == key })
}

// headerToken reports whether s is a valid header field name.
func headerToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r > 0x7e || r <= 0x20 || strings.ContainsRune("()<>@,;:\\\"/[]?={}", r) {
			return false
		}
	}
	return true
}

// request is the replay the form describes, or what is wrong with it.
func (f *replayForm) request() (api.ReplayRequest, error) {
	req := api.ReplayRequest{SourceID: f.sourceID, Outbound: f.outbound, VerifyTLS: f.verify}
	req.Method = strings.ToUpper(trim(f.method))
	if req.Method == "" || !headerToken(req.Method) {
		return req, fmt.Errorf("方法无效")
	}
	raw := trim(f.url)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
		return req, fmt.Errorf("URL 需为 http:// 或 https:// 开头的完整地址")
	}
	if u.User != nil {
		return req, fmt.Errorf("URL 不能包含账号和密码")
	}
	req.URL = raw
	for _, h := range f.headers {
		name := trim(h.name)
		if name == "" && h.value == "" {
			continue
		}
		if !headerToken(name) {
			return req, fmt.Errorf("请求头名称无效：%q", name)
		}
		if strings.ContainsAny(h.value, "\r\n") {
			return req, fmt.Errorf("请求头 %s 的值不能包含换行", name)
		}
		req.Headers = append(req.Headers, api.HTTPHeader{Name: name, Value: h.value})
	}
	if f.bodyMode == replayBodyEditable && f.body != "" {
		req.Body = []byte(f.body)
	}
	return req, nil
}

// replayUpsert puts the record a replay returned into the store so that the
// list shows it at once, ahead of the stream; a paused list shows it too.
func (m *httpMonitor) replayUpsert(rec api.HTTPRecord) {
	if rec.ID == "" {
		return
	}
	m.store.upsert(rec)
	m.store.trim(httpLimit, m.keepRow)
	if row := m.store.byID[rec.ID]; row != nil && m.paused && !slices.ContainsFunc(m.snap, func(r *httpRow) bool { return r.ID == rec.ID }) {
		m.snap = append(m.snap, row)
	}
	m.version++
}

// replayState is the replay dialog.
type replayState struct {
	open bool
	// shown remembers the dialog was open in the last frame, to see it close.
	shown bool
	busy  bool
	err   string
	form  *replayForm
	// gen tells the dialog now open from the one an answer belongs to.
	gen int
	// fromInspector: the dialog was opened by the inspector's button, else
	// by the list; the focus returns there.
	fromInspector bool
	// focusBack and focusNow return the focus to the opener one frame after
	// the dialog closed.
	focusBack, focusNow bool
}

// openReplay opens the replay dialog for a record.
func (m *httpMonitor) openReplay(r *httpRow, fromInspector bool) {
	rp := &m.rp
	rp.gen++
	rp.form, rp.err, rp.open, rp.fromInspector = newReplayForm(r), "", true, fromInspector
	if rp.form.bodyMode != replayBodyLoading {
		return
	}
	gen, id := rp.gen, r.ID
	m.fetchBody(id, api.HTTPSideRequest, 0, replayBodyLimit+1, r.RequestEncoding != "", func(body api.HTTPBody, err error) {
		if rp.gen != gen || rp.form == nil {
			return
		}
		text, note := replayBodyText(body, err)
		rp.form.body = text
		if note != "" {
			rp.form.bodyMode, rp.form.bodyNote = replayBodyBlocked, note
		} else {
			rp.form.bodyMode = replayBodyEditable
		}
	})
}

// canReplay reports whether the core replays requests.
func (m *httpMonitor) canReplay() bool { return m.cap != nil && m.cap.Replay }

// sendReplay sends the form's request, once at a time. The record that
// comes back enters the list; success closes the dialog and selects it,
// failure stays in the dialog.
func (m *httpMonitor) sendReplay() {
	rp := &m.rp
	if rp.busy || rp.form == nil || rp.form.bodyMode == replayBodyLoading {
		return
	}
	req, err := rp.form.request()
	if err != nil {
		rp.err = err.Error()
		return
	}
	rp.busy, rp.err = true, ""
	gen := rp.gen
	// The record comes back with a failed upstream too, beside the error.
	env.Go(m.env, func(ctx context.Context) (api.HTTPRecord, error) { return m.env.API.ReplayHTTP(ctx, req) }, func(rec api.HTTPRecord, err error) {
		rp.busy = false
		m.replayUpsert(rec)
		if rp.gen != gen || !rp.open {
			return
		}
		if err != nil {
			rp.err = err.Error()
			return
		}
		rp.open = false
		if m.store.byID[rec.ID] != nil {
			m.selectRow(rec.ID)
		}
	})
}

// replayDialog draws the dialog; it handles the focus' return after it
// closed.
func (m *httpMonitor) replayDialog(c *ui.Context) {
	rp := &m.rp
	if rp.shown && !rp.open {
		rp.focusBack = true
	}
	rp.shown = rp.open
	if !rp.open || rp.form == nil {
		return
	}
	f := rp.form
	k := kit.T(c)
	kit.Dialog(c, &rp.open, "重放请求", 620, func() {
		kit.FormRow(c, "方法", "", func() {
			options := make([]kit.Option, 0, len(replayMethods)+1)
			methods := replayMethods
			if f.method != "" && !slices.Contains(methods, f.method) {
				methods = append([]string{f.method}, methods...)
			}
			for _, name := range methods {
				options = append(options, kit.Option{Value: name, Label: name})
			}
			kit.Select(c, "replay-method", &f.method, options, kit.SelectOptions{Width: 130, Label: "方法"})
		})
		kit.FormRow(c, "URL", "", func() {
			kit.Input(c, "replay-url", &f.url, "https://").AutoFocus()
		})
		kit.FormRow(c, "请求头", "", func() { m.replayHeaderRows(c, f) })
		kit.FormRow(c, "请求正文", "", func() {
			switch f.bodyMode {
			case replayBodyLoading:
				ui.Text(c, "读取中…").FontSize(kit.FontTable).TextColor(k.Label3)
			case replayBodyBlocked:
				ui.Text(c, f.bodyNote).FontSize(kit.FontTable).TextColor(k.Label3).Wrap()
			default:
				kit.TextArea(c, "replay-body", &f.body, "", true).MinHeight(110).Label("请求正文")
			}
		})
		kit.FormRow(c, "出站", "", func() {
			doc := m.docOf()
			var tags map[string]string
			if doc != nil {
				tags = emit.EmittedTags(doc).Outbounds
			}
			forms.OutboundSelect(c, "replay-outbound", doc, &f.outbound, forms.OutboundSelectOptions{Width: 240, Label: "出站",
				Lead: []kit.Option{{Value: "", Label: "按路由规则"}}, Tags: tags})
		})
		kit.FormRow(c, "校验证书", "", func() { kit.Switch(c, &f.verify, "校验证书开关") })
		if rp.err != "" {
			kit.Notice(c, kit.DangerTone, "", rp.err, nil)
		}
	}, func() {
		kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(func() { rp.open = false })
		label := "发送"
		switch {
		case rp.busy:
			label = "发送中…"
		case rp.err != "":
			label = "重试"
		}
		kit.Button(c, label, kit.ButtonOptions{Variant: kit.Primary, Disabled: rp.busy || f.bodyMode == replayBodyLoading}).OnClick(m.sendReplay)
	})
}

// replayHeaderRows is the editable header table: a name and a value per
// row, a remove button for each and one to add a row.
func (m *httpMonitor) replayHeaderRows(c *ui.Context, f *replayForm) {
	remove := 0
	body := func() {
		for _, h := range f.headers {
			h := h
			ui.Row(c.Key(h.key)).Gap(6).Shrink(0).Children(func() {
				ui.Row(c).Width(170).Shrink(0).Children(func() { kit.Input(c, fmt.Sprint("n", h.key), &h.name, "名称") })
				ui.Row(c).Grow(1).MinWidth(0).Children(func() { kit.Input(c, fmt.Sprint("v", h.key), &h.value, "值") })
				if kit.IconButton(c, kit.SymX, "移除请求头 "+h.name, kit.IconButtonOptions{Size: 26, IconSize: 14}).Clicked() {
					remove = h.key
				}
			})
		}
	}
	ui.Column(c).Gap(6).Children(func() {
		if len(f.headers) > 0 {
			ui.Scroll(c).MaxHeight(210).Children(func() { ui.Column(c).Gap(6).Children(body) })
		}
		ui.Row(c).Children(func() {
			kit.Button(c, "添加请求头", kit.ButtonOptions{Small: true, Icon: kit.SymPlus}).OnClick(func() { f.addHeader("", "") })
		})
	})
	if remove != 0 {
		f.removeHeader(remove)
	}
}
