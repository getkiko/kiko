package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	mitmapi "github.com/sagernet/sing-box/common/mitm/api"
	"github.com/sagernet/sing-box/common/mitm/capture"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	harMaxRecords     = 5000
	harRedacted       = "[redacted]"
	harBodyLimit      = 16 << 20
	harEntryTimeout   = 30 * time.Second
	harRPCTimeout     = 10 * time.Second
	harListPageSize   = 500
	harBodyReadWindow = 64 << 10
)

type harFilterJSON struct {
	Search       string `json:"search"`
	Host         string `json:"host"`
	ConnectionID string `json:"connection_id"`
}

type harSanitizeJSON struct {
	AuthHeaders  bool `json:"auth_headers"`
	Cookies      bool `json:"cookies"`
	QuerySecrets bool `json:"query_secrets"`
	Bodies       bool `json:"bodies"`
}

type harRequestJSON struct {
	IDs      []string        `json:"ids"`
	Filter   harFilterJSON   `json:"filter"`
	Bodies   *bool           `json:"bodies"`
	Sanitize harSanitizeJSON `json:"sanitize"`
}

// harNameValue is the HAR name/value pair shared by headers, query strings
// and cookies.
type harNameValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type harCookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Path     string `json:"path,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Expires  string `json:"expires,omitempty"`
	HTTPOnly bool   `json:"httpOnly,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
}

type harTimings struct {
	Blocked float64 `json:"blocked"`
	DNS     float64 `json:"dns"`
	Connect float64 `json:"connect"`
	Send    float64 `json:"send"`
	Wait    float64 `json:"wait"`
	Receive float64 `json:"receive"`
	SSL     float64 `json:"ssl"`
}

type harRequest struct {
	Method      string         `json:"method"`
	URL         string         `json:"url"`
	HTTPVersion string         `json:"httpVersion"`
	Cookies     []harCookie    `json:"cookies"`
	Headers     []harNameValue `json:"headers"`
	QueryString []harNameValue `json:"queryString"`
	HeadersSize int64          `json:"headersSize"`
	BodySize    int64          `json:"bodySize"`
}

type harKiko struct {
	ID           string            `json:"id"`
	State        string            `json:"state"`
	Error        string            `json:"error,omitempty"`
	Inbound      string            `json:"inbound,omitempty"`
	Outbound     string            `json:"outbound,omitempty"`
	Rule         string            `json:"rule,omitempty"`
	Process      string            `json:"process,omitempty"`
	Unverified   bool              `json:"unverified,omitempty"`
	Rewritten    bool              `json:"rewritten,omitempty"`
	RequestIndex uint32            `json:"requestIndex,omitempty"`
	Rewrites     []httpRewriteJSON `json:"rewrites,omitempty"`
}

// harWriter writes one JSON document piece by piece.
type harWriter struct {
	w   *bufio.Writer
	err error
}

func (h *harWriter) raw(text string) {
	if h.err == nil {
		_, h.err = h.w.WriteString(text)
	}
}

func (h *harWriter) bytes(data []byte) {
	if h.err == nil {
		_, h.err = h.w.Write(data)
	}
}

func (h *harWriter) value(v any) {
	if h.err != nil {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		h.err = err
		return
	}
	h.bytes(data)
}

func (h *harWriter) field(name string, v any) {
	h.value(name)
	h.raw(":")
	h.value(v)
}

var harAuthHeaders = map[string]bool{
	"authorization": true, "proxy-authorization": true, "x-api-key": true, "api-key": true,
	"x-auth-token": true, "x-access-token": true, "x-csrf-token": true, "x-xsrf-token": true,
	"x-amz-security-token": true,
}

// harSecretParam shares the persisted-record rule so export and storage
// mask the same parameters.
func harSecretParam(name string) bool { return capture.SecretParam(name) }

// redactQuery replaces the values of secret-named parameters in a raw query,
// keeping the rest byte for byte.
func redactQuery(raw string) string {
	if raw == "" {
		return raw
	}
	parts := strings.Split(raw, "&")
	for i, part := range parts {
		name, _, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		decoded, err := url.QueryUnescape(name)
		if err != nil {
			decoded = name
		}
		if harSecretParam(decoded) {
			parts[i] = name + "=" + url.QueryEscape(harRedacted)
		}
	}
	return strings.Join(parts, "&")
}

