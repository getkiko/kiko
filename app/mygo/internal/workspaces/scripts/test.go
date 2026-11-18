package scripts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// TestSeed names a captured exchange the test area starts from: the
// record's ID and the side (api.HTTPSideRequest or api.HTTPSideResponse)
// the script is for.
type TestSeed struct {
	ID   string
	Side string
}

// Test input sources.
const (
	sourceCaptured = iota
	sourceManual
)

const (
	// recordLimit is how many captured requests the choice lists.
	recordLimit = 100
	// defaultBodyRead and maxBodyRead bound the bytes of a body the test
	// area fetches from a record.
	defaultBodyRead = 2 << 20
	maxBodyRead     = 8 << 20
)

// methods are the choices of a manual request's method.
var methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

// isRunType reports whether scripts of the type run without a message:
// generic and scheduled ones.
func isRunType(typ string) bool { return typ == model.ScriptGeneric || typ == model.ScriptCron }

// isDecisionType reports whether scripts of the type decide a rule condition
// or a DNS answer from a connection description.
func isDecisionType(typ string) bool { return typ == model.ScriptRule || typ == model.ScriptDNS }

// message is the exchange a test runs on.
type message struct {
	url, method string
	reqHeaders  []api.ScriptHeader
	reqBody     []byte
	status      int32
	respHeaders []api.ScriptHeader
	respBody    []byte
	hasResponse bool
	notes       []string
}

// outcome is a finished run with the input it ran on.
type outcome struct {
	res api.ScriptEvalResult
	in  message
	typ string
}

// tester is the state of the test area of one editor.
type tester struct {
	env    *env.Env
	source int

	// records are the captured requests the choice lists; query is the
	// search the list was asked for.
	records  []api.HTTPRecord
	listed   bool
	loading  bool
	recsErr  string
	query    string
	loadedQ  string
	reopen   bool
	recsGen  int
	recID    string
	msg      *message
	fetching bool
	fetchErr string
	fetchGen int

	// manual input.
	url, method, reqHeaders, status, respHeaders string
	reqBody, respBody                            kit.CodeState

	// rule is the $request of a rule or dns script.
	rule ruleInput

	running bool
	gen     int
	cancel  context.CancelFunc
	result  *outcome
	err     string

	reply   kit.CodeState
	disc    map[string]*bool
	diffKey int
	// reveal scrolls a new result into view.
	reveal bool
}

func newTester(e *env.Env) *tester {
	return &tester{env: e, method: "GET", status: "200", disc: map[string]*bool{}, rule: ruleInput{network: "tcp", port: "443", queryType: "A"}}
}

// ruleInput is the hand-written $request of a rule or dns script.
type ruleInput struct {
	domain, ip, port, network, protocol, inbound, sourceIP, process, queryType string
}

// apiInput converts the fields to the evaluate request; a port that is not a
// number between 0 and 65535 is an error. The query type only goes along
// with dns scripts.
func (r ruleInput) apiInput(typ string) (*api.ScriptRuleInput, error) {
	in := &api.ScriptRuleInput{
		Domain: strings.TrimSpace(r.domain), IP: strings.TrimSpace(r.ip), Network: r.network,
		Protocol: strings.TrimSpace(r.protocol), Inbound: strings.TrimSpace(r.inbound),
		SourceIP: strings.TrimSpace(r.sourceIP), ProcessName: strings.TrimSpace(r.process),
	}
	if p := strings.TrimSpace(r.port); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 65535 {
			return nil, fmt.Errorf("端口须在 0–65535 之间")
		}
		in.Port = uint32(n)
	}
	if typ == model.ScriptDNS {
		in.QueryType = strings.TrimSpace(r.queryType)
	}
	if in.Domain == "" && in.IP == "" {
		return nil, fmt.Errorf("请填写域名或 IP")
	}
	return in, nil
}

// stop abandons what is running; its answer is dropped.
func (t *tester) stop() {
	t.gen++
	t.recsGen++
	t.fetchGen++
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	t.running, t.loading, t.fetching = false, false, false
}

func (t *tester) open(key string) *bool {
	if v, ok := t.disc[key]; ok {
		return v
	}
	v := new(bool)
	*v = true
	t.disc[key] = v
	return v
}

