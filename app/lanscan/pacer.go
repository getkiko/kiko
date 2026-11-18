package lanscan

import (
	"context"
	"sync"
	"time"
)

// Pacer spaces packets so a scan never exceeds a global rate.
type Pacer struct {
	mu    sync.Mutex
	next  time.Time
	every time.Duration
}

// NewPacer allows at most perSecond events per second (minimum 1).
func NewPacer(perSecond int) *Pacer {
	if perSecond < 1 {
		perSecond = 1
	}
	return &Pacer{every: time.Second / time.Duration(perSecond)}
}

// Wait blocks until the caller may send, or ctx ends.
func (p *Pacer) Wait(ctx context.Context) error {
	p.mu.Lock()
	now := time.Now()
	if p.next.Before(now) {
		p.next = now
	}
	at := p.next
	p.next = p.next.Add(p.every)
	p.mu.Unlock()
	return sleep(ctx, time.Until(at))
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
