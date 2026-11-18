package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	mitmapi "github.com/sagernet/sing-box/common/mitm/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeHAR struct {
	mitmapi.UnimplementedHTTPCaptureServer
	records []*mitmapi.HTTPRecord
	bodies  map[string]*mitmapi.BodyChunk
	// chunk is the size the bodies are split into.
	chunk int
}

func (f *fakeHAR) ListRecords(_ context.Context, request *mitmapi.ListRecordsRequest) (*mitmapi.RecordList, error) {
	var out []*mitmapi.HTTPRecord
	for i := len(f.records) - 1; i >= 0; i-- {
		record := f.records[i]
		var id uint64
		fmt.Sscan(record.Id, &id)
		if request.Before != 0 && id >= request.Before {
			continue
		}
		if request.Host != "" && record.Host != request.Host {
			continue
		}
		out = append(out, record)
		if len(out) == int(request.Limit) {
			break
		}
	}
	list := &mitmapi.RecordList{Instance: "inst-1", Records: out}
	if len(out) == int(request.Limit) {
		fmt.Sscan(out[len(out)-1].Id, &list.NextBefore)
	}
	return list, nil
}

func (f *fakeHAR) GetRecord(_ context.Context, request *mitmapi.GetRecordRequest) (*mitmapi.HTTPRecord, error) {
	for _, record := range f.records {
		if record.Id == request.Id {
			return record, nil
		}
	}
	return nil, status.Error(codes.NotFound, "record not found")
}

func (f *fakeHAR) GetBody(request *mitmapi.GetBodyRequest, stream grpc.ServerStreamingServer[mitmapi.BodyChunk]) error {
	key := request.Id + ":" + request.Side.String()
	view, ok := f.bodies[key]
	if !ok {
		return status.Error(codes.NotFound, "record not found")
	}
	if !request.Decoded {
		return status.Error(codes.InvalidArgument, "the HAR export reads decoded views")
	}
	data := view.Data
	size := f.chunk
	if size == 0 {
		size = len(data)
	}
	for offset := 0; offset == 0 || offset < len(data); offset += size {
		end := min(offset+size, len(data))
		chunk := &mitmapi.BodyChunk{Data: data[offset:end], Truncated: view.Truncated, Total: view.Total, OriginalSize: view.OriginalSize, Saved: true, Offset: int64(offset), DecodeError: view.DecodeError}
		if err := stream.Send(chunk); err != nil {
			return err
		}
		if len(data) == 0 {
			break
		}
	}
	return nil
}

