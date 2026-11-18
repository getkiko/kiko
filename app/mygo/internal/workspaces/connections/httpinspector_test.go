package connections

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func TestParseJSONTree(t *testing.T) {
	root, err := parseJSONTree([]byte(`{"a":1,"b":[true,null,"x"],"c":{"d":1.5}}`))
	if err != nil {
		t.Fatal(err)
	}
	if root.label() != "{3}" || root.kids[0].label() != "a: 1" || root.kids[1].label() != "b: [3]" || root.kids[1].kids[2].label() != `"x"` || root.kids[2].kids[0].label() != "d: 1.5" {
		t.Fatalf("tree %+v", root)
	}
	if _, err := parseJSONTree([]byte(`{"a":`)); err == nil {
		t.Fatal("truncated document parsed")
	}
	// The node budget leaves the rest out and counts it.
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < httpTreeNodes+500; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(strconv.Itoa(i))
	}
	b.WriteString("]")
	big, err := parseJSONTree([]byte(b.String()))
	if err != nil || len(big.kids) != httpTreeNodes-1 || big.hidden != 501 || big.label() != fmt.Sprintf("[%d]", httpTreeNodes+500) {
		t.Fatalf("budget: %v kids %d hidden %d", err, len(big.kids), big.hidden)
	}
}

type fakeDoc struct{ doc *model.Doc }

func (f *fakeDoc) Doc() *model.Doc { return f.doc }
func (f *fakeDoc) Update(fn func(*model.Doc)) error {
	fn(f.doc)
	return nil
}

