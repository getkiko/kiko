package testkit

import (
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

// ScriptRunCall is a POST /scripts/run the fake received.
type ScriptRunCall struct {
	Tag      string
	Argument string
}

// scriptFake is the state of the fake's /scripts/* endpoints.
type scriptFake struct {
	mu          sync.Mutex
	unavailable bool
	status      api.ScriptStatus
	evaluate    func(api.ScriptEvalRequest) api.ScriptEvalResult
	evals       []api.ScriptEvalRequest
	run         func(ScriptRunCall) api.ScriptRunResult
	runs        []ScriptRunCall
	runResults  map[string]api.ScriptRunResult
	runDelay    time.Duration
	runStatus   int
	runStatusMs string
	logs        []api.ScriptLogEntry
	notes       []api.ScriptNotification
	// wake is closed and replaced by each push, waking the streams.
	wake       chan struct{}
	generation int
	store      map[string]map[string]string
}

const scriptStoreLimit = 1 << 20

func (f *scriptFake) init() {
	if f.wake == nil {
		f.wake = make(chan struct{})
		f.store = map[string]map[string]string{}
		f.status = api.ScriptStatus{Scripts: []api.ScriptState{}, StorePersistent: true, StoreLimit: scriptStoreLimit}
	}
}

func (f *scriptFake) wakeStreams() {
	close(f.wake)
	f.wake = make(chan struct{})
}

// SetScriptsUnavailable makes every /scripts/* endpoint answer 503, as
// while the core is not connected.
func (s *Server) SetScriptsUnavailable(unavailable bool) {
	f := &s.scripts
	f.mu.Lock()
	f.init()
	f.unavailable = unavailable
	f.wakeStreams()
	f.mu.Unlock()
}

// SetScriptStatus sets what GET /scripts/status answers.
func (s *Server) SetScriptStatus(status api.ScriptStatus) {
	f := &s.scripts
	f.mu.Lock()
	f.init()
	if status.Scripts == nil {
		status.Scripts = []api.ScriptState{}
	}
	f.status = status
	f.mu.Unlock()
}

// SetEvaluate sets how POST /scripts/evaluate answers; without it the
// action is "none". Every request is kept for ScriptEvalRequests.
func (s *Server) SetEvaluate(fn func(api.ScriptEvalRequest) api.ScriptEvalResult) {
	f := &s.scripts
	f.mu.Lock()
	f.init()
	f.evaluate = fn
	f.mu.Unlock()
}

// ScriptEvalRequests returns the evaluate requests received, oldest first.
func (s *Server) ScriptEvalRequests() []api.ScriptEvalRequest {
	f := &s.scripts
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.evals)
}

// SetScriptRun sets how POST /scripts/run answers; without it the action
// is "none".
func (s *Server) SetScriptRun(fn func(ScriptRunCall) api.ScriptRunResult) {
	f := &s.scripts
	f.mu.Lock()
	f.init()
	f.run = fn
	f.mu.Unlock()
}

// SetScriptRunResult sets what POST /scripts/run answers for tag (generic or
// cron script): output, logs, error and duration come from result. A
// function set by SetScriptRun takes precedence.
func (s *Server) SetScriptRunResult(tag string, result api.ScriptRunResult) {
	f := &s.scripts
	f.mu.Lock()
	f.init()
	if f.runResults == nil {
		f.runResults = map[string]api.ScriptRunResult{}
	}
	f.runResults[tag] = result
	f.mu.Unlock()
}

// SetScriptRunDelay makes POST /scripts/run wait before it answers (the
// request stays recorded at once), to test a running state or cancel.
func (s *Server) SetScriptRunDelay(d time.Duration) {
	f := &s.scripts
	f.mu.Lock()
	f.init()
	f.runDelay = d
	f.mu.Unlock()
}

// SetScriptRunFailure makes POST /scripts/run answer with an HTTP error
// status and message (for example 404 for an unknown tag); status 0 clears.
func (s *Server) SetScriptRunFailure(status int, message string) {
	f := &s.scripts
	f.mu.Lock()
	f.init()
	f.runStatus, f.runStatusMs = status, message
	f.mu.Unlock()
}

// ScriptRuns returns the run requests received, oldest first.
func (s *Server) ScriptRuns() []ScriptRunCall {
	f := &s.scripts
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.runs)
}