// seed starts the area from a captured record.
func (t *tester) seed(s *TestSeed, max int) {
	if s == nil || s.ID == "" {
		return
	}
	t.source = sourceCaptured
	t.recID = s.ID
	t.fetch(s.ID, max)
}

func unavailableText(err error) string {
	if api.IsScriptUnavailable(err) {
		return "核心未运行"
	}
	return err.Error()
}

// loadRecords asks for the newest captured requests matching query.
func (t *tester) loadRecords(query string) {
	if t.env == nil || t.env.API == nil {
		return
	}
	t.loading, t.recsErr = true, ""
	t.loadedQ = query
	t.recsGen++
	gen := t.recsGen
	env.Go(t.env, func(ctx context.Context) (api.HTTPList, error) {
		return t.env.API.ListHTTPRecords(ctx, api.HTTPQuery{Search: query, Limit: recordLimit})
	}, func(list api.HTTPList, err error) {
		if gen != t.recsGen {
			return
		}
		t.loading, t.listed = false, true
		if err != nil {
			t.recsErr = "无法读取请求记录：" + err.Error()
		} else {
			t.records = list.Records
		}
		// The text typed meanwhile asks for another list, once more after
		// a failure too.
		if t.query != query {
			t.loadRecords(t.query)
		}
	})
}

// fetch reads a record and the bodies a script may see.
func (t *tester) fetch(id string, max int) {
	if t.env == nil || t.env.API == nil {
		return
	}
	limit := int64(defaultBodyRead)
	if max > 0 {
		limit = int64(max)
	}
	limit = min(limit, maxBodyRead)
	t.fetching, t.fetchErr, t.msg = true, "", nil
	t.fetchGen++
	gen := t.fetchGen
	env.Go(t.env, func(ctx context.Context) (*message, error) {
		rec, err := t.env.API.GetHTTPRecord(ctx, id, "")
		if err != nil {
			return nil, err
		}
		m := &message{url: rec.URL, method: rec.Method, status: int32(rec.Status), hasResponse: rec.Status > 0}
		for _, h := range rec.RequestHeaders {
			m.reqHeaders = append(m.reqHeaders, api.ScriptHeader{Name: h.Name, Value: h.Value})
		}
		for _, h := range rec.ResponseHeaders {
			m.respHeaders = append(m.respHeaders, api.ScriptHeader{Name: h.Name, Value: h.Value})
		}
		body := func(side string, size int64, saved bool, label string) []byte {
			if size == 0 {
				return nil
			}
			if !saved {
				m.notes = append(m.notes, label+"正文未保存")
				return nil
			}
			b, err := t.env.API.HTTPBody(ctx, id, side, 0, limit, true)
			if err != nil {
				m.notes = append(m.notes, label+"正文读取失败："+err.Error())
				return nil
			}
			if b.DecodeError != "" {
				m.notes = append(m.notes, label+"正文解码失败："+b.DecodeError)
			}
			if b.Truncated || b.Total > int64(len(b.Data)) {
				m.notes = append(m.notes, label+"正文不完整："+kit.Bytes(float64(len(b.Data)))+" / "+kit.Bytes(float64(b.Total)))
			}
			return b.Data
		}
		m.reqBody = body(api.HTTPSideRequest, rec.RequestBytes, rec.RequestBodySaved, "请求")
		m.respBody = body(api.HTTPSideResponse, rec.ResponseBytes, rec.ResponseBodySaved, "响应")
		return m, nil
	}, func(m *message, err error) {
		if gen != t.fetchGen {
			return
		}
		t.fetching = false
		if err != nil {
			t.fetchErr = "无法读取记录：" + err.Error()
			return
		}
		t.msg = m
	})
}

// parseHeaders reads one "Name: value" per line.
func parseHeaders(text string) ([]api.ScriptHeader, error) {
	var out []api.ScriptHeader
	for i, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("第 %d 行应为「名称: 值」", i+1)
		}
		out = append(out, api.ScriptHeader{Name: name, Value: strings.TrimSpace(value)})
	}
	return out, nil
}