// redactUserInfo replaces the user:password part of an absolute URL.
func redactUserInfo(raw string) string {
	scheme, rest, found := strings.Cut(raw, "://")
	if !found {
		return raw
	}
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	at := strings.LastIndex(rest[:end], "@")
	if at < 0 {
		return raw
	}
	return scheme + "://" + harRedacted + "@" + rest[at+1:]
}

// redactURL redacts secrets of a URL: the user information, and the values
// of secret-named parameters in both the query and the fragment.
func redactURL(raw string) string {
	raw = redactUserInfo(raw)
	base, fragment, hasFragment := strings.Cut(raw, "#")
	base, query, hasQuery := strings.Cut(base, "?")
	if hasQuery {
		base += "?" + redactQuery(query)
	}
	if hasFragment {
		base += "#" + redactQuery(fragment)
	}
	return base
}

func harQueryString(raw string, redact bool) []harNameValue {
	result := []harNameValue{}
	_, query, found := strings.Cut(raw, "?")
	if !found {
		return result
	}
	query, _, _ = strings.Cut(query, "#")
	if query == "" {
		return result
	}
	for _, part := range strings.Split(query, "&") {
		if part == "" {
			continue
		}
		name, value, _ := strings.Cut(part, "=")
		if decoded, err := url.QueryUnescape(name); err == nil {
			name = decoded
		}
		if decoded, err := url.QueryUnescape(value); err == nil {
			value = decoded
		}
		if redact && harSecretParam(name) {
			value = harRedacted
		}
		result = append(result, harNameValue{Name: name, Value: value})
	}
	return result
}

type harOptions struct {
	sanitize harSanitizeJSON
	bodies   bool
}

func (o harOptions) headers(in []*mitmapi.HTTPHeader) []harNameValue {
	out := make([]harNameValue, 0, len(in))
	for _, header := range in {
		value := header.Value
		lower := strings.ToLower(header.Name)
		switch {
		case o.sanitize.AuthHeaders && harAuthHeaders[lower]:
			value = harRedacted
		case o.sanitize.Cookies && lower == "cookie":
			value = redactCookieHeader(value)
		case o.sanitize.Cookies && lower == "set-cookie":
			value = redactSetCookie(value)
		case o.sanitize.QuerySecrets && (lower == "location" || lower == "referer"):
			value = redactURL(value)
		case o.sanitize.AuthHeaders && (lower == "location" || lower == "referer"):
			value = redactUserInfo(value)
		}
		out = append(out, harNameValue{Name: header.Name, Value: value})
	}
	return out
}

func redactCookieHeader(value string) string {
	parts := strings.Split(value, ";")
	for i, part := range parts {
		name, _, found := strings.Cut(strings.TrimSpace(part), "=")
		if found {
			parts[i] = " " + name + "=" + harRedacted
		} else if name != "" {
			parts[i] = " " + harRedacted
		}
	}
	return strings.TrimSpace(strings.Join(parts, ";"))
}

func redactSetCookie(value string) string {
	first, rest, hasRest := strings.Cut(value, ";")
	name, _, found := strings.Cut(first, "=")
	if found {
		first = name + "=" + harRedacted
	} else if strings.TrimSpace(first) != "" {
		first = harRedacted
	}
	if hasRest {
		return first + ";" + rest
	}
	return first
}

func (o harOptions) requestCookies(headers []*mitmapi.HTTPHeader) []harCookie {
	cookies := []harCookie{}
	for _, header := range headers {
		if !strings.EqualFold(header.Name, "Cookie") {
			continue
		}
		parsed, err := http.ParseCookie(header.Value)
		if err != nil {
			continue
		}
		for _, cookie := range parsed {
			value := cookie.Value
			if o.sanitize.Cookies {
				value = harRedacted
			}
			cookies = append(cookies, harCookie{Name: cookie.Name, Value: value})
		}
	}
	return cookies
}

