package subscriptions

import (
	"context"
	"maps"
	"sync/atomic"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
)

// connPoll is how often the inbound cards' connection counts are read.
const connPoll = 2 * time.Second

// connCounts reads the active connections of each inbound every connPoll
// while the page shows and the core runs: GET /connections of the active
// state, one row, whose inbound facet counts them. Its fields are read
// and written on the UI thread; results reach it in a post, which asks
// for a frame only when they change.
type connCounts struct {
	env *env.Env
	// counts maps an inbound's tag in the core to its active connections;
	// ok reports that they are current: the core runs and the last query
	// answered. An inbound without connections is absent.
	counts map[string]int
	ok     bool

	cancel      context.CancelFunc
	unsubscribe func()
}

// count is the active connections of the inbound tagged tag, and whether
// the counts are current; the cards show "—" while they are not.
func (p *connCounts) count(tag string) (int, bool) {
	return p.counts[tag], p.ok
}

// start begins the polling, unless it runs.
func (p *connCounts) start() {
	if p.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	server := p.env.Server
	running := &atomic.Bool{}
	live := func() { running.Store(server.HasRuntime && server.Runtime.Running()) }
	live()
	p.unsubscribe = server.Subscribe(func(api.Event) { live() }, "runtime")
	go p.run(ctx, running, live)
}

// stop ends the polling and forgets the counts.
func (p *connCounts) stop() {
	if p.cancel == nil {
		return
	}
	p.cancel()
	p.unsubscribe()
	p.cancel, p.unsubscribe = nil, nil
	p.counts, p.ok = nil, false
}

// run queries the counts every connPoll until ctx ends, while running
// holds, and posts them as they change; live refreshes running on the
// UI thread. It keeps what it posted last, which the UI holds after the
// post, to tell a change: counts are replaced, never modified.
func (p *connCounts) run(ctx context.Context, running *atomic.Bool, live func()) {
	var posted map[string]int
	postedOK := false
	for {
		var counts map[string]int
		ok := false
		if running.Load() {
			query, cancel := context.WithTimeout(ctx, 5*time.Second)
			page, err := p.env.API.QueryConnections(query, api.ConnectionQuery{State: "active", Limit: 1})
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err == nil {
				ok = true
				counts = make(map[string]int, len(page.Facets["inbound"]))
				for _, facet := range page.Facets["inbound"] {
					counts[facet.Value] = facet.Count
				}
			}
		}
		if ok != postedOK || !maps.Equal(counts, posted) {
			posted, postedOK = counts, ok
			p.env.Post(func() {
				if ctx.Err() != nil {
					return
				}
				live()
				p.counts, p.ok = counts, ok
			})
		}
		timer := time.NewTimer(connPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
