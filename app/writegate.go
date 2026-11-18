package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	writeGateTimeout  = 30 * time.Second
	writeGateNotify   = 300 * time.Millisecond
	writeGateLongHold = 120 * time.Second
)

type gateHolder struct {
	Client clientIdentity
	Op     string
}

// lockState is both the `lock` SSE payload and the GET /api/v1/lock body.
type lockState struct {
	Held   bool            `json:"held"`
	Holder *clientIdentity `json:"holder,omitempty"`
	Op     string          `json:"op,omitempty"`
	Since  *time.Time      `json:"since,omitempty"`
	Queued int             `json:"queued"`
}

type gateBusyError struct {
	Holder clientIdentity
	Op     string
	Since  time.Time
}

func (e *gateBusyError) Error() string {
	return fmt.Sprintf("「%s」正在%s，请稍后重试", e.Holder.Name, e.Op)
}

type gateWaiter struct {
	holder  gateHolder
	ready   chan struct{}
	granted bool
	seq     uint64
}

// writeGate serializes every write request in strict FIFO order. A holder
// may hand its release to a background task that finishes the operation.
type writeGate struct {
	mu        sync.Mutex
	timeout   time.Duration
	notify    time.Duration
	longHold  time.Duration
	held      bool
	holder    gateHolder
	since     time.Time
	seq       uint64
	queue     []*gateWaiter
	announced bool
	timers    []*time.Timer
	emit      func(lockState)
	warn      func(gateHolder, time.Duration)
}

func newWriteGate(emit func(lockState), warn func(gateHolder, time.Duration)) *writeGate {
	return &writeGate{timeout: writeGateTimeout, notify: writeGateNotify, longHold: writeGateLongHold, emit: emit, warn: warn}
}

func (g *writeGate) stateLocked() lockState {
	state := lockState{Held: g.held, Queued: len(g.queue)}
	if g.held {
		holder, since := g.holder.Client, g.since
		state.Holder, state.Op, state.Since = &holder, g.holder.Op, &since
	}
	return state
}

func (g *writeGate) state() lockState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stateLocked()
}

// watcherState returns the state for a new event stream. A held gate counts
// as announced from then on, so the stream also hears its release.
func (g *writeGate) watcherState() lockState {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held {
		g.announced = true
	}
	return g.stateLocked()
}

func (g *writeGate) publish(state lockState) {
	if g.emit != nil {
		g.emit(state)
	}
}

// grantLocked makes h the holder and schedules its delayed announcement.
func (g *writeGate) grantLocked(h gateHolder) uint64 {
	g.held, g.holder, g.since = true, h, time.Now()
	g.seq++
	g.announced = false
	seq := g.seq
	for _, t := range g.timers {
		t.Stop()
	}
	g.timers = []*time.Timer{
		time.AfterFunc(g.notify, func() {
			g.mu.Lock()
			if !g.held || g.seq != seq || g.announced {
				g.mu.Unlock()
				return
			}
			g.announced = true
			state := g.stateLocked()
			g.mu.Unlock()
			g.publish(state)
		}),
		time.AfterFunc(g.longHold, func() {
			g.mu.Lock()
			current, holder, since := g.held && g.seq == seq, g.holder, g.since
			g.mu.Unlock()
			if current && g.warn != nil {
				g.warn(holder, time.Since(since))
			}
		}),
	}
	return seq
}

func (g *writeGate) releaser(seq uint64) func() {
	var once sync.Once
	return func() { once.Do(func() { g.release(seq) }) }
}

// acquire waits for the gate in FIFO order. It fails with *gateBusyError
// after the timeout, or with the context error when the caller goes away.
func (g *writeGate) acquire(ctx context.Context, h gateHolder) (func(), error) {
	g.mu.Lock()
	if !g.held && len(g.queue) == 0 {
		seq := g.grantLocked(h)
		g.mu.Unlock()
		return g.releaser(seq), nil
	}
	w := &gateWaiter{holder: h, ready: make(chan struct{})}
	g.queue = append(g.queue, w)
	announced, state := g.announced, g.stateLocked()
	g.mu.Unlock()
	if announced {
		g.publish(state)
	}
	timer := time.NewTimer(g.timeout)
	defer timer.Stop()
	var err error
	select {
	case <-w.ready:
		return g.releaser(w.seq), nil
	case <-ctx.Done():
		err = ctx.Err()
	case <-timer.C:
	}
	g.mu.Lock()
	if w.granted {
		g.mu.Unlock()
		return g.releaser(w.seq), nil
	}
	for i, queued := range g.queue {
		if queued == w {
			g.queue = append(g.queue[:i:i], g.queue[i+1:]...)
			break
		}
	}
	busy := &gateBusyError{Holder: g.holder.Client, Op: g.holder.Op, Since: g.since}
	announced, state = g.announced, g.stateLocked()
	g.mu.Unlock()
	if announced {
		g.publish(state)
	}
	if err != nil {
		return nil, err
	}
	return nil, busy
}