func (o harOptions) responseCookies(headers []*mitmapi.HTTPHeader) []harCookie {
	cookies := []harCookie{}
	for _, header := range headers {
		if !strings.EqualFold(header.Name, "Set-Cookie") {
			continue
		}
		cookie, err := http.ParseSetCookie(header.Value)
		if err != nil {
			continue
		}
		value := cookie.Value
		if o.sanitize.Cookies {
			value = harRedacted
		}
		item := harCookie{Name: cookie.Name, Value: value, Path: cookie.Path, Domain: cookie.Domain, HTTPOnly: cookie.HttpOnly, Secure: cookie.Secure}
		if !cookie.Expires.IsZero() {
			item.Expires = cookie.Expires.UTC().Format("2006-01-02T15:04:05.000Z07:00")
		}
		cookies = append(cookies, item)
	}
	return cookies
}

func harHeaderValue(headers []*mitmapi.HTTPHeader, name string) string {
	for _, header := range headers {
		if strings.EqualFold(header.Name, name) {
			return header.Value
		}
	}
	return ""
}

func harVersion(proto string) string {
	switch proto {
	case "h2":
		return "h2"
	case "http/1.1", "":
		return "HTTP/1.1"
	}
	return proto
}

func harMillis(nanos int64) float64 {
	return float64(nanos) / 1e6
}

// harTimes derives the HAR timings and the start of the entry. Setup
// durations (dns, connect, tls) precede the record's start; unknown ones are
// -1 and the others never negative. As the HAR format counts ssl inside
// connect, connect carries both.
func harTimes(t *mitmapi.HTTPTiming) (start time.Time, total float64, timings harTimings) {
	timings = harTimings{Blocked: -1, DNS: -1, Connect: -1, SSL: -1}
	if t == nil || t.Start == 0 {
		return time.Time{}, 0, harTimings{Blocked: -1, DNS: -1, Connect: -1, SSL: -1}
	}
	var setup int64
	if t.Dns > 0 {
		timings.DNS = harMillis(t.Dns)
		setup += t.Dns
	}
	if t.Connect > 0 || t.Tls > 0 {
		timings.Connect = harMillis(t.Connect + t.Tls)
		setup += t.Connect + t.Tls
	}
	if t.Tls > 0 {
		timings.SSL = harMillis(t.Tls)
	}
	sent := t.Start
	switch {
	case t.RequestBody > t.Start:
		sent = t.RequestBody
	case t.RequestHeaders > t.Start:
		sent = t.RequestHeaders
	}
	firstByte := sent
	switch {
	case t.FirstByte > sent:
		firstByte = t.FirstByte
	case t.ResponseHeaders > sent:
		firstByte = t.ResponseHeaders
	}
	end := firstByte
	switch {
	case t.End > firstByte:
		end = t.End
	case t.ResponseBody > firstByte:
		end = t.ResponseBody
	}
	timings.Send = harMillis(sent - t.Start)
	timings.Wait = harMillis(firstByte - sent)
	timings.Receive = harMillis(end - firstByte)
	total = timings.Send + timings.Wait + timings.Receive
	if timings.DNS > 0 {
		total += timings.DNS
	}
	if timings.Connect > 0 {
		total += timings.Connect
	}
	return time.Unix(0, t.Start-setup), total, timings
}

func harText(mimeType string) bool {
	if mimeType == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(mimeType)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(mimeType))
	}
	switch {
	case strings.HasPrefix(mediaType, "text/"), strings.HasSuffix(mediaType, "+json"), strings.HasSuffix(mediaType, "+xml"):
		return true
	}
	switch mediaType {
	case "application/json", "application/xml", "application/javascript", "application/x-javascript", "application/x-www-form-urlencoded",
		"application/graphql", "application/ecmascript", "image/svg+xml", "application/yaml", "application/x-ndjson":
		return true
	}
	return false
}

// harBodyStream reads one body view from the core, deciding from its first
// chunk whether it is text or base64. Nothing is written before the first
// chunk arrives, so a missing body leaves the entry intact.
type harBodyStream struct {
	stream grpc.ServerStreamingClient[mitmapi.BodyChunk]
	first  *mitmapi.BodyChunk
	err    error
	cancel context.CancelFunc
}

