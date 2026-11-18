package connections

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

func hrec(id uint64, host string, status int, state string) api.HTTPRecord {
	return api.HTTPRecord{
		ID: fmt.Sprint(id), Seq: api.Uint64(id), CreatedSeq: api.Uint64(id), ConnectionID: "c1",
		Method: "GET", Host: host, Path: "/p", URL: "https://" + host + "/p", Proto: "h2", Status: status, State: state,
		Connection:      &api.HTTPConnection{ID: "c1", Source: "172.18.0.1:5000", Outbound: "直连", ProcessName: "curl"},
		Timing:          &api.HTTPTiming{Start: 1_000_000_000, End: 1_250_000_000},
		ResponseHeaders: []api.HTTPHeader{{Name: "content-type", Value: "Application/JSON; charset=utf-8"}},
	}
}

func frame(reset bool, ids ...uint64) api.HTTPFrame {
	f := api.HTTPFrame{Instance: "i", Reset: reset}
	for _, id := range ids {
		r := hrec(id, "a.com", 200, api.HTTPStateComplete)
		f.Events = append(f.Events, api.HTTPEvent{Kind: api.HTTPEventNew, Record: &r})
	}
	return f
}

func ids(s *httpStore) []string {
	var out []string
	for _, r := range s.rows {
		out = append(out, r.ID)
	}
	return out
}

func TestHTTPStoreUpsert(t *testing.T) {
	s := newHTTPStore()
	live := hrec(5, "a.com", 0, api.HTTPStateInProgress)
	s.apply(api.HTTPFrame{Instance: "i", Events: []api.HTTPEvent{{Kind: "new", Record: &live}}})
	if s.live != 1 || s.counts[hfStatus][httpClassLive] != 1 {
		t.Fatalf("live %d %v", s.live, s.counts)
	}
	done := hrec(5, "a.com", 503, api.HTTPStateComplete)
	done.Seq = 9
	s.apply(api.HTTPFrame{Instance: "i", Events: []api.HTTPEvent{{Kind: "done", Record: &done}}})
	if len(s.rows) != 1 || s.live != 0 || s.errors != 1 || s.counts[hfStatus]["5xx"] != 1 || s.counts[hfStatus][httpClassLive] != 0 {
		t.Fatalf("done: rows %d live %d errors %d %v", len(s.rows), s.live, s.errors, s.counts[hfStatus])
	}
	// An older revision, delivered late, changes nothing.
	s.apply(api.HTTPFrame{Instance: "i", Events: []api.HTTPEvent{{Kind: "update", Record: &live}}})
	if s.byID["5"].Status != 503 || s.live != 0 {
		t.Fatalf("stale update applied: %+v", s.byID["5"].Status)
	}
	if s.added != 1 {
		t.Fatalf("added %d", s.added)
	}
}

func TestHTTPStoreOrderAndResend(t *testing.T) {
	s := newHTTPStore()
	s.apply(frame(false, 3, 4))
	s.apply(frame(false, 1, 2, 3, 4, 5))
	if got := ids(s); !slices.Equal(got, []string{"1", "2", "3", "4", "5"}) {
		t.Fatalf("order %v", got)
	}
	if s.counts[hfHost]["a.com"] != 5 || s.added != 5 {
		t.Fatalf("counts %v added %d", s.counts[hfHost], s.added)
	}
}

// A reset replaces the records; a split reset's later batches, which
// carry no reset flag, and a resend after reconnecting add by ID.
func TestHTTPStoreReset(t *testing.T) {
	s := newHTTPStore()
	s.apply(frame(false, 1, 2, 3))
	epoch := s.epoch
	first := frame(true, 8, 9)
	first.Gap = true
	s.apply(first)
	s.apply(frame(false, 7))
	s.apply(frame(false, 7, 8))
	if got := ids(s); !slices.Equal(got, []string{"7", "8", "9"}) || !s.gap || s.epoch == epoch {
		t.Fatalf("reset %v gap %v", got, s.gap)
	}
	if s.counts[hfHost]["a.com"] != 3 {
		t.Fatalf("counts %v", s.counts)
	}
	// Another instance replaces them too.
	other := frame(false, 1)
	other.Instance = "j"
	s.apply(other)
	if got := ids(s); !slices.Equal(got, []string{"1"}) || s.gap || s.instance != "j" {
		t.Fatalf("instance %v", got)
	}
}

