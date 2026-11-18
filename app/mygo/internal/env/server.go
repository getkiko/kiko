package env

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/supervisor"
)

// maxLogLines is how many log lines the console keeps. Logs may hold a
// quarter more before it is trimmed, so that trimming stays cheap.
const maxLogLines = 5000

// logBatchDelay is how long log lines gather before they reach Logs, in
// one post.
const logBatchDelay = 250 * time.Millisecond

// Server is kiko-server's live state. Its fields are read and written on
// the UI thread only.
type Server struct {
	env *Env

	// Connected reports whether the event stream is up.
	Connected bool
	// EverConnected reports whether it ever was, to tell a first start
	// from a lost connection.
	EverConnected bool
	// LastEvent is when the last event arrived.
	LastEvent time.Time
	// Runtime is the core's latest state.
	Runtime api.Runtime
	// HasRuntime reports whether Runtime was received.
	HasRuntime bool
	// Selectors are the running core's selector groups.
	Selectors []api.Selector
	// Logs holds the latest log lines, oldest first, at least the last
	// maxLogLines of them.
	Logs []string
	// LogsVersion changes whenever Logs does: it grows by one for each
	// line appended.
	LogsVersion int
	// Latest keeps the latest event of each type, for workspaces that
	// start after it arrived (endpoint status and the like).
	Latest map[string]api.Event
	// Process is the server process's supervision when the desktop shell
	// runs the server (HasProcess); see SetProcess.
	Process    supervisor.Status
	HasProcess bool
	// Failure is why the last connection attempt failed; nil once
	// connected. A failure api.Fatal reports stops the retries.
	Failure error
	// Info is GET /info, read at each connection: who the server takes
	// this console for, its name and capabilities.
	Info *api.ServerInfo
	// Lock is the server's write gate ("lock" events, GET /lock).
	Lock api.LockState

	subscribers map[int]subscriber
	reconnects  map[int]func()
	connects    map[int]func()
	nextID      int
	cancel      context.CancelFunc

	// logs gathers the current stream's log lines; nil while stopped.
	logs *logBatch
	// logsWatched is set while a view shows Logs: only then do new lines
	// request a frame.
	logsWatched atomic.Bool
}

// logBatch gathers one stream's log events off the UI thread.
type logBatch struct {
	mu     sync.Mutex
	events []api.Event
	// reset is set when the stream connects: Logs starts over, as the
	// server replays its snapshot.
	reset bool
	// scheduled is set while a take is pending.
	scheduled bool
}

type subscriber struct {
	types map[string]bool
	fn    func(api.Event)
}

func newServer(e *Env) *Server {
	return &Server{env: e, Latest: map[string]api.Event{}, subscribers: map[int]subscriber{}, reconnects: map[int]func(){}, connects: map[int]func(){}}
}

// Start follows the server's event stream until Stop. UI thread.
func (s *Server) Start() {
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	// A stopped stream's last posts can still be queued; they are dropped,
	// so they never report on the stream that replaced it.
	guard := func(fn func()) func() {
		return func() {
			if ctx.Err() == nil {
				fn()
			}
		}
	}
	post := func(fn func()) { s.env.Post(guard(fn)) }
	batch := &logBatch{}
	s.logs = batch
	client := s.env.API
	go client.Follow(ctx, func(up bool) {
		if up {
			// Lines of the previous connection are replayed again.
			batch.mu.Lock()
			batch.events, batch.reset = nil, true
			batch.mu.Unlock()
		}
		post(func() {
			s.Connected = up
			if !up {
				s.Lock = api.LockState{} // read again on the next connection
			}
			if up {
				s.Failure = nil
				go func() {
					if info, err := client.Info(ctx); err == nil {
						post(func() { s.Info = &info })
					}
					// A server without a write gate answers 404: no lock.
					if lock, err := client.Lock(ctx); err == nil {
						post(func() { s.Lock = lock })
					}
				}()
				again := s.EverConnected
				s.EverConnected = true
				// The server replays its log snapshot on connect.
				s.takeLogs(batch)
				for _, id := range sortedKeys(s.connects) {
					s.connects[id]()
				}
				if again {
					// The server may have restarted: whatever was read from
					// it before is stale.
					for _, id := range sortedKeys(s.reconnects) {
						s.reconnects[id]()
					}
				}
			}
		})
	}, func(err error) {
		post(func() { s.Failure = err })
	}, func(event api.Event) {
		if event.Type != "log" {
			post(func() { s.dispatch(event) })
			return
		}
		// Log lines can come by the hundred a second: they reach the UI
		// thread together, and wake it only while a view shows them.
		batch.mu.Lock()
		batch.events = append(batch.events, event)
		if len(batch.events) > maxLogLines+maxLogLines/4 {
			batch.events = trimFront(batch.events, maxLogLines)
		}
		if !batch.scheduled {
			batch.scheduled = true
			time.AfterFunc(logBatchDelay, func() {
				take := guard(func() { s.takeLogs(batch) })
				if s.logsWatched.Load() {
					s.env.Post(take)
				} else {
					s.env.PostQuiet(take)
				}
			})
		}
		batch.mu.Unlock()
	})
}

