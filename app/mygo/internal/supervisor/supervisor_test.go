package supervisor

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manual clock: timers fire only from Advance, on the
// test's goroutine.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
}

func newClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !t.stopped
		t.stopped = true
		return was
	}
}

// pending counts the timers not yet fired or stopped.
func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.stopped {
			n++
		}
	}
	return n
}

// Advance moves the clock and fires the timers due, in order.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	for _, t := range c.timers {
		if !t.stopped && !t.at.After(c.now) {
			t.stopped = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].at.Before(due[j].at) })
	for _, t := range due {
		t.f()
	}
}

// fakeProc is a process the test ends with crash.
type fakeProc struct {
	world *world
	done  chan struct{}
	once  sync.Once
	exit  Exit
}

func (p *fakeProc) Wait() Exit {
	<-p.done
	return p.exit
}

func (p *fakeProc) Stop() { p.end(Exit{Code: 0}, true) }

// crash ends the process on its own with code.
func (p *fakeProc) crash(code int) { p.end(Exit{Code: code, Stderr: "boom"}, false) }

func (p *fakeProc) end(exit Exit, stopped bool) {
	p.once.Do(func() {
		p.exit = exit
		p.world.mu.Lock()
		p.world.alive--
		if stopped {
			p.world.stopped++
		}
		p.world.mu.Unlock()
		close(p.done)
	})
}

// world is the fake launcher and the processes it started.
type world struct {
	mu       sync.Mutex
	procs    []*fakeProc
	launches int
	alive    int
	maxAlive int
	stopped  int
	// fail, while set, fails the launches.
	fail error
	// block, while set, holds launches until their context ends.
	block bool
}

func (w *world) launch(ctx context.Context) (Process, error) {
	w.mu.Lock()
	w.launches++
	fail, block := w.fail, w.block
	w.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if fail != nil {
		return nil, fail
	}
	p := &fakeProc{world: w, done: make(chan struct{})}
	w.mu.Lock()
	w.procs = append(w.procs, p)
	w.alive++
	w.maxAlive = max(w.maxAlive, w.alive)
	w.mu.Unlock()
	return p, nil
}

func (w *world) last() *fakeProc {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.procs[len(w.procs)-1]
}

func (w *world) count() (launches, alive int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.launches, w.alive
}

type fixture struct {
	t     *testing.T
	clock *fakeClock
	world *world
	sup   *Supervisor
}

func newFixture(t *testing.T, o Options) *fixture {
	t.Helper()
	if o.Jitter == 0 {
		o.Jitter = -1 // exact delays unless a test wants jitter
	}
	f := &fixture{t: t, clock: newClock(), world: &world{}}
	f.sup = New(f.world.launch, f.clock, o, nil)
	t.Cleanup(f.sup.Stop)
	return f
}

// until waits for the supervisor to reach state; watchers run on their
// own goroutines.
func (f *fixture) until(state State) Status {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := f.sup.Status()
		if st.State == state {
			return st
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("state %v, want %v", st.State, state)
		}
		time.Sleep(time.Millisecond)
	}
}

// crash ends the running process and waits for the supervisor to react.
func (f *fixture) crash(want State) Status {
	f.t.Helper()
	f.world.last().crash(1)
	return f.until(want)
}

func (f *fixture) start() {
	f.t.Helper()
	if err := f.sup.Start(); err != nil {
		f.t.Fatal(err)
	}
	f.until(Running)
}

// Restart delays double from the base and stop at the cap.
func TestBackoffDoublesUpToTheCap(t *testing.T) {
	f := newFixture(t, Options{BaseDelay: time.Second, MaxDelay: 8 * time.Second, MaxFailures: 100})
	f.start()
	var got []time.Duration
	for range 6 {
		st := f.crash(Restarting)
		delay := st.Next.Sub(f.clock.Now())
		got = append(got, delay)
		f.clock.Advance(delay)
		f.until(Running)
	}
	want := []time.Duration{1, 2, 4, 8, 8, 8}
	for i := range want {
		if got[i] != want[i]*time.Second {
			t.Fatalf("delays %v, want %v seconds", got, want)
		}
	}
	if launches, alive := f.world.count(); launches != 7 || alive != 1 {
		t.Errorf("launches %d alive %d", launches, alive)
	}
}