func (s *server) harOpenBody(ctx context.Context, client mitmapi.HTTPCaptureClient, record *mitmapi.HTTPRecord, side mitmapi.BodySide) *harBodyStream {
	ctx, cancel := context.WithCancel(ctx)
	stream, err := client.GetBody(ctx, &mitmapi.GetBodyRequest{Id: record.Id, Instance: record.Instance, Side: side, Decoded: true}, grpc.MaxCallRecvMsgSize(httpCaptureMaxMessage))
	if err != nil {
		cancel()
		return &harBodyStream{err: err}
	}
	first, err := stream.Recv()
	if err != nil {
		cancel()
		return &harBodyStream{err: err}
	}
	return &harBodyStream{stream: stream, first: first, cancel: cancel}
}

// size is the size of the whole view the core serves.
func (b *harBodyStream) size(fallback int64) int64 {
	if b.err != nil {
		return fallback
	}
	return b.first.Total
}

// harSplitRune returns where an incomplete rune at the end of data starts,
// or len(data) when the data ends on a rune boundary.
func harSplitRune(data []byte) int {
	for back := 1; back < utf8.UTFMax && back <= len(data); back++ {
		if utf8.RuneStart(data[len(data)-back]) {
			if utf8.FullRune(data[len(data)-back:]) {
				return len(data)
			}
			return len(data) - back
		}
	}
	return len(data)
}

// write streams the body as the string field "text", preceded by the
// "encoding":"base64" field when withEncoding is set and the content is
// binary. It returns the comment for the entry: how the body is cut or why it
// is missing, and whether the text is base64.
func (b *harBodyStream) write(h *harWriter, textLike bool, withEncoding bool) (comment string, base64Text bool) {
	if b.cancel != nil {
		defer b.cancel()
	}
	if b.err != nil {
		h.raw(`"text":""`)
		if code := status.Code(b.err); code == codes.NotFound || code == codes.FailedPrecondition {
			return "body unavailable", false
		}
		return "body unavailable: " + sanitizeHeader(status.Convert(b.err).Message()), false
	}
	first := b.first
	base64Text = !(textLike && harValidPrefix(first.Data))
	if base64Text && withEncoding {
		h.raw(`"encoding":"base64",`)
	}
	h.raw(`"text":"`)
	var (
		encoder io.WriteCloser
		carry   []byte
		sent    int64
	)
	if base64Text {
		encoder = base64.NewEncoder(base64.StdEncoding, harRawWriter{h})
	}
	truncated, decodeError := false, ""
	chunk := first
	for {
		data := chunk.Data
		if sent+int64(len(data)) > harBodyLimit {
			data = data[:harBodyLimit-sent]
			truncated = true
		}
		sent += int64(len(data))
		if encoder != nil {
			_, _ = encoder.Write(data)
		} else {
			data = append(carry, data...)
			cut := harSplitRune(data)
			h.bytes(harEscape(data[:cut]))
			carry = append([]byte(nil), data[cut:]...)
		}
		truncated = truncated || chunk.Truncated
		if chunk.DecodeError != "" {
			decodeError = chunk.DecodeError
		}
		if h.err != nil {
			break
		}
		if sent >= harBodyLimit {
			// A body of exactly the limit is complete; any further data
			// means the view is cut.
			if next, err := b.stream.Recv(); err == nil && (len(next.Data) > 0 || next.Truncated) {
				truncated = true
			}
			break
		}
		next, err := b.stream.Recv()
		if err != nil {
			if err != io.EOF {
				comment = "body interrupted"
			}
			break
		}
		chunk = next
	}
	if encoder != nil {
		_ = encoder.Close()
	} else if len(carry) > 0 {
		h.bytes(harEscape(carry))
	}
	h.raw(`"`)
	switch {
	case comment != "":
	case truncated:
		comment = fmt.Sprintf("truncated: %d of %d bytes", sent, max(first.OriginalSize, first.Total))
	case decodeError != "":
		comment = "decode: " + sanitizeHeader(decodeError)
	}
	return comment, base64Text
}

// harValidPrefix reports whether data is valid UTF-8 but for a rune cut at
// its end.
func harValidPrefix(data []byte) bool {
	for back := 0; back < utf8.UTFMax && back <= len(data); back++ {
		if utf8.Valid(data[:len(data)-back]) {
			return true
		}
	}
	return false
}

type harRawWriter struct{ h *harWriter }

func (w harRawWriter) Write(p []byte) (int, error) {
	w.h.bytes(p)
	return len(p), w.h.err
}