func TestHTTPStoreTrimKeepsLiveAndSelected(t *testing.T) {
	s := newHTTPStore()
	live := hrec(1, "a.com", 0, api.HTTPStateInProgress)
	s.apply(api.HTTPFrame{Instance: "i", Events: []api.HTTPEvent{{Kind: "new", Record: &live}}})
	s.apply(frame(false, 2, 3, 4, 5, 6))
	dropped := s.trim(3, func(r *httpRow) bool { return r.ID == "2" })
	if got := ids(s); dropped != 3 || !slices.Equal(got, []string{"1", "2", "6"}) {
		t.Fatalf("trim dropped %d, left %v", dropped, got)
	}
	if s.counts[hfHost]["a.com"] != 3 || s.evicted != 3 || s.byID["3"] != nil {
		t.Fatalf("counters %v evicted %d", s.counts, s.evicted)
	}
}

func TestHTTPStoreDropFinishedAndMerge(t *testing.T) {
	s := newHTTPStore()
	live := hrec(1, "a.com", 0, api.HTTPStateInProgress)
	s.apply(api.HTTPFrame{Instance: "i", Events: []api.HTTPEvent{{Kind: "new", Record: &live}}})
	s.apply(frame(false, 2, 3))
	s.dropFinished()
	if got := ids(s); !slices.Equal(got, []string{"1"}) || s.errors != 0 {
		t.Fatalf("drop %v", got)
	}
	// Older pages join; a short page ends the history; a page read before
	// a reset is dropped.
	epoch := s.epoch
	s.apply(frame(false, 10))
	older := api.HTTPList{Instance: "i", Records: []api.HTTPRecord{hrec(9, "b.com", 200, "complete"), hrec(8, "b.com", 200, "complete"), hrec(7, "b.com", 200, "complete")}}
	if n := s.mergeList(older, epoch, 500, 2); n != 2 || !s.exhausted {
		t.Fatalf("merge %d exhausted %v", n, s.exhausted)
	}
	if got := ids(s); !slices.Equal(got, []string{"1", "8", "9", "10"}) {
		t.Fatalf("after merge %v", got)
	}
	s.apply(frame(true, 20))
	if n := s.mergeList(older, epoch, 500, 10); n != 0 || len(s.rows) != 1 {
		t.Fatalf("stale page merged: %d %v", n, ids(s))
	}
}

func TestHTTPClassAndFlags(t *testing.T) {
	for _, c := range []struct {
		status int
		state  string
		want   string
	}{{200, "complete", "2xx"}, {101, "upgraded", "1xx"}, {404, "complete", "4xx"}, {502, "complete", "5xx"}, {0, "failed", "失败"}, {0, "complete", "失败"}, {0, "in_progress", "进行中"}, {200, "in_progress", "进行中"}} {
		r := hrec(1, "a.com", c.status, c.state)
		if got := httpClass(&r); got != c.want {
			t.Errorf("%d %s: %q", c.status, c.state, got)
		}
	}
	r := hrec(1, "a.com", 200, "complete")
	r.Unverified, r.Rewritten, r.Error = true, true, "reset"
	var labels []string
	for _, f := range httpFlags(newHTTPRow(r)) {
		labels = append(labels, f.label)
	}
	if !slices.Equal(labels, []string{"错误", "未验证", "已改写"}) {
		t.Fatalf("flags %v", labels)
	}
	row := newHTTPRow(hrec(1, "a.com", 200, "complete"))
	if row.ctype != "application/json" || row.total != 250*time.Millisecond || row.process != "curl" || row.source != "172.18.0.1" {
		t.Fatalf("row %q %v %q %q", row.ctype, row.total, row.process, row.source)
	}
}