// Jitter keeps each delay within its fraction of the step and the cap.
func TestJitterStaysInBounds(t *testing.T) {
	s := New(nil, newClock(), Options{BaseDelay: 4 * time.Second, MaxDelay: 30 * time.Second, Jitter: 0.25}, nil)
	for _, r := range []float64{0, 0.5, 0.999} {
		s.random = func() float64 { return r }
		d := s.delay(1)
		if d < 3*time.Second || d > 5*time.Second {
			t.Errorf("random %v: delay %v outside 3s..5s", r, d)
		}
		if d := s.delay(10); d > 30*time.Second {
			t.Errorf("random %v: delay %v above the cap", r, d)
		}
	}
}

// An exit after a stable run starts the backoff over.
func TestBackoffResetsAfterStableRun(t *testing.T) {
	f := newFixture(t, Options{BaseDelay: time.Second, StableAfter: time.Minute, MaxFailures: 100})
	f.start()
	for range 3 {
		st := f.crash(Restarting)
		f.clock.Advance(st.Next.Sub(f.clock.Now()))
		f.until(Running)
	}
	f.clock.Advance(61 * time.Second)
	st := f.crash(Restarting)
	if delay := st.Next.Sub(f.clock.Now()); delay != time.Second || st.Attempt != 1 {
		t.Errorf("after a stable run: attempt %d delay %v, want 1 and 1s", st.Attempt, delay)
	}
	if st.Last == nil || st.Last.Uptime != 61*time.Second || st.Last.Code != 1 || st.Last.Stderr != "boom" {
		t.Errorf("last exit %+v", st.Last)
	}
}

// A deliberate stop is never restarted, and no timer is left.
func TestStopDoesNotRestart(t *testing.T) {
	f := newFixture(t, Options{})
	f.start()
	proc := f.world.last()
	f.sup.Stop()
	if st := f.sup.Status(); st.State != Stopped || st.Last != nil {
		t.Errorf("status %+v", st)
	}
	select {
	case <-proc.done:
	default:
		t.Fatal("the process still runs")
	}
	f.clock.Advance(time.Hour)
	if launches, alive := f.world.count(); launches != 1 || alive != 0 || f.clock.pending() != 0 {
		t.Errorf("launches %d alive %d timers %d", launches, alive, f.clock.pending())
	}
	f.sup.Stop() // again
}

// Stopping while a restart is pending cancels it.
func TestStopCancelsPendingRestart(t *testing.T) {
	f := newFixture(t, Options{})
	f.start()
	f.crash(Restarting)
	f.sup.Stop()
	f.clock.Advance(time.Hour)
	if launches, _ := f.world.count(); launches != 1 || f.sup.Status().State != Stopped {
		t.Errorf("launches %d state %v", launches, f.sup.Status().State)
	}
}

// Stopping while a launch waits for readiness ends the launch.
func TestStopDuringLaunch(t *testing.T) {
	f := newFixture(t, Options{})
	f.start()
	f.world.mu.Lock()
	f.world.block = true
	f.world.mu.Unlock()
	st := f.crash(Restarting)
	go f.clock.Advance(st.Next.Sub(f.clock.Now()))
	f.until(Starting)
	f.sup.Stop()
	if _, alive := f.world.count(); alive != 0 || f.sup.Status().State != Stopped {
		t.Errorf("alive %d state %v", alive, f.sup.Status().State)
	}
}

// Too many failures within the window stop the retries.
func TestCrashLoopStopsRetrying(t *testing.T) {
	f := newFixture(t, Options{BaseDelay: time.Second, MaxFailures: 3, Window: time.Minute})
	f.start()
	for range 2 {
		st := f.crash(Restarting)
		f.clock.Advance(st.Next.Sub(f.clock.Now()))
		f.until(Running)
	}
	st := f.crash(CrashLoop)
	if st.Failures != 3 || st.Last == nil {
		t.Errorf("status %+v", st)
	}
	f.clock.Advance(time.Hour)
	if launches, alive := f.world.count(); launches != 3 || alive != 0 || f.clock.pending() != 0 {
		t.Errorf("launches %d alive %d timers %d", launches, alive, f.clock.pending())
	}
}

// Failures older than the window do not count toward the crash loop.
func TestOldFailuresLeaveTheWindow(t *testing.T) {
	f := newFixture(t, Options{BaseDelay: time.Second, MaxFailures: 3, Window: time.Minute, StableAfter: time.Hour})
	f.start()
	for range 4 {
		st := f.crash(Restarting)
		f.clock.Advance(st.Next.Sub(f.clock.Now()))
		f.until(Running)
		f.clock.Advance(40 * time.Second)
	}
	if st := f.sup.Status(); st.State != Running || st.Failures > 2 {
		t.Errorf("status %+v", st)
	}
}

