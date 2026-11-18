package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// Event is one message of the server's event stream (GET /events):
// runtime, log, store, selectors, tailscale, openconnect, openvpn,
// taildrop, taildropSend, usbip, usbipShare or sync.
type Event struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Decode unmarshals the event's data into v.
func (e Event) Decode(v any) error { return json.Unmarshal(e.Data, v) }

// Events follows GET /events until ctx ends, reconnecting after failures
// with a short backoff. connected reports each (re)connection, before the
// snapshot events the server replays; it may be nil.
func (c *Client) Events(ctx context.Context, connected func(bool), handle func(Event)) {
	c.Follow(ctx, connected, nil, handle)
}

// Follow is Events reporting why each connection attempt failed to
// failed (nil on success; failed may be nil). A failure no retry can mend
// — a revoked token (ErrUnauthorized) or a changed certificate
// (*FingerprintError) — ends it.
func (c *Client) Follow(ctx context.Context, connected func(bool), failed func(error), handle func(Event)) {
	backoff := eventsBackoff
	for ctx.Err() == nil {
		up, err := c.eventsOnce(ctx, connected, handle)
		// The server may come back as another version.
		c.info.drop()
		if connected != nil {
			connected(false)
		}
		if ctx.Err() != nil {
			return
		}
		if failed != nil {
			failed(err)
		}
		if Fatal(err) {
			return
		}
		// A stream that was up starts the backoff over, however it ended:
		// only failures to connect wait longer each time.
		if up {
			backoff = eventsBackoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, eventsMaxBackoff)
	}
}

// The waits between reconnections of the event stream.
var (
	eventsBackoff    = 500 * time.Millisecond
	eventsMaxBackoff = 5 * time.Second
)

// eventsIdle is how long a connection of the event stream may stay silent
// before it counts as stalled and reconnects. Servers that write a
// heartbeat (every 10 seconds) arm it with their first one; older servers
// send none and are never cut off for being quiet.
var eventsIdle = 30 * time.Second

// eventsOnce follows one connection of the event stream, reporting
// whether it connected.
func (c *Client) eventsOnce(ctx context.Context, connected func(bool), handle func(Event)) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL("/events"), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.stream().Do(req)
	err = transportError(err)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, errorFrom(resp)
	}
	if connected != nil {
		connected(true)
	}
	body := &idleReader{r: resp.Body, after: eventsIdle, cancel: cancel}
	defer body.stop()
	_ = readSSE(body, func(data []byte) {
		var event Event
		if json.Unmarshal(data, &event) == nil && event.Type != "" {
			handle(event)
		}
	}, body.arm)
	return true, nil
}

// idleReader cancels the request of a stream that stopped sending. It is
// armed by the first heartbeat, then restarts its timer whenever data
// arrives. Reads and arm happen on the reading goroutine.
type idleReader struct {
	r      io.Reader
	after  time.Duration
	cancel context.CancelFunc
	idle   *time.Timer
}

func (r *idleReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 && r.idle != nil {
		r.idle.Reset(r.after)
	}
	return n, err
}

func (r *idleReader) arm() {
	if r.idle == nil {
		r.idle = time.AfterFunc(r.after, r.cancel)
	}
}

func (r *idleReader) stop() {
	if r.idle != nil {
		r.idle.Stop()
	}
}

// readSSE calls message with the data of each server-sent event, and
// comment, when not nil, for each comment line (a heartbeat).
func readSSE(r io.Reader, message func([]byte), comment func()) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 32<<20)
	var data bytes.Buffer
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if data.Len() > 0 {
				message(bytes.Clone(data.Bytes()))
				data.Reset()
			}
		case strings.HasPrefix(line, ":"):
			if comment != nil {
				comment()
			}
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	return scanner.Err()
}

// NDJSON performs a request whose reply is newline-delimited JSON and
// calls item with each line until the stream or ctx ends. A line of the
// with a non-empty "error" field ends the stream with that error unless
// allowItemErrors, in which case it is passed on to item.
func (c *Client) NDJSON(ctx context.Context, method, path string, in any, allowItemErrors bool, item func(json.RawMessage) error) error {
	return c.ndjson(ctx, method, path, in, allowItemErrors, nil, item)
}

// ndjson is NDJSON with onOpen, called once the server has answered with
// a success status and before the first line.
func (c *Client) ndjson(ctx context.Context, method, path string, in any, allowItemErrors bool, onOpen func(), item func(json.RawMessage) error) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL(path), body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.stream().Do(req)
	err = transportError(err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return errorFrom(resp)
	}
	if onOpen != nil {
		onOpen()
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if !allowItemErrors {
			var failure struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(line, &failure) == nil && failure.Error != "" {
				return &Error{Status: resp.StatusCode, Message: failure.Error}
			}
		}
		if err := item(bytes.Clone(line)); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return ctx.Err()
}
