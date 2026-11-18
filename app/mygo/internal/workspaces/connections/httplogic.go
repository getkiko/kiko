package connections

import (
	"encoding/json"
	"fmt"
	"mime"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// This file is the pure logic of the HTTP Requests monitor: the record store
// the stream feeds, and the labels, timing phases, cURL command and body
// kinds the views show.

// httpLimit is how many records the console keeps; the oldest finished
// ones go first.
const httpLimit = 5000

// httpPageLimit is how many records one "Load earlier" reads.
const httpPageLimit = 500

// Status classes, the Status Code filter's values.
const (
	httpClassLive   = "进行中"
	httpClassFailed = "失败"
)

// httpClassOrder lists the status classes in the order the filter shows.
var httpClassOrder = []string{httpClassLive, "1xx", "2xx", "3xx", "4xx", "5xx", httpClassFailed}

// httpFacet is a filter dimension of the HTTP monitor.
type httpFacet string

const (
	hfHost    httpFacet = "host"
	hfMethod  httpFacet = "method"
	hfStatus  httpFacet = "status"
	hfType    httpFacet = "type"
	hfProcess httpFacet = "process"
	hfSource  httpFacet = "source"
)

var httpFacets = []struct {
	key   httpFacet
	label string
}{
	{hfHost, "主机"}, {hfMethod, "方法"}, {hfStatus, "状态码"}, {hfType, "内容类型"}, {hfProcess, "进程"}, {hfSource, "来源"},
}

// httpRow is a record as the monitor keeps it, with the fields the list
// filters and sorts by. A row is never changed once stored: an update
// replaces it, so a paused list keeps showing what it showed.
type httpRow struct {
	api.HTTPRecord
	// num is the numeric record ID, key the order records are created in.
	num, key uint64
	class    string
	ctype    string
	process  string
	source   string
	outbound string
	// hay is the lowercase text the search looks in.
	hay string
	// start is the Unix nanoseconds the exchange began, 0 when unknown.
	start int64
	// total is the exchange's duration, -1 while unknown.
	total time.Duration
}

func newHTTPRow(rec api.HTTPRecord) *httpRow {
	r := &httpRow{HTTPRecord: rec, total: -1}
	r.num, _ = strconv.ParseUint(rec.ID, 10, 64)
	r.key = uint64(rec.CreatedSeq)
	if r.key == 0 {
		r.key = r.num
	}
	r.class = httpClass(&rec)
	r.ctype = httpContentType(rec.ResponseHeaders)
	if conn := rec.Connection; conn != nil {
		r.process = conn.ProcessName
		r.source = sourceHost(conn.Source)
		r.outbound = conn.Outbound
	}
	if t := rec.Timing; t != nil {
		r.start = t.Start
		if t.Start > 0 && t.End >= t.Start {
			r.total = time.Duration(t.End - t.Start)
		}
	}
	r.hay = lower(strings.Join([]string{rec.Method, rec.URL, strconv.Itoa(rec.Status), r.process, r.ctype, r.outbound}, " "))
	return r
}

// replayed reports a record made by a replay: its connection came in
// through the core's internal inbound.
func (r *httpRow) replayed() bool {
	return r.Connection != nil && r.Connection.Inbound == api.ReplayInbound
}

func (r *httpRow) live() bool { return r.State == api.HTTPStateInProgress }

// problem reports a failed exchange or an error status.
func (r *httpRow) problem() bool {
	return r.class == httpClassFailed || r.Error != "" || r.Status >= 400
}

func (r *httpRow) less(o *httpRow) bool {
	return r.key < o.key || r.key == o.key && r.num < o.num
}

// httpClass is the status class of a record: "In progress", "Failed" (no
// response), or the code's hundreds as "2xx".
func httpClass(r *api.HTTPRecord) string {
	switch {
	case r.State == api.HTTPStateInProgress:
		return httpClassLive
	case r.State == api.HTTPStateFailed || r.Status <= 0:
		return httpClassFailed
	case r.Status < 100 || r.Status > 599:
		return httpClassFailed
	}
	return strconv.Itoa(r.Status/100) + "xx"
}

func httpClassTone(class string) kit.Tone {
	switch class {
	case httpClassLive:
		return kit.AccentTone
	case "2xx":
		return kit.SuccessTone
	case "4xx":
		return kit.WarningTone
	case "5xx", httpClassFailed:
		return kit.DangerTone
	}
	return kit.Neutral
}

// httpStatusText is the status column: the code, "—" while the exchange
// runs, "Failed" without a response.
func httpStatusText(r *httpRow) string {
	switch r.class {
	case httpClassLive:
		return "—"
	case httpClassFailed:
		return httpClassFailed
	}
	return strconv.Itoa(r.Status)
}

// httpHeader is the first value of the header name, any case.
func httpHeader(headers []api.HTTPHeader, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// httpContentType is the media type of a response's Content-Type, without
// parameters, lowercase.
func httpContentType(headers []api.HTTPHeader) string {
	value := httpHeader(headers, "Content-Type")
	if value == "" {
		return ""
	}
	if mt, _, err := mime.ParseMediaType(value); err == nil {
		return mt
	}
	mt, _, _ := strings.Cut(value, ";")
	return lower(trim(mt))
}

func httpFacetValue(r *httpRow, f httpFacet) string {
	switch f {
	case hfHost:
		return r.Host
	case hfMethod:
		return r.Method
	case hfStatus:
		return r.class
	case hfType:
		return r.ctype
	case hfProcess:
		return r.process
	}
	return r.source
}

// httpFlag is a marker of the Flags column.
type httpFlag struct {
	label string
	tone  kit.Tone
}

// httpFlags are the markers of a record.
func httpFlags(r *httpRow) []httpFlag {
	var flags []httpFlag
	if r.live() {
		flags = append(flags, httpFlag{"进行中", kit.AccentTone})
	}
	if r.Error != "" {
		flags = append(flags, httpFlag{"错误", kit.DangerTone})
	}
	if r.replayed() {
		flags = append(flags, httpFlag{"重放", kit.AccentTone})
	}
	if r.Unverified {
		flags = append(flags, httpFlag{"未验证", kit.WarningTone})
	}
	if r.Rewritten {
		flags = append(flags, httpFlag{"已改写", kit.AccentTone})
	}
	return flags
}

// httpStore is the records of one core instance, oldest first by creation.
type httpStore struct {
	instance string
	rows     []*httpRow
	byID     map[string]*httpRow
	counts   map[httpFacet]map[string]int
	errors   int
	live     int
	// gap: the last reset came from eviction or a clear.
	gap bool
	// quietGap: the next reset comes from a record this client deleted, so
	// it is no gap to report.
	quietGap bool
	// retained and total are the core's counters from the last frame.
	retained int
	total    api.Uint64
	// added counts the records new to the store; evicted those it dropped.
	added, evicted int
	// exhausted: there are no older records than the oldest kept.
	exhausted bool
	// extra holds the records read for one connection's list; they are
	// outside the page cursor: oldest skips them.
	extra map[string]bool
	// epoch changes on each reset, so that a page read before it is
	// dropped.
	epoch int
}

func newHTTPStore() *httpStore {
	return &httpStore{byID: map[string]*httpRow{}, counts: map[httpFacet]map[string]int{}}
}

func (s *httpStore) reset() {
	s.rows, s.byID = nil, map[string]*httpRow{}
	s.counts = map[httpFacet]map[string]int{}
	s.errors, s.live, s.gap, s.exhausted = 0, 0, false, false
	s.extra = nil
	s.epoch++
}

// account adds d (1 or -1) of a row to the counters.
func (s *httpStore) account(r *httpRow, d int) {
	for _, f := range httpFacets {
		v := httpFacetValue(r, f.key)
		if v == "" {
			continue
		}
		m := s.counts[f.key]
		if m == nil {
			m = map[string]int{}
			s.counts[f.key] = m
		}
		if m[v] += d; m[v] <= 0 {
			delete(m, v)
		}
	}
	if r.problem() {
		s.errors += d
	}
	if r.live() {
		s.live += d
	}
}

// find is the index where r sits or would be inserted.
func (s *httpStore) find(r *httpRow) int {
	return sort.Search(len(s.rows), func(i int) bool { return !s.rows[i].less(r) })
}

func (s *httpStore) insert(r *httpRow) {
	i := s.find(r)
	if i == len(s.rows) {
		s.rows = append(s.rows, r)
		return
	}
	s.rows = slices.Insert(s.rows, i, r)
}

func (s *httpStore) remove(r *httpRow) {
	i := s.find(r)
	if i >= len(s.rows) || s.rows[i] != r {
		if i = slices.Index(s.rows, r); i < 0 {
			return
		}
	}
	s.rows = slices.Delete(s.rows, i, i+1)
}

// forget drops a record the user deleted. The core answers with a reset;
// it is not reported as a gap.
func (s *httpStore) forget(id string) {
	r := s.byID[id]
	if r == nil {
		return
	}
	s.remove(r)
	delete(s.byID, id)
	delete(s.extra, id)
	s.account(r, -1)
	s.retained = max(0, s.retained-1)
	s.quietGap = true
}

// upsert stores a record by its ID: an older revision than the stored one
// is ignored. It reports whether the store changed.
func (s *httpStore) upsert(rec api.HTTPRecord) bool {
	if rec.ID == "" {
		return false
	}
	next := newHTTPRow(rec)
	old := s.byID[rec.ID]
	if old != nil {
		if next.Seq < old.Seq {
			return false
		}
		if i := s.find(old); old.key == next.key && i < len(s.rows) && s.rows[i] == old {
			s.rows[i] = next
		} else {
			s.remove(old)
			s.insert(next)
		}
		s.account(old, -1)
	} else {
		s.insert(next)
		s.added++
	}
	s.byID[rec.ID] = next
	s.account(next, 1)
	return true
}

// apply merges a stream frame: a reset or another instance replaces the
// records, every event is an upsert by record ID (a reconnect resends
// some). It reports whether the store changed.
func (s *httpStore) apply(f api.HTTPFrame) bool {
	changed := false
	if f.Reset || (s.instance != "" && s.instance != f.Instance) {
		s.reset()
		s.gap = f.Gap && !s.quietGap
		s.quietGap = false
		changed = true
	}
	s.instance = f.Instance
	for _, e := range f.Events {
		if e.Record != nil && s.upsert(*e.Record) {
			changed = true
		}
	}
	if st := f.Status; st != nil {
		s.retained, s.total = st.RecordCount, st.Total
	}
	return changed
}

// mergeList adds a page of records read for the store as of epoch: a page
// read before a reset is dropped, and at most room new records join.
// asked is the page size, a shorter page being the last. It returns how
// many records were new.
func (s *httpStore) mergeList(list api.HTTPList, epoch, asked, room int) int {
	if epoch != s.epoch || (s.instance != "" && list.Instance != s.instance) {
		return 0
	}
	if s.instance == "" {
		s.instance = list.Instance
	}
	n := 0
	for _, rec := range list.Records {
		if _, known := s.byID[rec.ID]; !known {
			if n >= room {
				continue
			}
			n++
		} else {
			// A record of the paging is no longer an extra.
			delete(s.extra, rec.ID)
		}
		s.upsert(rec)
	}
	if len(list.Records) < asked {
		s.exhausted = true
	}
	return n
}

// mergeConnection adds the records read for one connection as of epoch.
// They do not move the paging cursor (exhausted, oldest) of the
// whole list.
func (s *httpStore) mergeConnection(list api.HTTPList, epoch int) {
	if epoch != s.epoch || (s.instance != "" && list.Instance != s.instance) {
		return
	}
	if s.instance == "" {
		s.instance = list.Instance
	}
	for _, rec := range list.Records {
		if _, known := s.byID[rec.ID]; known {
			s.upsert(rec)
			continue
		}
		if s.extra == nil {
			s.extra = map[string]bool{}
		}
		s.extra[rec.ID] = true
		s.upsert(rec)
	}
}

// trim drops the oldest rows beyond limit, except those keep names.
func (s *httpStore) trim(limit int, keep func(*httpRow) bool) int {
	excess := len(s.rows) - limit
	if excess <= 0 {
		return 0
	}
	out := s.rows[:0]
	dropped := 0
	for _, r := range s.rows {
		if dropped < excess && !r.live() && (keep == nil || !keep(r)) {
			delete(s.byID, r.ID)
			delete(s.extra, r.ID)
			s.account(r, -1)
			dropped++
			continue
		}
		out = append(out, r)
	}
	clear(s.rows[len(out):])
	s.rows = out
	s.evicted += dropped
	return dropped
}

// dropFinished removes the finished rows, as the core's clear does.
func (s *httpStore) dropFinished() {
	out := s.rows[:0]
	for _, r := range s.rows {
		if r.live() {
			out = append(out, r)
			continue
		}
		delete(s.byID, r.ID)
		delete(s.extra, r.ID)
		s.account(r, -1)
	}
	clear(s.rows[len(out):])
	s.rows = out
	s.exhausted = false
	s.epoch++
}

// oldest is the row created first, nil without rows.
func (s *httpStore) oldest() *httpRow {
	for _, r := range s.rows {
		if !s.extra[r.ID] {
			return r
		}
	}
	return nil
}

// httpCounts counts the facet values, problems and running exchanges of
// rows, as a store keeps them for its own.
func httpCounts(rows []*httpRow) (counts map[httpFacet]map[string]int, problems, live int) {
	s := newHTTPStore()
	for _, r := range rows {
		s.account(r, 1)
	}
	return s.counts, s.errors, s.live
}

// httpOptions lists a facet's values, the busiest first; statuses keep
// their natural order.
func httpOptions(f httpFacet, counts map[string]int) []facetOption {
	options := make([]facetOption, 0, len(counts))
	for v, n := range counts {
		options = append(options, facetOption{v, n})
	}
	if f == hfStatus {
		slices.SortFunc(options, func(a, b facetOption) int {
			return slices.Index(httpClassOrder, a.value) - slices.Index(httpClassOrder, b.value)
		})
		return options
	}
	slices.SortFunc(options, func(a, b facetOption) int {
		if a.count != b.count {
			return b.count - a.count
		}
		return collateCompare(a.value, b.value)
	})
	return options
}

// httpFilter selects rows.
type httpFilter struct {
	facets kit.FacetChoice[httpFacet]
	search string
	conn   string
	// mode is httpModeAll, httpModeErrors or httpModeLive.
	mode int
}

// Scopes of the list.
const (
	httpModeAll = iota
	httpModeErrors
	httpModeLive
)

// httpModeGroup is the group of the list's scope among the conditions.
const httpModeGroup = "\x00mode"

// conditions are the limits of f, each filter group's under its facet
// and the scope's under httpModeGroup, for faceted counts.
func (f *httpFilter) conditions() *kit.FacetFilter[*httpRow] {
	conds := &kit.FacetFilter[*httpRow]{}
	switch f.mode {
	case httpModeErrors:
		conds.Add(httpModeGroup, (*httpRow).problem)
	case httpModeLive:
		conds.Add(httpModeGroup, (*httpRow).live)
	}
	if conn := f.conn; conn != "" {
		conds.Add("", func(r *httpRow) bool { return r.ConnectionID == conn })
	}
	for _, facet := range httpFacets {
		if chosen := f.facets.Match(facet.key); chosen != nil {
			conds.Add(string(facet.key), func(r *httpRow) bool { return chosen(httpFacetValue(r, facet.key)) })
		}
	}
	if search := f.search; search != "" {
		conds.Add("", func(r *httpRow) bool { return contains(r.hay, search) })
	}
	return conds
}

func (f *httpFilter) match(r *httpRow) bool {
	return f.conditions().Miss(r) == kit.FacetAll
}

// httpTime is the start time of day with milliseconds, "—" when unknown.
func httpTime(ns int64) string {
	if ns <= 0 {
		return "—"
	}
	return timefmt.Format(time.Unix(0, ns), "15:04:05.000")
}

// httpDuration formats a duration: "0.4 ms", "123 ms", "1.23 s", "—"
// when negative.
func httpDuration(d time.Duration) string {
	switch {
	case d < 0:
		return "—"
	case d < time.Millisecond:
		return fmt.Sprintf("%.1f ms", float64(d)/float64(time.Millisecond))
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.2f s", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// redacted is what replaces a secret in a copied command.
const redacted = "<redacted>"

// httpSensitive reports whether the value of a request header is a
// secret: credentials, cookies, and names with token, secret, key or
// password.
func httpSensitive(name string) bool {
	n := lower(name)
	switch n {
	case "authorization", "proxy-authorization", "cookie":
		return true
	}
	for _, part := range []string{"token", "secret", "key", "password", "passwd", "auth", "session", "signature", "credential", "csrf", "sid"} {
		if strings.Contains(n, part) {
			return true
		}
	}
	return false
}

// redactURL hides the credentials of a URL: the user information and the
// values of query parameters whose names are sensitive.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	out := raw
	if u.User != nil {
		if i := strings.Index(out, "://"); i >= 0 {
			rest := out[i+3:]
			if at := strings.LastIndex(strings.SplitN(rest, "/", 2)[0], "@"); at >= 0 {
				out = out[:i+3] + redacted + "@" + rest[at+1:]
			}
		}
	}
	if u.RawQuery == "" {
		return out
	}
	start := strings.Index(out, "?")
	if start < 0 {
		return out
	}
	end := len(out)
	if h := strings.Index(out[start:], "#"); h >= 0 {
		end = start + h
	}
	parts := strings.Split(out[start+1:end], "&")
	for i, p := range parts {
		name, _, hasValue := strings.Cut(p, "=")
		if key, err := url.QueryUnescape(name); err == nil {
			name = key
		}
		if hasValue && httpSensitive(name) {
			parts[i] = strings.SplitN(p, "=", 2)[0] + "=" + redacted
		}
	}
	return out[:start+1] + strings.Join(parts, "&") + out[end:]
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// httpCurl is the exchange as a curl command. Secrets in the request
// headers are replaced by a placeholder; body, when given, is added if it
// is text.
func httpCurl(r *api.HTTPRecord, body []byte) string { return httpCurlBody(r, body, false) }

// httpCurlBody is httpCurl with decoded saying that body is the request
// body after its Content-Encoding was undone: the header is then left out,
// as it would not describe the data sent.
func httpCurlBody(r *api.HTTPRecord, body []byte, decoded bool) string {
	lines := []string{"curl " + shellQuote(redactURL(r.URL))}
	switch {
	case r.Method == "HEAD":
		lines = append(lines, "-I")
	case r.Method != "" && r.Method != "GET":
		lines = append(lines, "-X "+shellQuote(r.Method))
	}
	compressed := false
	for _, h := range r.RequestHeaders {
		n := lower(h.Name)
		switch {
		case decoded && len(body) > 0 && n == "content-encoding":
			continue
		case strings.HasPrefix(n, ":"), n == "host", n == "content-length", n == "connection", n == "transfer-encoding", n == "set-cookie":
			continue
		case n == "accept-encoding":
			compressed = true
			continue
		}
		value := h.Value
		switch {
		case httpSensitive(h.Name):
			value = redacted
		case n == "referer" || n == "origin":
			value = redactURL(value)
		}
		lines = append(lines, "-H "+shellQuote(h.Name+": "+value))
	}
	if compressed {
		lines = append(lines, "--compressed")
	}
	if len(body) > 0 && utf8.Valid(body) {
		lines = append(lines, "--data-raw "+shellQuote(string(body)))
	}
	return strings.Join(lines, " \\\n  ")
}

// httpBodyKind is how a body is shown.
type httpBodyKind int

const (
	bodyEmpty httpBodyKind = iota
	bodyText
	bodyJSON
	bodyImage
	bodyHex
)

// trimPartialRune drops an incomplete UTF-8 sequence at the end of data,
// which a truncated body may end in.
func trimPartialRune(data []byte) []byte {
	for i := 1; i <= utf8.UTFMax-1 && i <= len(data); i++ {
		b := data[len(data)-i]
		if utf8.RuneStart(b) {
			if b >= 0x80 && !utf8.FullRune(data[len(data)-i:]) {
				return data[:len(data)-i]
			}
			break
		}
	}
	return data
}

func sniffImage(data []byte) bool {
	switch {
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
	case len(data) >= 2 && string(data[:2]) == "BM":
	default:
		return false
	}
	return true
}

var textTypes = []string{"application/xml", "application/javascript", "application/x-javascript", "application/ecmascript",
	"application/x-www-form-urlencoded", "application/x-ndjson", "application/graphql", "application/x-yaml", "application/yaml",
	"application/xhtml+xml", "image/svg+xml"}

func looksText(data []byte) bool {
	sample := trimPartialRune(data[:min(len(data), 1024)])
	if !utf8.Valid(sample) {
		return false
	}
	for _, b := range sample {
		if b < 0x20 && b != '\t' && b != '\n' && b != '\r' {
			return false
		}
	}
	return true
}

// httpBodyViewKind decides how to show data of the given Content-Type:
// JSON that parses, an image of a known format, text, else hexadecimal.
func httpBodyViewKind(contentType string, data []byte) httpBodyKind {
	if len(data) == 0 {
		return bodyEmpty
	}
	mt := ""
	if contentType != "" {
		if m, _, err := mime.ParseMediaType(contentType); err == nil {
			mt = m
		} else {
			mt, _, _ = strings.Cut(lower(contentType), ";")
			mt = trim(mt)
		}
	}
	switch {
	case strings.HasPrefix(mt, "image/") && mt != "image/svg+xml":
		if sniffImage(data) {
			return bodyImage
		}
		return bodyHex
	case mt == "" && sniffImage(data):
		return bodyImage
	case mt == "application/json" || mt == "text/json" || strings.HasSuffix(mt, "+json") || (mt == "" || mt == "text/plain") && len(data) > 0 && (data[0] == '{' || data[0] == '['):
		if json.Valid(data) {
			return bodyJSON
		}
	}
	textual := strings.HasPrefix(mt, "text/") || slices.Contains(textTypes, mt) || strings.HasSuffix(mt, "+xml") || strings.HasSuffix(mt, "+json") || mt == "application/json"
	if textual && utf8.Valid(trimPartialRune(data)) {
		return bodyText
	}
	if (mt == "" || mt == "application/octet-stream") && looksText(data) {
		return bodyText
	}
	return bodyHex
}

// hexDump formats up to limit bytes as 16 per line: offset, hexadecimal
// and printable characters.
func hexDump(data []byte, limit int) string {
	if len(data) > limit {
		data = data[:limit]
	}
	var b strings.Builder
	for off := 0; off < len(data); off += 16 {
		end := min(off+16, len(data))
		fmt.Fprintf(&b, "%08x  ", off)
		for i := off; i < off+16; i++ {
			if i < end {
				fmt.Fprintf(&b, "%02x ", data[i])
			} else {
				b.WriteString("   ")
			}
			if i == off+7 {
				b.WriteByte(' ')
			}
		}
		b.WriteString(" |")
		for _, c := range data[off:end] {
			if c >= 0x20 && c < 0x7f {
				b.WriteByte(c)
			} else {
				b.WriteByte('.')
			}
		}
		b.WriteString("|\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