// headersText writes headers one "Name: value" per line.
func headersText(h []api.ScriptHeader) string {
	var b strings.Builder
	for _, f := range h {
		b.WriteString(f.Name)
		b.WriteString(": ")
		b.WriteString(f.Value)
		b.WriteByte('\n')
	}
	return b.String()
}

// manualMessage reads the manual input.
func (t *tester) manualMessage(typ string) (*message, error) {
	m := &message{url: strings.TrimSpace(t.url), method: t.method}
	if m.url == "" {
		return nil, fmt.Errorf("请填写 URL")
	}
	var err error
	if m.reqHeaders, err = parseHeaders(t.reqHeaders); err != nil {
		return nil, fmt.Errorf("请求头：%w", err)
	}
	m.reqBody = []byte(t.reqBody.Text())
	if typ == model.ScriptHTTPResponse {
		status, err := strconv.Atoi(strings.TrimSpace(t.status))
		if err != nil || status < 100 || status > 599 {
			return nil, fmt.Errorf("状态码须在 100–599 之间")
		}
		m.status, m.hasResponse = int32(status), true
		if m.respHeaders, err = parseHeaders(t.respHeaders); err != nil {
			return nil, fmt.Errorf("响应头：%w", err)
		}
		m.respBody = []byte(t.respBody.Text())
	}
	return m, nil
}

// run dry-runs the draft on the chosen input.
func (t *tester) run(e *editor) {
	if t.running || t.env == nil || t.env.API == nil {
		return
	}
	typ := types[min(max(e.typ, 0), len(types)-1)]
	req := api.ScriptEvalRequest{
		Source:       e.code.Text(),
		Type:         typ,
		Argument:     e.argument,
		RequiresBody: e.requires,
		BinaryBody:   e.binary && e.requires,
		Tag:          e.effectiveTag(),
	}
	var in message
	if isDecisionType(typ) {
		ruleIn, err := t.rule.apiInput(typ)
		if err != nil {
			t.err = err.Error()
			return
		}
		req.Rule = ruleIn
		req.RequiresBody, req.BinaryBody = false, false
	} else if !isRunType(typ) {
		req.Pattern = e.pattern
		if n, err := strconv.Atoi(strings.TrimSpace(e.maxSize)); err == nil && n > 0 {
			req.MaxSize = int64(n) * 1024
		}
		var m *message
		if t.source == sourceCaptured {
			if t.msg == nil {
				t.err = "请选择一条请求"
				return
			}
			m = t.msg
		} else {
			var err error
			if m, err = t.manualMessage(typ); err != nil {
				t.err = err.Error()
				return
			}
		}
		in = *m
		req.Request = &api.ScriptRequestMessage{URL: m.url, Method: m.method, Headers: m.reqHeaders, Body: m.reqBody, HasBody: len(m.reqBody) > 0}
		if typ == model.ScriptHTTPResponse {
			req.Response = &api.ScriptResponseMessage{Status: m.status, Headers: m.respHeaders, Body: m.respBody, HasBody: len(m.respBody) > 0}
		}
	}
	if n, err := strconv.Atoi(strings.TrimSpace(e.timeout)); err == nil && n > 0 {
		if isDecisionType(typ) {
			req.TimeoutMS = int64(n)
		} else {
			req.TimeoutMS = int64(n) * 1000
		}
	}
	t.err, t.running = "", true
	t.gen++
	gen := t.gen
	ctx, cancel := context.WithTimeout(context.Background(), evalLimit)
	t.cancel = cancel
	env.Go(t.env, func(context.Context) (api.ScriptEvalResult, error) {
		return t.env.API.ScriptEvaluate(ctx, req)
	}, func(res api.ScriptEvalResult, err error) {
		if gen != t.gen {
			return
		}
		cancel()
		t.cancel, t.running = nil, false
		if err != nil {
			t.err = unavailableText(err)
			return
		}
		t.result = &outcome{res: res, in: in, typ: typ}
		t.diffKey++
		t.reveal = true
		t.reply.Reset(responseText(res.Response))
	})
}

// responseText is a simulated response as text: the status line, the
// headers and the body.
func responseText(r *api.ScriptResponseMessage) string {
	if r == nil {
		return ""
	}
	text := "HTTP " + strconv.Itoa(int(r.Status)) + "\n" + headersText(r.Headers)
	if len(r.Body) > 0 {
		text += "\n" + bodyText(r.Body, false)
	}
	return text
}

