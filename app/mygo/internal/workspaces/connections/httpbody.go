package connections

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/mitmpolicy"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// How much of a body the inspector reads and shows.
const (
	// httpBodyChunk is what one read takes; "Load more" takes the next.
	httpBodyChunk = 64 << 10
	// httpBodyText is the most text or hexadecimal data the inspector
	// loads by hand.
	httpBodyText = 1 << 20
	// httpBodyWhole is the largest body read in full to draw an image or
	// a JSON tree.
	httpBodyWhole = 4 << 20
	// httpHexLimit is how many bytes the hexadecimal view lists.
	httpHexLimit = 4096
	// httpTreeNodes and httpTreeKids bound the JSON tree.
	httpTreeNodes = 20000
	httpTreeKids  = 200
)

// The two sides of an exchange, as indexes of httpInspectorState.bodies.
const (
	sideRequest = iota
	sideResponse
)

var sideNames = [2]string{api.HTTPSideRequest, api.HTTPSideResponse}

// httpBody is one side's body as the inspector has read it.
type httpBody struct {
	// token tells the answers of a read apart from those of an earlier
	// one, such as before the decoding was switched.
	token   int
	fetched bool
	loading bool
	// state is the record's state when the body was read; a record that
	// moved on is read again.
	state string
	data  []byte
	info  api.HTTPBody
	err   string
	kind  httpBodyKind
	// raw asks for the bytes as sent, not decoded.
	raw bool
	// view is the JSON view: 0 the tree, 1 the text.
	view   int
	bitmap *ui.Bitmap
	tree   *jsonNode
	// treeFailed: the data did not parse as the tree needs.
	treeFailed bool
	copied     bool
	// jsonTry: the first chunk looks like JSON that the body's rest has
	// still to prove.
	jsonTry bool
}

// more reports whether the core holds bytes beyond those read.
func (b *httpBody) more() bool { return b.fetched && int64(len(b.data)) < b.info.Total }

// httpBodySide reads what the record says about one side's body.
type httpBodySide struct {
	bytes     int64
	saved     bool
	stored    int64
	truncated bool
	encoding  string
	headers   []api.HTTPHeader
}

func httpSideOf(r *httpRow, side int) httpBodySide {
	if side == sideRequest {
		return httpBodySide{r.RequestBytes, r.RequestBodySaved, r.RequestBodyStored, r.RequestBodyTruncated, r.RequestEncoding, r.RequestHeaders}
	}
	return httpBodySide{r.ResponseBytes, r.ResponseBodySaved, r.ResponseBodyStored, r.ResponseBodyTrunc, r.ResponseEncoding, r.ResponseHeaders}
}

// readable reports whether the core can serve the side's body.
func (s httpBodySide) readable(r *httpRow) bool { return s.bytes > 0 && s.saved && !r.BodyDropped }

// syncBody starts the read the side's body calls for: the first chunk when
// none was read, or again when the record moved on. UI thread.
func (m *httpMonitor) syncBody(r *httpRow, side int) {
	b := &m.insp.bodies[side]
	if !httpSideOf(r, side).readable(r) || b.loading {
		return
	}
	// A failed read is not repeated by itself: the retry button or a new
	// state of the record asks again.
	if !b.fetched && b.err != "" && b.state == r.State {
		return
	}
	if b.fetched && (b.state == r.State || !b.more() && b.info.Complete) {
		return
	}
	if b.fetched {
		m.insp.resetBody(side)
	}
	m.readBody(r, side, 0, httpBodyChunk, false)
}

// readBody reads length bytes of the side's body from offset and adds them
// to those held; full follows a first chunk that asked for the rest.
func (m *httpMonitor) readBody(r *httpRow, side int, offset, length int64, full bool) {
	b := &m.insp.bodies[side]
	b.loading, b.err = true, ""
	token, id, gen := b.token, r.ID, m.insp.gen
	state, raw := r.State, b.raw
	m.fetchBody(id, sideNames[side], offset, length, !raw, func(body api.HTTPBody, err error) {
		b := &m.insp.bodies[side]
		if m.insp.gen != gen || b.token != token {
			return
		}
		b.loading = false
		if err != nil {
			b.err, b.state = err.Error(), state
			return
		}
		if offset == 0 {
			b.data = nil
		}
		if int64(len(b.data)) != offset {
			// The read does not continue the data held.
			b.err, b.state = "正文读取错位", state
			return
		}
		b.data = append(b.data, body.Data...)
		b.info, b.fetched, b.state = body, true, state
		b.info.Total = max(body.Total, int64(len(b.data)))
		ctype := httpContentType(httpSideOf(r, side).headers)
		if offset == 0 {
			b.kind = httpBodyViewKind(ctype, b.data)
			// A JSON body cut by the first chunk does not parse yet: read it
			// whole, then decide.
			b.jsonTry = b.kind != bodyJSON && b.kind != bodyImage && looksJSON(ctype, b.data)
		}
		if b.jsonTry && !b.more() {
			b.jsonTry = false
			b.kind = httpBodyViewKind(ctype, b.data)
		}
		whole := b.kind == bodyImage || b.kind == bodyJSON || b.jsonTry
		if whole && !full && b.more() && b.info.Total <= httpBodyWhole {
			m.readBody(r, side, int64(len(b.data)), 0, true)
			return
		}
		b.bitmap, b.tree, b.treeFailed = nil, nil, false
	})
}