// Failed restarts count as failures and are retried.
func TestFailedLaunchIsRetried(t *testing.T) {
	f := newFixture(t, Options{BaseDelay: time.Second, MaxFailures: 10})
	f.start()
	f.world.mu.Lock()
	f.world.fail = errors.New("port in use")
	f.world.mu.Unlock()
	st := f.crash(Restarting)
	f.clock.Advance(st.Next.Sub(f.clock.Now()))
	st = f.until(Restarting)
	if st.Attempt != 2 || st.Last == nil || st.Last.Err != "port in use" || st.Last.Code != -1 {
		t.Errorf("status %+v last %+v", st, st.Last)
	}
	f.world.mu.Lock()
	f.world.fail = nil
	f.world.mu.Unlock()
	f.clock.Advance(st.Next.Sub(f.clock.Now()))
	f.until(Running)
}

// A failed first start is reported and not retried.
func TestFirstStartFailureIsReturned(t *testing.T) {
	f := newFixture(t, Options{})
	f.world.fail = errors.New("missing binary")
	if err := f.sup.Start(); err == nil || err.Error() != "missing binary" {
		t.Fatalf("err %v", err)
	}
	f.clock.Advance(time.Hour)
	if launches, _ := f.world.count(); launches != 1 || f.sup.Status().State != Stopped {
		t.Errorf("launches %d state %v", launches, f.sup.Status().State)
	}
}

// A manual restart leaves the crash loop with the failures forgotten.
func TestRestartClearsCrashLoop(t *testing.T) {
	f := newFixture(t, Options{MaxFailures: 1})
	f.start()
	f.crash(CrashLoop)
	f.sup.Restart()
	st := f.until(Running)
	if st.Failures != 0 || st.Attempt != 0 || st.Last == nil {
		t.Errorf("status %+v", st)
	}
	// The next crash restarts again instead of looping at once.
	f.sup.options.MaxFailures = 2
	f.crash(Restarting)
}

// A manual restart replaces the running process; the old one is stopped,
// not restarted.
func TestRestartReplacesTheProcess(t *testing.T) {
	f := newFixture(t, Options{})
	f.start()
	old := f.world.last()
	f.sup.Restart()
	f.until(Running)
	select {
	case <-old.done:
	default:
		t.Fatal("the old process still runs")
	}
	f.clock.Advance(time.Hour)
	if launches, alive := f.world.count(); launches != 2 || alive != 1 {
		t.Errorf("launches %d alive %d", launches, alive)
	}
}

// Concurrent stops and restarts never run two processes, and the last
// stop leaves none.
func TestConcurrentStopRestartSpawnsNoDuplicate(t *testing.T) {
	f := newFixture(t, Options{})
	f.start()
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				f.sup.Restart()
			} else {
				f.sup.Stop()
			}
		}()
	}
	wg.Wait()
	f.sup.Stop()
	f.world.mu.Lock()
	defer f.world.mu.Unlock()
	if f.world.maxAlive != 1 || f.world.alive != 0 {
		t.Errorf("max alive %d, alive %d", f.world.maxAlive, f.world.alive)
	}
}

// The change callback sees each transition.
func TestOnChangeReportsTransitions(t *testing.T) {
	clock, w := newClock(), &world{}
	var mu sync.Mutex
	var states []State
	s := New(w.launch, clock, Options{Jitter: -1}, func(st Status) {
		mu.Lock()
		states = append(states, st.State)
		mu.Unlock()
	})
	t.Cleanup(s.Stop)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	w.last().crash(2)
	deadline := time.Now().Add(5 * time.Second)
	for s.Status().State != Restarting && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	s.Stop()
	mu.Lock()
	defer mu.Unlock()
	want := []State{Starting, Running, Restarting, Stopped}
	if len(states) != len(want) {
		t.Fatalf("states %v, want %v", states, want)
	}
	for i := range want {
		if states[i] != want[i] {
			t.Fatalf("states %v, want %v", states, want)
		}
	}
}

// After Shutdown, a restart racing the quit starts nothing.
func TestShutdownRefusesLaterRestarts(t *testing.T) {
	f := newFixture(t, Options{})
	f.start()
	f.sup.Shutdown()
	f.sup.Restart()
	if err := f.sup.Start(); !errors.Is(err, ErrStopped) {
		t.Errorf("start after shutdown: %v", err)
	}
	f.clock.Advance(time.Hour)
	if launches, alive := f.world.count(); launches != 1 || alive != 0 || f.sup.Status().State != Stopped {
		t.Errorf("launches %d alive %d state %v", launches, alive, f.sup.Status().State)
	}
}