// bodyText shows a body as text: JSON indented when pretty, binary as its
// size.
func isText(b []byte) bool { return utf8.Valid(b) && bytes.IndexByte(b, 0) < 0 }

func bodyText(b []byte, pretty bool) string {
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return "二进制 " + strconv.Itoa(len(b)) + " 字节"
	}
	if pretty && json.Valid(b) {
		var out bytes.Buffer
		if json.Indent(&out, b, "", "  ") == nil {
			return out.String()
		}
	}
	return string(b)
}

// applyChanges applies header changes to headers, for results that list
// changes without the whole header list.
func applyChanges(headers []api.ScriptHeader, changes []api.ScriptChange) []api.ScriptHeader {
	out := append([]api.ScriptHeader(nil), headers...)
	for _, ch := range changes {
		if ch.Kind != "header" {
			continue
		}
		at := -1
		for i, h := range out {
			if strings.EqualFold(h.Name, ch.Name) {
				at = i
				break
			}
		}
		switch {
		case !ch.HasAfter && at >= 0:
			out = append(out[:at:at], out[at+1:]...)
		case ch.HasAfter && at >= 0:
			out[at].Value = ch.After
		case ch.HasAfter:
			out = append(out, api.ScriptHeader{Name: ch.Name, Value: ch.After})
		}
	}
	return out
}

// diffs are the changes a result shows, by part.
type diffs struct {
	url          [2]string
	hasURL       bool
	headers      [2]string
	hasHeaders   bool
	body         [2]string
	hasBody      bool
	status       [2]int32
	hasStatus    bool
	hasResponse  bool
	any, isReply bool
}

// evalLimit bounds one test run: the request itself has no timeout of its
// own, and a script may run up to its own limit.
var evalLimit = 75 * time.Second

// diffOf works out what the result changed on the input.
func diffOf(o *outcome) diffs {
	var d diffs
	res, in := o.res, o.in
	if res.URL != "" && res.URL != in.url {
		d.url, d.hasURL = [2]string{in.url, res.URL}, true
	}
	before := in.reqHeaders
	body := in.reqBody
	if o.typ == model.ScriptHTTPResponse {
		before, body = in.respHeaders, in.respBody
		if res.Status != 0 && res.Status != in.status {
			d.status, d.hasStatus = [2]int32{in.status, res.Status}, true
		}
	}
	switch {
	case res.HeadersChanged:
		d.headers, d.hasHeaders = [2]string{headersText(before), headersText(res.Headers)}, true
	default:
		for _, ch := range res.Changes {
			if ch.Kind == "header" {
				d.headers, d.hasHeaders = [2]string{headersText(before), headersText(applyChanges(before, res.Changes))}, true
				break
			}
		}
	}
	if d.hasHeaders && d.headers[0] == d.headers[1] {
		d.hasHeaders = false
	}
	if res.BodyChanged {
		d.body, d.hasBody = [2]string{bodyText(body, true), bodyText(res.Body, true)}, true
		if d.body[0] == d.body[1] && !bytes.Equal(body, res.Body) {
			if isText(body) && isText(res.Body) {
				// Only the formatting changed: compare the text as sent.
				d.body = [2]string{bodyText(body, false), bodyText(res.Body, false)}
			} else {
				// Binary bodies of one size read the same: say they differ.
				d.body[1] = "二进制内容已变化"
			}
		}
	}
	d.isReply = res.Action == api.ScriptActionRespond && res.Response != nil
	d.hasResponse = d.isReply
	d.any = d.hasURL || d.hasHeaders || d.hasBody || d.hasStatus || d.hasResponse
	return d
}

func actionTone(action string) kit.Tone {
	switch action {
	case api.ScriptActionModify, api.ScriptActionRespond, api.ScriptActionAbort:
		return kit.AccentTone
	case api.ScriptActionError, api.ScriptActionTimeout, api.ScriptActionLimit:
		return kit.DangerTone
	case api.ScriptActionPaused, api.ScriptActionSkipped:
		return kit.WarningTone
	}
	return kit.Neutral
}