func TestHTTPFilter(t *testing.T) {
	a := newHTTPRow(hrec(1, "a.com", 200, "complete"))
	b := newHTTPRow(hrec(2, "b.com", 500, "complete"))
	b.ConnectionID = "c2"
	live := newHTTPRow(hrec(3, "a.com", 0, "in_progress"))
	for _, c := range []struct {
		f    httpFilter
		want []bool
	}{
		{httpFilter{}, []bool{true, true, true}},
		{httpFilter{mode: httpModeErrors}, []bool{false, true, false}},
		{httpFilter{mode: httpModeLive}, []bool{false, false, true}},
		{httpFilter{conn: "c2"}, []bool{false, true, false}},
		{httpFilter{search: "b.com"}, []bool{false, true, false}},
		{httpFilter{search: "curl"}, []bool{true, true, true}},
		{httpFilter{facets: kit.FacetChoice[httpFacet]{hfHost: {"a.com"}, hfStatus: {"2xx"}}}, []bool{true, false, false}},
	} {
		got := []bool{c.f.match(a), c.f.match(b), c.f.match(live)}
		if !slices.Equal(got, c.want) {
			t.Errorf("%+v: %v, want %v", c.f, got, c.want)
		}
	}
}

func TestHTTPOptionsOrder(t *testing.T) {
	status := httpOptions(hfStatus, map[string]int{"5xx": 9, "2xx": 1, "失败": 3, "进行中": 1})
	var got []string
	for _, o := range status {
		got = append(got, o.value)
	}
	if !slices.Equal(got, []string{"进行中", "2xx", "5xx", "失败"}) {
		t.Fatalf("status %v", got)
	}
	hosts := httpOptions(hfHost, map[string]int{"a": 1, "b": 5, "c": 5})
	if hosts[0].value != "b" || hosts[1].value != "c" || hosts[2].value != "a" {
		t.Fatalf("hosts %v", hosts)
	}
}

func TestHTTPDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{-1: "—", 400 * time.Microsecond: "0.4 ms", 123 * time.Millisecond: "123 ms", 1230 * time.Millisecond: "1.23 s", 75 * time.Second: "1m15s"} {
		if got := httpDuration(d); got != want {
			t.Errorf("%v: %q", d, got)
		}
	}
}

