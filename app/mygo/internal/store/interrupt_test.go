package store_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// callLog records the order of the requests an apply makes.
type callLog struct {
	mu    sync.Mutex
	calls []string
	ids   []string
}

func (l *callLog) add(call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

func (l *callLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.calls)
}

// interruptServer answers the connections of rule rr-1 (a and b) and logs
// the listing, the apply and the close in order.
func interruptServer(h *harness, l *callLog) {
	h.srv.Route("GET", "/connections", func(w http.ResponseWriter, r *http.Request) {
		l.add("list " + r.URL.Query().Get("rule"))
		page := api.ConnectionPage{}
		if r.URL.Query().Get("rule") == "rr-1" {
			page.Rows = []api.ConnectionRow{{Connection: api.Connection{ID: "a"}}, {Connection: api.Connection{ID: "b"}}}
			page.Total = 2
		}
		testkit.JSON(page)(w, r)
	})
	h.srv.Route("POST", "/apply/live", func(w http.ResponseWriter, r *http.Request) {
		l.add("apply")
		testkit.JSON(map[string]any{"applied": true, "live": true})(w, r)
	})
	h.srv.Route("POST", "/connections/close", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs []string `json:"ids"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		l.mu.Lock()
		l.ids = body.IDs
		l.mu.Unlock()
		l.add("close")
		testkit.JSON(api.CloseResult{Matched: len(body.IDs), Closed: len(body.IDs)})(w, r)
	})
}

// withRule adds an enabled traffic rule to the outbound exit to the document
// and returns the exit.
func (h *harness) withRule(id string) string {
	h.t.Helper()
	exit := h.s.Doc().Outbounds[0].ID
	if err := h.s.Update(func(doc *model.Doc) {
		doc.Route.Rules = append(doc.Route.Rules, &model.RouteRule{ID: id, Enabled: true, Action: model.ActionRoute, Outbound: model.Some(exit), Match: map[string]any{}})
	}); err != nil {
		h.t.Fatal(err)
	}
	return exit
}

func (h *harness) applyLive() error {
	done := make(chan error, 1)
	h.env.Post(func() { h.s.ApplyLive(func(err error) { done <- err }) })
	h.until(func() bool { return len(done) == 1 })
	return <-done
}

// A live apply lists the marked rule's connections before it, closes them
// after it succeeds, and spends the marks; unmarked rules are not asked.
func TestLiveApplyClosesMarkedRuleConnections(t *testing.T) {
	h := newHarness(t, nil)
	l := &callLog{}
	interruptServer(h, l)
	exit := h.withRule("rr-1")
	h.s.SetInterrupt("rr-1", "orig", exit, true)
	h.s.SetInterrupt("rr-2", "orig", exit, false)
	if err := h.applyLive(); err != nil {
		t.Fatal(err)
	}
	if got := l.list(); !slices.Equal(got, []string{"list rr-1", "apply", "close"}) {
		t.Fatalf("calls %v", got)
	}
	if !slices.Equal(l.ids, []string{"a", "b"}) {
		t.Fatalf("closed ids %v", l.ids)
	}
	if out := h.s.InterruptOutcome(); out.Closed != 2 || out.Error != "" {
		t.Fatalf("outcome %+v", out)
	}
	if rules := h.s.InterruptRules(); len(rules) != 0 {
		t.Fatalf("marks left: %v", rules)
	}
}

// Without marks an apply touches no connection.
func TestLiveApplyWithoutMarksClosesNothing(t *testing.T) {
	h := newHarness(t, nil)
	l := &callLog{}
	interruptServer(h, l)
	if err := h.applyLive(); err != nil {
		t.Fatal(err)
	}
	if got := l.list(); !slices.Equal(got, []string{"apply"}) {
		t.Fatalf("calls %v", got)
	}
}

// A refused apply closes nothing and keeps the marks for the next one.
func TestFailedLiveApplyKeepsTheMarks(t *testing.T) {
	h := newHarness(t, nil)
	l := &callLog{}
	interruptServer(h, l)
	h.srv.Route("POST", "/apply/live", func(w http.ResponseWriter, r *http.Request) {
		l.add("apply")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"不停机应用失败"}`))
	})
	exit := h.withRule("rr-1")
	h.s.SetInterrupt("rr-1", "orig", exit, true)
	if err := h.applyLive(); err == nil {
		t.Fatal("the apply should fail")
	}
	if got := l.list(); !slices.Equal(got, []string{"list rr-1", "apply"}) {
		t.Fatalf("calls %v", got)
	}
	if rules := h.s.InterruptRules(); !slices.Equal(rules, []string{"rr-1"}) {
		t.Fatalf("marks %v", rules)
	}
}