// harEscape returns the JSON string escape of text without quotes.
func harEscape(data []byte) []byte {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(string(data))
	encoded := bytes.TrimRight(buffer.Bytes(), "\n")
	return encoded[1 : len(encoded)-1]
}

func harDestinationIP(record *mitmapi.HTTPRecord) string {
	if record.Connection == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(record.Connection.Destination)
	if err != nil {
		host = record.Connection.Destination
	}
	if net.ParseIP(host) == nil {
		return ""
	}
	return host
}

func (s *server) harEntry(ctx context.Context, client mitmapi.HTTPCaptureClient, h *harWriter, record *mitmapi.HTTPRecord, options harOptions) {
	started, total, timings := harTimes(record.Timing)
	if started.IsZero() {
		started = time.Unix(0, 0)
	}
	recordURL := record.Url
	if options.sanitize.QuerySecrets {
		recordURL = redactURL(recordURL)
	} else if options.sanitize.AuthHeaders {
		recordURL = redactUserInfo(recordURL)
	}
	h.raw("{")
	h.field("startedDateTime", started.UTC().Format("2006-01-02T15:04:05.000Z"))
	h.raw(",")
	h.field("time", total)
	h.raw(",")
	request := harRequest{
		Method:      record.Method,
		URL:         recordURL,
		HTTPVersion: harVersion(record.Proto),
		Cookies:     options.requestCookies(record.RequestHeaders),
		Headers:     options.headers(record.RequestHeaders),
		QueryString: harQueryString(record.Url, options.sanitize.QuerySecrets),
		HeadersSize: -1,
		BodySize:    record.RequestBytes,
	}
	h.value("request")
	h.raw(":")
	requestJSON, _ := json.Marshal(request)
	// postData is added to the request object after its closing brace.
	h.bytes(requestJSON[:len(requestJSON)-1])
	if record.RequestBytes > 0 {
		mimeType := harHeaderValue(record.RequestHeaders, "Content-Type")
		h.raw(`,"postData":{`)
		h.field("mimeType", mimeType)
		h.raw(",")
		comment := ""
		if options.bodies {
			stream := s.harOpenBody(ctx, client, record, mitmapi.BodySide_BODY_SIDE_REQUEST)
			var binary bool
			comment, binary = stream.write(h, harText(mimeType), false)
			if binary {
				comment = strings.TrimSpace("base64 " + comment)
			}
		} else {
			h.raw(`"text":""`)
		}
		if comment != "" {
			h.raw(",")
			h.field("comment", comment)
		}
		h.raw("}")
	}
	h.raw("},")
	mimeType := harHeaderValue(record.ResponseHeaders, "Content-Type")
	redirect := harHeaderValue(record.ResponseHeaders, "Location")
	if options.sanitize.QuerySecrets {
		redirect = redactURL(redirect)
	} else if options.sanitize.AuthHeaders {
		redirect = redactUserInfo(redirect)
	}
	h.value("response")
	h.raw(":{")
	h.field("status", record.Status)
	h.raw(",")
	h.field("statusText", http.StatusText(int(record.Status)))
	h.raw(",")
	h.field("httpVersion", harVersion(record.Proto))
	h.raw(",")
	h.field("cookies", options.responseCookies(record.ResponseHeaders))
	h.raw(",")
	h.field("headers", options.headers(record.ResponseHeaders))
	h.raw(`,"content":{`)
	if record.ResponseBytes > 0 && options.bodies {
		stream := s.harOpenBody(ctx, client, record, mitmapi.BodySide_BODY_SIDE_RESPONSE)
		h.field("size", stream.size(record.ResponseBytes))
		h.raw(",")
		h.field("mimeType", mimeType)
		h.raw(",")
		if comment, _ := stream.write(h, harText(mimeType), true); comment != "" {
			h.raw(",")
			h.field("comment", comment)
		}
	} else {
		h.field("size", record.ResponseBytes)
		h.raw(",")
		h.field("mimeType", mimeType)
		h.raw(`,"text":""`)
	}
	h.raw("},")
	h.field("redirectURL", redirect)
	h.raw(",")
	h.field("headersSize", -1)
	h.raw(",")
	h.field("bodySize", record.ResponseBytes)
	h.raw("},")
	h.raw(`"cache":{},`)
	h.field("timings", timings)
	h.raw(",")
	h.field("serverIPAddress", harDestinationIP(record))
	h.raw(",")
	h.field("connection", record.ConnectionId)
	kiko := harKiko{
		ID: record.Id, State: httpState(record.State), Error: record.Error,
		Unverified: record.Unverified, Rewritten: record.Rewritten, RequestIndex: record.RequestIndex,
		Rewrites: httpRewrites(record.Rewrites),
	}
	if c := record.Connection; c != nil {
		kiko.Inbound, kiko.Outbound, kiko.Rule, kiko.Process = c.Inbound, c.Outbound, c.Rule, c.ProcessName
	}
	h.raw(",")
	h.field("_kiko", kiko)
	h.raw("}")
}