// SkipLabel names why a script did not run on a message.
func SkipLabel(reason string) string {
	switch reason {
	case "stream":
		return "流式传输"
	case "size":
		return "超出缓冲上限"
	case "timeout":
		return "缓冲超时"
	case "encoding":
		return "不支持的编码"
	case "decode":
		return "解码失败"
	case "decoded-size":
		return "解码后过大"
	case "busy":
		return "缓冲繁忙"
	case "":
		return "已跳过"
	}
	return reason
}

// view builds the test area.
func (t *tester) view(c *ui.Context, e *editor, narrow bool) {
	typ := types[min(max(e.typ, 0), len(types)-1)]
	decision := isDecisionType(typ)
	generic := isRunType(typ) || decision
	ui.Row(c).Gap(10).Shrink(0).Children(func() {
		if !generic {
			kit.Segmented(c, "sc-test-source", &t.source, kit.Segment{Label: "已抓取"}, kit.Segment{Label: "手填"}).Label("测试来源")
		}
		ui.Spacer(c)
		label := "运行测试"
		if t.running {
			label = "运行中…"
		}
		run := kit.Button(c, label, kit.ButtonOptions{Variant: kit.Primary, Icon: kit.SymPlay, Disabled: t.running})
		if run.Clicked() {
			t.run(e)
		}
	})
	if t.err != "" {
		kit.Notice(c, kit.DangerTone, "", t.err, nil)
	}
	if decision {
		t.ruleForm(c, typ, narrow)
	}
	if !generic {
		if t.source == sourceCaptured {
			t.captured(c, e)
		} else {
			t.manual(c, typ)
		}
	}
	if t.result != nil {
		t.resultView(c, t.result)
	}
}

// captured is the choice of a captured request and what was read of it.
func (t *tester) captured(c *ui.Context, e *editor) {
	k := kit.T(c)
	if !t.listed && !t.loading && t.recsErr == "" {
		t.loadRecords("")
	}
	options := make([]kit.Option, 0, len(t.records)+1)
	has := false
	for _, r := range t.records {
		has = has || r.ID == t.recID
		options = append(options, recordOption(r))
	}
	if t.recID != "" && !has && t.msg != nil {
		options = append([]kit.Option{{Value: t.recID, Label: t.msg.method + " " + hostPath(t.msg.url)}}, options...)
	}
	kit.FormRow(c, "请求", "", func() {
		sel := kit.SearchSelect(c, "sc-test-record", &t.recID, options, kit.SearchSelectOptions{
			Label: "已抓取的请求", Placeholder: "搜索 URL、主机", PopoverWidth: 520, Empty: "选择一条请求",
			OnOpen: func() { t.reopen = true },
			OnQuery: func(q string) {
				t.query = q
				if t.reopen {
					// Every open reads the records again.
					t.reopen = false
					t.loadedQ = "\x00"
				}
				if !t.loading && q != t.loadedQ {
					t.loadRecords(q)
				}
			},
		})
		if sel.Changed() {
			t.fetch(t.recID, e.maxBytes())
		}
		kit.FormError(c, t.recsErr)
		kit.FormError(c, t.fetchErr)
	})
	if t.fetching {
		ui.Text(c, "读取中…").FontSize(kit.FontTable).TextColor(k.Label3)
		return
	}
	m := t.msg
	if m == nil {
		return
	}
	ui.Column(c).Gap(2).Shrink(0).Children(func() {
		ui.Text(c, m.method+" "+m.url).FontSize(kit.FontTable).Font(kit.MonoFont).Selectable().Wrap()
		line := fmt.Sprintf("请求头 %d · 请求正文 %s", len(m.reqHeaders), bodySummary(m.reqBody))
		if e.typ == 1 {
			line += fmt.Sprintf(" · 响应 %d · 响应头 %d · 响应正文 %s", m.status, len(m.respHeaders), bodySummary(m.respBody))
		}
		ui.Text(c, line).FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum")
		for _, n := range m.notes {
			ui.Text(c, n).FontSize(kit.FontCaption).TextColor(k.Warning).Wrap()
		}
	})
}

func bodySummary(b []byte) string {
	switch {
	case len(b) == 0:
		return "无"
	case !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0:
		return "二进制 " + strconv.Itoa(len(b)) + " 字节"
	}
	return kit.Bytes(float64(len(b)))
}