// PushScriptLog adds a console line to the log stream; a zero Seq takes
// the next number.
func (s *Server) PushScriptLog(entry api.ScriptLogEntry) {
	f := &s.scripts
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if entry.Seq == 0 {
		entry.Seq = api.Uint64(len(f.logs) + 1)
		if n := len(f.logs); n > 0 {
			entry.Seq = f.logs[n-1].Seq + 1
		}
	}
	f.logs = append(f.logs, entry)
	f.wakeStreams()
}

// PushScriptNotification adds a notification to its stream; a zero Seq
// takes the next number.
func (s *Server) PushScriptNotification(note api.ScriptNotification) {
	f := &s.scripts
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if note.Seq == 0 {
		note.Seq = api.Uint64(len(f.notes) + 1)
		if n := len(f.notes); n > 0 {
			note.Seq = f.notes[n-1].Seq + 1
		}
	}
	f.notes = append(f.notes, note)
	f.wakeStreams()
}

// RestartScriptHub forgets the log and notification history, as a core
// restart does: the numbering starts from 1 again and the open streams
// end.
func (s *Server) RestartScriptHub() {
	f := &s.scripts
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	f.logs, f.notes = nil, nil
	f.generation++
	f.wakeStreams()
}

// SetScriptStore sets a value of the fake store.
func (s *Server) SetScriptStore(namespace, key, value string) {
	f := &s.scripts
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	f.put(namespace, key, value)
}

// ScriptStore returns a copy of the store: namespace to key to value.
func (s *Server) ScriptStore() map[string]map[string]string {
	f := &s.scripts
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]map[string]string{}
	for ns, kv := range f.store {
		out[ns] = map[string]string{}
		for k, v := range kv {
			out[ns][k] = v
		}
	}
	return out
}

func (f *scriptFake) put(namespace, key, value string) {
	if f.store[namespace] == nil {
		f.store[namespace] = map[string]string{}
	}
	f.store[namespace][key] = value
}

func (f *scriptFake) info() api.ScriptStoreInfo {
	var total int
	for _, kv := range f.store {
		for k, v := range kv {
			total += len(k) + len(v)
		}
	}
	return api.ScriptStoreInfo{Persistent: f.status.StorePersistent, Bytes: api.Uint64(total), Limit: scriptStoreLimit}
}