// The apply stands when closing fails; the outcome tells why.
func TestLiveApplyReportsAFailedClose(t *testing.T) {
	h := newHarness(t, nil)
	l := &callLog{}
	interruptServer(h, l)
	h.srv.Route("POST", "/connections/close", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"核心未响应"}`))
	})
	exit := h.withRule("rr-1")
	h.s.SetInterrupt("rr-1", "orig", exit, true)
	if err := h.applyLive(); err != nil {
		t.Fatalf("the apply failed: %v", err)
	}
	if out := h.s.InterruptOutcome(); out.Error == "" || out.Closed != 0 {
		t.Fatalf("outcome %+v", out)
	}
}

func TestInterruptMarks(t *testing.T) {
	h := newHarness(t, nil)
	h.s.SetInterrupt("b", "f", "x", true)
	h.s.SetInterrupt("a", "f", "x", true)
	h.s.SetInterrupt("a", "f", "x", true)
	if got := h.s.InterruptRules(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("marks %v", got)
	}
	h.s.SetInterrupt("a", "f", "x", false)
	h.s.SetInterrupt("", "f", "x", true)
	if got := h.s.InterruptRules(); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("marks %v", got)
	}
	h.s.ClearInterrupts()
	if got := h.s.InterruptRules(); len(got) != 0 {
		t.Fatalf("marks %v", got)
	}
}

// Marks that no longer describe a pending change close nothing and are
// dropped: the rule was deleted, disabled, or goes to another outbound
// than the answer was given for (a restored configuration).
func TestStaleMarksAreDroppedNotActedOn(t *testing.T) {
	h := newHarness(t, nil)
	l := &callLog{}
	interruptServer(h, l)
	exit := h.withRule("rr-1")
	h.s.SetInterrupt("rr-1", "orig", exit, true)
	h.s.SetInterrupt("gone", "orig", exit, true)
	h.s.SetInterrupt("rr-1b", "orig", "elsewhere", true)
	if err := h.applyLive(); err != nil {
		t.Fatal(err)
	}
	if got := l.list(); !slices.Equal(got, []string{"list rr-1", "apply", "close"}) {
		t.Fatalf("calls %v", got)
	}
	if rules := h.s.InterruptRules(); len(rules) != 0 {
		t.Fatalf("marks left: %v", rules)
	}
	// A disabled rule and a changed outbound.
	h.s.SetInterrupt("rr-1", "orig", exit, true)
	if err := h.s.Update(func(doc *model.Doc) { doc.Route.Rules[0].Enabled = false }); err != nil {
		t.Fatal(err)
	}
	if err := h.applyLive(); err != nil {
		t.Fatal(err)
	}
	if got := l.list(); len(got) != 4 || got[3] != "apply" {
		t.Fatalf("a disabled rule's connections were listed or closed: %v", got)
	}
	if rules := h.s.InterruptRules(); len(rules) != 0 {
		t.Fatalf("marks left: %v", rules)
	}
}

// An answer given while an apply runs is kept for the next one.
func TestNewAnswerDuringApplySurvives(t *testing.T) {
	h := newHarness(t, nil)
	l := &callLog{}
	interruptServer(h, l)
	exit := h.withRule("rr-1")
	h.s.SetInterrupt("rr-1", "orig", exit, true)
	h.srv.Route("POST", "/apply/live", func(w http.ResponseWriter, r *http.Request) {
		l.add("apply")
		h.s.SetInterrupt("rr-1", "orig", exit, true)
		testkit.JSON(map[string]any{"applied": true, "live": true})(w, r)
	})
	if err := h.applyLive(); err != nil {
		t.Fatal(err)
	}
	if rules := h.s.InterruptRules(); !slices.Equal(rules, []string{"rr-1"}) {
		t.Fatalf("the newer answer was spent: %v", rules)
	}
}

// When the connections cannot be listed the marks stay for a retry.
func TestListFailureKeepsTheMarks(t *testing.T) {
	h := newHarness(t, nil)
	l := &callLog{}
	interruptServer(h, l)
	h.srv.Route("GET", "/connections", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"核心未响应"}`))
	})
	exit := h.withRule("rr-1")
	h.s.SetInterrupt("rr-1", "orig", exit, true)
	if err := h.applyLive(); err != nil {
		t.Fatal(err)
	}
	if out := h.s.InterruptOutcome(); out.Error == "" {
		t.Fatalf("outcome %+v", out)
	}
	if rules := h.s.InterruptRules(); !slices.Equal(rules, []string{"rr-1"}) {
		t.Fatalf("marks %v", rules)
	}
}

