package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// DefaultLatencyURL is the HTTP 204 address outbounds are tested with.
const DefaultLatencyURL = "https://www.gstatic.com/generate_204"

// latencyLimit is how many outbounds are measured at once; the rest
// of those asked for wait their turn.
const latencyLimit = 8

// latencyJob is the measurement of one outbound: queued, then running.
// Asking again for an outbound with a job joins it.
type latencyJob struct {
	target  api.LatencyTarget
	started bool
	// epoch is the store's latencyEpoch when the job started; a result
	// of an older one was measured the way the setting no longer says.
	epoch int
	// batch is the test-all run counting this job, nil for none.
	batch *latencyBatch
}

// latencyBatch counts the results of one test-all run.
type latencyBatch struct{ total, completed int }

// MeasureLatency tests every outbound with a server and port, as
// MeasureLatencyOf does, and counts the run in LatencyProgress. Results
// of other outbounds stay. Asked while a run is under way it does
// nothing.
func (s *Store) MeasureLatency() {
	if s.LatencyProgress.Running {
		return
	}
	targets := latencyTargets(s.Doc(), nil)
	if len(targets) == 0 {
		return
	}
	batch := &latencyBatch{total: len(targets)}
	s.LatencyProgress = LatencyProgress{Total: len(targets), Running: true}
	for _, target := range targets {
		s.enqueueLatency(target, batch)
	}
	s.pumpLatency()
}

// MeasureLatencyOf tests the outbounds ids that have a server and port,
// replacing only their results: those of other outbounds stay. Tests
// run in parallel with any already going, at most eight at a time; one
// asked for an outbound being tested joins that test. It reports
// whether one of ids has a server and port, for callers to tell when
// there is nothing to test.
func (s *Store) MeasureLatencyOf(ids []string) bool {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	targets := latencyTargets(s.Doc(), want)
	if len(targets) == 0 {
		return false
	}
	for _, target := range targets {
		s.enqueueLatency(target, nil)
	}
	s.pumpLatency()
	return true
}

// Testing tells that a test of outbound id is waiting or running.
func (s *Store) Testing(id string) bool { return s.latencyJobs[id] != nil }

// Measurable tells that outbound id has a server and port to test, as
// MeasureLatencyOf asks for.
func (s *Store) Measurable(id string) bool {
	return id != "" && len(latencyTargets(s.Doc(), map[string]bool{id: true})) > 0
}

// MeasurableOf are those of ids that have a server and port to test
// (Measurable), in order.
func (s *Store) MeasurableOf(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	has := map[string]bool{}
	for _, t := range latencyTargets(s.Doc(), want) {
		has[t.ID] = true
	}
	var out []string
	for _, id := range ids {
		if has[id] {
			out = append(out, id)
		}
	}
	return out
}

// TestingAny tells that a test of one of ids is waiting or running.
func (s *Store) TestingAny(ids []string) bool {
	for _, id := range ids {
		if s.latencyJobs[id] != nil {
			return true
		}
	}
	return false
}