// The fake's scripts: GET /scripts/status, POST /scripts/evaluate and
// /scripts/run, the NDJSON streams /scripts/logs (after, tag) and
// /scripts/notifications (after), and the store under /scripts/store.
func (s *Server) serveScripts(key string, w http.ResponseWriter, r *http.Request) bool {
	switch key {
	case "GET /api/v1/scripts/status", "POST /api/v1/scripts/evaluate", "POST /api/v1/scripts/run", "GET /api/v1/scripts/logs",
		"GET /api/v1/scripts/notifications", "GET /api/v1/scripts/store", "GET /api/v1/scripts/store/value",
		"PUT /api/v1/scripts/store/value", "DELETE /api/v1/scripts/store/value":
	default:
		return false
	}
	f := &s.scripts
	f.mu.Lock()
	f.init()
	unavailable := f.unavailable
	f.mu.Unlock()
	if unavailable {
		w.WriteHeader(http.StatusServiceUnavailable)
		JSON(map[string]string{"error": "脚本等待核心连接"})(w, r)
		return true
	}
	switch key {
	case "GET /api/v1/scripts/status":
		f.mu.Lock()
		status := f.status
		f.mu.Unlock()
		JSON(status)(w, r)
	case "POST /api/v1/scripts/evaluate":
		var req api.ScriptEvalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		f.mu.Lock()
		f.evals = append(f.evals, req)
		fn := f.evaluate
		f.mu.Unlock()
		res := api.ScriptEvalResult{Action: api.ScriptActionNone}
		if fn != nil {
			res = fn(req)
		}
		JSON(res)(w, r)
	case "POST /api/v1/scripts/run":
		var req struct {
			Tag      string `json:"tag"`
			Argument string `json:"argument"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		call := ScriptRunCall{Tag: req.Tag, Argument: req.Argument}
		f.mu.Lock()
		f.runs = append(f.runs, call)
		fn, delay := f.run, f.runDelay
		res, ok := f.runResults[req.Tag]
		failure, message := f.runStatus, f.runStatusMs
		f.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return true
			}
		}
		if failure != 0 {
			w.WriteHeader(failure)
			JSON(map[string]string{"error": message})(w, r)
			return true
		}
		if !ok {
			res = api.ScriptRunResult{Action: api.ScriptActionNone}
		}
		if fn != nil {
			res = fn(call)
		}
		JSON(res)(w, r)
	case "GET /api/v1/scripts/logs":
		tag := r.URL.Query().Get("tag")
		s.scriptStream(w, r, func(after api.Uint64) (items []any, last api.Uint64) {
			last = after
			if n := len(f.logs); n > 0 && after > f.logs[n-1].Seq {
				// A cursor past the newest entry belongs to an earlier core.
				last = 0
			}
			for _, e := range f.logs {
				if e.Seq <= last {
					continue
				}
				last = e.Seq
				if tag == "" || e.Script == tag {
					items = append(items, e)
				}
			}
			return items, last
		})
	case "GET /api/v1/scripts/notifications":
		s.scriptStream(w, r, func(after api.Uint64) (items []any, last api.Uint64) {
			last = after
			if n := len(f.notes); n > 0 && after > f.notes[n-1].Seq {
				last = 0
			}
			for _, e := range f.notes {
				if e.Seq > last {
					last = e.Seq
					items = append(items, e)
				}
			}
			return items, last
		})
	case "GET /api/v1/scripts/store":
		namespace := r.URL.Query().Get("namespace")
		f.mu.Lock()
		out := api.ScriptStoreList{Namespaces: []api.ScriptNamespace{}, Entries: []api.ScriptStoreItem{}}
		for ns, kv := range f.store {
			var bytes int
			for k, v := range kv {
				bytes += len(k) + len(v)
			}
			out.Namespaces = append(out.Namespaces, api.ScriptNamespace{Name: ns, Entries: uint32(len(kv)), Bytes: api.Uint64(bytes)})
		}
		sort.Slice(out.Namespaces, func(i, j int) bool { return out.Namespaces[i].Name < out.Namespaces[j].Name })
		for k, v := range f.store[namespace] {
			out.Entries = append(out.Entries, api.ScriptStoreItem{Key: k, Value: v, Size: uint32(len(v))})
		}
		sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].Key < out.Entries[j].Key })
		info := f.info()
		out.Info = &info
		f.mu.Unlock()
		JSON(out)(w, r)
	case "GET /api/v1/scripts/store/value":
		namespace, k := r.URL.Query().Get("namespace"), r.URL.Query().Get("key")
		f.mu.Lock()
		value, found := f.store[namespace][k]
		f.mu.Unlock()
		JSON(api.ScriptStoreValue{Namespace: namespace, Key: k, Value: value, Found: found})(w, r)
	case "PUT /api/v1/scripts/store/value":
		var in api.ScriptStoreValue
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Namespace == "" || in.Key == "" {
			w.WriteHeader(http.StatusBadRequest)
			JSON(map[string]string{"error": "请指定 namespace 与 key"})(w, r)
			return true
		}
		f.mu.Lock()
		f.put(in.Namespace, in.Key, in.Value)
		info := f.info()
		f.mu.Unlock()
		JSON(info)(w, r)
	case "DELETE /api/v1/scripts/store/value":
		namespace, k := r.URL.Query().Get("namespace"), r.URL.Query().Get("key")
		if namespace == "" || k == "" {
			w.WriteHeader(http.StatusBadRequest)
			JSON(map[string]string{"error": "请指定 namespace 与 key"})(w, r)
			return true
		}
		f.mu.Lock()
		delete(f.store[namespace], k)
		if len(f.store[namespace]) == 0 {
			delete(f.store, namespace)
		}
		info := f.info()
		f.mu.Unlock()
		JSON(info)(w, r)
	}
	return true
}

// scriptStream writes NDJSON: collect (called with the lock held) returns
// the items after the cursor and the new cursor; the stream then waits for
// a push. It ends when the client leaves, the scripts become unavailable
// or the hub restarts.
func (s *Server) scriptStream(w http.ResponseWriter, r *http.Request, collect func(after api.Uint64) ([]any, api.Uint64)) {
	f := &s.scripts
	var after api.Uint64
	if text := r.URL.Query().Get("after"); text != "" {
		if err := after.UnmarshalJSON([]byte(text)); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			JSON(map[string]string{"error": "无效游标: after"})(w, r)
			return
		}
	}
	f.mu.Lock()
	generation := f.generation
	f.mu.Unlock()
	flusher := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	encoder := json.NewEncoder(w)
	for {
		f.mu.Lock()
		items, last := collect(after)
		unavailable := f.unavailable || f.generation != generation
		wake := f.wake
		f.mu.Unlock()
		for _, item := range items {
			if encoder.Encode(item) != nil {
				return
			}
		}
		after = last
		flusher.Flush()
		if unavailable {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-wake:
		}
	}
}
