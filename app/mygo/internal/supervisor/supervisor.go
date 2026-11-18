// Package supervisor keeps a child process running: it restarts the
// process when it exits on its own, with exponential backoff, and stops
// retrying after too many failures in a short time. The process and the
// clock are injected, so the policy is tested without real processes or
// waiting.
package supervisor

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"
)

// Process is a running child.
type Process interface {
	// Wait blocks until the process exits and has been reaped, its pipes
	// drained and closed, and describes how it ended.
	Wait() Exit
	// Stop ends the process deliberately: gracefully, then by force. It
	// returns once the process has exited; Wait then returns at once.
	Stop()
}

// Launcher starts the process and waits until it is ready to serve. When
// ctx ends first, or the process is not ready, it ends what it started and
// returns an error.
type Launcher func(ctx context.Context) (Process, error)

// Exit describes how a process ended, or why it did not start.
type Exit struct {
	// Code is the exit code, -1 when the process was killed by a signal
	// or did not start.
	Code int
	// Signal names the signal that killed the process, "" for none.
	Signal string
	// Err is the launch error or the wait error.
	Err string
	// Stderr is the tail of the process's standard error.
	Stderr string
	// At is when it ended; Uptime how long it had run, 0 when it did not
	// start.
	At     time.Time
	Uptime time.Duration
}

// State is what the supervisor is doing.
type State int

const (
	// Stopped: not started yet, or stopped deliberately.
	Stopped State = iota
	// Starting: launching the process and waiting for it to be ready.
	Starting
	// Running: the process is serving.
	Running
	// Restarting: the process exited on its own; the next launch is at
	// Status.Next.
	Restarting
	// CrashLoop: the process failed too often; nothing runs until Restart.
	CrashLoop
)

func (s State) String() string {
	return [...]string{"stopped", "starting", "running", "restarting", "crash-loop"}[s]
}

// Status is a snapshot of the supervisor.
type Status struct {
	State State
	// Attempt counts the restarts since the process last ran stably: the
	// launch pending or in progress while Restarting or Starting again.
	Attempt int
	// Next is when the pending restart launches, while Restarting.
	Next time.Time
	// Last is how the process last ended unexpectedly, nil when it never
	// did.
	Last *Exit
	// Failures counts the unexpected exits within the crash-loop window.
	Failures int
}

// Options tune the restart policy. Zero fields take the defaults.
type Options struct {
	// BaseDelay is the first restart's delay, doubling for each further
	// attempt up to MaxDelay. Default 1s and 30s.
	BaseDelay, MaxDelay time.Duration
	// Jitter randomizes each delay by up to this fraction, 0 to 1.
	// Default 0.2; negative for none.
	Jitter float64
	// StableAfter is how long a process must run for its exit to start the
	// backoff over. Default 60s.
	StableAfter time.Duration
	// MaxFailures unexpected exits within Window put the supervisor in
	// CrashLoop. Default 5 within 5 minutes.
	MaxFailures int
	Window      time.Duration
}

func (o Options) withDefaults() Options {
	if o.BaseDelay <= 0 {
		o.BaseDelay = time.Second
	}
	if o.MaxDelay <= 0 {
		o.MaxDelay = 30 * time.Second
	}
	if o.Jitter == 0 {
		o.Jitter = 0.2
	}
	if o.StableAfter <= 0 {
		o.StableAfter = 60 * time.Second
	}
	if o.MaxFailures <= 0 {
		o.MaxFailures = 5
	}
	if o.Window <= 0 {
		o.Window = 5 * time.Minute
	}
	return o
}