func harRecords() *fakeHAR {
	start := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC).UnixNano()
	record := func(id, method, url, host, path string, status int32) *mitmapi.HTTPRecord {
		var n uint64
		fmt.Sscan(id, &n)
		return &mitmapi.HTTPRecord{
			Id: id, Instance: "inst-1", Seq: n, CreatedSeq: n, ConnectionId: "conn-" + id, Method: method, Scheme: "https",
			Authority: host, Host: host, Path: path, Url: url, Proto: "h2", Status: status,
			State:      mitmapi.RecordState_RECORD_STATE_COMPLETE,
			Connection: &mitmapi.HTTPConnection{Id: "conn-" + id, Destination: "93.184.216.34:443", Inbound: "mixed", Outbound: "proxy", ProcessName: "curl"},
			Timing: &mitmapi.HTTPTiming{
				Start: start, RequestHeaders: start + 1e6, RequestBody: start + 2e6, FirstByte: start + 52e6, ResponseHeaders: start + 53e6, ResponseBody: start + 60e6, End: start + 61e6,
				Connect: 20e6, Tls: 30e6, Dns: 5e6,
			},
		}
	}
	post := record("3", "POST", "https://api.example.com/v1/login?token=abc&user=bob&Api_Key=zzz", "api.example.com", "/v1/login?token=abc&user=bob&Api_Key=zzz", 302)
	post.RequestHeaders = []*mitmapi.HTTPHeader{
		{Name: "Authorization", Value: "Bearer secret"}, {Name: "Cookie", Value: "sid=111; theme=dark"}, {Name: "Content-Type", Value: "application/json"},
		{Name: "X-Api-Key", Value: "k"},
	}
	post.ResponseHeaders = []*mitmapi.HTTPHeader{
		{Name: "Set-Cookie", Value: "sid=222; Path=/; HttpOnly; Secure"}, {Name: "Content-Type", Value: "application/json; charset=utf-8"},
		{Name: "Location", Value: "https://api.example.com/next?code=1&access_token=t"},
	}
	post.RequestBytes, post.ResponseBytes = int64(len(`{"p":"é"}`)), 20
	get := record("2", "GET", "https://www.example.com/logo.png", "www.example.com", "/logo.png", 200)
	get.ResponseHeaders = []*mitmapi.HTTPHeader{{Name: "Content-Type", Value: "image/png"}}
	get.ResponseBytes = 8
	get.Timing.Dns, get.Timing.Connect, get.Timing.Tls = 0, 0, 0
	failed := record("4", "GET", "https://down.example.com/", "down.example.com", "/", 0)
	failed.State, failed.Error, failed.Timing = mitmapi.RecordState_RECORD_STATE_FAILED, "connection refused", &mitmapi.HTTPTiming{Start: start}
	text := record("5", "GET", "https://www.example.com/page", "www.example.com", "/page", 200)
	text.ResponseHeaders = []*mitmapi.HTTPHeader{{Name: "Content-Type", Value: "text/html"}}
	text.ResponseBytes = 30
	return &fakeHAR{
		records: []*mitmapi.HTTPRecord{get, post, failed, text},
		chunk:   4,
		bodies: map[string]*mitmapi.BodyChunk{
			"3:BODY_SIDE_REQUEST":  {Data: []byte(`{"p":"é"}`), Total: 9, OriginalSize: 9},
			"3:BODY_SIDE_RESPONSE": {Data: []byte(`{"ok":"héllo 世界 <b>"}`), Total: 25, OriginalSize: 20},
			"2:BODY_SIDE_RESPONSE": {Data: []byte{0x89, 'P', 'N', 'G', 0, 1, 2, 3}, Total: 8, OriginalSize: 8},
			"5:BODY_SIDE_RESPONSE": {Data: []byte("<html>世界"), Total: 100, OriginalSize: 100, Truncated: true},
		},
	}
}

// harAllowed lists the keys the HAR 1.2 specification defines per object;
// anything else must start with an underscore.
var harAllowed = map[string][]string{
	"log":      {"version", "creator", "browser", "pages", "entries", "comment"},
	"creator":  {"name", "version", "comment"},
	"entry":    {"pageref", "startedDateTime", "time", "request", "response", "cache", "timings", "serverIPAddress", "connection", "comment"},
	"request":  {"method", "url", "httpVersion", "cookies", "headers", "queryString", "postData", "headersSize", "bodySize", "comment"},
	"response": {"status", "statusText", "httpVersion", "cookies", "headers", "content", "redirectURL", "headersSize", "bodySize", "comment"},
	"content":  {"size", "compression", "mimeType", "text", "encoding", "comment"},
	"postData": {"mimeType", "params", "text", "comment"},
	"timings":  {"blocked", "dns", "connect", "send", "wait", "receive", "ssl", "comment"},
	"cookie":   {"name", "value", "path", "domain", "expires", "httpOnly", "secure", "comment"},
	"pair":     {"name", "value", "comment"},
}