func hostPath(url string) string {
	if i := strings.Index(url, "://"); i >= 0 {
		return url[i+3:]
	}
	return url
}

func recordOption(r api.HTTPRecord) kit.Option {
	detail := "–"
	if r.Status > 0 {
		detail = strconv.Itoa(r.Status)
	}
	if r.Timing != nil && r.Timing.Start > 0 {
		detail += " · " + timefmt.Format(time.Unix(0, r.Timing.Start), "15:04:05")
	}
	return kit.Option{Value: r.ID, Label: r.Method + " " + firstText(r.Host+r.Path, hostPath(r.URL)), Detail: detail}
}

// manual is the hand-written request.
func (t *tester) manual(c *ui.Context, typ string) {
	options := make([]kit.Option, len(methods))
	for i, m := range methods {
		options[i] = kit.Option{Value: m, Label: m}
	}
	kit.FormRow(c, "请求", "", func() {
		ui.Row(c).Gap(8).Children(func() {
			kit.Select(c, "sc-test-method", &t.method, options, kit.SelectOptions{Width: 108, Label: "方法"})
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				kit.Input(c, "sc-test-url", &t.url, "https://example.com/path").Label("URL").Font(kit.MonoFont)
			})
		})
	})
	kit.FormRow(c, "请求头", "", func() {
		kit.TextArea(c, "sc-test-reqh", &t.reqHeaders, "Name: value", true).MinHeight(72).Label("请求头")
	})
	kit.FormRow(c, "请求正文", "", func() {
		kit.CodeEditor(c.Key("sc-test-reqb"), &t.reqBody, kit.CodeOptions{Language: kit.LangJSON, Label: "请求正文", MinHeight: 96, Wrap: true}).Height(120)
	})
	if typ != model.ScriptHTTPResponse {
		return
	}
	kit.FormRow(c, "状态码", "", func() {
		ui.Row(c).Width(110).Children(func() { kit.Input(c, "sc-test-status", &t.status, "200").Label("状态码").FontFeatures("tnum") })
	})
	kit.FormRow(c, "响应头", "", func() {
		kit.TextArea(c, "sc-test-respH", &t.respHeaders, "Name: value", true).MinHeight(72).Label("响应头")
	})
	kit.FormRow(c, "响应正文", "", func() {
		kit.CodeEditor(c.Key("sc-test-respb"), &t.respBody, kit.CodeOptions{Language: kit.LangJSON, Label: "响应正文", MinHeight: 96, Wrap: true}).Height(120)
	})
}

// diffHeight sizes a diff to its text.
func diffHeight(a, b string) float32 {
	lines := max(strings.Count(a, "\n"), strings.Count(b, "\n")) + 2
	return float32(min(max(lines*18+8, 56), 260))
}

