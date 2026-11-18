package store_test

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// latencyRig is a store of n outbounds out0..out(n-1) whose measurements
// wait until the test releases them.
type latencyRig struct {
	h *harness

	mu      sync.Mutex
	asked   map[string]int
	active  int
	maxLive int
	gates   map[string]chan struct{}
	failing map[string]bool
	// unified lists, per outbound, the Unified flag of each request.
	unified map[string][]bool
}

func newLatencyRig(t *testing.T, n int) *latencyRig {
	t.Helper()
	doc := model.EmptyDoc("linux")
	for i := range n {
		id := fmt.Sprintf("out%d", i)
		doc.Outbounds = append(doc.Outbounds, &model.Outbound{
			Entity: model.Entity{ID: id, Tag: id, Enabled: true}, Kind: "outbound", Type: "trojan",
			Fields: model.Fields{"server": id + ".example", "server_port": 443.0},
		})
	}
	r := &latencyRig{asked: map[string]int{}, gates: map[string]chan struct{}{}, failing: map[string]bool{}, unified: map[string][]bool{}}
	r.h = newHarness(t, ptr(storeWith(t, 1, profile("p1", "家里", doc))))
	r.h.srv.Route(http.MethodPost, "/latency/test", func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Targets []api.LatencyTarget `json:"targets"`
		}
		_ = testkit.Body(req, &body)
		var results []api.LatencyResult
		for _, target := range body.Targets {
			r.mu.Lock()
			r.asked[target.ID]++
			r.unified[target.ID] = append(r.unified[target.ID], target.Unified)
			r.active++
			r.maxLive = max(r.maxLive, r.active)
			gate := r.gate(target.ID)
			r.mu.Unlock()
			<-gate
			r.mu.Lock()
			r.active--
			fail := r.failing[target.ID]
			r.mu.Unlock()
			ms := 40.0
			result := api.LatencyResult{ID: target.ID, MedianConnectMs: &ms, SuccessSamples: 1}
			if fail {
				result = api.LatencyResult{ID: target.ID, Error: "timeout"}
			}
			results = append(results, result)
		}
		testkit.JSON(map[string]any{"results": results})(w, req)
	})
	return r
}

// gate is the channel measuring id waits on; the caller holds mu.
func (r *latencyRig) gate(id string) chan struct{} {
	g := r.gates[id]
	if g == nil {
		g = make(chan struct{})
		r.gates[id] = g
	}
	return g
}

func (r *latencyRig) release(id string) {
	r.mu.Lock()
	g := r.gate(id)
	r.mu.Unlock()
	close(g)
}

func (r *latencyRig) releaseAll(n int) {
	for i := range n {
		r.mu.Lock()
		g := r.gate(fmt.Sprintf("out%d", i))
		select {
		case <-g:
		default:
			close(g)
		}
		r.mu.Unlock()
	}
}

func (r *latencyRig) askedCount(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.asked[id]
}

// Testing one outbound leaves the others free, and another test starts
// at once while the first runs.
func TestLatencyParallelPerOutbound(t *testing.T) {
	r := newLatencyRig(t, 3)
	defer r.releaseAll(3)
	r.h.s.MeasureLatencyOf([]string{"out0"})
	if !r.h.s.Testing("out0") || r.h.s.Testing("out1") {
		t.Fatalf("testing out0 %v out1 %v", r.h.s.Testing("out0"), r.h.s.Testing("out1"))
	}
	r.h.s.MeasureLatencyOf([]string{"out1"})
	r.h.until(func() bool { return r.askedCount("out0") == 1 && r.askedCount("out1") == 1 })
	if r.h.s.TestingAny([]string{"out2"}) {
		t.Error("untouched outbound shows testing")
	}
	r.release("out1")
	r.h.until(func() bool { return !r.h.s.Testing("out1") && r.h.s.Latency["out1"].ID != "" })
	if !r.h.s.Testing("out0") {
		t.Error("out0 finished with out1")
	}
	if r.h.s.Latency["out0"].ID != "" {
		t.Error("out0 has a result before it finished")
	}
	if r.h.s.LatencyProgress.Running || r.h.s.LatencyProgress.Total != 0 {
		t.Errorf("per-outbound test touched progress %+v", r.h.s.LatencyProgress)
	}
	r.release("out0")
	r.h.until(func() bool { return !r.h.s.Testing("out0") })
}

// A second request for an outbound under test joins it: one request goes
// out.
func TestLatencyMergesDuplicates(t *testing.T) {
	r := newLatencyRig(t, 1)
	defer r.releaseAll(1)
	r.h.s.MeasureLatencyOf([]string{"out0"})
	r.h.s.MeasureLatencyOf([]string{"out0", "out0"})
	r.h.until(func() bool { return r.askedCount("out0") >= 1 })
	r.release("out0")
	r.h.until(func() bool { return !r.h.s.Testing("out0") })
	if n := r.askedCount("out0"); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

// No more than eight run at once; the rest wait and show testing.
func TestLatencyLimitsConcurrency(t *testing.T) {
	r := newLatencyRig(t, 12)
	defer r.releaseAll(12)
	ids := make([]string, 12)
	for i := range ids {
		ids[i] = fmt.Sprintf("out%d", i)
	}
	r.h.s.MeasureLatencyOf(ids)
	r.h.until(func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.active == 8
	})
	for _, id := range ids {
		if !r.h.s.Testing(id) {
			t.Fatalf("%s queued but not testing", id)
		}
	}
	r.release("out0")
	r.h.until(func() bool { return r.askedCount("out8") == 1 })
	r.releaseAll(12)
	r.h.until(func() bool { return !r.h.s.TestingAny(ids) })
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.maxLive != 8 {
		t.Errorf("at most %d at once, want 8", r.maxLive)
	}
	for _, id := range ids {
		if r.asked[id] != 1 {
			t.Errorf("%s asked %d times", id, r.asked[id])
		}
	}
}