// looksJSON reports whether a body announces JSON by its Content-Type or
// its first character.
func looksJSON(contentType string, data []byte) bool {
	if strings.Contains(lower(contentType), "json") {
		return true
	}
	data = bytes.TrimLeft(data, " \t\r\n")
	return len(data) > 0 && (data[0] == '{' || data[0] == '[')
}

// retryBody reads the side's body again after a failure.
func (m *httpMonitor) retryBody(r *httpRow, side int) {
	m.insp.resetBody(side)
	m.syncBody(r, side)
}

// loadMore reads the next chunk of a text or hexadecimal body.
func (m *httpMonitor) loadMore(r *httpRow, side int) {
	b := &m.insp.bodies[side]
	if b.loading || !b.more() {
		return
	}
	m.readBody(r, side, int64(len(b.data)), httpBodyChunk, true)
}

// text is the loaded data as text.
func (b *httpBody) text() string { return string(trimPartialRune(b.data)) }

// jsonNode is a value of a parsed JSON document.
type jsonNode struct {
	key string
	// kind is '{', '[' or 0 for a scalar.
	kind   byte
	scalar string
	kids   []*jsonNode
	// hidden counts the values left out by the node budget.
	hidden int
}

// label is the line the tree shows for the node.
func (n *jsonNode) label() string {
	name := ""
	if n.key != "" {
		name = n.key + ": "
	}
	switch n.kind {
	case '{':
		return name + "{" + strconv.Itoa(len(n.kids)+n.hidden) + "}"
	case '[':
		return name + "[" + strconv.Itoa(len(n.kids)+n.hidden) + "]"
	}
	return name + n.scalar
}

// parseJSONTree parses data into a tree of at most httpTreeNodes values;
// the rest is counted in hidden.
func parseJSONTree(data []byte) (*jsonNode, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	budget := httpTreeNodes
	n, err := decodeJSONNode(dec, "", &budget)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return nil, errors.New("empty")
	}
	return n, nil
}

func decodeJSONNode(dec *json.Decoder, key string, budget *int) (*jsonNode, error) {
	if *budget <= 0 {
		var skip json.RawMessage
		return nil, dec.Decode(&skip)
	}
	*budget--
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	n := &jsonNode{key: key}
	switch t := tok.(type) {
	case json.Delim:
		n.kind = byte(t)
		for dec.More() {
			name := ""
			if n.kind == '{' {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				name, _ = kt.(string)
			}
			child, err := decodeJSONNode(dec, name, budget)
			if err != nil {
				return nil, err
			}
			if child == nil {
				n.hidden++
			} else {
				n.kids = append(n.kids, child)
			}
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
	case string:
		n.scalar = strconv.Quote(clip(t, 300))
	case json.Number:
		n.scalar = t.String()
	case bool:
		n.scalar = strconv.FormatBool(t)
	case nil:
		n.scalar = "null"
	default:
		n.scalar = fmt.Sprint(t)
	}
	return n, nil
}

// clip shortens s to n runes with an ellipsis.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// docEditor is the document the skip-decryption action changes.
type docEditor interface {
	Doc() *model.Doc
	Update(fn func(doc *model.Doc)) error
}

// hostExcluded reports whether decryption leaves host alone by the
// document's exclude list or that of an enabled module.
func hostExcluded(doc *model.Doc, host string) bool {
	if doc == nil || doc.HTTPTools == nil {
		return false
	}
	name, err := mitmpolicy.NormalizeHost(host)
	if err != nil {
		return false
	}
	matches := func(p string) bool { return mitmpolicy.MatchHost(p, name) }
	if doc.HTTPTools.MITM != nil && slices.ContainsFunc(mitmpolicy.HostList(*doc.HTTPTools.MITM, true), matches) {
		return true
	}
	for _, h := range model.ModuleHosts(doc) {
		if h.Enabled && h.Exclude && matches(h.Host) {
			return true
		}
	}
	return false
}

// excludeDecryption adds host to the exclude list of Settings › HTTPS
// Decryption (the logic of its "Add to exclusions") and returns the
// pattern added.
func excludeDecryption(ed docEditor, host string) (string, error) {
	name, err := mitmpolicy.NormalizeHost(host)
	if err != nil {
		return "", err
	}
	var failed error
	if err := ed.Update(func(d *model.Doc) {
		if d.HTTPTools == nil {
			d.HTTPTools = &model.HTTPTools{}
		}
		if d.HTTPTools.MITM == nil {
			d.HTTPTools.MITM = &model.MITMSettings{}
		}
		failed = mitmpolicy.ExcludeHost(d.HTTPTools.MITM, name)
	}); err != nil {
		return "", err
	}
	return name, failed
}