// resultView shows what a run produced.
func (t *tester) resultView(c *ui.Context, o *outcome) {
	k := kit.T(c)
	res := o.res
	col := ui.Column(c).Gap(8).Shrink(0).Margin(4, 0, 0)
	col.Children(func() {
		reason := ""
		if res.Action == api.ScriptActionSkipped || res.Reason != "" {
			reason = SkipLabel(res.Reason)
		}
		action := res.Action
		if isDecisionType(o.typ) && action == api.ScriptActionNone {
			// The decision row carries the outcome of these scripts.
			action = ""
		}
		resultHead(c, action, reason, res.DurationUS, res.Error)
		if isDecisionType(o.typ) {
			if label := DecisionLabel(o.typ, res.Decision); label != "" {
				ui.Row(c).Gap(8).AlignItems(ui.Start).Shrink(0).Children(func() {
					ui.Text(c, "判定").FontSize(kit.FontCaption).TextColor(k.Label3).Width(32).Shrink(0).Margin(2, 0, 0)
					ui.Text(c, label).FontSize(kit.FontTable).Font(kit.MonoFont).Selectable().Wrap().Grow(1).MinWidth(0)
				})
			}
		} else {
			d := diffOf(o)
			kit.Disclosure(c, "差异", t.open("diff"), func() {
				ui.Column(c).Gap(8).Shrink(0).Children(func() {
					t.diffParts(c, o, d)
				})
			})
		}
		if len(res.Logs) > 0 || res.Output != "" {
			kit.Disclosure(c, fmt.Sprintf("日志 · %d", len(res.Logs)), t.open("logs"), func() {
				ui.Column(c).Gap(2).Shrink(0).Children(func() {
					logLines(c, res.Logs)
					if res.Output != "" {
						ui.Row(c).Gap(8).AlignItems(ui.Start).Margin(4, 0, 0).Children(func() {
							ui.Text(c, "输出").FontSize(kit.FontCaption).TextColor(k.Label3).Width(32).Shrink(0).Margin(2, 0, 0)
							ui.Text(c, res.Output).FontSize(kit.FontTable).Font(kit.MonoFont).Selectable().Wrap().Grow(1).MinWidth(0)
						})
					}
				})
			})
		}
		if len(res.Notifications) > 0 {
			kit.Disclosure(c, fmt.Sprintf("通知 · %d", len(res.Notifications)), t.open("notes"), func() {
				ui.Column(c).Gap(6).Shrink(0).Children(func() {
					for i, n := range res.Notifications {
						ui.Column(c.Key(i)).Gap(1).Children(func() {
							ui.Text(c, n.Title).FontSize(kit.FontTable).FontWeight(600).Selectable().Wrap()
							if n.Subtitle != "" {
								ui.Text(c, n.Subtitle).FontSize(kit.FontTable).TextColor(k.Label2).Selectable().Wrap()
							}
							if n.Body != "" {
								ui.Text(c, n.Body).FontSize(kit.FontTable).TextColor(k.Label2).Selectable().Wrap()
							}
						})
					}
				})
			})
		}
	})
	if t.reveal {
		t.reveal = false
		col.ScrollIntoView()
	}
}

// resultHead is the first lines of a run's result: the action as a badge,
// the duration, a note (why a run was skipped) and the error.
func resultHead(c *ui.Context, action, note string, durationUS int64, errText string) {
	k := kit.T(c)
	ui.Row(c).Gap(8).Shrink(0).Children(func() {
		if action != "" {
			kit.Badge(c, ActionLabel(action), actionTone(action))
		}
		ui.Text(c, Duration(durationUS)).FontSize(kit.FontTable).FontFeatures("tnum").TextColor(k.Label2)
		if note != "" {
			ui.Text(c, note).FontSize(kit.FontTable).TextColor(k.Label3)
		}
	})
	if errText != "" {
		ui.Text(c, errText).FontSize(kit.FontTable).Font(kit.MonoFont).Wrap().Selectable().TextColor(k.Danger)
	}
}

// logLines lists console lines, each behind a dot of its level's color.
func logLines(c *ui.Context, logs []api.ScriptLogLine) {
	k := kit.T(c)
	for i, l := range logs {
		ui.Row(c.Key(i)).Gap(8).AlignItems(ui.Start).Children(func() {
			ui.Box(c).Size(6, 6).Radius(3).Background(levelColor(k, l.Level)).Margin(5, 0, 0).Shrink(0)
			ui.Text(c, l.Message).FontSize(kit.FontTable).Font(kit.MonoFont).Selectable().Wrap().Grow(1).MinWidth(0)
		})
	}
}

func levelColor(k *kit.Tokens, level string) ui.Color {
	switch level {
	case api.ScriptLogLevelError:
		return k.Danger
	case api.ScriptLogLevelWarn:
		return k.Warning
	case api.ScriptLogLevelInfo:
		return k.Accent
	}
	return k.Label4
}