func (g *writeGate) release(seq uint64) {
	g.mu.Lock()
	if !g.held || g.seq != seq {
		g.mu.Unlock()
		return
	}
	announced := g.announced
	for _, t := range g.timers {
		t.Stop()
	}
	g.timers = nil
	if len(g.queue) > 0 {
		next := g.queue[0]
		g.queue = g.queue[1:]
		next.seq = g.grantLocked(next.holder)
		next.granted = true
		close(next.ready)
		// Watchers that saw the previous holder learn about the hand-off now.
		g.announced = announced
	} else {
		g.held, g.holder, g.since = false, gateHolder{}, time.Time{}
	}
	state := g.stateLocked()
	g.mu.Unlock()
	if announced {
		g.publish(state)
	}
}

func (s *server) writeGate() *writeGate {
	s.gateOnce.Do(func() {
		s.gate = newWriteGate(func(state lockState) { s.emit("lock", state) }, func(h gateHolder, held time.Duration) {
			if s.logs != nil {
				s.logs.add(fmt.Sprintf("[kiko] 写操作「%s」由「%s」持有已超过 %s", h.Op, h.Client.Name, held.Round(time.Second)))
			}
		})
	})
	return s.gate
}

type gateHoldKey struct{}

type gateHold struct {
	mu       sync.Mutex
	release  func()
	detached bool
}

// detachWriteGate hands the request's gate release to the caller, which must
// call it once the asynchronous operation started by the handler finishes.
func detachWriteGate(r *http.Request) func() {
	hold, _ := r.Context().Value(gateHoldKey{}).(*gateHold)
	if hold == nil {
		return func() {}
	}
	hold.mu.Lock()
	defer hold.mu.Unlock()
	hold.detached = true
	return hold.release
}

func (h *gateHold) finish() {
	h.mu.Lock()
	detached := h.detached
	h.mu.Unlock()
	if !detached {
		h.release()
	}
}

func (s *server) gated(op string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		began := time.Now()
		client := requestClient(r)
		release, err := s.writeGate().acquire(r.Context(), gateHolder{Client: client, Op: op})
		if err != nil {
			var busy *gateBusyError
			if !errors.As(err, &busy) {
				return
			}
			w.Header().Set("Retry-After", "2")
			writeJSON(w, http.StatusLocked, map[string]any{
				"error": busy.Error(), "holder": busy.Holder, "op": busy.Op, "since": busy.Since.Format(time.RFC3339),
			})
			s.audit(client, r, op, http.StatusLocked, began)
			return
		}
		hold := &gateHold{release: release}
		recorder := &statusWriter{ResponseWriter: w}
		defer func() {
			hold.finish()
			s.audit(client, r, op, recorder.code(), began)
		}()
		next(recorder, r.WithContext(context.WithValue(r.Context(), gateHoldKey{}, hold)))
	}
}

func (s *server) audited(op string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		began := time.Now()
		recorder := &statusWriter{ResponseWriter: w}
		defer func() { s.audit(requestClient(r), r, op, recorder.code(), began) }()
		next(recorder, r)
	}
}

// audit appends one line per write request to logs/kiko.log.
func (s *server) audit(client clientIdentity, r *http.Request, op string, status int, began time.Time) {
	if s.logs == nil {
		return
	}
	s.logs.record(fmt.Sprintf("[审计] %s（%s）%s %s %s → %d，耗时 %s", client.Name, client.ID, op, r.Method, r.URL.Path, status, time.Since(began).Round(time.Millisecond)))
}

func (s *server) lockStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.writeGate().state())
}

// statusWriter records the response status and keeps streaming and
// hijacking available to the wrapped handler.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) code() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