// MeasureBatch tests ids as MeasureLatencyOf does, for the batch entry
// key (a button or menu item testing a group, a subscription or a
// region: its kind and the group's, subscription's or region's name),
// which BatchTesting then reports busy until all its tests are done.
// Outbounds being tested already join in, counted until that test ends.
// Asked again while its batch is under way it does nothing, reporting
// true. It reports whether one of ids has a server and port.
func (s *Store) MeasureBatch(key string, ids []string) bool {
	if s.BatchTesting(key) {
		return true
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	targets := latencyTargets(s.Doc(), want)
	if len(targets) == 0 {
		return false
	}
	pending := make(map[string]bool, len(targets))
	batch := make([]string, len(targets))
	for i, t := range targets {
		batch[i] = t.ID
		pending[t.ID] = true
	}
	if s.latencyBatches == nil {
		s.latencyBatches = map[string]map[string]bool{}
	}
	s.latencyBatches[key] = pending
	return s.MeasureLatencyOf(batch)
}

// BatchTesting tells that a test the batch entry key started
// (MeasureBatch) is waiting or running. Tests started elsewhere, of one
// member or by another entry, leave it alone.
func (s *Store) BatchTesting(key string) bool { return len(s.latencyBatches[key]) > 0 }

// endBatches takes outbound id, whose test ended, out of every batch,
// forgetting the batches left with nothing to wait for.
func (s *Store) endBatches(id string) {
	for key, pending := range s.latencyBatches {
		delete(pending, id)
		if len(pending) == 0 {
			delete(s.latencyBatches, key)
		}
	}
}

// enqueueLatency adds the job of target, or joins the one it has, and
// makes batch count it.
func (s *Store) enqueueLatency(target api.LatencyTarget, batch *latencyBatch) {
	job := s.latencyJobs[target.ID]
	if job == nil {
		if s.latencyJobs == nil {
			s.latencyJobs = map[string]*latencyJob{}
		}
		job = &latencyJob{target: target}
		s.latencyJobs[target.ID] = job
		s.latencyQueue = append(s.latencyQueue, job)
		s.latencyGen++
	}
	if batch != nil {
		job.batch = batch
	}
}

// pumpLatency starts queued jobs while fewer than latencyLimit run.
func (s *Store) pumpLatency() {
	for s.latencyActive < latencyLimit && len(s.latencyQueue) > 0 {
		job := s.latencyQueue[0]
		s.latencyQueue = s.latencyQueue[1:]
		job.started = true
		job.epoch = s.latencyEpoch
		// The measure may have changed while the job waited.
		if fresh := latencyTargets(s.Doc(), map[string]bool{job.target.ID: true}); len(fresh) == 1 {
			job.target = fresh[0]
		}
		s.latencyActive++
		go s.runLatency(job)
	}
}

// runLatency measures job's target and posts the result.
func (s *Store) runLatency(job *latencyJob) {
	target := job.target
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	results, err := s.env.API.Latency(ctx, []api.LatencyTarget{target})
	cancel()
	result := api.LatencyResult{ID: target.ID, TestedAt: time.Now().UTC().Format(time.RFC3339)}
	switch {
	case err != nil:
		result.Error = err.Error()
	case len(results) == 0:
		result.Error = "无结果"
	default:
		result = results[0]
		result.ID = target.ID
	}
	s.env.Post(func() {
		if job.epoch == s.latencyEpoch {
			s.Latency[target.ID] = result
			s.recordLatency(target.ID, result)
		}
		if s.latencyJobs[target.ID] == job {
			delete(s.latencyJobs, target.ID)
			s.endBatches(target.ID)
		}
		s.latencyActive--
		s.latencyGen++
		if batch := job.batch; batch != nil {
			batch.completed++
			s.LatencyProgress.Completed = batch.completed
			if batch.completed >= batch.total {
				s.LatencyProgress.Running = false
			}
		}
		s.pumpLatency()
	})
}

// LatencyGen changes as Latency does, to tell when views derived from it
// are stale.
func (s *Store) LatencyGen() int { return s.latencyGen }

// latencyHistoryLen is how many measurements LatencyHistory keeps per
// outbound.
const latencyHistoryLen = 20

// LatencyHistory is outbound id's latest measured latencies, the median
// connect time in milliseconds of each successful test since the console
// started, oldest first, at most 20; failures are left out. The slice is
// read only: a new result replaces it.
func (s *Store) LatencyHistory(id string) []float64 { return s.latencyHistory[id] }

// recordLatency appends a successful result of outbound id to its
// history unless it failed or was a direct probe, dropping the oldest beyond latencyHistoryLen. It builds a new
// slice, so one a view holds stays as it was.
func (s *Store) recordLatency(id string, r api.LatencyResult) {
	// A direct probe (core stopped) is another measure; it does not
	// join the history of requests through the outbound.
	if r.Error != "" || r.MedianConnectMs == nil || r.Mode == api.LatencyModeDirect {
		return
	}
	if s.latencyHistoryMode[id] != r.Mode {
		// Another measure than the history's: start over, never mix.
		delete(s.latencyHistory, id)
	}
	if s.latencyHistoryMode == nil {
		s.latencyHistoryMode = map[string]string{}
	}
	s.latencyHistoryMode[id] = r.Mode
	old := s.latencyHistory[id]
	if len(old) >= latencyHistoryLen {
		old = old[len(old)-latencyHistoryLen+1:]
	}
	next := make([]float64, len(old), len(old)+1)
	copy(next, old)
	if s.latencyHistory == nil {
		s.latencyHistory = map[string][]float64{}
	}
	s.latencyHistory[id] = append(next, *r.MedianConnectMs)
}

// ResetLatency drops every measured result and history, for when the
// measure changes (the unified delay setting) and old values would be
// read as the new one. Tests under way finish but their results are
// discarded, and later asks start tests of their own; waiting tests run
// with the new measure. Batches under way are forgotten.
func (s *Store) ResetLatency() {
	s.latencyEpoch++
	for id, job := range s.latencyJobs {
		if job.started {
			delete(s.latencyJobs, id)
		}
	}
	s.latencyBatches = nil
	s.Latency = map[string]api.LatencyResult{}
	s.latencyHistory = nil
	s.latencyHistoryMode = nil
	s.latencyGen++
}

// latencyTargets are the outbounds of doc with a server and port, those
// in want only unless it is nil.
func latencyTargets(doc *model.Doc, want map[string]bool) []api.LatencyTarget {
	testURL := doc.LatencyTestURL
	if testURL == "" {
		testURL = DefaultLatencyURL
	}
	var targets []api.LatencyTarget
	for _, o := range doc.Outbounds {
		if want != nil && !want[o.ID] {
			continue
		}
		host, port, ok := latencyAddress(o)
		if !ok {
			continue
		}
		targets = append(targets, api.LatencyTarget{ID: o.ID, Host: host, Port: port, Tag: o.Tag, URL: testURL, Unified: !doc.LatencyPlain})
	}
	return targets
}

// latencyAddress is the server and port outbound o is tested at, false
// without them.
func latencyAddress(o *model.Outbound) (string, int, bool) {
	host := ""
	if v := o.Fields["server"]; v != nil {
		host = strings.TrimSpace(fmt.Sprint(v))
	}
	port := 443
	switch v := o.Fields["server_port"].(type) {
	case float64:
		port = int(v)
	case int:
		port = v
	}
	if host == "" || port <= 0 || port > 65535 {
		return "", 0, false
	}
	return host, port, true
}