// "Test all" keeps other results, counts its own batch only, and does not
// stop a single test on a member from finishing separately.
func TestLatencyTestAllReplacesOwnResults(t *testing.T) {
	r := newLatencyRig(t, 3)
	defer r.releaseAll(3)
	old := 99.0
	r.h.s.Latency["gone"] = api.LatencyResult{ID: "gone", MedianConnectMs: &old, SuccessSamples: 1}
	r.h.s.MeasureLatencyOf([]string{"out1"})
	r.h.s.MeasureLatency()
	if p := r.h.s.LatencyProgress; p.Total != 3 || p.Completed != 0 || !p.Running {
		t.Fatalf("progress %+v", p)
	}
	if r.h.s.Latency["gone"].ID == "" {
		t.Fatal("test-all emptied the results")
	}
	r.release("out1")
	r.h.until(func() bool { return r.h.s.LatencyProgress.Completed == 1 })
	r.releaseAll(3)
	r.h.until(func() bool { return !r.h.s.LatencyProgress.Running })
	if p := r.h.s.LatencyProgress; p.Completed != 3 {
		t.Errorf("progress %+v", p)
	}
	if n := r.askedCount("out1"); n != 1 {
		t.Errorf("out1 asked %d times, the run joins its test", n)
	}
	if r.h.s.Latency["gone"].ID == "" {
		t.Error("result of an outbound outside the run was lost")
	}
}

// A failure belongs to its outbound alone; test-all asked again while it
// runs is ignored.
func TestLatencyFailureIsLocal(t *testing.T) {
	r := newLatencyRig(t, 2)
	defer r.releaseAll(2)
	r.failing["out0"] = true
	r.h.s.MeasureLatency()
	r.h.s.MeasureLatency()
	r.releaseAll(2)
	r.h.until(func() bool { return !r.h.s.LatencyProgress.Running })
	if r.h.s.Latency["out0"].Error == "" || r.h.s.Latency["out1"].Error != "" {
		t.Errorf("out0 %+v out1 %+v", r.h.s.Latency["out0"], r.h.s.Latency["out1"])
	}
	if r.askedCount("out0") != 1 || r.askedCount("out1") != 1 {
		t.Errorf("asked %v", r.asked)
	}
	if len(r.h.s.LatencyHistory("out0")) != 0 || len(r.h.s.LatencyHistory("out1")) != 1 {
		t.Error("history of a failure recorded")
	}
}

func (r *latencyRig) unifiedOf(id string) []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.unified[id]...)
}

// A reset while a batch runs drops the results of tests started before it
// and queued tests run with the new measure.
func TestLatencyResetDuringBatchDiscardsOldMode(t *testing.T) {
	const n = 10
	r := newLatencyRig(t, n)
	defer r.releaseAll(n)
	r.h.s.MeasureLatency()
	r.h.until(func() bool {
		count := 0
		for i := range 8 {
			count += r.askedCount(fmt.Sprintf("out%d", i))
		}
		return count == 8
	})
	if err := r.h.s.Update(func(d *model.Doc) { d.LatencyPlain = true }); err != nil {
		t.Fatal(err)
	}
	r.h.s.ResetLatency()
	r.releaseAll(n)
	r.h.until(func() bool { return !r.h.s.LatencyProgress.Running })
	if p := r.h.s.LatencyProgress; p.Completed != n {
		t.Errorf("progress %+v, discarded jobs still count", p)
	}
	for i := range 8 {
		id := fmt.Sprintf("out%d", i)
		if r.h.s.Latency[id].ID != "" || len(r.h.s.LatencyHistory(id)) != 0 {
			t.Errorf("%s kept an old-mode result %+v", id, r.h.s.Latency[id])
		}
		if r.h.s.Testing(id) {
			t.Errorf("%s still shows testing", id)
		}
	}
	for _, id := range []string{"out8", "out9"} {
		if got := r.unifiedOf(id); len(got) != 1 || got[0] {
			t.Errorf("%s queued across the reset measured with unified %v, want plain", id, got)
		}
		if r.h.s.Latency[id].ID == "" {
			t.Errorf("%s lost its new-mode result", id)
		}
	}
}

// An ask after a reset does not join a test of the old measure.
func TestLatencyJoinAfterResetUsesNewMode(t *testing.T) {
	r := newLatencyRig(t, 1)
	defer r.releaseAll(1)
	r.h.s.MeasureLatencyOf([]string{"out0"})
	r.h.until(func() bool { return r.askedCount("out0") == 1 })
	if err := r.h.s.Update(func(d *model.Doc) { d.LatencyPlain = true }); err != nil {
		t.Fatal(err)
	}
	r.h.s.ResetLatency()
	r.h.s.MeasureLatencyOf([]string{"out0"})
	r.release("out0")
	r.h.until(func() bool { return r.h.s.Latency["out0"].ID != "" && !r.h.s.Testing("out0") })
	got := r.unifiedOf("out0")
	if len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("requests unified = %v, want [true false]", got)
	}
}