// takeLogs moves the lines batch gathered into Logs. UI thread.
func (s *Server) takeLogs(batch *logBatch) {
	batch.mu.Lock()
	events, reset := batch.events, batch.reset
	batch.events, batch.reset, batch.scheduled = nil, false, false
	batch.mu.Unlock()
	if reset {
		s.Logs = nil
		s.LogsVersion++
	}
	for _, event := range events {
		s.dispatch(event)
	}
}

// WatchLogs records whether a view shows Logs, which then follows new
// lines as they come; they are otherwise taken in quietly. UI thread.
func (s *Server) WatchLogs(on bool) {
	s.logsWatched.Store(on)
	if on && s.logs != nil {
		s.takeLogs(s.logs)
	}
}

// trimFront keeps the last n items of items, in place.
func trimFront[T any](items []T, n int) []T {
	kept := copy(items, items[len(items)-n:])
	clear(items[kept:])
	return items[:kept]
}

// Self is this console's id at the server: its device id, or "local" on
// the loopback listener.
func (s *Server) Self() string {
	if s.Info != nil && s.Info.Client != nil && s.Info.Client.ID != "" {
		return s.Info.Client.ID
	}
	return "local"
}

// LockedByOther reports another device holding the write gate.
func (s *Server) LockedByOther() bool {
	return s.Lock.Held && s.Lock.Holder.ID != s.Self()
}

// NeedsPairing reports a connection no retry mends: the token was
// revoked or the certificate changed.
func (s *Server) NeedsPairing() bool { return api.Fatal(s.Failure) }

// Stop ends the event stream. UI thread.
func (s *Server) Stop() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.logs = nil
}

// Dispatch handles an event as if it came from the stream, for tests.
// UI thread.
func (s *Server) Dispatch(event api.Event) { s.dispatch(event) }

func (s *Server) dispatch(event api.Event) {
	s.LastEvent = time.Now()
	switch event.Type {
	case "runtime":
		var runtime api.Runtime
		if event.Decode(&runtime) == nil {
			s.Runtime, s.HasRuntime = runtime, true
		}
	case "selectors":
		var payload struct {
			Selectors []api.Selector `json:"selectors"`
		}
		if event.Decode(&payload) == nil {
			s.Selectors = payload.Selectors
		}
	case "lock":
		if lock, ok := event.Lock(); ok {
			s.Lock = lock
		}
	case "log":
		var line string
		if json.Unmarshal(event.Data, &line) == nil {
			s.Logs = append(s.Logs, line)
			if len(s.Logs) > maxLogLines+maxLogLines/4 {
				s.Logs = trimFront(s.Logs, maxLogLines)
			}
			s.LogsVersion++
		}
	}
	if event.Type != "log" {
		s.Latest[event.Type] = event
	}
	for _, sub := range s.subscribers {
		if len(sub.types) == 0 || sub.types[event.Type] {
			sub.fn(event)
		}
	}
}

// Subscribe calls fn on the UI thread with each event of the given types
// (every type when none). It returns the function that unsubscribes.
func (s *Server) Subscribe(fn func(api.Event), types ...string) func() {
	sub := subscriber{fn: fn, types: map[string]bool{}}
	for _, t := range types {
		sub.types[t] = true
	}
	id := s.nextID
	s.nextID++
	s.subscribers[id] = sub
	return func() { delete(s.subscribers, id) }
}

// OnReconnect calls fn each time the event stream connects again after an
// earlier connection, the server possibly restarted meanwhile: fn re-reads
// what it showed. It returns a function removing fn. UI thread.
func (s *Server) OnReconnect(fn func()) func() {
	id := s.nextID
	s.nextID++
	s.reconnects[id] = fn
	return func() { delete(s.reconnects, id) }
}

// OnConnect calls fn each time the event stream connects, the first time
// included. It returns a function removing fn. UI thread.
func (s *Server) OnConnect(fn func()) func() {
	id := s.nextID
	s.nextID++
	s.connects[id] = fn
	return func() { delete(s.connects, id) }
}

func sortedKeys(m map[int]func()) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// SetProcess records the server process's supervision status from the
// shell. A server that runs again after it was down is reconnected at once
// instead of after the event stream's backoff. UI thread.
func (s *Server) SetProcess(status supervisor.Status) {
	wasDown := s.HasProcess && s.Process.State != supervisor.Running
	s.Process, s.HasProcess = status, true
	if wasDown && status.State == supervisor.Running && s.cancel != nil {
		s.Retry()
	}
}

// SetRuntime records a runtime state from a request's reply. UI thread.
func (s *Server) SetRuntime(runtime api.Runtime) {
	s.Runtime, s.HasRuntime = runtime, true
}

// ClearLogs empties the kept lines. UI thread.
func (s *Server) ClearLogs() {
	s.Logs = nil
	s.LogsVersion++
}

// Retry reconnects the event stream at once. UI thread.
func (s *Server) Retry() {
	s.Stop()
	s.Start()
}
