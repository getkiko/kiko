package scripts

import (
	"context"
	"sync"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
)

// feedRetry is how long the follower waits before it reads a stream again
// after the stream ended or failed.
const feedRetry = 3 * time.Second

// feed follows a stream of numbered entries (the script log, the script
// notifications) for a page. The stream reconnects by itself and reports
// each change of the link; a failure it cannot mend shows as a broken link
// too and is tried again after feedRetry. Entries of one frame reach the UI
// thread together, in one call of take.
type feed[T any] struct {
	env *env.Env
	// open reads the stream after cursor until ctx ends.
	open func(ctx context.Context, after api.Uint64, handle func(T), onState func(connected bool)) error
	seq  func(T) api.Uint64
	// take receives a batch on the UI thread.
	take func(batch []T)

	active bool
	cancel context.CancelFunc

	mu     sync.Mutex
	inbox  []T
	posted bool
	cursor api.Uint64

	// disconnected is set while the link is broken; read it on the UI
	// thread.
	disconnected bool
}

// setActive starts or stops following. The entries kept by the page stay
// when it stops.
func (f *feed[T]) setActive(active bool) {
	if active == f.active {
		return
	}
	f.active = active
	if !active {
		if f.cancel != nil {
			f.cancel()
			f.cancel = nil
		}
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	go f.follow(ctx)
}

func (f *feed[T]) follow(ctx context.Context) {
	for ctx.Err() == nil {
		f.mu.Lock()
		after := f.cursor
		f.mu.Unlock()
		err := f.open(ctx, after, f.push, func(connected bool) {
			f.env.Post(func() {
				if ctx.Err() == nil {
					f.disconnected = !connected
				}
			})
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			f.env.Post(func() {
				if ctx.Err() == nil {
					f.disconnected = true
				}
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(feedRetry):
		}
	}
}

// push takes an entry from the stream.
func (f *feed[T]) push(e T) {
	f.mu.Lock()
	f.inbox = append(f.inbox, e)
	f.cursor = f.seq(e)
	post := !f.posted
	f.posted = true
	f.mu.Unlock()
	if post {
		f.env.Post(f.drain)
	}
}

func (f *feed[T]) drain() {
	f.mu.Lock()
	batch := f.inbox
	f.inbox, f.posted = nil, false
	f.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	f.disconnected = false
	f.take(batch)
}