// collectHARRecords gathers the records to export, oldest first.
func (s *server) collectHARRecords(ctx context.Context, client mitmapi.HTTPCaptureClient, in harRequestJSON) ([]*mitmapi.HTTPRecord, error) {
	var records []*mitmapi.HTTPRecord
	if len(in.IDs) > 0 {
		seen := map[string]bool{}
		for _, id := range in.IDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			record, err := client.GetRecord(ctx, &mitmapi.GetRecordRequest{Id: id}, grpc.MaxCallRecvMsgSize(httpCaptureMaxMessage))
			if err != nil {
				if status.Code(err) == codes.NotFound {
					continue
				}
				return nil, err
			}
			records = append(records, record)
		}
	} else {
		var before uint64
		for len(records) < harMaxRecords {
			list, err := client.ListRecords(ctx, &mitmapi.ListRecordsRequest{
				Search: in.Filter.Search, Host: in.Filter.Host, ConnectionId: in.Filter.ConnectionID,
				Limit: harListPageSize, Before: before,
			}, grpc.MaxCallRecvMsgSize(httpCaptureMaxMessage))
			if err != nil {
				return nil, err
			}
			records = append(records, list.Records...)
			if list.NextBefore == 0 || len(list.Records) == 0 {
				break
			}
			before = list.NextBefore
		}
		if len(records) > harMaxRecords {
			records = records[:harMaxRecords]
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		a, errA := strconv.ParseUint(records[i].Id, 10, 64)
		b, errB := strconv.ParseUint(records[j].Id, 10, 64)
		return errA == nil && errB == nil && a < b
	})
	return records, nil
}

// exportHTTPHAR writes the selected records as HAR 1.2. Records are gathered
// before the response starts, so a failure there is a normal error response;
// entries are then written one at a time with bodies streamed from the core.
func (s *server) exportHTTPHAR(w http.ResponseWriter, r *http.Request) {
	client := s.requireHTTPCapture(w)
	if client == nil {
		return
	}
	var in harRequestJSON
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	if len(in.IDs) > harMaxRecords {
		httpBadRequest(w, "一次最多导出 5000 条记录")
		return
	}
	options := harOptions{sanitize: in.Sanitize, bodies: (in.Bodies == nil || *in.Bodies) && !in.Sanitize.Bodies}
	ctx, cancel := context.WithTimeout(r.Context(), harRPCTimeout)
	records, err := s.collectHARRecords(ctx, client, in)
	cancel()
	if err != nil {
		writeRPCError(w, err)
		return
	}
	ctx, cancelStream := context.WithCancel(r.Context())
	defer cancelStream()
	header := w.Header()
	header.Set("Content-Type", "application/json")
	header.Set("Content-Disposition", `attachment; filename="kiko-`+time.Now().Format("20060102-150405")+`.har"`)
	header.Set("Cache-Control", "no-store")
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	h := &harWriter{w: bufio.NewWriterSize(w, 64<<10)}
	h.raw(`{"log":{"version":"1.2","creator":`)
	h.value(map[string]string{"name": "Kiko", "version": version})
	h.raw(`,"pages":[],"entries":[`)
	for i, record := range records {
		_ = controller.SetWriteDeadline(time.Now().Add(harEntryTimeout))
		if i > 0 {
			h.raw(",")
		}
		s.harEntry(ctx, client, h, record, options)
		if h.err != nil {
			return
		}
		if h.w.Buffered() > 32<<10 {
			if h.err = h.w.Flush(); h.err != nil {
				return
			}
		}
	}
	h.raw("]}}")
	if h.err == nil {
		_ = h.w.Flush()
	}
}