// diffParts lists the changed parts of a result.
func (t *tester) diffParts(c *ui.Context, o *outcome, d diffs) {
	k := kit.T(c)
	part := func(title string, fn func()) {
		ui.Column(c.Key(title)).Gap(4).Shrink(0).Children(func() {
			ui.Text(c, title).FontSize(kit.FontCaption).FontWeight(600).TextColor(k.Label3)
			fn()
		})
	}
	if !d.any {
		ui.Text(c, "无改动").FontSize(kit.FontTable).TextColor(k.Label3)
		return
	}
	if d.hasURL {
		part("URL", func() {
			kit.CodeDiff(c, d.url[0], d.url[1], kit.DiffOptions{Key: "sc-diff-url", Wrap: true, MinHeight: 40, NoWords: false}).Height(diffHeight(d.url[0], d.url[1]))
		})
	}
	if d.hasStatus {
		part("状态码", func() {
			ui.Text(c, fmt.Sprintf("%d → %d", d.status[0], d.status[1])).FontSize(kit.FontTable).Font(kit.MonoFont).FontFeatures("tnum").Selectable()
		})
	}
	if d.hasHeaders {
		title := "请求头"
		if o.typ == model.ScriptHTTPResponse {
			title = "响应头"
		}
		part(title, func() {
			kit.CodeDiff(c, d.headers[0], d.headers[1], kit.DiffOptions{Key: "sc-diff-headers", Wrap: true, MinHeight: 40}).Height(diffHeight(d.headers[0], d.headers[1]))
		})
	}
	if d.hasBody {
		part("正文", func() {
			kit.CodeDiff(c, d.body[0], d.body[1], kit.DiffOptions{Key: "sc-diff-body", Language: bodyLang(d.body[1]), Wrap: true, MinHeight: 40}).Height(diffHeight(d.body[0], d.body[1]))
		})
	}
	if d.hasResponse {
		part("模拟响应", func() {
			text := t.reply.Text()
			kit.CodeEditor(c.Key("sc-reply"), &t.reply, kit.CodeOptions{Language: kit.LangText, ReadOnly: true, Label: "模拟响应", MinHeight: 56, Wrap: true}).
				Height(diffHeight(text, text))
		})
	}
}

func bodyLang(text string) string {
	if json.Valid([]byte(text)) {
		return kit.LangJSON
	}
	return kit.LangText
}

// networks are the choices of the transport of a rule input.
var networks = []string{"tcp", "udp"}

// queryTypes are the choices of the question type of a dns script input.
var queryTypes = []string{"A", "AAAA", "HTTPS", "CNAME", "MX", "TXT"}

// ruleForm is the $request input of a rule or dns script.
func (t *tester) ruleForm(c *ui.Context, typ string, narrow bool) {
	r := &t.rule
	pair(c, narrow, half(c, narrow, "域名", func() {
		kit.Input(c, "sc-rule-domain", &r.domain, "example.com").Label("域名").Font(kit.MonoFont)
	}), half(c, narrow, "IP", func() {
		kit.Input(c, "sc-rule-ip", &r.ip, "203.0.113.1").Label("IP").Font(kit.MonoFont)
	}))
	netOptions := make([]kit.Option, len(networks))
	for i, n := range networks {
		netOptions[i] = kit.Option{Value: n, Label: strings.ToUpper(n)}
	}
	pair(c, narrow, half(c, narrow, "端口", func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.Row(c).Width(96).Children(func() { kit.Input(c, "sc-rule-port", &r.port, "443").Label("端口").FontFeatures("tnum") })
			kit.Select(c, "sc-rule-network", &r.network, netOptions, kit.SelectOptions{Width: 88, Label: "传输协议"})
		})
	}), half(c, narrow, "应用协议", func() {
		kit.Input(c, "sc-rule-protocol", &r.protocol, "tls").Label("应用协议")
	}))
	pair(c, narrow, half(c, narrow, "入站", func() {
		kit.Input(c, "sc-rule-inbound", &r.inbound, "").Label("入站")
	}), half(c, narrow, "来源 IP", func() {
		kit.Input(c, "sc-rule-source", &r.sourceIP, "192.168.1.2").Label("来源 IP").Font(kit.MonoFont)
	}))
	pair(c, narrow, half(c, narrow, "进程", func() {
		kit.Input(c, "sc-rule-process", &r.process, "").Label("进程名")
	}), func() {
		if typ != model.ScriptDNS {
			return
		}
		qOptions := make([]kit.Option, len(queryTypes))
		for i, q := range queryTypes {
			qOptions[i] = kit.Option{Value: q, Label: q}
		}
		half(c, narrow, "查询类型", func() {
			kit.Select(c, "sc-rule-qtype", &r.queryType, qOptions, kit.SelectOptions{Width: 108, Label: "查询类型"})
		})()
	})
}