func TestHTTPCurlRedaction(t *testing.T) {
	r := api.HTTPRecord{
		Method: "POST", URL: "https://api.example.com/v1/items?a=1&b='x'",
		RequestHeaders: []api.HTTPHeader{
			{Name: ":authority", Value: "api.example.com"}, {Name: "Host", Value: "api.example.com"}, {Name: "Content-Length", Value: "9"},
			{Name: "Authorization", Value: "Bearer secret-abc"}, {Name: "Cookie", Value: "sid=1"}, {Name: "Proxy-Authorization", Value: "Basic zzz"},
			{Name: "X-Api-Key", Value: "k-123"}, {Name: "X-Auth-Token", Value: "t-123"}, {Name: "X-Client-Secret", Value: "s-123"},
			{Name: "Set-Cookie", Value: "leak=1"}, {Name: "Content-Type", Value: "application/json"}, {Name: "Accept-Encoding", Value: "gzip"},
			{Name: "User-Agent", Value: "it's me"},
		},
	}
	got := httpCurl(&r, []byte(`{"a":"it's"}`))
	for _, secret := range []string{"secret-abc", "sid=1", "zzz", "k-123", "t-123", "s-123", "leak"} {
		if strings.Contains(got, secret) {
			t.Errorf("%q leaked in\n%s", secret, got)
		}
	}
	for _, want := range []string{`curl 'https://api.example.com/v1/items?a=1&b='\''x'\'''`, "-X 'POST'", "-H 'Authorization: <redacted>'", "-H 'Cookie: <redacted>'",
		"-H 'X-Api-Key: <redacted>'", "-H 'Content-Type: application/json'", "-H 'User-Agent: it'\\''s me'", "--compressed", `--data-raw '{"a":"it'\''s"}'`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	for _, absent := range []string{":authority", "Content-Length", "Host:", "Accept-Encoding"} {
		if strings.Contains(got, absent) {
			t.Errorf("%q kept in\n%s", absent, got)
		}
	}
	get := httpCurl(&api.HTTPRecord{Method: "GET", URL: "https://a.com/"}, nil)
	if get != "curl 'https://a.com/'" {
		t.Errorf("get %q", get)
	}
	if head := httpCurl(&api.HTTPRecord{Method: "HEAD", URL: "https://a.com/"}, nil); !strings.Contains(head, "-I") {
		t.Errorf("head %q", head)
	}
	// A binary body is not put on the command line.
	if bin := httpCurl(&api.HTTPRecord{Method: "PUT", URL: "https://a.com/"}, []byte{0xff, 0xfe}); strings.Contains(bin, "--data-raw") {
		t.Errorf("binary %q", bin)
	}
}

func TestHTTPBodyViewKind(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n....")
	for _, c := range []struct {
		name, ctype string
		data        []byte
		want        httpBodyKind
	}{
		{"empty", "text/plain", nil, bodyEmpty},
		{"json", "application/json; charset=utf-8", []byte(`{"a":[1,2]}`), bodyJSON},
		{"json suffix", "application/vnd.api+json", []byte(`[1]`), bodyJSON},
		{"json without type", "", []byte(` {"a":1}`[1:]), bodyJSON},
		{"truncated json", "application/json", []byte(`{"a":[1,`), bodyText},
		{"html", "text/html", []byte("<p>héllo</p>"), bodyText},
		{"form", "application/x-www-form-urlencoded", []byte("a=1&b=2"), bodyText},
		{"truncated utf8", "text/plain", append([]byte("日本"), 0xe6, 0x97), bodyText},
		{"png", "image/png", png, bodyImage},
		{"png without type", "", png, bodyImage},
		{"fake image", "image/png", []byte("not an image"), bodyHex},
		{"svg is text", "image/svg+xml", []byte("<svg/>"), bodyText},
		{"gzip", "application/octet-stream", []byte{0x1f, 0x8b, 0x08, 0x00, 0x00}, bodyHex},
		{"binary", "application/pdf", []byte("%PDF-1.7"), bodyHex},
		{"sniffed text", "", []byte("plain words\nhere"), bodyText},
		{"invalid utf8 text", "text/plain", []byte{0xff, 0xfe, 'a'}, bodyHex},
	} {
		if got := httpBodyViewKind(c.ctype, c.data); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHexDump(t *testing.T) {
	got := strings.Split(hexDump([]byte("ABCDEFGHIJKLMNOPQ\x00"), 64), "\n")
	if len(got) != 2 || got[0] != "00000000  41 42 43 44 45 46 47 48  49 4a 4b 4c 4d 4e 4f 50  |ABCDEFGHIJKLMNOP|" {
		t.Fatalf("lines %q", got)
	}
	if !strings.HasPrefix(got[1], "00000010  51 00 ") || !strings.HasSuffix(got[1], "|Q.|") || strings.Index(got[1], "|") != strings.Index(got[0], "|") {
		t.Fatalf("second line %q", got[1])
	}
	if limited := hexDump(make([]byte, 100), 16); strings.Count(limited, "\n") != 0 {
		t.Fatalf("limit: %q", limited)
	}
}

// A thousand events per second for a minute keep the store within its
// limit, with counters that match the rows.
func TestHTTPStoreHighRate(t *testing.T) {
	s := newHTTPStore()
	start := time.Now()
	id := uint64(0)
	for batch := 0; batch < 60; batch++ {
		var events []api.HTTPEvent
		for i := 0; i < 1000; i++ {
			id++
			r := hrec(id, fmt.Sprintf("h%d.com", id%50), 200, api.HTTPStateComplete)
			events = append(events, api.HTTPEvent{Kind: "new", Record: &r})
		}
		s.apply(api.HTTPFrame{Instance: "i", Events: events})
		s.trim(httpLimit, nil)
	}
	if len(s.rows) != httpLimit || len(s.byID) != httpLimit {
		t.Fatalf("rows %d byID %d", len(s.rows), len(s.byID))
	}
	total := 0
	for _, n := range s.counts[hfHost] {
		total += n
	}
	if total != httpLimit || s.evicted != 60000-httpLimit {
		t.Fatalf("counted %d evicted %d", total, s.evicted)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("60000 events took %v", elapsed)
	}
}