// Clock is the time source; RealClock outside tests.
type Clock interface {
	Now() time.Time
	// AfterFunc runs f on its own goroutine after d; the returned stop
	// cancels it, reporting whether it did before f ran.
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type realClock struct{}

// RealClock is the system clock.
var RealClock Clock = realClock{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// ExitError is a launch error that knows how the process ended, as one
// that exited before it was ready.
type ExitError struct {
	Exit Exit
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// ErrStopped reports a launch abandoned because the supervisor stopped or
// restarted meanwhile.
var ErrStopped = errors.New("supervisor: stopped")

// Supervisor runs a process and restarts it when it exits on its own.
//
// Start, Stop and Restart are deliberate and serialized: at most one
// process runs, and a process they stop is never restarted. A generation,
// bumped by each of them, retires every pending timer, launch and watcher
// of the processes before.
type Supervisor struct {
	launch   Launcher
	clock    Clock
	options  Options
	onChange func(Status)
	random   func() float64

	op     sync.Mutex // serializes Start, Stop, Restart and Shutdown
	closed bool       // Shutdown ran: nothing starts again; guarded by op

	mu        sync.Mutex
	gen       uint64
	status    Status
	proc      Process
	startedAt time.Time
	failures  []time.Time
	cancel    context.CancelFunc // of the launch in progress
	stopTimer func() bool

	// work counts the goroutines of the current generation: launches and
	// watchers. Stop waits for them, so none outlives it.
	work sync.WaitGroup
}

// New returns a stopped supervisor of the processes launch starts.
// onChange, when not nil, is called with the new status after each change,
// on the goroutine that made it; it must not call back into the supervisor
// synchronously.
func New(launch Launcher, clock Clock, options Options, onChange func(Status)) *Supervisor {
	if clock == nil {
		clock = RealClock
	}
	return &Supervisor{
		launch:   launch,
		clock:    clock,
		options:  options.withDefaults(),
		onChange: onChange,
		random:   rand.Float64,
	}
}

// Status returns the current status.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot()
}

func (s *Supervisor) snapshot() Status {
	status := s.status
	if status.Last != nil {
		last := *status.Last
		status.Last = &last
	}
	return status
}

// Start launches the process and waits until it is ready, as the app's
// first start does. A failed first launch is returned and not retried:
// the caller decides. Start does nothing while a process runs.
func (s *Supervisor) Start() error {
	s.op.Lock()
	defer s.op.Unlock()
	if s.closed {
		return ErrStopped
	}
	s.mu.Lock()
	if s.status.State != Stopped {
		s.mu.Unlock()
		return nil
	}
	gen := s.gen
	s.work.Add(1)
	s.mu.Unlock()
	err := s.spawn(gen, false)
	if err != nil {
		// The first launch reports its error instead of restarting.
		s.stopLocked()
	}
	return err
}

// Stop ends the process deliberately and cancels any pending restart or
// launch. It returns when the process has exited and every goroutine of
// the supervisor has finished. It is safe to call more than once.
func (s *Supervisor) Stop() {
	s.op.Lock()
	defer s.op.Unlock()
	s.stopLocked()
}

// Shutdown stops the process for good, as the app quits: a Start or
// Restart that comes after, as a user's restart racing the quit, does
// nothing.
func (s *Supervisor) Shutdown() {
	s.op.Lock()
	defer s.op.Unlock()
	s.closed = true
	s.stopLocked()
}

// Restart replaces the process now, as a user's restart: it stops the
// running one deliberately, forgets the failures and the crash loop, and
// launches anew. A failed launch then follows the restart policy.
func (s *Supervisor) Restart() {
	s.op.Lock()
	defer s.op.Unlock()
	if s.closed {
		return
	}
	s.stopLocked()
	s.mu.Lock()
	s.failures = nil
	s.status = Status{State: Stopped, Last: s.status.Last}
	gen := s.gen
	s.work.Add(1)
	s.mu.Unlock()
	go s.spawn(gen, true)
}

// stopLocked retires the current generation and waits for its work. The
// caller holds s.op.
func (s *Supervisor) stopLocked() {
	s.mu.Lock()
	s.gen++
	if s.stopTimer != nil {
		s.stopTimer()
		s.stopTimer = nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	proc := s.proc
	s.proc = nil
	changed := s.status.State != Stopped && s.status.State != CrashLoop
	if changed {
		s.status.State = Stopped
		s.status.Next = time.Time{}
	}
	status := s.snapshot()
	s.mu.Unlock()
	if proc != nil {
		proc.Stop()
	}
	// Launches end on their canceled context and stop what they started;
	// watchers end when their process has exited.
	s.work.Wait()
	if changed {
		s.notify(status)
	}
}

// spawn launches a process of generation gen. The caller added to s.work.
// restart reports whether failures follow the restart policy.
func (s *Supervisor) spawn(gen uint64, restart bool) error {
	defer s.work.Done()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.mu.Lock()
	if s.gen != gen {
		s.mu.Unlock()
		return ErrStopped
	}
	s.cancel = cancel
	s.status.State = Starting
	s.status.Next = time.Time{}
	status := s.snapshot()
	s.mu.Unlock()
	s.notify(status)

	proc, err := s.launch(ctx)

	s.mu.Lock()
	s.cancel = nil
	if s.gen != gen {
		// Stopped or restarted meanwhile: what launched must not run.
		s.mu.Unlock()
		if proc != nil {
			proc.Stop()
		}
		return ErrStopped
	}
	if err != nil {
		if restart {
			exit := Exit{Code: -1}
			var exitErr *ExitError
			if errors.As(err, &exitErr) {
				exit = exitErr.Exit
			}
			exit.Err = err.Error()
			exit.At = s.clock.Now()
			s.failed(gen, exit)
		}
		status := s.snapshot()
		s.mu.Unlock()
		s.notify(status)
		return err
	}
	s.proc = proc
	s.startedAt = s.clock.Now()
	s.status.State = Running
	s.work.Add(1)
	status = s.snapshot()
	s.mu.Unlock()
	s.notify(status)
	go s.watch(gen, proc)
	return nil
}

// watch waits for proc to exit and restarts it unless it was stopped.
func (s *Supervisor) watch(gen uint64, proc Process) {
	defer s.work.Done()
	exit := proc.Wait()
	s.mu.Lock()
	if s.gen != gen || s.proc != proc {
		s.mu.Unlock()
		return // stopped deliberately
	}
	s.proc = nil
	if exit.At.IsZero() {
		exit.At = s.clock.Now()
	}
	exit.Uptime = exit.At.Sub(s.startedAt)
	if exit.Uptime >= s.options.StableAfter {
		// It ran stably: this exit starts the backoff over.
		s.status.Attempt = 0
	}
	s.failed(gen, exit)
	status := s.snapshot()
	s.mu.Unlock()
	s.notify(status)
}

// failed records an unexpected exit or a failed restart and schedules the
// next attempt, or enters CrashLoop. The caller holds s.mu.
func (s *Supervisor) failed(gen uint64, exit Exit) {
	now := s.clock.Now()
	s.status.Last = &exit
	kept := s.failures[:0]
	for _, at := range s.failures {
		if now.Sub(at) < s.options.Window {
			kept = append(kept, at)
		}
	}
	s.failures = append(kept, now)
	s.status.Failures = len(s.failures)
	if len(s.failures) >= s.options.MaxFailures {
		s.status.State = CrashLoop
		s.status.Next = time.Time{}
		return
	}
	s.status.Attempt++
	delay := s.delay(s.status.Attempt)
	s.status.State = Restarting
	s.status.Next = now.Add(delay)
	s.stopTimer = s.clock.AfterFunc(delay, func() {
		s.mu.Lock()
		if s.gen != gen || s.status.State != Restarting {
			s.mu.Unlock()
			return
		}
		s.stopTimer = nil
		s.work.Add(1)
		s.mu.Unlock()
		s.spawn(gen, true)
	})
}

// delay is the wait before restart attempt n (from 1): BaseDelay doubled
// per attempt, capped at MaxDelay, then jittered.
func (s *Supervisor) delay(attempt int) time.Duration {
	d := s.options.BaseDelay
	for i := 1; i < attempt && d < s.options.MaxDelay; i++ {
		d *= 2
	}
	d = min(d, s.options.MaxDelay)
	if j := s.options.Jitter; j > 0 {
		d = time.Duration(float64(d) * (1 - j + 2*j*s.random()))
		d = min(d, s.options.MaxDelay)
	}
	return d
}

func (s *Supervisor) notify(status Status) {
	if s.onChange != nil {
		s.onChange(status)
	}
}