func TestExcludeDecryption(t *testing.T) {
	f := &fakeDoc{doc: model.EmptyDoc("")}
	if hostExcluded(f.doc, "api.github.com") {
		t.Fatal("excluded at first")
	}
	if name, err := excludeDecryption(f, "API.GitHub.com."); err != nil || name != "api.github.com" {
		t.Fatalf("%q %v", name, err)
	}
	if !hostExcluded(f.doc, "api.github.com") || len(f.doc.HTTPTools.MITM.Hostnames.Exclude) != 1 {
		t.Fatalf("doc %+v", f.doc.HTTPTools.MITM.Hostnames)
	}
	// Again changes nothing; an included host moves over.
	if _, err := excludeDecryption(f, "api.github.com"); err != nil || len(f.doc.HTTPTools.MITM.Hostnames.Exclude) != 1 {
		t.Fatalf("again: %v %+v", err, f.doc.HTTPTools.MITM.Hostnames)
	}
	f.doc.HTTPTools.MITM.Hostnames.Include = []string{"chatgpt.com"}
	if _, err := excludeDecryption(f, "chatgpt.com"); err != nil || len(f.doc.HTTPTools.MITM.Hostnames.Include) != 0 || len(f.doc.HTTPTools.MITM.Hostnames.Exclude) != 2 {
		t.Fatalf("move: %v %+v", err, f.doc.HTTPTools.MITM.Hostnames)
	}
	for _, bad := range []string{"", "*", "http://x.com/a", "cloudflare-ech.com"} {
		if _, err := excludeDecryption(f, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func pngBytes() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for x := 0; x < 40; x++ {
		for y := 0; y < 20; y++ {
			img.Set(x, y, color.RGBA{uint8(x * 6), 120, uint8(y * 12), 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func inspectorFrame() api.HTTPFrame {
	mk := func(id uint64, path string, ctype string, size int64) api.HTTPRecord {
		r := hrec(id, "api.github.com", 200, api.HTTPStateComplete)
		r.Method, r.Path, r.URL = "POST", path, "https://api.github.com"+path
		r.ConnectionID = "c9"
		r.Connection = &api.HTTPConnection{ID: "c9", Source: "172.18.0.1:5000", Destination: "140.82.112.6:443", Outbound: "美国板瓦", OutboundType: "vless", Rule: "rule_set=[Gfw]", ProcessName: "curl", ProcessID: 4242}
		r.RequestHeaders = []api.HTTPHeader{{Name: "Authorization", Value: "Bearer s3cret"}, {Name: "Content-Type", Value: "application/json"}, {Name: "Accept-Encoding", Value: "gzip"}}
		r.ResponseHeaders = []api.HTTPHeader{{Name: "Content-Type", Value: ctype}}
		r.ResponseTrailers = []api.HTTPHeader{{Name: "X-Checksum", Value: "abc123"}}
		r.ResponseBytes, r.ResponseBodySaved = size, true
		r.Timing = &api.HTTPTiming{Start: 1_000_000_000, DNS: 4_000_000, Connect: 20_000_000, TLS: 30_000_000, RequestHeaders: 1_002_000_000, FirstByte: 1_100_000_000, ResponseHeaders: 1_101_000_000, End: 1_250_000_000}
		return r
	}
	recs := []api.HTTPRecord{
		mk(21, "/json", "application/json", 40),
		mk(22, "/text", "text/plain; charset=utf-8", 24),
		mk(23, "/image", "image/png", int64(len(pngBytes()))),
		mk(24, "/binary", "application/octet-stream", 8000),
		mk(25, "/big", "text/plain", 3<<20),
		mk(26, "/dropped", "text/plain", 50),
		mk(27, "/unsaved", "text/plain", 50),
	}
	recs[5].BodyDropped = true
	recs[6].ResponseBodySaved = false
	recs[4].ResponseBodyTrunc, recs[4].ResponseBodyStored = true, 2<<20
	recs[1].RequestBytes, recs[1].RequestBodySaved = 7, true
	recs[1].RequestHeaders[1].Value = "application/x-www-form-urlencoded"
	recs[1].ResponseEncoding = "gzip"
	f := api.HTTPFrame{Instance: "i", Seq: 27, Floor: 1, Reset: true, Status: &api.HTTPStatus{Instance: "i", RecordCount: 7, Total: 7}}
	for i := range recs {
		f.Events = append(f.Events, api.HTTPEvent{Kind: api.HTTPEventNew, Record: &recs[i]})
	}
	return f
}

// bodyRoute serves bodies by record, honouring offset, length and the
// decoded flag.
// decodeErrorIDs are the records whose body route reports a decode error.
var decodeErrorIDs = map[string]bool{}

func bodyRoute(requests *[]string) func(*testkit.Server) {
	binary := func() []byte {
		b := make([]byte, 8000)
		for i := range b {
			b[i] = byte(i % 251)
		}
		b[0] = 0
		return b
	}()
	big := bytes.Repeat([]byte("line of text in a big body\n"), (3<<20)/27+1)[:3<<20]
	bodies := map[string][]byte{
		"21/response": []byte(`{"id":7,"login":"octocat","tags":["a","b"]}`),
		"22/response": []byte("hello decoded text\nsecond line"),
		"23/response": pngBytes(),
		"24/response": binary,
		"25/response": big,
		"22/request":  []byte("a=1&b=2"),
	}
	return func(srv *testkit.Server) {
		srv.Route("GET", "/http/records/", func(w http.ResponseWriter, r *http.Request) {
			path := strings.TrimPrefix(r.URL.Path, "/api/v1/http/records/")
			id, _, _ := strings.Cut(path, "/")
			q := r.URL.Query()
			*requests = append(*requests, id+" "+r.URL.RawQuery)
			data := bodies[id+"/"+q.Get("side")]
			if q.Get("side") == "response" && id == "22" && q.Get("decoded") != "true" {
				data = []byte("\x1f\x8b raw gzip bytes")
			}
			total := int64(len(data))
			offset, _ := strconv.ParseInt(q.Get("offset"), 10, 64)
			length, _ := strconv.ParseInt(q.Get("length"), 10, 64)
			end := total
			if length > 0 {
				end = min(total, offset+length)
			}
			part := data[min(offset, total):end]
			h := w.Header()
			h.Set("X-Total", strconv.FormatInt(total, 10))
			h.Set("X-Original-Size", strconv.FormatInt(total, 10))
			h.Set("X-Offset", strconv.FormatInt(offset, 10))
			h.Set("X-Saved", "true")
			h.Set("X-Complete", strconv.FormatBool(end == total))
			if id == "25" {
				h.Set("X-Truncated", "true")
			}
			if decodeErrorIDs[id] {
				h.Set("X-Decode-Error", "unexpected EOF")
			}
			_, _ = w.Write(part)
		})
	}
}

func newInspectorHarness(t *testing.T, width, height int) (*harness, *[]string) {
	requests := new([]string)
	h := newHTTPHarness(t, width, height, enabled(), newHTTPFeed(inspectorFrame()), bodyRoute(requests))
	h.w.http.doc = &fakeDoc{doc: model.EmptyDoc("")}
	testkit.UntilText(t, h.tt, "/json")
	return h, requests
}

func TestHTTPInspectorOverview(t *testing.T) {
	h, _ := newInspectorHarness(t, 1440, 1300)
	tt := h.tt
	h.click(t, "/json")
	testkit.UntilText(t, tt, "概览")
	for _, want := range []string{"请求头 · 3", "响应头 · 2", "计时", "DNS", "连接", "TLS", "等待响应", "总计", "250 ms", "美国板瓦 · vless", "curl · PID 4242", "rule_set=[Gfw]", "172.18.0.1:5000", "c9", "#21 · 第 1 个请求"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
	savePreview(t, tt, "http-inspector-light.png")

	// The headers fold: closed at first, open on a click, closed again.
	if tt.HasText("Bearer s3cret") {
		t.Fatal("headers open at first")
	}
	h.click(t, "请求头 · 3")
	testkit.UntilText(t, tt, "Bearer s3cret")
	h.click(t, "响应头 · 2")
	testkit.UntilText(t, tt, "X-Checksum")
	savePreview(t, tt, "http-inspector-headers-light.png")
	h.click(t, "请求头 · 3")
	testkit.Until(t, tt, func() bool { return !tt.HasText("Bearer s3cret") })
}

func TestHTTPInspectorBodies(t *testing.T) {
	h, requests := newInspectorHarness(t, 1440, 1800)
	tt := h.tt

	// JSON shows as a tree, and as text on request.
	h.click(t, "/json")
	testkit.UntilText(t, tt, "响应正文")
	testkit.UntilText(t, tt, "login: \"octocat\"")
	if !tt.HasText("tags: [2]") || !tt.HasText("文本") {
		t.Fatalf("tree: %q", tt.Texts())
	}
	savePreview(t, tt, "http-body-json-light.png")
	h.click(t, "文本")
	testkit.UntilText(t, tt, `{"id":7,"login":"octocat","tags":["a","b"]}`)
	h.click(t, "复制正文")
	if got := tt.Clipboard(); got != `{"id":7,"login":"octocat","tags":["a","b"]}` {
		t.Fatalf("clipboard %q", got)
	}

	// Text with an encoding: decoded first, original on request.
	h.click(t, "/text")
	testkit.UntilText(t, tt, "hello decoded text\nsecond line")
	if !tt.HasText("a=1&b=2") || !tt.HasText("解码 gzip") || !tt.HasText("原始") {
		t.Fatalf("encoding switch: %q", tt.Texts())
	}
	h.click(t, "原始")
	testkit.Until(t, tt, func() bool { return !tt.HasText("hello decoded text\nsecond line") })
	// The bytes as sent are no text: they list as hexadecimal.
	testkit.Until(t, tt, func() bool { return strings.Contains(strings.Join(tt.Texts(), "|"), "1f 8b 20 72") })
	if !slices.ContainsFunc(*requests, func(r string) bool {
		return strings.HasPrefix(r, "22 ") && !strings.Contains(r, "decoded=true") && strings.Contains(r, "side=response")
	}) {
		t.Fatalf("requests %q", *requests)
	}

	// An image is decoded and sized.
	h.click(t, "/image")
	testkit.UntilText(t, tt, "40 × 20")
	savePreview(t, tt, "http-body-image-light.png")

	// Binary data lists as hexadecimal, the first 4 KB.
	h.click(t, "/binary")
	testkit.UntilText(t, tt, "仅显示前 4.0 KB")
	if !strings.Contains(strings.Join(tt.Texts(), "|"), "00000000  00 01 02") {
		t.Fatalf("hex: %q", tt.Texts())
	}
	savePreview(t, tt, "http-body-hex-light.png")

	// A big truncated body loads by chunks and stops at the limit.
	h.click(t, "/big")
	testkit.UntilText(t, tt, "正文已截断，保存 2.0 MB / 3.0 MB")
	if !strings.Contains(strings.Join(tt.Texts(), "|"), "加载更多 · 64.0 KB / 3.0 MB") {
		t.Fatalf("more: %q", tt.Texts())
	}
	h.click(t, "加载更多 · 64.0 KB / 3.0 MB")
	testkit.UntilText(t, tt, "加载更多 · 128 KB / 3.0 MB")
	savePreview(t, tt, "http-body-truncated-light.png")

	// Dropped and unsaved bodies say so without a read.
	before := len(*requests)
	h.click(t, "/dropped")
	testkit.UntilText(t, tt, "正文已丢弃")
	h.click(t, "/unsaved")
	testkit.UntilText(t, tt, "未保存正文")
	for _, r := range (*requests)[before:] {
		if strings.HasPrefix(r, "26 ") || strings.HasPrefix(r, "27 ") && strings.Contains(r, "response") {
			t.Errorf("read of a body that is not there: %q", r)
		}
	}
	savePreview(t, tt, "http-body-unsaved-light.png")
}

func TestHTTPInspectorActions(t *testing.T) {
	h, _ := newInspectorHarness(t, 1440, 1300)
	tt := h.tt
	h.click(t, "/text")
	testkit.UntilText(t, tt, "复制 URL")
	h.click(t, "复制 URL")
	if tt.Clipboard() != "https://api.github.com/text" {
		t.Fatalf("url %q", tt.Clipboard())
	}
	testkit.UntilText(t, tt, "已复制")
	// The command hides the credentials and takes the body when read.
	testkit.UntilText(t, tt, "hello decoded text\nsecond line")
	h.click(t, "复制为 cURL")
	curl := tt.Clipboard()
	for _, want := range []string{"curl 'https://api.github.com/text'", "-X 'POST'", "Authorization: <redacted>", "--compressed", `--data-raw 'a=1&b=2'`} {
		if !strings.Contains(curl, want) {
			t.Errorf("curl lacks %q: %s", want, curl)
		}
	}
	if strings.Contains(curl, "s3cret") {
		t.Errorf("secret in %s", curl)
	}

	// The connection opens in Network Connections.
	h.click(t, "查看连接")
	if want := "connections:{map[] }"; len(h.pages) == 0 {
		t.Fatalf("no navigation, want %s", want)
	}
	arg := h.pages[len(h.pages)-1]
	if !strings.HasPrefix(arg, "connections:") || !strings.Contains(arg, "c9") {
		t.Fatalf("navigate %q", arg)
	}

	// Skipping decryption excludes the host and says so.
	h.click(t, "跳过解密此主机")
	testkit.UntilText(t, tt, "已加入排除：api.github.com")
	doc := h.w.http.doc.Doc()
	if got := doc.HTTPTools.MITM.Hostnames.Exclude; len(got) != 1 || got[0] != "api.github.com" {
		t.Fatalf("exclude %v", got)
	}
	if !tt.HasText("已跳过解密") {
		t.Fatalf("button: %q", tt.Texts())
	}
	// Another record resets the notice; the host stays excluded.
	h.click(t, "/json")
	testkit.UntilText(t, tt, "已跳过解密")
	if tt.HasText("已加入排除：api.github.com") {
		t.Fatalf("notice kept: %q", tt.Texts())
	}
}

func TestHTTPInspectorEscapeAndFocus(t *testing.T) {
	h, _ := newInspectorHarness(t, 1440, 1300)
	tt := h.tt
	h.click(t, "/json")
	testkit.UntilText(t, tt, "概览")
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return !tt.HasText("概览") })
	if h.w.http.refocus {
		// The list took the focus back in the frame after closing.
		tt.Frame()
	}
	if h.w.http.refocus {
		t.Fatal("focus not returned to the list")
	}
	// The list keeps the keyboard: Down then Enter selects.
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	testkit.UntilText(t, tt, "概览")
}

func TestHTTPInspectorNarrow(t *testing.T) {
	h, _ := newInspectorHarness(t, 760, 700)
	tt := h.tt
	h.click(t, "/json")
	testkit.UntilText(t, tt, "概览")
	testkit.UntilText(t, tt, "login: \"octocat\"")
	savePreview(t, tt, "http-inspector-narrow-light.png")
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return !tt.HasText("概览") })
	tt.SetDark(true)
	tt.Frame()
	h.click(t, "/json")
	testkit.UntilText(t, tt, "概览")
	savePreview(t, tt, "http-inspector-narrow-dark.png")
}

func TestHTTPInspectorDark(t *testing.T) {
	h, _ := newInspectorHarness(t, 1440, 1300)
	tt := h.tt
	tt.SetDark(true)
	h.click(t, "/json")
	testkit.UntilText(t, tt, "login: \"octocat\"")
	savePreview(t, tt, "http-inspector-dark.png")
	h.click(t, "/big")
	testkit.UntilText(t, tt, "正文已截断，保存 2.0 MB / 3.0 MB")
	savePreview(t, tt, "http-body-truncated-dark.png")
}

// The network inspector leads to the connection's requests, and the
// network monitor opens limited to one connection.
func TestConnectionLinksHTTP(t *testing.T) {
	h := newMitmHarness(t, 1440, 820)
	tt := h.tt
	h.click(t, "mtalk.google.com")
	testkit.UntilText(t, tt, "TCP · HTTPS · 已解密")
	h.click(t, "查看此连接的 HTTP 请求")
	if len(h.pages) == 0 || !strings.HasPrefix(h.pages[len(h.pages)-1], "connections:") || !strings.Contains(h.pages[len(h.pages)-1], "c1") {
		t.Fatalf("navigate %v", h.pages)
	}

	h.w.Open(env.ConnectionsView{ConnectionID: "c2"})
	testkit.UntilText(t, tt, "连接 c2")
	testkit.Until(t, tt, func() bool { return h.requestedAll("GET /api/v1/connections", "filter.id=c2") })
	if tt.HasText("mtalk.google.com") || !tt.HasText("api.github.com") {
		t.Fatalf("limited: %q", tt.Texts())
	}
	h.click(t, "移除 连接 c2")
	testkit.UntilText(t, tt, "mtalk.google.com")
}