var harTime = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}(Z|[+-]\d{2}:\d{2})$`)

type harChecker struct {
	t *testing.T
}

func (c harChecker) object(kind string, v any, path string) map[string]any {
	c.t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		c.t.Fatalf("%s is not an object: %v", path, v)
	}
	for key := range m {
		if strings.HasPrefix(key, "_") {
			continue
		}
		found := false
		for _, name := range harAllowed[kind] {
			found = found || name == key
		}
		if !found {
			c.t.Fatalf("%s has unknown field %q", path, key)
		}
	}
	return m
}

func (c harChecker) require(m map[string]any, path string, names ...string) {
	c.t.Helper()
	for _, name := range names {
		if _, ok := m[name]; !ok {
			c.t.Fatalf("%s lacks %q", path, name)
		}
	}
}

func (c harChecker) str(m map[string]any, key, path string) string {
	c.t.Helper()
	v, ok := m[key].(string)
	if !ok {
		c.t.Fatalf("%s.%s is not a string: %v", path, key, m[key])
	}
	return v
}

func (c harChecker) num(m map[string]any, key, path string) float64 {
	c.t.Helper()
	v, ok := m[key].(float64)
	if !ok {
		c.t.Fatalf("%s.%s is not a number: %v", path, key, m[key])
	}
	return v
}

func (c harChecker) pairs(kind string, v any, path string) {
	c.t.Helper()
	list, ok := v.([]any)
	if !ok {
		c.t.Fatalf("%s is not an array: %v", path, v)
	}
	for i, item := range list {
		p := fmt.Sprintf("%s[%d]", path, i)
		m := c.object(kind, item, p)
		c.require(m, p, "name", "value")
		c.str(m, "name", p)
		c.str(m, "value", p)
	}
}

// validate checks a document against the structure of HAR 1.2 that Chrome
// DevTools relies on: required fields, their JSON types and the value
// ranges of sizes and timings.
func (c harChecker) validate(data []byte) []map[string]any {
	c.t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		c.t.Fatalf("not JSON: %v", err)
	}
	if len(doc) != 1 {
		c.t.Fatalf("top level: %v", doc)
	}
	log := c.object("log", doc["log"], "log")
	c.require(log, "log", "version", "creator", "entries")
	if c.str(log, "version", "log") != "1.2" {
		c.t.Fatalf("version %v", log["version"])
	}
	creator := c.object("creator", log["creator"], "creator")
	if c.str(creator, "name", "creator") != "Kiko" || c.str(creator, "version", "creator") == "" {
		c.t.Fatalf("creator %v", creator)
	}
	entries, ok := log["entries"].([]any)
	if !ok {
		c.t.Fatalf("entries: %v", log["entries"])
	}
	var result []map[string]any
	for i, raw := range entries {
		path := fmt.Sprintf("entries[%d]", i)
		entry := c.object("entry", raw, path)
		c.require(entry, path, "startedDateTime", "time", "request", "response", "cache", "timings")
		if !harTime.MatchString(c.str(entry, "startedDateTime", path)) {
			c.t.Fatalf("%s startedDateTime %q", path, entry["startedDateTime"])
		}
		if c.num(entry, "time", path) < 0 {
			c.t.Fatalf("%s time negative", path)
		}
		c.str(entry, "serverIPAddress", path)
		c.str(entry, "connection", path)

		request := c.object("request", entry["request"], path+".request")
		rp := path + ".request"
		c.require(request, rp, "method", "url", "httpVersion", "cookies", "headers", "queryString", "headersSize", "bodySize")
		c.str(request, "method", rp)
		c.str(request, "url", rp)
		c.str(request, "httpVersion", rp)
		c.num(request, "headersSize", rp)
		c.num(request, "bodySize", rp)
		c.pairs("cookie", request["cookies"], rp+".cookies")
		c.pairs("pair", request["headers"], rp+".headers")
		c.pairs("pair", request["queryString"], rp+".queryString")
		if post, ok := request["postData"]; ok {
			pm := c.object("postData", post, rp+".postData")
			c.require(pm, rp+".postData", "mimeType")
			c.str(pm, "mimeType", rp+".postData")
			_, hasText := pm["text"]
			_, hasParams := pm["params"]
			if hasText == hasParams {
				c.t.Fatalf("%s.postData needs exactly one of text and params", rp)
			}
		}

		response := c.object("response", entry["response"], path+".response")
		sp := path + ".response"
		c.require(response, sp, "status", "statusText", "httpVersion", "cookies", "headers", "content", "redirectURL", "headersSize", "bodySize")
		if status := c.num(response, "status", sp); status != float64(int(status)) {
			c.t.Fatalf("%s status %v", sp, status)
		}
		c.str(response, "statusText", sp)
		c.str(response, "httpVersion", sp)
		c.str(response, "redirectURL", sp)
		c.num(response, "headersSize", sp)
		c.num(response, "bodySize", sp)
		c.pairs("cookie", response["cookies"], sp+".cookies")
		c.pairs("pair", response["headers"], sp+".headers")
		content := c.object("content", response["content"], sp+".content")
		c.require(content, sp+".content", "size", "mimeType")
		if c.num(content, "size", sp+".content") < 0 {
			c.t.Fatalf("%s content size negative", sp)
		}
		c.str(content, "mimeType", sp+".content")
		if _, ok := content["text"]; ok {
			c.str(content, "text", sp+".content")
		}
		if _, ok := content["encoding"]; ok && content["encoding"] != "base64" {
			c.t.Fatalf("%s encoding %v", sp, content["encoding"])
		}

		if _, ok := entry["cache"].(map[string]any); !ok {
			c.t.Fatalf("%s cache", path)
		}
		timings := c.object("timings", entry["timings"], path+".timings")
		c.require(timings, path+".timings", "send", "wait", "receive")
		for _, key := range []string{"send", "wait", "receive"} {
			if c.num(timings, key, path+".timings") < 0 {
				c.t.Fatalf("%s timings.%s negative", path, key)
			}
		}
		for _, key := range []string{"blocked", "dns", "connect", "ssl"} {
			if v, ok := timings[key]; ok {
				if n := c.num(timings, key, path+".timings"); n < 0 && n != -1 {
					c.t.Fatalf("%s timings.%s = %v", path, key, v)
				}
			}
		}
		result = append(result, entry)
	}
	return result
}

func exportHAR(t *testing.T, fake *fakeHAR, body string) (*httptest.ResponseRecorder, []map[string]any) {
	t.Helper()
	s := startHTTPCaptureServer(t, fake)
	w := httptest.NewRecorder()
	s.exportHTTPHAR(w, httptest.NewRequest("POST", "/api/v1/http/har", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("export: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/json" || !regexp.MustCompile(`^attachment; filename="kiko-\d{8}-\d{6}\.har"$`).MatchString(w.Header().Get("Content-Disposition")) {
		t.Fatalf("headers: %v", w.Header())
	}
	return w, harChecker{t}.validate(w.Body.Bytes())
}

func entryByID(entries []map[string]any, id string) map[string]any {
	for _, entry := range entries {
		if p, _ := entry["_kiko"].(map[string]any); p != nil && p["id"] == id {
			return entry
		}
	}
	return nil
}

func TestHAREntriesStructure(t *testing.T) {
	_, entries := exportHAR(t, harRecords(), `{}`)
	if len(entries) != 4 {
		t.Fatalf("entries: %d", len(entries))
	}
	var ids []string
	for _, entry := range entries {
		ids = append(ids, entry["_kiko"].(map[string]any)["id"].(string))
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatalf("not oldest first: %v", ids)
	}

	post := entryByID(entries, "3")
	request := post["request"].(map[string]any)
	response := post["response"].(map[string]any)
	if request["method"] != "POST" || request["httpVersion"] != "h2" || len(request["queryString"].([]any)) != 3 {
		t.Fatalf("request: %v", request)
	}
	pd := request["postData"].(map[string]any)
	if pd["mimeType"] != "application/json" || pd["text"] != `{"p":"é"}` {
		t.Fatalf("postData: %v", pd)
	}
	content := response["content"].(map[string]any)
	if content["text"] != `{"ok":"héllo 世界 <b>"}` || content["size"] != float64(25) || content["mimeType"] != "application/json; charset=utf-8" || content["encoding"] != nil {
		t.Fatalf("content: %v", content)
	}
	if response["redirectURL"] != "https://api.example.com/next?code=1&access_token=t" || response["statusText"] != "Found" {
		t.Fatalf("response: %v", response)
	}
	cookies := response["cookies"].([]any)[0].(map[string]any)
	if cookies["name"] != "sid" || cookies["value"] != "222" || cookies["httpOnly"] != true || cookies["secure"] != true || cookies["path"] != "/" {
		t.Fatalf("cookies: %v", cookies)
	}
	if post["serverIPAddress"] != "93.184.216.34" || post["connection"] != "conn-3" {
		t.Fatalf("connection: %v", post)
	}

	// Setup durations precede the record start; connect carries ssl.
	timings := post["timings"].(map[string]any)
	if timings["dns"] != 5.0 || timings["connect"] != 50.0 || timings["ssl"] != 30.0 || timings["blocked"] != -1.0 {
		t.Fatalf("timings: %v", timings)
	}
	if timings["send"] != 2.0 || timings["wait"] != 50.0 || timings["receive"] != 9.0 {
		t.Fatalf("phases: %v", timings)
	}
	if post["startedDateTime"] != "2026-11-03T09:59:59.945Z" || post["time"] != 116.0 {
		t.Fatalf("start/time: %v %v", post["startedDateTime"], post["time"])
	}

	png := entryByID(entries, "2")["response"].(map[string]any)["content"].(map[string]any)
	decoded, err := base64.StdEncoding.DecodeString(png["text"].(string))
	if err != nil || png["encoding"] != "base64" || string(decoded) != "\x89PNG\x00\x01\x02\x03" {
		t.Fatalf("binary content: %v %v", png, err)
	}
	pngTimings := entryByID(entries, "2")["timings"].(map[string]any)
	if pngTimings["dns"] != -1.0 || pngTimings["connect"] != -1.0 || pngTimings["ssl"] != -1.0 {
		t.Fatalf("unknown setup: %v", pngTimings)
	}

	failed := entryByID(entries, "4")
	if failed["response"].(map[string]any)["status"] != 0.0 || failed["_kiko"].(map[string]any)["state"] != "failed" || failed["time"] != 0.0 {
		t.Fatalf("failed: %v", failed)
	}

	cut := entryByID(entries, "5")["response"].(map[string]any)["content"].(map[string]any)
	if cut["text"] != "<html>世界" || !strings.HasPrefix(cut["comment"].(string), "truncated:") || cut["size"] != 100.0 {
		t.Fatalf("truncated: %v", cut)
	}
}

func TestHARSelectionAndFilter(t *testing.T) {
	_, entries := exportHAR(t, harRecords(), `{"ids":["5","3","99","3"]}`)
	if len(entries) != 2 || entries[0]["_kiko"].(map[string]any)["id"] != "3" || entries[1]["_kiko"].(map[string]any)["id"] != "5" {
		t.Fatalf("ids: %v", entries)
	}
	_, entries = exportHAR(t, harRecords(), `{"filter":{"host":"www.example.com"}}`)
	if len(entries) != 2 {
		t.Fatalf("filter: %d", len(entries))
	}
	_, entries = exportHAR(t, &fakeHAR{}, ``)
	if len(entries) != 0 {
		t.Fatalf("empty: %v", entries)
	}
}

func TestHARPaginatesBeyondOnePage(t *testing.T) {
	fake := &fakeHAR{}
	for i := 1; i <= 1203; i++ {
		fake.records = append(fake.records, &mitmapi.HTTPRecord{Id: fmt.Sprint(i), Instance: "inst-1", Method: "GET", Scheme: "https", Host: "h", Url: "https://h/", Proto: "http/1.1", Status: 200})
	}
	_, entries := exportHAR(t, fake, `{"bodies":false}`)
	if len(entries) != 1203 {
		t.Fatalf("entries: %d", len(entries))
	}
}

func TestHARSanitize(t *testing.T) {
	body := `{"sanitize":{"auth_headers":true,"cookies":true,"query_secrets":true,"bodies":true}}`
	w, entries := exportHAR(t, harRecords(), body)
	text := w.Body.String()
	for _, secret := range []string{"Bearer secret", "sid=111", "sid=222", "abc", "zzz", "access_token=t\"", `"k"`} {
		if strings.Contains(text, secret) {
			t.Fatalf("%q leaked:\n%s", secret, text)
		}
	}
	post := entryByID(entries, "3")
	request := post["request"].(map[string]any)
	headers := map[string]string{}
	for _, h := range request["headers"].([]any) {
		pair := h.(map[string]any)
		headers[pair["name"].(string)] = pair["value"].(string)
	}
	if headers["Authorization"] != "[redacted]" || headers["X-Api-Key"] != "[redacted]" || headers["Cookie"] != "sid=[redacted]; theme=[redacted]" || headers["Content-Type"] != "application/json" {
		t.Fatalf("headers: %v", headers)
	}
	if !strings.Contains(request["url"].(string), "user=bob") || strings.Contains(request["url"].(string), "token=abc") {
		t.Fatalf("url: %v", request["url"])
	}
	for _, q := range request["queryString"].([]any) {
		pair := q.(map[string]any)
		if (pair["name"] == "token" || pair["name"] == "Api_Key") && pair["value"] != "[redacted]" || pair["name"] == "user" && pair["value"] != "bob" {
			t.Fatalf("query: %v", pair)
		}
	}
	for _, c := range request["cookies"].([]any) {
		if c.(map[string]any)["value"] != "[redacted]" {
			t.Fatalf("cookie: %v", c)
		}
	}
	response := post["response"].(map[string]any)
	if response["cookies"].([]any)[0].(map[string]any)["value"] != "[redacted]" {
		t.Fatalf("set-cookie: %v", response["cookies"])
	}
	content := response["content"].(map[string]any)
	if content["text"] != "" || content["size"] != 20.0 {
		t.Fatalf("bodies kept: %v", content)
	}
	if request["postData"].(map[string]any)["text"] != "" {
		t.Fatalf("request body kept: %v", request["postData"])
	}
}

func TestHARWithoutSanitizeKeepsValues(t *testing.T) {
	w, _ := exportHAR(t, harRecords(), `{"bodies":false}`)
	text := w.Body.String()
	for _, kept := range []string{"Bearer secret", "token=abc"} {
		if !strings.Contains(text, kept) {
			t.Fatalf("%q missing", kept)
		}
	}
	if strings.Contains(text, "héllo") {
		t.Fatal("bodies exported although switched off")
	}
}

func TestHARBodyMissingKeepsEntry(t *testing.T) {
	fake := harRecords()
	delete(fake.bodies, "3:BODY_SIDE_RESPONSE")
	_, entries := exportHAR(t, fake, `{}`)
	content := entryByID(entries, "3")["response"].(map[string]any)["content"].(map[string]any)
	if content["text"] != "" || content["comment"] != "body unavailable" || content["size"] != 20.0 {
		t.Fatalf("missing body: %v", content)
	}
}

func TestHARRejectsTooManyIDs(t *testing.T) {
	s := startHTTPCaptureServer(t, harRecords())
	ids := make([]string, harMaxRecords+1)
	for i := range ids {
		ids[i] = fmt.Sprint(i)
	}
	data, _ := json.Marshal(map[string]any{"ids": ids})
	w := httptest.NewRecorder()
	s.exportHTTPHAR(w, httptest.NewRequest("POST", "/api/v1/http/har", strings.NewReader(string(data))))
	if w.Code != 400 {
		t.Fatalf("too many: %d", w.Code)
	}
	w = httptest.NewRecorder()
	newGRPCTestServer(t).exportHTTPHAR(w, httptest.NewRequest("POST", "/api/v1/http/har", strings.NewReader(`{}`)))
	if w.Code != 503 {
		t.Fatalf("no core: %d", w.Code)
	}
}

func TestHARRedactionHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"https://x/p?a=1&token=2&b=3#f":         "https://x/p?a=1&token=%5Bredacted%5D&b=3#f",
		"https://x/p?SIGNATURE=2&flag":          "https://x/p?SIGNATURE=%5Bredacted%5D&flag",
		"https://x/p":                           "https://x/p",
		"https://x/cb#access_token=abc&state=1": "https://x/cb#access_token=%5Bredacted%5D&state=1",
		"https://u:pw@x:8080/p?a=1":             "https://[redacted]@x:8080/p?a=1",
		"https://x/p?next=a@b":                  "https://x/p?next=a@b",
		"https://x/p?monkey=1&nokey%5B0%5D=":    "https://x/p?monkey=%5Bredacted%5D&nokey%5B0%5D=%5Bredacted%5D",
	} {
		if got := redactURL(in); got != want {
			t.Fatalf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
	headers := harOptions{sanitize: harSanitizeJSON{QuerySecrets: true}}.headers([]*mitmapi.HTTPHeader{
		{Name: "Referer", Value: "https://x/p?token=1#access_token=2"},
		{Name: "Location", Value: "https://x/cb#id_token=3"},
	})
	if headers[0].Value != "https://x/p?token=%5Bredacted%5D#access_token=%5Bredacted%5D" || headers[1].Value != "https://x/cb#id_token=%5Bredacted%5D" {
		t.Fatalf("headers: %+v", headers)
	}
	if got := redactSetCookie("a=b; Path=/; Secure"); got != "a=[redacted]; Path=/; Secure" {
		t.Fatalf("set-cookie: %q", got)
	}
	if got := redactCookieHeader("a=1;b=2"); got != "a=[redacted]; b=[redacted]" {
		t.Fatalf("cookie: %q", got)
	}
	if got := redactCookieHeader("token123; a=1"); got != "[redacted]; a=[redacted]" {
		t.Fatalf("cookie without name: %q", got)
	}
	if got := redactSetCookie("token123; Path=/"); got != "[redacted]; Path=/" {
		t.Fatalf("set-cookie without name: %q", got)
	}
}

func TestHARSplitRune(t *testing.T) {
	data := []byte("a世")
	if harSplitRune(data) != 4 || harSplitRune(data[:3]) != 1 || harSplitRune(data[:2]) != 1 || harSplitRune(data[:1]) != 1 {
		t.Fatalf("split: %d %d %d", harSplitRune(data[:3]), harSplitRune(data[:2]), harSplitRune(data[:1]))
	}
}

func TestHARAuthHeadersRedactURLUserInfo(t *testing.T) {
	fake := harRecords()
	for _, record := range fake.records {
		if record.Id == "3" {
			record.Url = "https://user:pw@api.example.com/v1/login?token=abc"
		}
	}
	_, entries := exportHAR(t, fake, `{"bodies":false,"sanitize":{"auth_headers":true}}`)
	request := entryByID(entries, "3")["request"].(map[string]any)
	if request["url"] != "https://[redacted]@api.example.com/v1/login?token=abc" {
		t.Fatalf("url: %v", request["url"])
	}
	w, _ := exportHAR(t, fake, `{"bodies":false,"sanitize":{"cookies":true}}`)
	if !strings.Contains(w.Body.String(), "user:pw@") {
		t.Fatal("user information redacted without auth_headers")
	}
}

func TestHARBodyLimitBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		size      int
		truncated bool
	}{{"exact", harBodyLimit, false}, {"over", harBodyLimit + 1, true}} {
		t.Run(tc.name, func(t *testing.T) {
			fake := harRecords()
			fake.chunk = 1 << 20
			data := []byte(strings.Repeat("a", tc.size))
			fake.bodies["5:BODY_SIDE_RESPONSE"] = &mitmapi.BodyChunk{Data: data, Total: int64(tc.size), OriginalSize: int64(tc.size)}
			_, entries := exportHAR(t, fake, `{}`)
			content := entryByID(entries, "5")["response"].(map[string]any)["content"].(map[string]any)
			comment, _ := content["comment"].(string)
			if tc.truncated != strings.HasPrefix(comment, "truncated") {
				t.Fatalf("comment %q", comment)
			}
			if len(content["text"].(string)) != harBodyLimit {
				t.Fatalf("text length %d", len(content["text"].(string)))
			}
		})
	}
}