// A rule sent back to the outbound its connections use has nothing to
// close, whatever the answer said on the way.
func TestMarkSentBackToItsOriginIsDropped(t *testing.T) {
	h := newHarness(t, nil)
	l := &callLog{}
	interruptServer(h, l)
	exit := h.withRule("rr-1")
	h.s.SetInterrupt("rr-1", exit, "other", true)
	h.s.SetInterrupt("rr-1", "later", exit, true)
	if from, ok := h.s.InterruptFrom("rr-1"); !ok || from != exit {
		t.Fatalf("a later answer replaced the origin: %q %v", from, ok)
	}
	if err := h.applyLive(); err != nil {
		t.Fatal(err)
	}
	if got := l.list(); !slices.Equal(got, []string{"apply"}) {
		t.Fatalf("calls %v", got)
	}
}

// Answering keep, then interrupt, while an apply runs leaves the newer mark
// even though the first answer removed the mark and the second recreated it.
func TestRecreatedMarkSurvivesApply(t *testing.T) {
	h := newHarness(t, nil)
	l := &callLog{}
	interruptServer(h, l)
	exit := h.withRule("rr-1")
	h.s.SetInterrupt("rr-1", "orig", exit, true)
	h.srv.Route("POST", "/apply/live", func(w http.ResponseWriter, r *http.Request) {
		l.add("apply")
		h.s.SetInterrupt("rr-1", "orig", exit, false)
		h.s.SetInterrupt("rr-1", "orig", exit, true)
		testkit.JSON(map[string]any{"applied": true, "live": true})(w, r)
	})
	if err := h.applyLive(); err != nil {
		t.Fatal(err)
	}
	if rules := h.s.InterruptRules(); !slices.Equal(rules, []string{"rr-1"}) {
		t.Fatalf("the recreated mark was spent: %v", rules)
	}
}

// Another profile has no use for the marks of the last one.
func TestSwitchingProfileClearsTheMarks(t *testing.T) {
	h := newHarness(t, nil)
	h.s.SetInterrupt("rr-1", "orig", "x", true)
	active := h.s.ActiveProfileID()
	h.env.Post(func() { h.s.SetProfiles(h.s.Profiles(), active, nil) })
	h.until(func() bool { return !h.s.Saving() })
	if len(h.s.InterruptRules()) != 1 {
		t.Fatal("the same profile cleared the marks")
	}
	h.env.Post(func() { h.s.SetProfiles(h.s.Profiles(), "another", nil) })
	h.until(func() bool { return len(h.s.InterruptRules()) == 0 })
}

// A(applied) → B (keep) → A (interrupt): the connections are on A, so a
// mark for A has nothing to close; the origin is the applied outbound, not
// the draft the second answer found.
func TestOriginIsTheAppliedOutbound(t *testing.T) {
	h := newHarness(t, nil)
	l := &callLog{}
	interruptServer(h, l)
	exit := h.withRule("rr-1")
	if err := h.applyLive(); err != nil {
		t.Fatal(err)
	}
	if got, ok := h.s.AppliedExit("rr-1"); !ok || got != exit {
		t.Fatalf("applied exit %q %v", got, ok)
	}
	// The draft moved to B and was kept; now it goes back to A and the
	// answer is interrupt, given with B as the draft's old outbound.
	h.s.SetInterrupt("rr-1", "B", exit, true)
	before := len(l.list())
	if err := h.applyLive(); err != nil {
		t.Fatal(err)
	}
	if got := l.list()[before:]; !slices.Equal(got, []string{"apply"}) {
		t.Fatalf("calls %v", got)
	}
}
